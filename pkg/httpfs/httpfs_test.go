package httpfs

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"testing/fstest"

	"github.com/the-maldridge/dumbsync/pkg/index"
)

func publish(t *testing.T) (*FS, fstest.MapFS) {
	t.Helper()

	files := fstest.MapFS{
		"a.txt":          &fstest.MapFile{Data: []byte("alpha")},
		"sub/b.txt":      &fstest.MapFile{Data: []byte("bravo")},
		"sub/deep/c.txt": &fstest.MapFile{Data: []byte("charlie")},
	}

	idx, err := index.IndexFS(files, index.Options{Hash: index.XXHash})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.FileServer(http.FS(files)))
	t.Cleanup(srv.Close)

	fsys, err := New(srv.Client(), srv.URL+"/", idx)
	if err != nil {
		t.Fatal(err)
	}

	return fsys, files
}

func TestPublishedIndexIsUsedWithoutReading(t *testing.T) {
	fsys, _ := publish(t)

	if got := fsys.Index(); got.HashType != index.XXHash {
		t.Errorf("index = %v, want the published algorithm", got.HashType)
	}
	if len(fsys.Index().Files) != 3 {
		t.Errorf("published %d files, want 3", len(fsys.Index().Files))
	}
}

func TestContentIsFetchedOnOpen(t *testing.T) {
	fsys, files := publish(t)

	for _, name := range []string{"a.txt", "sub/b.txt", "sub/deep/c.txt"} {
		got, err := fs.ReadFile(fsys, name)
		if err != nil {
			t.Fatalf("ReadFile(%q): %v", name, err)
		}
		if string(got) != string(files[name].Data) {
			t.Errorf("ReadFile(%q) = %q, want %q", name, got, files[name].Data)
		}
	}
}

func TestMissingFileLooksLikeOne(t *testing.T) {
	fsys, _ := publish(t)

	_, err := fsys.Open("not-here.txt")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open of a missing file = %v, want fs.ErrNotExist", err)
	}
	if _, err := fs.Stat(fsys, "not-here.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Stat of a missing file = %v, want fs.ErrNotExist", err)
	}
}

// The index is a flat list of keys; a walk has to see directories in
// it, otherwise nothing downstream can tell a prefix from a file.
func TestDirectoriesAreImpliedByKeys(t *testing.T) {
	fsys, _ := publish(t)

	info, err := fs.Stat(fsys, "sub/deep")
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Error("sub/deep is not a directory")
	}

	names := listNames(t, fsys, ".")
	if want := []string{"a.txt", "sub"}; !equal(names, want) {
		t.Errorf("root holds %v, want %v", names, want)
	}

	names = listNames(t, fsys, "sub")
	if want := []string{"b.txt", "deep"}; !equal(names, want) {
		t.Errorf("sub holds %v, want %v", names, want)
	}
}

func TestWalkSeesEveryKey(t *testing.T) {
	fsys, files := publish(t)

	var got []string
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			got = append(got, name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var want []string
	for name := range files {
		want = append(want, name)
	}
	sort.Strings(want)

	if !equal(got, want) {
		t.Errorf("walk found %v, want %v", got, want)
	}
}

func TestBehavesLikeAFileSystem(t *testing.T) {
	fsys, _ := publish(t)

	if err := fstest.TestFS(fsys, "a.txt", "sub/b.txt", "sub/deep/c.txt"); err != nil {
		t.Error(err)
	}
}

func TestRejectsNamesThatAreNotKeys(t *testing.T) {
	fsys, _ := publish(t)

	for _, name := range []string{"/etc/passwd", "../a.txt", "./a.txt", "a.txt/"} {
		if _, err := fsys.Open(name); !errors.Is(err, fs.ErrInvalid) {
			t.Errorf("Open(%q) = %v, want fs.ErrInvalid", name, err)
		}
	}
}

func listNames(t *testing.T, fsys fs.FS, dir string) []string {
	t.Helper()

	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		t.Fatal(err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
