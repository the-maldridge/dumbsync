package index

import (
	"crypto/md5"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// IndexPath walks the directory structure below basepath and
// generates an index.
func (i *Indexer) IndexPath(basepath string) (*Index, error) {
	i.basepath = basepath
	i.idx = &Index{Files: make(map[string]FileData)}

	if err := fs.WalkDir(os.DirFS(basepath), ".", i.walkDir); err != nil {
		return new(Index), err
	}

	return i.idx, nil
}

func (i *Indexer) walkDir(path string, d fs.DirEntry, err error) error {
	if err != nil {
		return err
	}

	if d.IsDir() {
		return nil
	}

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}

	fdat := FileData{HashType: MD5, HashValue: h.Sum(nil)}

	rpath, _ := filepath.Rel(i.basepath, path)
	i.mux.Lock()
	i.idx.Files[rpath] = fdat
	i.mux.Unlock()

	return nil
}
