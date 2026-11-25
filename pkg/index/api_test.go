package index

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/the-maldridge/dumbsync/pkg/scope"
)

func TestIndexFSUsesTheRequestedAlgorithm(t *testing.T) {
	files := fstest.MapFS{
		"dir/nested.txt": &fstest.MapFile{Data: []byte("nested")},
		"top.txt":        &fstest.MapFile{Data: []byte("top")},
		"skipme.json":    &fstest.MapFile{Data: []byte("manifest")},
	}

	idx, err := IndexFS(files, Options{Hash: XXHash, Rules: scope.Rules{scope.MustParse("skipme.json", false)}})
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

	idx, err := IndexFS(files, Options{Rules: scope.Rules{scope.MustParse("vcs", false)}})
	if err != nil {
		t.Fatal(err)
	}

	if got := sortedKeys(idx); len(got) != 1 || got[0] != "keep/a.txt" {
		t.Errorf("keys = %v, want only keep/a.txt", got)
	}
}

func TestIndexFSBringsBackWhatALaterRuleNames(t *testing.T) {
	files := fstest.MapFS{
		"foo/a.txt":      &fstest.MapFile{Data: []byte("a")},
		"foo/deep/b.txt": &fstest.MapFile{Data: []byte("b")},
		"bar/c.txt":      &fstest.MapFile{Data: []byte("c")},
		"other/d.txt":    &fstest.MapFile{Data: []byte("d")},
		"foo.txt":        &fstest.MapFile{Data: []byte("not the directory")},
	}

	idx, err := IndexFS(files, Options{Rules: scope.Rules{
		scope.MustParse("*", false),
		scope.MustParse("foo/", true),
		scope.MustParse("bar/", true),
	}})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"bar/c.txt", "foo/a.txt", "foo/deep/b.txt"}
	if got := sortedKeys(idx); !reflect.DeepEqual(got, want) {
		t.Errorf("keys = %v, want %v", got, want)
	}
}

// A directory nobody asked for is allowed to be unreadable: nothing is
// copied out of it and nothing is removed from it, so a permission error
// there is not a reason to call the whole tree untrustworthy.
func TestUnreadableDirectoryOutsideScopeStaysComplete(t *testing.T) {
	files := unreadableFS{
		MapFS: fstest.MapFS{
			"keep/a.txt":        &fstest.MapFile{Data: []byte("a")},
			"secret/b.txt":      &fstest.MapFile{Data: []byte("b")},
			"secret/deep/c.txt": &fstest.MapFile{Data: []byte("c")},
			"other/d.txt":       &fstest.MapFile{Data: []byte("d")},
		},

		denied: "secret",
	}

	idx, err := IndexFS(files, Options{Rules: scope.Rules{
		scope.MustParse("**", false),
		scope.MustParse("keep/", true),
		scope.MustParse("other/", true),
	}})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"keep/a.txt", "other/d.txt"}
	if got := sortedKeys(idx); !reflect.DeepEqual(got, want) {
		t.Errorf("keys = %v, want %v", got, want)
	}
	if idx.Incomplete {
		t.Errorf("a subtree outside the scope made the index incomplete: %v", idx.Errors)
	}
}

// A rule that can only name something deeper than the directories the
// walk would skip has to keep the walk going, and a wide tree is where
// skipping one branch too many would go unnoticed.
func TestPruningNeverLosesWhatIsInScope(t *testing.T) {
	files := make(fstest.MapFS, 200)
	for dir := range 20 {
		for file := range 10 {
			files[fmt.Sprintf("dir%02d/file%d", dir, file)] = &fstest.MapFile{Data: []byte("body")}
		}
	}

	idx, err := IndexFS(files, Options{Rules: scope.Rules{
		scope.MustParse("**", false),
		scope.MustParse("dir03/", true),
		scope.MustParse("dir17/", true),
	}})
	if err != nil {
		t.Fatal(err)
	}

	var want []string
	for _, dir := range []string{"dir03", "dir17"} {
		for file := range 10 {
			want = append(want, fmt.Sprintf("%s/file%d", dir, file))
		}
	}
	slices.Sort(want)

	if got := sortedKeys(idx); !reflect.DeepEqual(got, want) {
		t.Errorf("keys = %v, want %d keys from the two directories named", got, len(want))
	}
}

// unreadableFS cannot read one directory or anything below it, standing
// in for a subtree the operator has no business reading.
type unreadableFS struct {
	fstest.MapFS

	denied string
}

func (u unreadableFS) Open(name string) (fs.File, error) {
	if name == u.denied || strings.HasPrefix(name, u.denied+"/") {
		return nil, fs.ErrPermission
	}

	return u.MapFS.Open(name)
}

func TestIndexOfUsesPublishedHashesWithoutReading(t *testing.T) {
	want := &Index{
		Files: map[string][]byte{
			"a.txt":     []byte("published"),
			"drop.json": []byte("also published"),
		},
		HashType: XXHash,
	}

	got, err := IndexOf(publishedFS{MapFS: fstest.MapFS{
		"a.txt":     &fstest.MapFile{Data: []byte("different on disk")},
		"drop.json": &fstest.MapFile{Data: []byte("x")},
	}, idx: want}, Options{Hash: MD5, Rules: scope.Rules{scope.MustParse("drop.json", false)}})
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

	// Narrowing what one caller is allowed to see does not narrow the
	// index for anyone else using the same file system.
	if len(want.Files) != 2 {
		t.Errorf("the published index itself was changed to %v", want.Files)
	}
}

// What a publisher said about the completeness of its list has to survive
// the trip, and a manifest written before the claim existed has to arrive
// as a description of a whole tree so that nothing changes underneath a
// client still running an old build.
func TestSubsetClaimSurvivesTheWireFormat(t *testing.T) {
	data, err := json.Marshal(&Index{
		Files:  map[string][]byte{"a.txt": []byte("x")},
		Subset: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	decoded := new(Index)
	if err := json.Unmarshal(data, decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.Subset {
		t.Errorf("%s decoded as a whole tree", data)
	}

	old := new(Index)
	if err := json.Unmarshal([]byte(`{"Files":{"a.txt":"eJw="},"HashType":0,"Incomplete":false}`), old); err != nil {
		t.Fatal(err)
	}
	if old.Subset || old.Incomplete {
		t.Errorf("a manifest predating both flags decoded as subset %v incomplete %v", old.Subset, old.Incomplete)
	}
}

// Narrowing somebody else's list must not make the holes it left out
// unmentioned.
func TestNarrowingASubsetKeepsItASubset(t *testing.T) {
	published := &Index{
		Files:  map[string][]byte{"foo/a.txt": nil, "bar/b.txt": nil},
		Subset: true,
	}

	got, err := IndexOf(publishedFS{MapFS: fstest.MapFS{}, idx: published}, Options{
		Rules: scope.Rules{
			scope.MustParse("*", false),
			scope.MustParse("foo/", true),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(got.Files) != 1 {
		t.Fatalf("files = %v, want only the one in scope", got.Files)
	}
	if !got.Subset {
		t.Error("narrowing a subset manifest lost the claim that it is one")
	}
}

// publishedFS stands in for a file system whose hashes come from
// somewhere else, such as a manifest fetched over HTTP.
type publishedFS struct {
	fstest.MapFS
	idx *Index
}

func (p publishedFS) Index() *Index { return p.idx }

func TestKeysListsWhatAnIndexDescribes(t *testing.T) {
	idx := &Index{Files: map[string][]byte{"a.txt": nil, "sub/b.txt": nil}}

	got := idx.Keys()
	slices.Sort(got)

	if want := []string{"a.txt", "sub/b.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("keys = %v, want %v", got, want)
	}
}

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
