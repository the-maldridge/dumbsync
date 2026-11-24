// dumbsync-index writes a manifest describing the checksums of every
// file below a directory. Serve the manifest and the files it names from
// the same root and dumbsync can tell what it is missing.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/the-maldridge/dumbsync/pkg/index"
)

var (
	indexFilePath = flag.String("index", "dumbsync.json", "Index filename")
)

func main() {
	os.Exit(run())
}

func run() int {
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "Usage: dumbsync-index <path>")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "You must specify a path to index!")
		return 2
	}

	// A manifest describes files by their name inside the tree, so it has
	// to leave itself out by that name rather than by wherever it happens
	// to be written.
	exclude, err := manifestKey(flag.Arg(0), *indexFilePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error while resolving the index path:", err)
		return 2
	}

	idx, err := index.IndexFS(os.DirFS(flag.Arg(0)), index.Options{
		Hash:    index.HashTypeFromEnv(),
		Exclude: exclude,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error while indexing path:", err)
		return 1
	}

	// A manifest for a tree that could not be read completely is still
	// written, marked as such, so anything syncing from it can tell a tree
	// that really did lose files apart from one that was only misread.
	if idx.Incomplete {
		for _, problem := range idx.Errors {
			fmt.Fprintln(os.Stderr, "Could not read:", problem)
		}
	}

	data, err := json.Marshal(idx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error while marhsalling index:", err)
		return 1
	}

	if err := os.WriteFile(*indexFilePath, data, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "Error while writing index:", err)
		return 1
	}

	if idx.Incomplete {
		return 1
	}

	return 0
}

// manifestKey returns the name the manifest has inside the tree being
// indexed, and nothing at all when it is written outside of that tree.
func manifestKey(treePath, manifestPath string) ([]string, error) {
	root, err := filepath.Abs(treePath)
	if err != nil {
		return nil, err
	}

	manifest, err := filepath.Abs(manifestPath)
	if err != nil {
		return nil, err
	}

	rel, err := filepath.Rel(root, manifest)
	if err != nil {
		return nil, err
	}

	key := filepath.ToSlash(rel)
	if key == ".." || strings.HasPrefix(key, "../") {
		return nil, nil
	}

	return []string{key}, nil
}
