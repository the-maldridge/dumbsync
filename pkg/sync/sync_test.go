package sync_test

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/the-maldridge/dumbsync/pkg/index"
	"github.com/the-maldridge/dumbsync/pkg/scope"
	dsync "github.com/the-maldridge/dumbsync/pkg/sync"
	"github.com/the-maldridge/dumbsync/pkg/tree"
)

func syncTo(t *testing.T, src fs.FS, dst string, tweak func(*dsync.Options)) (dsync.Report, error) {
	t.Helper()

	tr, err := tree.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })

	o := dsync.Options{Source: src, Target: tr, Threads: 4}
	if tweak != nil {
		tweak(&o)
	}

	return doWithDeadline(t, o)
}

// doWithDeadline fails rather than hangs if the engine ever loses track
// of a worker: a sync that never returns is the worst way this can be
// wrong, and it has happened before.
func doWithDeadline(t *testing.T, o dsync.Options) (dsync.Report, error) {
	t.Helper()

	done := make(chan struct{})
	var rep dsync.Report
	var err error
	go func() {
		defer close(done)
		rep, err = dsync.Sync(o)
	}()

	select {
	case <-done:
		return rep, err
	case <-time.After(20 * time.Second):
		t.Fatal("sync did not finish")
		return dsync.Report{}, nil
	}
}

func TestOnlyMissingFilesAreCopied(t *testing.T) {
	dst := t.TempDir()
	seed(t, dst, map[string]string{
		"same.txt":  "unchanged",
		"stale.txt": "no longer in the source",
	})

	rep, err := syncTo(t, fstest.MapFS{
		"same.txt":  &fstest.MapFile{Data: []byte("unchanged")},
		"new.txt":   &fstest.MapFile{Data: []byte("brand new")},
		"sub/b.txt": &fstest.MapFile{Data: []byte("nested")},
	}, dst, nil)
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"new.txt", "sub/b.txt"}; !equal(rep.Added, want) {
		t.Errorf("added = %v, want %v", rep.Added, want)
	}
	if len(rep.Changed) != 0 {
		t.Errorf("changed = %v, want nothing", rep.Changed)
	}
	if len(rep.Failed) != 0 {
		t.Errorf("failed = %v", rep.Failed)
	}

	check(t, dst, "sub/b.txt", "nested")

	// Without Delete a sync never removes anything, but it says what it
	// left behind.
	if want := []string{"stale.txt"}; !equal(rep.Leftover, want) {
		t.Errorf("leftover = %v, want %v", rep.Leftover, want)
	}
	if _, err := fs.Stat(osDirFS(dst), "stale.txt"); err != nil {
		t.Errorf("stale.txt was removed without Delete: %v", err)
	}
}

func TestChangedFilesAreReplaced(t *testing.T) {
	dst := t.TempDir()
	seed(t, dst, map[string]string{"a.txt": "first version"})

	rep, err := syncTo(t, fstest.MapFS{"a.txt": &fstest.MapFile{Data: []byte("second version")}}, dst, nil)
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"a.txt"}; !equal(rep.Changed, want) {
		t.Errorf("changed = %v, want %v", rep.Changed, want)
	}
	check(t, dst, "a.txt", "second version")
}

func TestDeleteRemovesWhatTheSourceNoLongerHas(t *testing.T) {
	dst := t.TempDir()
	seed(t, dst, map[string]string{"gone.txt": "bye", "keep.txt": "stay"})

	rep, err := syncTo(t, fstest.MapFS{"keep.txt": &fstest.MapFile{Data: []byte("stay")}}, dst, func(o *dsync.Options) {
		o.Delete = true
	})
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"gone.txt"}; !equal(rep.Removed, want) {
		t.Errorf("removed = %v, want %v", rep.Removed, want)
	}
	if _, err := fs.Stat(osDirFS(dst), "gone.txt"); !os.IsNotExist(err) {
		t.Errorf("gone.txt survived: %v", err)
	}
	check(t, dst, "keep.txt", "stay")
}

// A file the source could not read looks exactly like a file that was
// deleted, so a partial source must not drive removals.
func TestIncompleteSourceCannotDelete(t *testing.T) {
	dst := t.TempDir()
	seed(t, dst, map[string]string{"keep.txt": "stay", "maybe.txt": "still there"})

	src := fstest.MapFS{"keep.txt": &fstest.MapFile{Data: []byte("stay")}}
	idx, err := index.IndexFS(src, index.Options{Hash: index.XXHash})
	if err != nil {
		t.Fatal(err)
	}
	idx.Incomplete = true

	rep, err := syncTo(t, publisher{FS: src, idx: idx}, dst, func(o *dsync.Options) {
		o.Delete = true
	})
	if !errors.Is(err, dsync.ErrIncomplete) {
		t.Fatalf("err = %v, want dsync.ErrIncomplete", err)
	}

	if want := []string{"maybe.txt"}; !equal(rep.Leftover, want) {
		t.Errorf("leftover = %v, want %v", rep.Leftover, want)
	}
	if len(rep.Removed) != 0 {
		t.Errorf("removed = %v, want nothing at all", rep.Removed)
	}
	check(t, dst, "maybe.txt", "still there")
}

// The destination has to be hashed the way the source published itself,
// otherwise every run finds a tree full of differences.
func TestDestinationFollowsTheSourceAlgorithm(t *testing.T) {
	dst := t.TempDir()
	src := fstest.MapFS{
		"a.txt":     &fstest.MapFile{Data: []byte("alpha")},
		"sub/b.txt": &fstest.MapFile{Data: []byte("bravo")},
	}
	idx, err := index.IndexFS(src, index.Options{Hash: index.XXHash})
	if err != nil {
		t.Fatal(err)
	}
	if idx.HashType != index.XXHash {
		t.Fatal("published index is not xxhash")
	}

	o := dsync.Options{Source: publisher{FS: src, idx: idx}, Threads: 4}
	tr, err := tree.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	o.Target = tr

	// The caller asked for nothing in particular; the index decides.
	o.Hash = index.MD5
	if _, err := doWithDeadline(t, o); err != nil {
		t.Fatal(err)
	}

	second, err := doWithDeadline(t, o)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Empty() {
		t.Errorf("a second sync found work to do: added %v changed %v", second.Added, second.Changed)
	}
}

// Content that arrives different from what the source described must
// not be accepted just because the transfer completed.
func TestMismatchedContentIsRefused(t *testing.T) {
	for _, atomic := range []bool{false, true} {
		dst := t.TempDir()
		src := fstest.MapFS{"a.txt": &fstest.MapFile{Data: []byte("the real bytes")}}
		idx, err := index.IndexFS(src, index.Options{})
		if err != nil {
			t.Fatal(err)
		}

		// Stand in for a transfer that was truncated or mangled.
		src["a.txt"] = &fstest.MapFile{Data: []byte("truncated")}

		rep, err := syncTo(t, publisher{FS: src, idx: idx}, dst, func(o *dsync.Options) {
			o.Atomic = atomic
		})
		if err != nil {
			t.Fatalf("atomic=%v: %v", atomic, err)
		}

		if len(rep.Failed) != 1 {
			t.Fatalf("atomic=%v: failed = %v, want one refusal", atomic, rep.Failed)
		}
		if !errors.Is(rep.Failed["a.txt"], dsync.ErrCorrupt) {
			t.Errorf("atomic=%v: failure = %v, want dsync.ErrCorrupt", atomic, rep.Failed["a.txt"])
		}
		if _, err := fs.Stat(osDirFS(dst), "a.txt"); !os.IsNotExist(err) {
			t.Errorf("atomic=%v: bad content was left at the destination: %v", atomic, err)
		}
	}
}

// The destination failing part way through must account for every file
// and still return.
func TestDestinationFailuresAreReportedNotStuck(t *testing.T) {
	tr, err := tree.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	rep, err := dsync.Sync(dsync.Options{
		Source: fstest.MapFS{
			"a.txt":     &fstest.MapFile{Data: []byte("a")},
			"b.txt":     &fstest.MapFile{Data: []byte("b")},
			"sub/c.txt": &fstest.MapFile{Data: []byte("c")},
		},
		Target:  &brokenTree{Tree: tr},
		Threads: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(rep.Failed) != 3 {
		t.Errorf("failed = %v, want one entry per file", rep.Failed)
	}
	if len(rep.Added) != 0 || len(rep.Changed) != 0 {
		t.Errorf("a sync that could not write reported %v added, %v changed", rep.Added, rep.Changed)
	}
}

func TestAtomicSyncLeavesNoScratchFiles(t *testing.T) {
	dst := t.TempDir()

	_, err := syncTo(t, fstest.MapFS{
		"a.txt":     &fstest.MapFile{Data: []byte("a")},
		"sub/b.txt": &fstest.MapFile{Data: []byte("b")},
	}, dst, func(o *dsync.Options) { o.Atomic = true })
	if err != nil {
		t.Fatal(err)
	}

	check(t, dst, "a.txt", "a")
	check(t, dst, "sub/b.txt", "b")

	var scratch []string
	if err := fs.WalkDir(osDirFS(dst), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && len(d.Name()) > 10 && d.Name()[:10] == ".dumbsync-" {
			scratch = append(scratch, name)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(scratch) != 0 {
		t.Errorf("staged files left behind: %v", scratch)
	}
}

func TestProgressCoversEveryAction(t *testing.T) {
	dst := t.TempDir()
	seed(t, dst, map[string]string{"stale.txt": "bye"})

	// Progress is called from whichever goroutine is doing the work, so
	// a caller collecting events has to guard its own state.
	var mu sync.Mutex
	seen := map[string]dsync.EventKind{}
	_, err := syncTo(t, fstest.MapFS{
		"a.txt": &fstest.MapFile{Data: []byte("a")},
		"b.txt": &fstest.MapFile{Data: []byte("b")},
	}, dst, func(o *dsync.Options) {
		o.Delete = true
		o.Progress = func(e dsync.Event) {
			mu.Lock()
			defer mu.Unlock()
			seen[e.Key] = e.Kind
		}
	})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]dsync.EventKind{"a.txt": dsync.Added, "b.txt": dsync.Added, "stale.txt": dsync.Removed}
	if !reflect.DeepEqual(seen, want) {
		t.Errorf("progress = %v, want %v", seen, want)
	}
}

func TestRulesKeepFilesOffBothSides(t *testing.T) {
	dst := t.TempDir()
	seed(t, dst, map[string]string{"marker": "mine"})

	src := fstest.MapFS{
		"marker": &fstest.MapFile{Data: []byte("theirs")},
		"a.txt":  &fstest.MapFile{Data: []byte("a")},
	}

	rep, err := syncTo(t, src, dst, func(o *dsync.Options) {
		o.Delete = true
		o.Rules = scope.Rules{scope.MustParse("marker", false)}
	})
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"a.txt"}; !equal(rep.Added, want) {
		t.Errorf("added = %v, want %v", rep.Added, want)
	}
	if len(rep.Leftover) != 0 || len(rep.Removed) != 0 {
		t.Errorf("excluded key was acted on: leftover %v removed %v", rep.Leftover, rep.Removed)
	}
	check(t, dst, "marker", "mine")
}

// What the rules leave out is missing from both indexes, so it is not
// copied across, not compared, and above all not removed from the
// destination even when deletions were asked for.
func TestOnlyWhatTheRulesNameIsTouched(t *testing.T) {
	dst := t.TempDir()
	seed(t, dst, map[string]string{
		"foo/stale.txt":  "gone from the source",
		"other/keep.txt": "never in scope",
	})

	src := fstest.MapFS{
		"foo/a.txt":      &fstest.MapFile{Data: []byte("a")},
		"foo/deep/b.txt": &fstest.MapFile{Data: []byte("b")},
		"bar/c.txt":      &fstest.MapFile{Data: []byte("c")},
		"other/d.txt":    &fstest.MapFile{Data: []byte("d")},
	}

	rep, err := syncTo(t, src, dst, func(o *dsync.Options) {
		o.Delete = true
		o.Rules = scope.Rules{
			scope.MustParse("*", false),
			scope.MustParse("foo/", true),
		}
	})
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"foo/a.txt", "foo/deep/b.txt"}; !equal(rep.Added, want) {
		t.Errorf("added = %v, want %v", rep.Added, want)
	}
	if want := []string{"foo/stale.txt"}; !equal(rep.Removed, want) {
		t.Errorf("removed = %v, want only the file inside scope the source no longer has", rep.Removed)
	}
	if len(rep.Leftover) != 0 {
		t.Errorf("leftover = %v, want nothing outside scope listed as a deletion", rep.Leftover)
	}

	check(t, dst, "other/keep.txt", "never in scope")
	if _, err := fs.Stat(osDirFS(dst), "other/d.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a file outside the scope was copied: stat = %v", err)
	}
}

// An include naming nothing the source has is a mistyped name far more
// often than a deliberate no-op, and with everything else excluded the
// run would otherwise report a clean success having done nothing.
func TestARuleNamingNothingStopsBeforeAnythingIsWritten(t *testing.T) {
	dst := t.TempDir()
	seed(t, dst, map[string]string{"keep.txt": "untouched"})

	rep, err := syncTo(t, fstest.MapFS{
		"foo/a.txt": &fstest.MapFile{Data: []byte("a")},
	}, dst, func(o *dsync.Options) {
		o.Delete = true
		o.Rules = scope.Rules{
			scope.MustParse("*", false),
			scope.MustParse("nope/", true),
		}
	})
	if !errors.Is(err, dsync.ErrUncovered) {
		t.Fatalf("err = %v, want %v", err, dsync.ErrUncovered)
	}
	if !rep.Empty() {
		t.Errorf("report = %+v, want nothing done", rep)
	}

	check(t, dst, "keep.txt", "untouched")
}

// publisher stands in for a source that reports its own hashes, as a
// tree fetched from elsewhere does.
type publisher struct {
	fs.FS
	idx *index.Index
}

func (p publisher) Index() *index.Index { return p.idx }

// brokenTree stands in for a destination that cannot take writes.
type brokenTree struct {
	tree.Tree
}

func (b *brokenTree) OpenWrite(string) (io.WriteCloser, error) {
	return nil, errors.New("no space left on device")
}

func seed(t *testing.T, dir string, files map[string]string) {
	t.Helper()

	for name, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func check(t *testing.T, dir, name, want string) {
	t.Helper()

	got, err := fs.ReadFile(osDirFS(dir), name)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", name, err)
	}
	if string(got) != want {
		t.Errorf("%q holds %q, want %q", name, got, want)
	}
}

func osDirFS(dir string) fs.FS { return os.DirFS(dir) }

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sorted := append([]string(nil), a...)
	sort.Strings(sorted)
	for i := range sorted {
		if sorted[i] != b[i] {
			return false
		}
	}
	return true
}
