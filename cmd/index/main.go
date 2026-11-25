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
	"slices"
	"strings"

	"github.com/the-maldridge/dumbsync/pkg/index"
	"github.com/the-maldridge/dumbsync/pkg/scope"
)

var (
	indexFilePath = flag.String("index", "dumbsync.json", "Index filename")

	// indexRules is a single ordered list that both flags append to, so
	// the order the arguments were typed in survives to the manifest.
	indexRules scope.Rules
)

func init() {
	flag.Var(&scope.RuleFlag{Rules: &indexRules, Include: false}, "exclude", "Leave out everything matching a pattern")
	flag.Var(&scope.RuleFlag{Rules: &indexRules, Include: true}, "include", "Put back everything matching a pattern, over any earlier -exclude")
}

func main() {
	os.Exit(run())
}

func run() int {
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "Usage: dumbsync-index <path>")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "You must specify a path to index!")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "-exclude and -include choose which files the manifest describes. Both")
		fmt.Fprintln(os.Stderr, "may be repeated, and the last rule matching a name decides. A client")
		fmt.Fprintln(os.Stderr, "syncing from this manifest will believe it describes the whole tree,")
		fmt.Fprintln(os.Stderr, "and will remove anything it is told to delete that the manifest omits.")
		return 2
	}

	key, err := manifestKey(flag.Arg(0), *indexFilePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error while resolving the index path:", err)
		return 2
	}

	// The manifest leaves itself out by the name it has inside the tree,
	// added last so that no rule typed earlier can pull it back in.
	rules := slices.Clone(indexRules)
	if key != "" {
		rule, err := scope.Parse(scope.Quote(key), false)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s cannot be left out of its own manifest: %s\n", key, err)
			return 2
		}

		rules = append(rules, rule)
	}

	idx, err := index.IndexFS(os.DirFS(flag.Arg(0)), index.Options{
		Hash:  index.HashTypeFromEnv(),
		Rules: rules,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error while indexing path:", err)
		return 1
	}

	// The same mistake is refused here as at the far end: a pattern that
	// named nothing would publish a manifest describing almost nothing,
	// and a client told to delete would bring the destination down to
	// match it.
	if missing := indexRules.Uncovered(idx.Keys()); len(missing) > 0 {
		for _, rule := range missing {
			fmt.Fprintf(os.Stderr, "%s names no file below %s\n", rule, flag.Arg(0))
		}
		fmt.Fprintln(os.Stderr, "No manifest was written; check the patterns against the tree")
		return 2
	}

	// Whoever serves this manifest and whoever syncs from it both see a
	// subset now, which is worth one line in the log of a scheduled run.
	if len(indexRules) > 0 {
		fmt.Printf("Publishing a subset of %s: %v\n", flag.Arg(0), indexRules)
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
// indexed, and the empty string when it is written outside of that tree.
//
// A manifest describes files by their name inside the tree, so it has to
// leave itself out by that name rather than by wherever it happens to be
// written.
func manifestKey(treePath, manifestPath string) (string, error) {
	root, err := filepath.Abs(treePath)
	if err != nil {
		return "", err
	}

	manifest, err := filepath.Abs(manifestPath)
	if err != nil {
		return "", err
	}

	rel, err := filepath.Rel(root, manifest)
	if err != nil {
		return "", err
	}

	key := filepath.ToSlash(rel)
	if key == ".." || strings.HasPrefix(key, "../") {
		return "", nil
	}

	return key, nil
}
