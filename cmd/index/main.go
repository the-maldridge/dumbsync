package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/the-maldridge/dumbsync/pkg/index"
)

var (
	indexFilePath = flag.String("index", "dumbsync.json", "Index filename")
)

func main() {
	flag.Parse()
	if len(flag.Args()) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: dumbsync-index <path>")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "You must specify a path to index!")
	}

	i := new(index.Indexer)

	idx, err := i.IndexPath(flag.Args()[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error while indexing path:", err)
		return
	}

	bytes, err := json.Marshal(idx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error while marhsalling index:", err)
		return
	}

	if err := os.WriteFile(*indexFilePath, bytes, 0644); err != nil {
		fmt.Fprintln(os.Stderr, "Error while writing index:", err)
		return
	}
}
