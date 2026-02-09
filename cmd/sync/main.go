package main

import (
	"crypto/tls"
	"encoding/json"
	"errors"
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
	syncFetchTimeout = flag.Duration("timeout", time.Second*10, "Timeout for file acquisition")
	syncFileName     = flag.String("index", "dumbsync.json", "Index filename")
	syncThreads      = flag.Int("threads", 10, "Number of threads to use while syncing")
	syncCertFile     = flag.String("cert", "", "Client Certificate")
	syncCertKeyFile  = flag.String("key", "", "Client Key")
	syncExec         = flag.String("exec", "", "Execute a command when files are changed")
	syncDelayChanges = flag.Bool("delay-changes", false, "Atomically update files after downloads")
)

func main() {
	os.Exit(func() int {
		flag.Parse()
		if len(flag.Args()) != 2 {
			fmt.Fprintln(os.Stderr, "Usage: dumbsync <url> <path>")
			fmt.Fprintln(os.Stderr, "")
			fmt.Fprintln(os.Stderr, "You must specify a URL to sync from, and a path to sync to!")
			return 2
		}

		httpClient := http.Client{Timeout: *syncFetchTimeout}

		if *syncCertFile != "" && *syncCertKeyFile != "" {
			cert, err := tls.LoadX509KeyPair(*syncCertFile, *syncCertKeyFile)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading client certificate: %s\n", err)
				return 2
			}

			httpClient.Transport = &http.Transport{
				TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{cert}},
			}
		}

		u, err := url.Parse(flag.Args()[0])
		if err != nil {
			fmt.Println(err)
			return 2
		}
		du := *u
		du.Path = path.Join(du.Path, *syncFileName)

		fmt.Printf("Synchronizing against %s\n", du.String())
		resp, err := httpClient.Get(du.String())
		if err != nil {
			fmt.Println(err)
			return 1
		}
		defer resp.Body.Close()

		sidx := new(index.Index)
		dec := json.NewDecoder(resp.Body)
		if err := dec.Decode(sidx); err != nil {
			fmt.Println(err)
			return 1
		}

		i := new(index.Indexer)
		if _, err := i.IndexPath(flag.Args()[1]); err != nil {
			fmt.Println(err)
			return 1
		}
		i.PruneFile(filepath.Join(flag.Args()[1], *syncFileName))

		added, removed, changed := i.ComputeDifference(sidx)
		sort.Strings(added)
		sort.Strings(removed)
		sort.Strings(changed)

		var wg sync.WaitGroup

		limit := make(chan struct{}, *syncThreads)
		for _, file := range added {
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

		type tempfile struct {
			tmp, dst string
		}
		var tempfiles []tempfile
		for _, file := range changed {
			wg.Add(1)
			go func(f string) {
				limit <- struct{}{}
				fmt.Printf("[~] %s\n", f)
				if *syncDelayChanges {
					tmp, dst, err := syncCmdGetFileTemp(httpClient, *u, f)
					if err != nil {
						return
					}
					tempfiles = append(tempfiles, tempfile{tmp, dst})
				} else {
					syncCmdGetFile(httpClient, *u, f)
				}
				<-limit
				wg.Done()
			}(file)
		}
		wg.Wait()

		for _, t := range tempfiles {
			if err := os.Rename(t.tmp, t.dst); err != nil {
				fmt.Println(err)
				os.Remove(t.tmp)
			}
		}

		for _, file := range removed {
			fmt.Printf("[-] %s\n", file)
			if err := os.RemoveAll(filepath.Join(flag.Args()[1], file)); err != nil {
				fmt.Println(err)
			}
		}

		if *syncExec != "" && ((len(added) > 0) || (len(changed) > 0) || (len(removed) > 0)) {
			parts, err := shlex.Split(*syncExec)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Could not exec cmd: %s\n", err)
				return 1
			}
			cmd := exec.Command(parts[0], parts[1:]...)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil && errors.Is(err, &exec.ExitError{}) {
				exitError := err.(*exec.ExitError)
				fmt.Fprintf(os.Stderr, "Command did not complete successfully: %s\n", err)
				return exitError.ExitCode()
			} else if err != nil {
				fmt.Fprintf(os.Stderr, "Command did not complete successfully: %s\n", err)
				return 255
			}
		}
		return 0
	}())
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

func syncCmdGetFileTemp(c http.Client, tu url.URL, file string) (string, string, error) {
	tu.Path = path.Join(tu.Path, file)
	resp, err := c.Get(tu.String())
	if err != nil {
		fmt.Println(err)
		return "", "", err
	}
	dir := filepath.Dir(filepath.Join(flag.Args()[1], file))
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Println(err)
	}

	f, err := os.CreateTemp(dir, ".dumbsync.XXXXXXX")
	if err != nil {
		fmt.Println(err)
		return "", "", err
	}
	_, err1 := io.Copy(f, resp.Body)
	if err1 != nil {
		fmt.Println(err1)
	}
	err2 := f.Close()
	if err2 != nil {
		fmt.Println(err2)
	}
	err3 := resp.Body.Close()
	if err3 != nil {
		fmt.Println(err3)
	}
	if err1 != nil || err2 != nil || err3 != nil {
		os.Remove(f.Name())
		return "", "", err
	}
	target := filepath.Join(flag.Args()[1], file)
	return f.Name(), target, nil
}
