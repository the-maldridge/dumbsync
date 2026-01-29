package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/google/shlex"

	"github.com/the-maldridge/dumbsync/pkg/index"
)

var (
	syncFileName    = flag.String("index", "dumbsync.json", "Index filename")
	syncThreads     = flag.Int("threads", 10, "Number of threads to use while syncing")
	syncCertFile    = flag.String("cert", "", "Client Certificate")
	syncCertKeyFile = flag.String("key", "", "Client Key")
	syncExec        = flag.String("exec", "", "Execute a command when files are changed")
)

func main() {
	flag.Parse()
	if len(flag.Args()) != 2 {
		fmt.Fprintln(os.Stderr, "Usage: dumbsync <url> <path>")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "You must specify a URL to sync from, and a path to sync to!")
		return
	}

	httpClient := http.Client{Timeout: time.Second * 10}

	if *syncCertFile != "" && *syncCertKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(*syncCertFile, *syncCertKeyFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading client certificate: %s\n", err)
			return
		}

		httpClient.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{cert}},
		}
	}

	u, err := url.Parse(flag.Args()[0])
	if err != nil {
		fmt.Println(err)
		return
	}
	du := *u
	du.Path = path.Join(du.Path, *syncFileName)

	fmt.Printf("Synchronizing against %s\n", du.String())
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
	i.PruneFile(filepath.Join(flag.Args()[1], *syncFileName))

	need, dump := i.ComputeDifference(sidx)
	sort.Strings(need)
	sort.Strings(dump)

	var wg sync.WaitGroup
	limit := make(chan struct{}, *syncThreads)
	for _, file := range need {
		wg.Add(1)
		go func(f string) {
			limit <- struct{}{}
			fmt.Printf("[+] %s\n", f)
			syncCmdGetFile(httpClient, *u, f)
			<-limit
			wg.Done()
		}(file)
	}
	wg.Wait()

	for _, file := range dump {
		fmt.Printf("[-] %s\n", file)
		if err := os.RemoveAll(filepath.Join(flag.Args()[1], file)); err != nil {
			fmt.Println(err)
		}
	}

	if *syncExec != "" && ((len(need) > 0) || (len(dump) > 0)) {
		parts, err := shlex.Split(*syncExec)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Could not exec cmd: %s\n", err)
			return
		}
		cmd := exec.Command(parts[0], parts[1:]...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "Command did not complete successfully: %s\n", err)
			return
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
	if err := os.MkdirAll(filepath.Dir(filepath.Join(flag.Args()[1], file)), 0755); err != nil {
		fmt.Println(err)
	}

	f, err := os.Create(filepath.Join(flag.Args()[1], file))
	if err != nil {
		fmt.Println(err)
		return
	}
	io.Copy(f, resp.Body)
	f.Close()
	resp.Body.Close()
}
