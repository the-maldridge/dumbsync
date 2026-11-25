// dumbsync brings a destination directory in line with a tree, whether
// that tree is published over HTTP by dumbsync-index or is simply
// another directory on this machine.
package main

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"slices"
	"sort"
	"time"

	"github.com/google/shlex"

	"github.com/the-maldridge/dumbsync/pkg/httpfs"
	"github.com/the-maldridge/dumbsync/pkg/index"
	"github.com/the-maldridge/dumbsync/pkg/scope"
	dsync "github.com/the-maldridge/dumbsync/pkg/sync"
	"github.com/the-maldridge/dumbsync/pkg/tree"
)

// marker is written into a destination so that a later run can tell a
// directory dumbsync synced from one it was pointed at by mistake.
const marker = ".dumbsync-managed"

const markerContents = `This file marks a directory as one dumbsync synchronizes.

Without it dumbsync refuses to run with -delete, so that pointing -delete
at a directory that was never a destination cannot take it apart. Delete
this file to hand the directory back.
`

var (
	syncFetchTimeout = flag.Duration("timeout", time.Second*10, "Timeout for file acquisition")
	syncFileName     = flag.String("index", "dumbsync.json", "Index filename")
	syncThreads      = flag.Int("threads", 10, "Number of threads to use while syncing")
	syncCertFile     = flag.String("cert", "", "Client Certificate")
	syncCertKeyFile  = flag.String("key", "", "Client Key")
	syncExec         = flag.String("exec", "", "Execute a command when files are changed")
	syncDelayChanges = flag.Bool("delay-changes", false, "Atomically update files after downloads")
	syncDelete       = flag.Bool("delete", false, "Remove files the source no longer has")
	syncAllowSubset  = flag.Bool("allow-subset", false, "Allow -delete to remove files a manifest that says it is partial leaves out")

	// syncRules is a single ordered list that both flags append to, so
	// the order the arguments were typed in survives to the decision.
	syncRules scope.Rules
)

func init() {
	flag.Var(&scope.RuleFlag{Rules: &syncRules, Include: false}, "exclude", "Leave out everything matching a pattern")
	flag.Var(&scope.RuleFlag{Rules: &syncRules, Include: true}, "include", "Put back everything matching a pattern, over any earlier -exclude")
}

// usageError reports a command invoked in a way that cannot be acted
// on, as opposed to a source that could not be reached or a file that
// could not be written.
type usageError struct {
	msg string
}

func (e *usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error {
	return &usageError{msg: fmt.Sprintf(format, a...)}
}

func main() {
	os.Exit(run())
}

func run() int {
	flag.Parse()
	if flag.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "Usage: dumbsync <source> <destination>")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "The source is a URL published by dumbsync-index, or a directory on")
		fmt.Fprintln(os.Stderr, "this machine given as a path or a file:// URL.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "-exclude and -include choose which files are synchronized. Both may be")
		fmt.Fprintln(os.Stderr, "repeated, and the last rule matching a name decides. * covers one path")
		fmt.Fprintln(os.Stderr, "segment, ** crosses segments, ? is a single character, and naming a")
		fmt.Fprintln(os.Stderr, "directory names everything under it. To take two directories and")
		fmt.Fprintln(os.Stderr, "nothing else:")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  dumbsync -exclude '*' -include foo/ -include bar/ https://host/ /srv/www")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "A manifest built from a subset says that it is one. Such a manifest is")
		fmt.Fprintln(os.Stderr, "still copied from in full, but -delete will not remove anything it omits")
		fmt.Fprintln(os.Stderr, "until -allow-subset is given as well, since an omission and a deletion")
		fmt.Fprintln(os.Stderr, "look the same from here.")
		return 2
	}

	dest, err := tree.Open(flag.Arg(1))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not use %s as a destination: %s\n", flag.Arg(1), err)
		return 1
	}
	defer dest.Close()

	owned, err := claim(dest)
	if err != nil {
		return fail(err)
	}

	source, err := openSource(flag.Arg(0))
	if err != nil {
		return fail(err)
	}

	// The names this command owns for itself are put out of scope last,
	// where no rule typed earlier can bring them back into the sync.
	rules, err := pinned(syncRules, append(owned, *syncFileName)...)
	if err != nil {
		return fail(err)
	}

	report, syncErr := dsync.Sync(dsync.Options{
		Source:      source,
		Target:      dest,
		Hash:        index.HashTypeFromEnv(),
		Rules:       rules,
		Threads:     *syncThreads,
		Atomic:      *syncDelayChanges,
		Delete:      *syncDelete,
		AllowSubset: *syncAllowSubset,
		Progress: func(e dsync.Event) {
			fmt.Printf("%s %s\n", action[e.Kind], e.Key)
		},
	})

	failed := make([]string, 0, len(report.Failed))
	for key := range report.Failed {
		failed = append(failed, key)
	}
	sort.Strings(failed)
	for _, key := range failed {
		fmt.Fprintf(os.Stderr, "Failed to synchronize %s: %s\n", key, report.Failed[key])
	}

	if len(report.Leftover) > 0 {
		switch {
		case !*syncDelete:
			fmt.Printf("Kept %d file(s) the source no longer has, pass -delete to remove them\n", len(report.Leftover))
		case report.Subset && !errors.Is(syncErr, dsync.ErrIncomplete):
			fmt.Printf("Kept %d file(s) that a source describing only part of its tree does not mention, pass -allow-subset to remove them\n", len(report.Leftover))
		default:
			fmt.Printf("Kept %d file(s) the source no longer has, because it could not be read completely\n", len(report.Leftover))
		}
	}

	if syncErr != nil {
		if errors.Is(syncErr, dsync.ErrUncovered) {
			// The rules asked for something the source does not have,
			// which is a complaint about how the command was written and
			// not a transfer that went wrong.
			fmt.Fprintln(os.Stderr, syncErr)
			return 2
		}

		if errors.Is(syncErr, dsync.ErrSubset) {
			// Nothing failed on the way here, and the copies that were
			// asked for were made. What is missing is a second word from
			// whoever typed the command line, which makes this the same
			// kind of problem as a rule that names nothing.
			fmt.Fprintf(os.Stderr, "Not deleting: %s\n", syncErr)
			fmt.Fprintln(os.Stderr, "Add -allow-subset if this manifest is expected to cover everything worth keeping")
			return 2
		}

		if errors.Is(syncErr, dsync.ErrIncomplete) {
			// Anything the source could not be read is a file whose
			// absence means nothing, so say so loudly rather than
			// letting a quiet exit imply the trees now match.
			fmt.Fprintln(os.Stderr, "Warning: the source could not be read completely, the destination may be out of date:", syncErr)
			return 1
		}
		fmt.Fprintln(os.Stderr, syncErr)
		return 1
	}

	if len(failed) > 0 {
		return 1
	}

	if *syncExec != "" && !report.Empty() {
		return hook(*syncExec)
	}

	return 0
}

// action is how each kind of work is reported, kept as the marks long
// standing scripts of ours already parse.
var action = map[dsync.EventKind]string{
	dsync.Added:   "[+]",
	dsync.Changed: "[~]",
	dsync.Removed: "[-]",
}

// fail reports an error and returns the status to exit with, which is
// the usage status for a complaint about the invocation and a general
// failure for anything else.
func fail(err error) int {
	fmt.Fprintln(os.Stderr, err)

	var usage *usageError
	if errors.As(err, &usage) {
		return 2
	}

	return 1
}

// pinned adds the names a command owns for itself to the end of the rule
// list, where nothing typed earlier can put them back in scope. Each one
// is quoted, being a file rather than something anybody meant as a
// pattern.
func pinned(rules scope.Rules, names ...string) (scope.Rules, error) {
	out := slices.Clone(rules)

	for _, name := range names {
		rule, err := scope.Parse(scope.Quote(name), false)
		if err != nil {
			return nil, usagef("%s cannot be kept out of the sync: %s", name, err)
		}

		out = append(out, rule)
	}

	return out, nil
}

// claim notes the files the destination owns for itself, which are
// neither compared with the source nor removed for it, and marks the
// directory as a destination so that a later -delete run may remove
// files from it.
//
// A directory that already holds work of somebody else's is not marked,
// which is what keeps -delete from wiping a path that was misspelled or
// aimed somewhere it should not have been.
func claim(dest tree.Tree) ([]string, error) {
	_, err := fs.Stat(dest.FS(), marker)
	switch {
	case err == nil:
		return []string{marker}, nil
	case !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("Could not inspect %s: %s", dest.Name(), err)
	}

	if *syncDelete {
		entries, err := fs.ReadDir(dest.FS(), ".")
		if err != nil {
			return nil, fmt.Errorf("Could not inspect %s: %s", dest.Name(), err)
		}
		if len(entries) > 0 {
			return nil, usagef("%s holds files dumbsync did not put there, so it will not be deleted from. Run once without -delete to take it over", dest.Name())
		}
	}

	f, err := dest.OpenWrite(marker)
	if err != nil {
		return nil, fmt.Errorf("Could not mark %s as a dumbsync destination: %s", dest.Name(), err)
	}
	defer f.Close()

	if _, err := f.Write([]byte(markerContents)); err != nil {
		return nil, fmt.Errorf("Could not mark %s as a dumbsync destination: %s", dest.Name(), err)
	}

	return []string{marker}, nil
}

// openSource returns the tree named by source, from wherever it is.
func openSource(source string) (fs.FS, error) {
	u, err := url.Parse(source)
	if err != nil {
		return nil, usagef("%s", err)
	}

	switch u.Scheme {
	case "http", "https":
		return published(u)
	case "", "file":
		return directory(u)
	default:
		return nil, usagef("cannot sync from a %s URL", u.Scheme)
	}
}

// directory returns a tree on this machine. The name is a path rather
// than a URL, so a plain directory name is enough and no one has to
// write file:// in front of it to get the same code.
func directory(u *url.URL) (fs.FS, error) {
	if u.Scheme == "file" && u.Host != "" && u.Host != "localhost" {
		return nil, usagef("a file URL names a directory on this machine, %q goes nowhere it can reach", u.Host)
	}

	info, err := os.Stat(u.Path)
	if err != nil {
		return nil, fmt.Errorf("%s could not be read as a tree: %s", u.Path, err)
	}
	if !info.IsDir() {
		return nil, usagef("%s is not a directory", u.Path)
	}

	fmt.Printf("Synchronizing against %s\n", u.Path)

	return os.DirFS(u.Path), nil
}

// published reads the index a server publishes and returns the tree it
// describes, so that names and hashes are known before any content is
// fetched.
func published(u *url.URL) (fs.FS, error) {
	client, err := httpClient()
	if err != nil {
		return nil, err
	}

	manifest := *u
	manifest.Path = path.Join(manifest.Path, *syncFileName)

	fmt.Printf("Synchronizing against %s\n", manifest.String())

	resp, err := client.Get(manifest.String())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", manifest.String(), resp.Status)
	}

	idx := new(index.Index)
	if err := json.NewDecoder(resp.Body).Decode(idx); err != nil {
		return nil, fmt.Errorf("%s is not a dumbsync index: %s", manifest.String(), err)
	}

	return httpfs.New(client, u.String(), idx)
}

// httpClient builds the client used for the whole run. The default
// transport is copied rather than replaced, so that a client
// certificate costs the pooling and proxy handling every other request
// would have had.
func httpClient() (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()

	if *syncCertFile != "" || *syncCertKeyFile != "" {
		if *syncCertFile == "" || *syncCertKeyFile == "" {
			return nil, usagef("a client certificate needs both -cert and -key")
		}

		cert, err := tls.LoadX509KeyPair(*syncCertFile, *syncCertKeyFile)
		if err != nil {
			return nil, usagef("%s", err)
		}

		transport.TLSClientConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
	}

	return &http.Client{Timeout: *syncFetchTimeout, Transport: transport}, nil
}

// hook runs the command asked for after files changed, and returns the
// status the caller should exit with.
func hook(command string) int {
	parts, err := shlex.Split(command)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not exec cmd: %s\n", err)
		return 1
	}
	if len(parts) == 0 {
		fmt.Fprintf(os.Stderr, "Could not exec cmd: nothing to run\n")
		return 1
	}

	cmd := exec.Command(parts[0], parts[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Command did not complete successfully: %s\n", err)

		// The command ran and chose its own status; only a dumbsync
		// that could not start it at all gets a made up one.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		return 255
	}

	return 0
}
