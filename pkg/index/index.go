package index

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// Options describes how a tree is hashed into an index.
type Options struct {
	// Hash is the algorithm used for every file.  Callers syncing two
	// trees should take it from the index of the tree they are copying
	// from, so that both ends agree without anyone having to configure
	// the same value twice.
	Hash HashType

	// Exclude lists keys that are never indexed.  A name is dropped
	// when it matches a file, and a matching directory drops the whole
	// subtree below it.  Names are keys, not operating system paths.
	Exclude []string
}

// IndexFS hashes every file below fsys and returns an Index describing
// the tree.  Names in the result are relative to fsys and use forward
// slashes, which is what makes an index from one machine comparable to
// an index from another.
func IndexFS(fsys fs.FS, o Options) (*Index, error) {
	exclude, err := keySet(o.Exclude)
	if err != nil {
		return nil, err
	}

	return walk(fsys, o.Hash, exclude)
}

// Provider is a file system that already knows the hashes of the files
// it exposes, such as one backed by an index fetched from elsewhere.
// Indexing one costs nothing because no content has to be read.
type Provider interface {
	fs.FS

	// Index returns the hashes this file system publishes for its
	// contents, relative to itself.
	Index() *Index
}

// IndexOf returns the index describing fsys, asking the file system for
// its own hashes when it can supply them and reading the contents
// otherwise.
func IndexOf(fsys fs.FS, o Options) (*Index, error) {
	if p, ok := fsys.(Provider); ok {
		idx := p.Index()

		exclude, err := keySet(o.Exclude)
		if err != nil {
			return nil, err
		}
		for name := range exclude {
			delete(idx.Files, name)
		}

		return idx, nil
	}

	return IndexFS(fsys, o)
}

// HashTypeFromEnv reads the algorithm an operator asked for out of
// DUMBSYNC_HASH.  It only belongs at the end that builds an index; a
// destination takes its algorithm from the index it is being synced
// against.
func HashTypeFromEnv() HashType {
	switch strings.ToUpper(os.Getenv("DUMBSYNC_HASH")) {
	case "XX":
		return XXHash
	default:
		return MD5
	}
}

// Difference compares two indexes of the same algorithm and returns the
// keys the target has that the receiver does not, the keys the receiver
// has that the target does not, and the keys both have with different
// hashes.  The receiver is normally the destination and the target the
// source.
func (i *Index) Difference(target *Index) ([]string, []string, []string) {
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

// keySet validates names that must be keys and returns them for lookup.
func keySet(names []string) (map[string]bool, error) {
	if len(names) == 0 {
		return nil, nil
	}

	set := make(map[string]bool, len(names))
	for _, name := range names {
		if !fs.ValidPath(name) {
			return nil, fmt.Errorf("%q is not a key relative to the tree", name)
		}
		set[name] = true
	}

	return set, nil
}
