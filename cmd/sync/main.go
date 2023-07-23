package main

import (
	"encoding/json"
	"flag"
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
)

var (
	syncFileName = flag.String("index", "dumbsync.json", "Index filename")
	syncThreads  = flag.Int("threads", 10, "Number of threads to use while syncing")
)

func main() {
	flag.Parse()
	if len(flag.Args()) != 2 {
		fmt.Fprintln(os.Stderr, "Usage: dumbsync <url> <path>")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "You must specify a URL to sync from, and a path to sync to!")
	}

	httpClient := http.Client{Timeout: time.Second * 10}

	u, err := url.Parse(flag.Args()[0])
	if err != nil {
		fmt.Println(err)
		return
	}
	du := *u
	du.Path = path.Join(du.Path, *syncFileName)

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
	if _, err := i.IndexPath(flag.Args()[1]); err != nil {
		fmt.Println(err)
		return
	}

	need, dump := i.ComputeDifference(sidx)
	sort.Strings(need)
	sort.Strings(dump)

	var wg sync.WaitGroup
	limit := make(chan struct{}, *syncThreads)
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
