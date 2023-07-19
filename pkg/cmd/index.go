package cmd

import (
	"fmt"

	"github.com/the-maldridge/dumbsync/pkg/index"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(indexCmd)
}

var indexCmd = &cobra.Command{
	Use:   "index <path>",
	Short: "index produces an index of files",
	Long: `index produces an index rooted at <path> that
	contains the files and sums necessary to determine if the files
	have changed remotely.`,
	Run:  indexCmdRun,
	Args: cobra.ExactArgs(1),
}

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
}
