package index

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestComputeDifference(t *testing.T) {
	local := Index{Files: map[string][]byte{
		"a": []byte("deadbeef"),
		"b": []byte("deadbeef"),
		"c": []byte("deadbeef"),
		"e": []byte("deadbeef"),
	}}
	remote := Index{Files: map[string][]byte{
		"a": []byte("caffeeee"),
		"c": []byte("deadbeef"),
		"d": []byte("deadbeef"),
		"e": []byte("deadbeef"),
	}}
	added, removed, changed := local.computeDifference(&remote)
	if len(added) != 1 || len(removed) != 1 || len(changed) != 1 {
		t.Fatal()
	}
	if added[0] != "d" {
		t.Fatal()
	}
	if removed[0] != "b" {
		t.Fatal()
	}
	if changed[0] != "a" {
		t.Fatal()
	}
}

func TestIndexPathKeysAreRelativeToTheRoot(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"top.txt", "sub/nested.txt"} {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// A root is an operating system path, so absolute roots and roots
	// with a trailing separator are both acceptable and must produce
	// the same relative keys.
	for _, root := range []string{root, root + string(os.PathSeparator)} {
		i := new(Indexer)
		idx, err := i.IndexPath(root)
		if err != nil {
			t.Fatalf("IndexPath(%q): %v", root, err)
		}

		want := []string{"sub/nested.txt", "top.txt"}
		if got := sortedKeys(idx); !reflect.DeepEqual(got, want) {
			t.Errorf("IndexPath(%q) keys = %v, want %v", root, got, want)
		}
	}
}

func TestPruneFileRejectsNonKeys(t *testing.T) {
	i := new(Indexer)
	if _, err := i.IndexPath(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	i.idx.Files["keep.txt"] = []byte("deadbeef")

	for _, name := range []string{"/keep.txt", "../keep.txt", "./keep.txt", "keep.txt/", ""} {
		if err := i.PruneFile(name); err == nil {
			t.Errorf("PruneFile(%q) accepted a name that cannot be a key", name)
		}
	}

	if err := i.PruneFile("keep.txt"); err != nil {
		t.Errorf("PruneFile(keep.txt): %v", err)
	}
	if len(i.idx.Files) != 0 {
		t.Errorf("PruneFile left %v in the index", i.idx.Files)
	}
}

func sortedKeys(idx *Index) []string {
	keys := make([]string, 0, len(idx.Files))
	for k := range idx.Files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
