// Package tree provides a writable file tree with a root that
// operations cannot escape.
//
// A sync destination is addressed by the same keys as the index
// describing it, but unlike a source it has to be written to, and a
// key that resolves outside the destination is a bug waiting to become
// data loss. The trees here reject such names, and because they are
// rooted at an open descriptor a symbolic link inside the tree cannot
// be used to reach a location outside it either.
package tree

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
)

// Tree is a file tree rooted at a single directory, addressed by keys
// relative to that root.
//
// Keys are the same names an index uses: unrooted, forward slashes, no
// .. segment. Roots belong to Open and never appear in a key.
type Tree interface {
	// FS returns a read-only view of the tree, which is how the
	// destination is hashed to find out what it already holds.
	FS() fs.FS

	// MkdirAll creates dir and any missing parents of it.
	MkdirAll(dir string) error

	// OpenWrite creates or truncates the file at key. The parent
	// directory must already exist.
	OpenWrite(key string) (io.WriteCloser, error)

	// Rename moves from onto to, replacing to if it exists. Both keys
	// are in this tree, so a rename cannot move content out of it.
	Rename(from, to string) error

	// RemoveAll deletes key and anything below it.
	RemoveAll(key string) error

	// Name returns the directory this tree is rooted at, for messages
	// about where something was written.
	Name() string

	// Close releases the reference to the root directory.
	Close() error
}

// Open returns the tree rooted at dir, creating it if it does not exist
// yet.
func Open(dir string) (Tree, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}

	return &osTree{root: root, dir: dir}, nil
}

type osTree struct {
	root *os.Root
	dir  string
}

func (t *osTree) FS() fs.FS { return t.root.FS() }

func (t *osTree) Name() string { return t.dir }

func (t *osTree) MkdirAll(dir string) error {
	if _, err := writable(dir); err != nil {
		return err
	}

	return t.root.MkdirAll(dir, 0o755)
}

func (t *osTree) OpenWrite(key string) (io.WriteCloser, error) {
	if _, err := writable(key); err != nil {
		return nil, err
	}

	return t.root.Create(key)
}

func (t *osTree) Rename(from, to string) error {
	if _, err := writable(from); err != nil {
		return err
	}
	if _, err := writable(to); err != nil {
		return err
	}

	return t.root.Rename(from, to)
}

func (t *osTree) RemoveAll(key string) error {
	if _, err := writable(key); err != nil {
		return err
	}

	return t.root.RemoveAll(key)
}

func (t *osTree) Close() error { return t.root.Close() }

// writable checks that name could address something inside a tree. The
// root itself is refused: it is the caller's own directory, and no key
// operation should be able to touch it.
func writable(name string) (string, error) {
	if !fs.ValidPath(name) || name == "." {
		return "", fmt.Errorf("%q does not address a file inside the tree", name)
	}

	return path.Clean(name), nil
}

// TempKey returns a key for a scratch file beside key, in the same
// directory so that Rename can swap the two without ever leaving the
// destination without a complete file.
func TempKey(key string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail in practice; a name that is only
		// unique per process is better than no scratch file at all.
		return path.Join(path.Dir(key), ".dumbsync-"+path.Base(key))
	}

	return path.Join(path.Dir(key), ".dumbsync-"+hex.EncodeToString(b[:])+"-"+path.Base(key))
}
