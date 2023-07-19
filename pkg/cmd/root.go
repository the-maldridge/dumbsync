package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "dumbsync",
	Short: "dumbsync is a very simple sync utility",
	Long: `dumbsync produces indexes for file tree, and can verify
	a remote index against a local filetree, downloading or
	removing files as necessary to achieve a mirror of the content
	at the source.`,
}

// Execute is the main entrypoint of the command tree.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
