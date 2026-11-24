package index

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
)

// IndexPath walks the directory structure below basepath and
// generates an index.
//
// basepath names a directory on the local system and is interpreted
// by the operating system, so it may be absolute or relative.  The
// keys it produces are always relative to basepath and use forward
// slashes; nothing below here accepts a rooted path.
func (i *Indexer) IndexPath(basepath string) (*Index, error) {
	idx, err := indexFS(os.DirFS(basepath))
	i.idx = idx
	return idx, err
}

// PruneFile removes a file from the index by its key, which must be
// relative to the tree that was indexed.  Roots belong to IndexPath,
// so a name that could never be a key, for example one that is
// absolute, contains .., or uses the native separator, is reported
// instead of silently matching nothing.
func (i *Indexer) PruneFile(file string) error {
	if !fs.ValidPath(file) {
		return fmt.Errorf("%q is not a key relative to the indexed tree", file)
	}

	delete(i.idx.Files, file)
	return nil
}

// ComputeDifference works out what is missing locally from the target
// index and what exists locally that has been removed from the target
// index.
func (i *Indexer) ComputeDifference(target *Index) ([]string, []string, []string) {
	return i.idx.computeDifference(target)
}

func (i *Index) computeDifference(target *Index) ([]string, []string, []string) {
	added := []string{}
	removed := []string{}
	changed := []string{}

	remote := make(map[string][]byte, len(target.Files))
	for k, v := range target.Files {
		remote[k] = v
	}

	local := make(map[string][]byte, len(i.Files))
	for k, v := range i.Files {
		local[k] = v
	}

	// Get missing files or files that have changed.
	for rfile, rsum := range remote {
		lsum, ok := local[rfile]
		if !ok {
			added = append(added, rfile)
		} else if !bytes.Equal(lsum, rsum) {
			changed = append(changed, rfile)
		}
		delete(remote, rfile)
		delete(local, rfile)
	}

	// Anything left no longer exists in the remote index.
	for lfile := range local {
		removed = append(removed, lfile)
	}

	return added, removed, changed
}
