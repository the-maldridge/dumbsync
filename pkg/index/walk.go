package index

import (
	"crypto/md5"
	"hash"
	"io"
	"io/fs"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/cespare/xxhash/v2"
)

// workers bounds how many files are hashed at the same time.  Hashing
// one goroutine per file opened as many descriptors as the tree held,
// which is the first thing to fail on a large index.
const workers = 16

// indexFS hashes every file below fsys and returns an Index describing
// the tree.  Names in the resulting Index are relative to fsys.
func indexFS(fsys fs.FS) (*Index, error) {
	idx := &Index{
		Files:    make(map[string][]byte),
		HashType: hashTypeFromEnv(),
		Mutex:    new(sync.Mutex),
	}

	w := &walker{
		idx:   idx,
		fs:    fsys,
		slots: make(chan struct{}, workers),
	}

	if err := fs.WalkDir(fsys, ".", w.visit); err != nil {
		return new(Index), err
	}
	w.wg.Wait()

	return idx, nil
}

// walker hashes the files found by a directory walk.
type walker struct {
	idx   *Index
	fs    fs.FS
	wg    sync.WaitGroup
	slots chan struct{}
}

func (w *walker) visit(name string, d fs.DirEntry, err error) error {
	if err != nil {
		log.Println(err)
		return nil
	}

	if d.IsDir() {
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

func (w *walker) hashFile(name string) {
	f, err := w.fs.Open(name)
	if err != nil {
		log.Println(err)
		return
	}
	defer f.Close()

	h := w.idx.hasher()
	if _, err := io.Copy(h, f); err != nil {
		log.Println(err)
		return
	}

	w.idx.Lock()
	w.idx.Files[name] = h.Sum(nil)
	w.idx.Unlock()
}

// hasher returns a hash for the algorithm recorded in the index.
func (idx *Index) hasher() hash.Hash {
	if idx.HashType == XXHash {
		return xxhash.New()
	}
	return md5.New()
}

// hashTypeFromEnv reads the algorithm to hash with from DUMBSYNC_HASH,
// which is how an operator chooses between the two supported hashes
// without reconfiguring the tree being indexed.
func hashTypeFromEnv() HashType {
	switch strings.ToUpper(os.Getenv("DUMBSYNC_HASH")) {
	case "XX":
		return XXHash
	default:
		return MD5
	}
}
