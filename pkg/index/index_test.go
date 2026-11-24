package index

import (
	"encoding/json"
	"fmt"
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
	added, removed, changed := local.Difference(&remote)
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
		write(t, root, name, name)
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

func TestIndexPathBoundedPoolStillIndexesEverything(t *testing.T) {
	root := t.TempDir()
	const files = 200

	want := make([]string, 0, files)
	for n := range files {
		name := fmt.Sprintf("dir%02d/file%03d.txt", n%7, n)
		write(t, root, name, name)
		want = append(want, name)
	}

	i := new(Indexer)
	idx, err := i.IndexPath(root)
	if err != nil {
		t.Fatal(err)
	}

	sort.Strings(want)
	if got := sortedKeys(idx); !reflect.DeepEqual(got, want) {
		t.Errorf("indexed %d files, want %d", len(got), len(want))
	}
}

// A file that cannot be read must not silently vanish from the index:
// the far end would read its absence as a deletion.
func TestUnreadableFileMarksTheIndexIncomplete(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, file permissions are not enforced")
	}

	root := t.TempDir()
	write(t, root, "present.txt", "present")
	write(t, root, "unreadable.txt", "unreadable")
	if err := os.Chmod(filepath.Join(root, "unreadable.txt"), 0o000); err != nil {
		t.Fatal(err)
	}

	i := new(Indexer)
	idx, err := i.IndexPath(root)
	if err != nil {
		t.Fatalf("IndexPath should report read failures on the index, not fail the walk: %v", err)
	}

	if !idx.Incomplete {
		t.Error("index from an unreadable tree is not marked incomplete")
	}
	if _, ok := idx.Files["unreadable.txt"]; ok {
		t.Error("unreadable file was given a hash")
	}
	if _, ok := idx.Files["present.txt"]; !ok {
		t.Error("readable sibling was dropped from the index")
	}
	if len(idx.Errors) == 0 {
		t.Error("no explanation recorded for the incomplete index")
	}

	// The flag has to survive the trip to the other end of a sync.
	data, err := json.Marshal(idx)
	if err != nil {
		t.Fatal(err)
	}
	decoded := new(Index)
	if err := json.Unmarshal(data, decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.Incomplete {
		t.Error("incomplete flag lost across the wire format")
	}
}

func TestCleanTreeIsComplete(t *testing.T) {
	root := t.TempDir()
	write(t, root, "one.txt", "one")
	write(t, root, "dir/two.txt", "two")

	i := new(Indexer)
	idx, err := i.IndexPath(root)
	if err != nil {
		t.Fatal(err)
	}
	if idx.Incomplete || len(idx.Errors) != 0 {
		t.Errorf("clean tree reported incomplete: %v %v", idx.Incomplete, idx.Errors)
	}
}

func write(t *testing.T, root, name, body string) {
	t.Helper()

	full := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
