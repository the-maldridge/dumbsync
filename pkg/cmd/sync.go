package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"time"

	"github.com/the-maldridge/dumbsync/pkg/index"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(syncCmd)

	syncCmd.Flags().StringVarP(&syncCmdFileName, "index", "i", "dumbsync.json", "Index File")
}

var (
	syncCmdFileName string

	syncCmd = &cobra.Command{
		Use:   "sync <source> <path>",
		Short: "sync makes path equivalent to the source described by index",
		Long: `sync fetches a remote index, compares it to a local filesystem,
and downloads or removes files to make the local filesystem match the
remote one.`,
		Run:  syncCmdRun,
		Args: cobra.ExactArgs(2),
	}
)

func syncCmdRun(cmd *cobra.Command, args []string) {
	httpClient := http.Client{Timeout: time.Second * 10}

	u, err := url.Parse(args[0])
	if err != nil {
		fmt.Println(err)
		return
	}
	u.Path = path.Join(u.Path, syncCmdFileName)

	resp, err := httpClient.Get(u.String())
	if err != nil {
		fmt.Println(err)
		return
	}
	defer resp.Body.Close()

	sidx := new(index.Index)
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(sidx); err != nil {
		fmt.Println(err)
		return
	}

	i := new(index.Indexer)
	if _, err := i.IndexPath(args[1]); err != nil {
		fmt.Println(err)
		return
	}

	need, dump := i.ComputeDifference(sidx)

	fmt.Println("need", need)
	fmt.Println("dump", dump)
}
