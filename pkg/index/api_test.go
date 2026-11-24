package index

import (
	"bytes"
	"encoding/json"
	"testing"
	"testing/fstest"
)

func TestIndexFSUsesTheRequestedAlgorithm(t *testing.T) {
	files := fstest.MapFS{
		"dir/nested.txt": &fstest.MapFile{Data: []byte("nested")},
		"top.txt":        &fstest.MapFile{Data: []byte("top")},
		"skipme.json":    &fstest.MapFile{Data: []byte("manifest")},
	}

	idx, err := IndexFS(files, Options{Hash: XXHash, Exclude: []string{"skipme.json"}})
	if err != nil {
		t.Fatal(err)
	}

	if idx.HashType != XXHash {
		t.Errorf("index records %v, want %v", idx.HashType, XXHash)
	}
	if _, ok := idx.Files["skipme.json"]; ok {
		t.Error("excluded key was indexed")
	}
	if len(idx.Files) != 2 {
		t.Fatalf("indexed %v, want the two files that were not excluded", sortedKeys(idx))
	}
	if idx.Incomplete {
		t.Errorf("clean in-memory tree reported incomplete: %v", idx.Errors)
	}
}

func TestIndexFSExcludesWholeSubtrees(t *testing.T) {
	files := fstest.MapFS{
		"keep/a.txt": &fstest.MapFile{Data: []byte("a")},
		"vcs/a.txt":  &fstest.MapFile{Data: []byte("a")},
		"vcs/deep/b": &fstest.MapFile{Data: []byte("b")},
	}

	idx, err := IndexFS(files, Options{Exclude: []string{"vcs"}})
	if err != nil {
		t.Fatal(err)
	}

	if got := sortedKeys(idx); len(got) != 1 || got[0] != "keep/a.txt" {
		t.Errorf("keys = %v, want only keep/a.txt", got)
	}
}

func TestIndexOfUsesPublishedHashesWithoutReading(t *testing.T) {
	want := &Index{
		Files:    map[string][]byte{"a.txt": []byte("published")},
		HashType: XXHash,
	}

	got, err := IndexOf(publishedFS{MapFS: fstest.MapFS{
		"a.txt":     &fstest.MapFile{Data: []byte("different on disk")},
		"drop.json": &fstest.MapFile{Data: []byte("x")},
	}, idx: want}, Options{Hash: MD5, Exclude: []string{"drop.json"}})
	if err != nil {
		t.Fatal(err)
	}

	// The published digest wins over what is on disk, and the algorithm
	// comes from the source rather than the caller's default.
	if !bytes.Equal(got.Files["a.txt"], []byte("published")) {
		t.Errorf("rehashed content: %q", got.Files["a.txt"])
	}
	if got.HashType != XXHash {
		t.Errorf("hash type = %v, want the published %v", got.HashType, XXHash)
	}
	if _, ok := got.Files["drop.json"]; ok {
		t.Error("excluded key survived a published index")
	}
}

// publishedFS stands in for a file system whose hashes come from
// somewhere else, such as a manifest fetched over HTTP.
type publishedFS struct {
	fstest.MapFS
	idx *Index
}

func (p publishedFS) Index() *Index { return p.idx }

func TestDifferenceIsStableAcrossTheWireFormat(t *testing.T) {
	src, err := IndexFS(fstest.MapFS{
		"a.txt": &fstest.MapFile{Data: []byte("new contents")},
		"b.txt": &fstest.MapFile{Data: []byte("same")},
		"c.txt": &fstest.MapFile{Data: []byte("only in source")},
	}, Options{Hash: MD5})
	if err != nil {
		t.Fatal(err)
	}

	data, err := json.Marshal(src)
	if err != nil {
		t.Fatal(err)
	}
	remote := new(Index)
	if err := json.Unmarshal(data, remote); err != nil {
		t.Fatal(err)
	}

	dst, err := IndexFS(fstest.MapFS{
		"a.txt": &fstest.MapFile{Data: []byte("old contents")},
		"b.txt": &fstest.MapFile{Data: []byte("same")},
		"d.txt": &fstest.MapFile{Data: []byte("leftover")},
	}, Options{Hash: remote.HashType})
	if err != nil {
		t.Fatal(err)
	}

	added, removed, changed := dst.Difference(remote)
	if len(added) != 1 || added[0] != "c.txt" {
		t.Errorf("added = %v, want [c.txt]", added)
	}
	if len(removed) != 1 || removed[0] != "d.txt" {
		t.Errorf("removed = %v, want [d.txt]", removed)
	}
	if len(changed) != 1 || changed[0] != "a.txt" {
		t.Errorf("changed = %v, want [a.txt]", changed)
	}
}

func TestExcludedNamesMustBeKeys(t *testing.T) {
	for _, name := range []string{"/etc/passwd", "../escape", "a/", ""} {
		if _, err := IndexFS(fstest.MapFS{}, Options{Exclude: []string{name}}); err == nil {
			t.Errorf("Exclude %q accepted a name that cannot be a key", name)
		}
	}
}
