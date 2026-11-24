package tree

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openTree(t *testing.T) (Tree, string) {
	t.Helper()

	dir := t.TempDir()
	tr, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })

	if tr.Name() != dir {
		t.Errorf("Name() = %q, want the root %q", tr.Name(), dir)
	}

	return tr, dir
}

func TestWriteRenameAndRemove(t *testing.T) {
	tr, _ := openTree(t)

	if err := tr.MkdirAll("sub/deep"); err != nil {
		t.Fatal(err)
	}

	f, err := tr.OpenWrite("sub/deep/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(f, "written")
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// The destination is hashed through FS() to decide what still needs
	// copying, so what was written has to be visible there.
	got, err := fs.ReadFile(tr.FS(), "sub/deep/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "written" {
		t.Errorf("read back %q, want %q", got, "written")
	}

	// A staged file has to replace the real one in a single step, which
	// is the whole point of staging.
	f, err = tr.OpenWrite(TempKey("sub/deep/file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(f, "replacement")
	f.Close()

	staged := stagedKey(t, tr, "sub/deep/file.txt")
	if err := tr.Rename(staged, "sub/deep/file.txt"); err != nil {
		t.Fatalf("rename over an existing file: %v", err)
	}

	got, err = fs.ReadFile(tr.FS(), "sub/deep/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "replacement" {
		t.Errorf("after rename read %q, want %q", got, "replacement")
	}

	if err := tr.RemoveAll("sub"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat(tr.FS(), "sub"); !os.IsNotExist(err) {
		t.Errorf("RemoveAll left sub behind: %v", err)
	}
}

// A destination that already contains a link out of the tree must not
// become a way to write anywhere else.
func TestWritesCannotLeaveTheRoot(t *testing.T) {
	tr, root := openTree(t)

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("untouched"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.txt")); err != nil {
		t.Fatal(err)
	}

	names := []string{
		"/etc/passwd",
		"../outside.txt",
		"sub/../../outside.txt",
		".",
		"",
	}

	for _, name := range names {
		if _, err := tr.OpenWrite(name); err == nil {
			t.Errorf("OpenWrite(%q) was accepted", name)
		}
		if err := tr.RemoveAll(name); err == nil {
			t.Errorf("RemoveAll(%q) was accepted", name)
		}
	}

	// Writing through the link is refused outright.
	if _, err := tr.OpenWrite("escape.txt"); err == nil {
		t.Error("OpenWrite followed a link out of the tree")
	}

	// Deleting the link itself is legitimate, as long as it stops at the
	// link instead of following it.
	if err := tr.RemoveAll("escape.txt"); err != nil {
		t.Errorf("RemoveAll of a link inside the tree: %v", err)
	}

	body, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "untouched" {
		t.Errorf("destination reached outside its root, file now holds %q", body)
	}
}

func TestRenameCannotCrossTheRoot(t *testing.T) {
	tr, root := openTree(t)

	if err := tr.MkdirAll("keep"); err != nil {
		t.Fatal(err)
	}
	f, err := tr.OpenWrite("keep/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(f, "data")
	f.Close()

	if err := tr.Rename("keep/file.txt", "../escaped.txt"); err == nil {
		t.Error("rename out of the root was accepted")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escaped.txt")); err == nil {
		t.Error("rename wrote outside the root")
	}
}

func TestTempKeyStaysBesideTheFile(t *testing.T) {
	for _, key := range []string{"file.txt", "a/b/c/file.txt"} {
		tmp := TempKey(key)
		if filepath.Dir(tmp) != filepath.Dir(key) {
			t.Errorf("TempKey(%q) = %q, not in the same directory", key, tmp)
		}
		if !strings.HasPrefix(filepath.Base(tmp), ".dumbsync-") {
			t.Errorf("TempKey(%q) = %q, does not look like scratch", key, tmp)
		}
	}

	if TempKey("a.txt") == TempKey("a.txt") {
		t.Error("TempKey reused the same name for two staged files")
	}
}

// stagedKey finds the scratch file created for key, so the test uses
// the name the tree chose rather than guessing it.
func stagedKey(t *testing.T, tr Tree, key string) string {
	t.Helper()

	dir := filepath.Dir(key)
	entries, err := fs.ReadDir(tr.FS(), dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range entries {
		if e.Name() != filepath.Base(key) && strings.HasPrefix(e.Name(), ".dumbsync-") {
			if dir == "." {
				return e.Name()
			}
			return dir + "/" + e.Name()
		}
	}

	t.Fatalf("no staged file beside %q", key)
	return ""
}
