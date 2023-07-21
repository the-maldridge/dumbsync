package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/the-maldridge/dumbsync/pkg/index"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(syncCmd)

	syncCmd.Flags().StringVarP(&syncCmdFileName, "index", "i", "dumbsync.json", "Index File")
	syncCmd.Flags().IntVarP(&syncCmdThreads, "theads", "t", 10, "Sync Threads")
}

var (
	syncCmdFileName string
	syncCmdThreads  int

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
	du := *u
	du.Path = path.Join(du.Path, syncCmdFileName)

	resp, err := httpClient.Get(du.String())
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
	sort.Strings(need)
	sort.Strings(dump)

	var wg sync.WaitGroup
	limit := make(chan struct{}, syncCmdThreads)
	for _, file := range need {
		go func(f string) {
			wg.Add(1)
			limit <- struct{}{}
			fmt.Println(f)
			syncCmdGetFile(httpClient, *u, f)
			<-limit
			wg.Done()
		}(file)
	}
	wg.Wait()

	for _, file := range dump {
		if err := os.RemoveAll(file); err != nil {
			fmt.Println(err)
		}
	}
}

func syncCmdGetFile(c http.Client, tu url.URL, file string) {
	tu.Path = path.Join(tu.Path, file)
	resp, err := c.Get(tu.String())
	if err != nil {
		fmt.Println(err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		fmt.Println(err)
	}

	f, err := os.Create(file)
	if err != nil {
		fmt.Println(err)
		return
	}
	io.Copy(f, resp.Body)
	f.Close()
	resp.Body.Close()
}
