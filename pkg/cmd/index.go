package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/the-maldridge/dumbsync/pkg/index"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(indexCmd)

	indexCmd.Flags().StringVarP(&indexCmdFilePath, "index", "i", "dumbsync.json", "Index File")
}

var (
	indexCmdFilePath string

	indexCmd = &cobra.Command{
		Use:   "index <path>",
		Short: "index produces an index of files",
		Long: `index produces an index rooted at <path> that
	contains the files and sums necessary to determine if the files
	have changed remotely.`,
		Run:  indexCmdRun,
		Args: cobra.ExactArgs(1),
	}
)

func indexCmdRun(cmd *cobra.Command, args []string) {
	i := new(index.Indexer)

	idx, err := i.IndexPath(args[0])
	if err != nil {
		fmt.Println(err)
		return
	}

	for f, h := range idx.Files {
		fmt.Printf("%X: %s\n", h, f)
	}

	bytes, err := json.Marshal(idx)
	if err != nil {
		fmt.Println(err)
		return
	}

	if err := os.WriteFile(indexCmdFilePath, bytes, 0644); err != nil {
		fmt.Println(err)
		return
	}
}
