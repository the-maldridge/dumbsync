package index

import (
	"bytes"
	"io/fs"
	"os"
	"strings"
	"sync"

	"github.com/the-maldridge/dumbsync/pkg/scope"
)

// Options describes how a tree is hashed into an index.
type Options struct {
	// Hash is the algorithm used for every file.  Callers syncing two
	// trees should take it from the index of the tree they are copying
	// from, so that both ends agree without anyone having to configure
	// the same value twice.
	Hash HashType

	// Rules decides which keys the index is allowed to describe.  What
	// a rule drops is absent from the result on both ends of a sync, so
	// it is neither copied, nor compared, nor removed.  An empty set of
	// rules describes the whole tree.
	Rules scope.Rules
}

// IndexFS hashes every file below fsys and returns an Index describing
// the tree.  Names in the result are relative to fsys and use forward
// slashes, which is what makes an index from one machine comparable to
// an index from another.
func IndexFS(fsys fs.FS, o Options) (*Index, error) {
	return walk(fsys, o.Hash, o.Rules)
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
		if len(o.Rules) == 0 {
			return idx, nil
		}

		// The published index belongs to the file system that produced
		// it, so narrowing it to a scope produces a new index rather
		// than removing keys from the one still being served.
		scoped := &Index{
			Files:      make(map[string][]byte, len(idx.Files)),
			HashType:   idx.HashType,
			Incomplete: idx.Incomplete,
			Errors:     idx.Errors,
			Mutex:      new(sync.Mutex),
		}
		for name, sum := range idx.Files {
			if o.Rules.Keep(name) {
				scoped.Files[name] = sum
			}
		}

		return scoped, nil
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
