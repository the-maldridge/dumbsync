package index

import (
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
