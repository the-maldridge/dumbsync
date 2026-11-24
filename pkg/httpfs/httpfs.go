// Package httpfs presents a tree published by dumbsync over HTTP as a
// rooted file system.
//
// The tree is described by an index fetched from the server, so the
// names, and the hashes of what they stand for, are known before a
// single byte of content is read. That makes the far end of a sync the
// same shape as a local directory: both are an fs.FS that can answer
// what it holds, and only the bytes behind an Open differ.
package httpfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/the-maldridge/dumbsync/pkg/index"
)

// errNotDir stands in for ENOTDIR without pulling syscall into a
// package that has to build for every platform goreleaser targets.
var errNotDir = errors.New("not a directory")

// FS is the tree served under a URL, described by an index that was
// fetched from there.
type FS struct {
	client *http.Client
	base   url.URL
	idx    *index.Index

	// dirs holds the directories implied by the index keys, so that a
	// walk sees a tree rather than a flat list of names.
	dirs map[string]bool
}

// New returns the tree served at base, described by idx. The base is
// the root of the served tree, not the location of the index itself.
func New(client *http.Client, base string, idx *index.Index) (*FS, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}

	f := &FS{client: client, base: *u, idx: idx, dirs: map[string]bool{}}
	for name := range idx.Files {
		for dir := path.Dir(name); dir != "."; dir = path.Dir(dir) {
			f.dirs[dir] = true
		}
	}

	return f, nil
}

// Index returns the hashes published by the server. They describe the
// content Open will return, so the other end of a sync can compare and
// verify without re-reading anything here.
func (f *FS) Index() *index.Index { return f.idx }

// Open returns the content stored at name, fetching it from the server.
func (f *FS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}

	if name == "." || f.dirs[name] {
		return &dirFile{info: f.info(name), entries: f.list(name)}, nil
	}

	u := f.base
	u.Path = path.Join(u.Path, name)

	resp, err := f.client.Get(u.String())
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, &fs.PathError{Op: "open", Path: name, Err: fmt.Errorf("%s returned %s", u.String(), resp.Status)}
	}

	return &openFile{info: f.info(name), body: resp.Body}, nil
}

// Stat answers from the index, which already knows every name in the
// tree. Sizes and modification times are not published, so they are
// reported as unknown.
func (f *FS) Stat(name string) (fs.FileInfo, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrInvalid}
	}

	if name != "." && !f.dirs[name] {
		if _, ok := f.idx.Files[name]; !ok {
			return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
		}
	}

	return f.info(name), nil
}

// ReadDir lists the names directly below name.
func (f *FS) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}

	if name != "." && !f.dirs[name] {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: errNotDir}
	}

	entries := f.list(name)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

// info describes a name without contacting the server.
func (f *FS) info(name string) fileInfo {
	if name == "." || f.dirs[name] {
		return fileInfo{name: path.Base(name), dir: true}
	}

	return fileInfo{name: path.Base(name)}
}

// list returns the immediate children of dir.
func (f *FS) list(dir string) []fs.DirEntry {
	prefix := ""
	if dir != "." {
		prefix = dir + "/"
	}

	seen := map[string]fs.DirEntry{}
	for name := range f.idx.Files {
		if !strings.HasPrefix(name, prefix) || name == dir {
			continue
		}

		rest := strings.TrimPrefix(name, prefix)
		if i := strings.Index(rest, "/"); i >= 0 {
			child := prefix + rest[:i]
			if _, ok := seen[rest[:i]]; !ok {
				seen[rest[:i]] = dirEntry{info: fileInfo{name: path.Base(child), dir: true}}
			}
			continue
		}

		seen[rest] = dirEntry{info: f.info(name)}
	}

	entries := make([]fs.DirEntry, 0, len(seen))
	for _, e := range seen {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	return entries
}

type fileInfo struct {
	name string
	dir  bool
}

func (i fileInfo) Name() string { return i.name }

func (i fileInfo) Size() int64 { return -1 }

func (i fileInfo) ModTime() time.Time { return time.Time{} }

func (i fileInfo) IsDir() bool { return i.dir }

func (i fileInfo) Sys() any { return nil }

func (i fileInfo) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0o555
	}
	return 0o444
}

type dirEntry struct{ info fileInfo }

func (e dirEntry) Name() string               { return e.info.Name() }
func (e dirEntry) IsDir() bool                { return e.info.IsDir() }
func (e dirEntry) Type() fs.FileMode          { return e.info.Mode().Type() }
func (e dirEntry) Info() (fs.FileInfo, error) { return e.info, nil }

// openFile is a fetched file. The name comes from the index so it is
// stable regardless of how the server spells the URL it answered.
type openFile struct {
	info fileInfo
	body io.ReadCloser
}

func (f *openFile) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f *openFile) Read(p []byte) (int, error) { return f.body.Read(p) }
func (f *openFile) Close() error               { return f.body.Close() }

// dirFile has no content of its own, only children.
type dirFile struct {
	info    fileInfo
	entries []fs.DirEntry
	offset  int
}

func (d *dirFile) Stat() (fs.FileInfo, error) { return d.info, nil }
func (d *dirFile) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.info.Name(), Err: fs.ErrInvalid}
}
func (d *dirFile) Close() error { return nil }

func (d *dirFile) ReadDir(n int) ([]fs.DirEntry, error) {
	left := len(d.entries) - d.offset
	if n <= 0 {
		entries := d.entries[d.offset:]
		d.offset = len(d.entries)
		return entries, nil
	}
	if left < n {
		n = left
	}
	if n == 0 {
		return nil, io.EOF
	}

	entries := d.entries[d.offset : d.offset+n]
	d.offset += n
	return entries, nil
}
