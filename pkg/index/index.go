package index

import (
	"bytes"
	"crypto/md5"
	"errors"
	"io"
	"io/fs"
	"log"
	"os"
	"sync"
)

// IndexPath walks the directory structure below basepath and
// generates an index.
func (i *Indexer) IndexPath(basepath string) (*Index, error) {
	if !fs.ValidPath(basepath) {
		return nil, errors.New("path must be unrooted, cannot start or end with /, cannot contain ..")
	}

	i.basepath = basepath
	i.idx = &Index{Files: make(map[string][]byte), Mutex: new(sync.Mutex)}
	i.idx.fs = os.DirFS(i.basepath)
	i.idx.HashType = MD5

	if err := fs.WalkDir(i.idx.fs, ".", i.walkDir); err != nil {
		return new(Index), err
	}

	i.idx.wg.Wait()
	return i.idx, nil
}

func (i *Indexer) walkDir(path string, d fs.DirEntry, err error) error {
	if err != nil {
		log.Println(err)
		return nil
	}

	if d.IsDir() {
		return nil
	}

	go i.handleFile(path, d)
	i.idx.wg.Add(1)
	return nil
}

func (i *Indexer) handleFile(path string, d fs.DirEntry) {
	defer i.idx.wg.Done()
	f, err := i.idx.fs.Open(path)
	if err != nil {
		log.Println(err)
		return
	}
	defer f.Close()

	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return
	}

	i.idx.Lock()
	i.idx.Files[path] = h.Sum(nil)
	i.idx.Unlock()
}

// ComputeDifference works out what is missing locally from the target
// index and what exists locally that has been removed from the target
// index.
func (i *Indexer) ComputeDifference(target *Index) ([]string, []string) {
	need := []string{}
	dump := []string{}

	remote := make(map[string][]byte, len(target.Files))
	for k, v := range target.Files {
		remote[k] = v
	}

	local := make(map[string][]byte, len(i.idx.Files))
	for k, v := range i.idx.Files {
		local[k] = v
	}

	// Get missing files or files that have changed.
	for rfile, rsum := range remote {
		lsum, ok := local[rfile]
		if !ok || !bytes.Equal(lsum, rsum) {
			need = append(need, rfile)
		}
		delete(remote, rfile)
		delete(local, rfile)
	}

	// Anything left no longer exists in the remote index.
	for lfile := range local {
		dump = append(dump, lfile)
	}

	return need, dump
}
