// Package sync copies one rooted tree into another.
//
// A sync is a comparison of two indexes followed by the copies and
// removals that comparison implies. Nothing in here knows whether the
// source is a directory on disk or a tree behind HTTP, or whether the
// destination is reached through a descriptor or something else: the
// source is an fs.FS, the destination is a tree.Tree, and both are
// addressed by the same keys.
package sync

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/the-maldridge/dumbsync/pkg/index"
	"github.com/the-maldridge/dumbsync/pkg/scope"
	"github.com/the-maldridge/dumbsync/pkg/tree"
)

// ErrIncomplete is returned when the source could not be read
// completely while it was being described. Copies that already ran are
// kept, but nothing is deleted on a partial description: a file the
// source failed to read is indistinguishable from one that was removed.
var ErrIncomplete = errors.New("source could not be read completely")

// ErrCorrupt is reported for a file whose content did not match the hash
// the source published for it.
var ErrCorrupt = errors.New("copied content does not match the source")

// ErrUncovered is returned when an include rule matches no file in the
// source.  Everything else is usually excluded when includes are used at
// all, so such a run would change nothing and report a clean success,
// which looks exactly like a healthy run that had nothing to do.  A name
// covering nothing is a mistake in the rules far more often than it is
// an empty directory someone meant to sync, so it stops the run before
// the destination is touched.
var ErrUncovered = errors.New("an include rule names nothing in the source")

// Options describes a single sync.
type Options struct {
	// Source is the tree to copy from, rooted at itself.
	Source fs.FS

	// Target is the tree to copy into.
	Target tree.Tree

	// Hash is the algorithm used when the source does not publish one.
	// The destination is hashed with the algorithm of the source, so the
	// two ends cannot disagree by configuration alone.
	Hash index.HashType

	// Rules decides which keys the sync is allowed to see on either side.
	// Rules a caller keeps for its own safety go last, since the last
	// rule covering a key is the one that decides.
	Rules scope.Rules

	// Threads is how many files are copied at once. Values below one use
	// the default of ten.
	Threads int

	// Atomic stages each new copy beside the file it replaces and moves it
	// into place only once every copy has finished, so a destination never
	// serves a half written file while a transfer is in flight.
	Atomic bool

	// Delete removes destination files the source no longer describes.
	// Without it a sync only adds and updates.
	Delete bool

	// Progress, when set, is called from the goroutine doing the work
	// just before each file is copied or removed.
	Progress func(Event)
}

// EventKind says what a sync is about to do with a file.
type EventKind int

const (
	// Added is a file the destination does not have yet.
	Added EventKind = iota

	// Changed is a file the destination has with different content.
	Changed

	// Removed is a file the source no longer describes.
	Removed
)

// Event reports work in progress.
type Event struct {
	Kind EventKind
	Key  string
}

// Report is what a sync did. Every list holds keys in sorted order.
type Report struct {
	Added   []string
	Changed []string
	Removed []string

	// Leftover holds keys the source no longer describes that were kept,
	// either because Delete was not set or because the source was not
	// read completely enough to trust.
	Leftover []string

	// Failed holds one entry per key that could not be brought across,
	// with the reason. A sync that fails part way through is not rolled
	// back; the next run retries whatever is left.
	Failed map[string]error
}

// Empty reports whether the sync changed nothing at the destination.
func (r Report) Empty() bool {
	return len(r.Added) == 0 && len(r.Changed) == 0 && len(r.Removed) == 0
}

// Sync brings the target up to date with the source.
//
// A source that publishes its own hashes, such as the index of a tree
// fetched from elsewhere, is taken as published and its content is read
// once per copied file rather than twice.
func Sync(o Options) (Report, error) {
	if o.Source == nil || o.Target == nil {
		return Report{}, errors.New("a sync needs both a source and a target")
	}

	threads := o.Threads
	if threads < 1 {
		threads = 10
	}

	src, err := index.IndexOf(o.Source, index.Options{Hash: o.Hash, Rules: o.Rules})
	if err != nil {
		return Report{}, err
	}

	// Asked before the destination is hashed, because a rule naming
	// nothing is answered by the source alone and a mistyped name is not
	// worth reading a whole tree for.
	if missing := o.Rules.Uncovered(keysOf(src.Files)); len(missing) > 0 {
		names := make([]string, 0, len(missing))
		for _, rule := range missing {
			names = append(names, rule.String())
		}

		return Report{}, fmt.Errorf("%w: %s", ErrUncovered, strings.Join(names, ", "))
	}

	// The destination has to be hashed the way the source was, so the
	// algorithm travels from one end to the other instead of being
	// configured twice.
	dst, err := index.IndexFS(o.Target.FS(), index.Options{Hash: src.HashType, Rules: o.Rules})
	if err != nil {
		return Report{}, err
	}

	added, removed, changed := dst.Difference(src)
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(changed)

	r := &run{
		o:       o,
		hash:    src.HashType,
		want:    src.Files,
		threads: threads,
		report:  Report{Failed: map[string]error{}},
	}

	r.report.Added, r.report.Changed = r.copy(added, changed)

	// Deletions are the only part of a sync that cannot be undone, so
	// they need both an explicit request and a source that can be
	// trusted to be telling the whole story.
	if len(removed) > 0 && o.Delete && !src.Incomplete {
		r.report.Removed = r.remove(removed)
	} else {
		r.report.Leftover = removed
	}

	if src.Incomplete {
		return r.report, fmt.Errorf("%w: %v", ErrIncomplete, src.Errors)
	}

	return r.report, nil
}

// keysOf lists what an index describes, which is what the include rules
// are checked against.
func keysOf(files map[string][]byte) []string {
	keys := make([]string, 0, len(files))
	for key := range files {
		keys = append(keys, key)
	}

	return keys
}

type run struct {
	o       Options
	hash    index.HashType
	want    map[string][]byte
	threads int
	report  Report
}

type job struct {
	key  string
	kind EventKind
}

// copy brings every listed key across.
//
// Additions and updates share one pool: both end as a complete file at
// the destination key and neither depends on the other. When staging is
// asked for, the renames happen after every transfer has been attempted,
// so a destination that is serving files while it is being synced only
// ever serves complete ones.
func (r *run) copy(added, changed []string) ([]string, []string) {
	jobs := make([]job, 0, len(added)+len(changed))
	for _, key := range added {
		jobs = append(jobs, job{key: key, kind: Added})
	}
	for _, key := range changed {
		jobs = append(jobs, job{key: key, kind: Changed})
	}

	// Each goroutine fills its own slot, so collecting results needs no
	// lock, and a goroutine that fails still accounts for its file.
	staged := make([]string, len(jobs))
	failures := make([]error, len(jobs))

	var wg sync.WaitGroup
	slots := make(chan struct{}, r.threads)
	for i := range jobs {
		slots <- struct{}{}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-slots }()

			staged[i], failures[i] = r.transfer(jobs[i])
		}(i)
	}
	wg.Wait()

	for i := range jobs {
		if failures[i] != nil || staged[i] == "" {
			continue
		}
		if err := r.o.Target.Rename(staged[i], jobs[i].key); err != nil {
			failures[i] = err
			r.o.Target.RemoveAll(staged[i])
		}
	}

	var got, updated []string
	for i, j := range jobs {
		if failures[i] != nil {
			r.report.Failed[j.key] = failures[i]
			continue
		}
		if j.kind == Added {
			got = append(got, j.key)
		} else {
			updated = append(updated, j.key)
		}
	}

	return got, updated
}

// transfer writes one key into the destination and returns the name it
// needs to be renamed from, empty when the file already sits where it
// belongs.
func (r *run) transfer(j job) (string, error) {
	r.progress(j.kind, j.key)

	dst := j.key
	if r.o.Atomic {
		dst = tree.TempKey(j.key)
	}

	if dir := path.Dir(j.key); dir != "." {
		if err := r.o.Target.MkdirAll(dir); err != nil {
			return stagedOr(dst, j.key), err
		}
	}

	src, err := r.o.Source.Open(j.key)
	if err != nil {
		r.abandon(j, dst)
		return stagedOr(dst, j.key), err
	}
	defer src.Close()

	f, err := r.o.Target.OpenWrite(dst)
	if err != nil {
		r.abandon(j, dst)
		return stagedOr(dst, j.key), err
	}

	h := index.NewHash(r.hash)
	if _, err := io.Copy(io.MultiWriter(f, h), src); err != nil {
		f.Close()
		r.abandon(j, dst)
		return stagedOr(dst, j.key), err
	}
	if err := f.Close(); err != nil {
		r.abandon(j, dst)
		return stagedOr(dst, j.key), err
	}

	// Bytes lost on the way would otherwise wait until the next sync to
	// be noticed, and would look like a file the source happened to
	// change again.
	if want, ok := r.want[j.key]; ok && !bytes.Equal(h.Sum(nil), want) {
		r.abandon(j, dst)
		return stagedOr(dst, j.key), fmt.Errorf("%w: %s", ErrCorrupt, j.key)
	}

	if !r.o.Atomic {
		return "", nil
	}
	return dst, nil
}

// stagedOr is only about the name to clean up later: a staged file is
// tracked so it can be removed, a file written in place is not.
func stagedOr(dst, key string) string {
	if dst == key {
		return ""
	}
	return dst
}

// abandon cleans up after a transfer that did not finish. A staged file
// always goes away. Written in place, a file the destination did not
// have before goes with it so the next run starts from the same place
// this one did; one that was being replaced is left as it is rather than
// deleted outright.
func (r *run) abandon(j job, dst string) {
	if dst != j.key {
		r.o.Target.RemoveAll(dst)
		return
	}
	if j.kind == Added {
		r.o.Target.RemoveAll(j.key)
	}
}

// remove deletes keys the source no longer describes.
func (r *run) remove(removed []string) []string {
	var gone []string
	for _, key := range removed {
		r.progress(Removed, key)
		if err := r.o.Target.RemoveAll(key); err != nil {
			r.report.Failed[key] = err
			continue
		}
		gone = append(gone, key)
	}
	return gone
}

func (r *run) progress(kind EventKind, key string) {
	if r.o.Progress == nil {
		return
	}
	r.o.Progress(Event{Kind: kind, Key: key})
}
