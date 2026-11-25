package index

import (
	"crypto/md5"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"log"
	"sync"

	"github.com/cespare/xxhash/v2"

	"github.com/the-maldridge/dumbsync/pkg/scope"
)

// workers bounds how many files are hashed at the same time.  Hashing
// one goroutine per file opened as many descriptors as the tree held,
// which is the first thing to fail on a large index.
const workers = 16

// maxProblems bounds how many read failures a single index records.
// Problems travel to the far end of a sync inside the index itself, so
// the list has to stay small enough to be worth transferring.
const maxProblems = 32

// walk hashes every file below fsys into an index, leaving out whatever
// the rules put out of scope.  The index records its algorithm so the
// far end can hash the way this end did.
func walk(fsys fs.FS, hashType HashType, rules scope.Rules) (*Index, error) {
	idx := &Index{
		Files:    make(map[string][]byte),
		HashType: hashType,
		Mutex:    new(sync.Mutex),
	}

	w := &walker{
		idx:   idx,
		fs:    fsys,
		rules: rules,
		slots: make(chan struct{}, workers),
	}

	err := fs.WalkDir(fsys, ".", w.visit)
	w.wg.Wait()
	if err != nil {
		// Mark the partial result unusable for deletions so that a
		// caller which ignores the error still cannot remove anything
		// with it.
		w.problem(".", err)
	}

	return idx, err
}

// walker hashes the files found by a directory walk.
type walker struct {
	idx   *Index
	fs    fs.FS
	rules scope.Rules
	wg    sync.WaitGroup
	slots chan struct{}
}

func (w *walker) visit(name string, d fs.DirEntry, err error) error {
	if err != nil {
		// A directory nobody is allowed to look inside is not a read
		// failure worth calling the whole tree unreadable.  Nothing will
		// be copied out of it, and nothing will be removed from it.
		if !w.inScope(name) {
			return nil
		}

		log.Println(err)
		w.problem(name, err)
		return nil
	}

	if d.IsDir() {
		// Descending is asked for rather than assumed because a rule
		// later in the list can still bring something back from under a
		// directory the earlier rules excluded.
		if name != "." && !w.rules.Descend(name) {
			return fs.SkipDir
		}

		return nil
	}

	if !w.inScope(name) {
		return nil
	}

	// Taking a slot before starting the goroutine keeps both the
	// number of live goroutines and the number of open files at the
	// bound, rather than queueing work in the goroutines themselves.
	w.slots <- struct{}{}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		defer func() { <-w.slots }()
		w.hashFile(name)
	}()

	return nil
}

// inScope reports whether a name found by the walk belongs in the
// index.  The root always does, otherwise the whole tree would
// disappear along with the name used to describe it.
func (w *walker) inScope(name string) bool {
	return name == "." || w.rules.Keep(name)
}

func (w *walker) hashFile(name string) {
	f, err := w.fs.Open(name)
	if err != nil {
		log.Println(err)
		w.problem(name, err)
		return
	}
	defer f.Close()

	h := w.idx.hasher()
	if _, err := io.Copy(h, f); err != nil {
		log.Println(err)
		w.problem(name, err)
		return
	}

	w.idx.Lock()
	w.idx.Files[name] = h.Sum(nil)
	w.idx.Unlock()
}

// problem records that part of the tree could not be read, which makes
// the index unsafe to treat as a complete description of the tree.
func (w *walker) problem(name string, err error) {
	w.idx.Lock()
	defer w.idx.Unlock()

	w.idx.Incomplete = true
	if len(w.idx.Errors) < maxProblems {
		w.idx.Errors = append(w.idx.Errors, fmt.Sprintf("%s: %v", name, err))
	}
}

// hasher returns a hash for the algorithm recorded in the index.
func (idx *Index) hasher() hash.Hash { return NewHash(idx.HashType) }

// NewHash returns a hash for the given algorithm. Anyone writing a file
// for this package should hash the bytes on their way through so that
// what landed can be compared with what the source said it would be.
func NewHash(t HashType) hash.Hash {
	if t == XXHash {
		return xxhash.New()
	}
	return md5.New()
}
