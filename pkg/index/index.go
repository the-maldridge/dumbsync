package index

import (
	"crypto/md5"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// IndexPath walks the directory structure below basepath and
// generates an index.
func (i *Indexer) IndexPath(basepath string) (*Index, error) {
	i.basepath = basepath
	i.idx = &Index{Files: make(map[string]FileData), Mutex: new(sync.Mutex)}

	if err := fs.WalkDir(os.DirFS(basepath), basepath, i.walkDir); err != nil {
		return new(Index), err
	}

	i.idx.wg.Wait()
	return i.idx, nil
}

func (i *Indexer) walkDir(path string, d fs.DirEntry, err error) error {
	if err != nil {
		return err
	}

	if d.IsDir() {
		return nil
	}

	go i.handleFile(path, d)
	i.idx.wg.Add(1)
	return nil
}

func (i *Indexer) handleFile(path string, d fs.DirEntry) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return
	}

	fdat := FileData{HashType: MD5, HashValue: h.Sum(nil)}

	rpath, _ := filepath.Rel(i.basepath, path)
	i.idx.Lock()
	i.idx.Files[rpath] = fdat
	i.idx.Unlock()
	i.idx.wg.Done()
}
