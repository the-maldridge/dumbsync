package main

import (
	"fmt"
	"os"

	"github.com/the-maldridge/dumbsync/pkg/index"
)

func main() {
	i := new(index.Indexer)

	idx, err := i.IndexPath(os.Args[1])
	if err != nil {
		fmt.Println(err)
		return
	}

	for f, d := range idx.Files {
		fmt.Printf("%X: %s\n", d.HashValue, f)
	}
}
