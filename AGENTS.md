# AGENTS.md

dumbsync: one-way file sync. An indexer publishes a flat JSON manifest of file
hashes; a client reads that manifest, hashes its own tree, and copies what is
missing or different. Removals happen only when asked for with `-delete`, and only
inside a directory dumbsync wrote its own marker into. No auth, no conflict
resolution, no bidirectionality — see README, which tells people to use
syncthing/csync2 instead. Keep changes minimal and dependency-free; the whole point
is a tiny static binary set.

## Commands

```sh
# Build (two binaries, neither named after its package dir — go build ./... gives
# you "index" and "sync", which is wrong)
CGO_ENABLED=0 go build -o dumbsync-index ./cmd/index/main.go
CGO_ENABLED=0 go build -o dumbsync      ./cmd/sync/main.go

go test ./...                 # stdlib testing only, no test files in cmd/
go test -race ./...           # worth it: every pool here is real concurrency
go vet ./...
gofmt -l .                    # tree is gofmt-clean; keep it that way

docker build -t dumbsync .    # scratch image, tini + both binaries, ENTRYPOINT /dumbsync
goreleaser release --clean    # on tag push only (v*), .goreleaser.yaml
```

CI (`.github/workflows/build.yml`) fires only on `v*` tags: builds/pushes a
container to `ghcr.io/<repo>:<tag>` and runs goreleaser. Nothing lints or tests on
PRs, so run `go vet ./...`, `go test -race ./...` and `gofmt -l .` yourself.

## Layout

- `pkg/index/` — describing a tree. `type.go` = `Index`, `HashType`; `index.go` =
  `Options`, `IndexFS`, `IndexOf`, `Provider`, `Index.Difference`,
  `HashTypeFromEnv`; `walk.go` = the bounded pool that hashes a tree, plus
  `NewHash`, which anyone writing files must use to check what landed.
- `pkg/tree/` — the writable end. `tree.Open(dir)` returns a `Tree` rooted at an
  open `os.Root`: `FS`, `MkdirAll`, `OpenWrite`, `Rename`, `RemoveAll`, `Name`,
  `Close`, plus `TempKey` for staging a file beside the one it replaces.
- `pkg/httpfs/` — a published tree as an `fs.FS`. `httpfs.New(client, base, idx)`
  answers `Index()` from the manifest without reading content, and `Open` fetches
  one file at a time. Implied directories come from the manifest keys.
- `pkg/sync/` — the engine. `Sync(Options)` compares a source `fs.FS` against a
  target `tree.Tree` and returns a `Report`. Knows nothing about URLs or flags.
- `cmd/index/main.go` → `dumbsync-index <path>`: writes the manifest (`-index`,
  default `dumbsync.json`). Server side, on a watcher or timer.
- `cmd/sync/main.go` → `dumbsync <source> <destination>`: the source is an
  `http(s)` URL, a directory on the machine, or a `file://` URL. Client side, on a
  timer.

Flow in `pkg/sync.Sync`: index the source with `IndexOf` (its own hashes when it
publishes them, a walk otherwise) → index the destination **with the source's
algorithm** → `Difference` → copy adds and changes through one pool sized by
`-threads`, hashing each file on the way in and refusing it when the digest is not
what the source said → move staged files into place when `-delay-changes` →
removals only if `-delete` and the source was read completely → `Report{Added,
Changed, Removed, Leftover, Failed}`.

## Manifest format

`dumbsync.json` is `json.Marshal` of `Index` with **no struct tags**, so keys are
Go field names: `{"Files": {...}, "HashType": 1, "Incomplete": true, "Errors":
[...]}`.

- `Files` keys are slash-separated paths **relative to the indexed root**
  (`sub/b.txt`), never absolute, never rooted with `./`.
- `Files` values are base64 of the **raw digest bytes**, not hex: 16 bytes for
  MD5, 8 for xxHash64. Anything generating or comparing manifests by hand must use
  raw-digest base64 or nothing will match.
- `HashType`: `0` = MD5, `1` = XXHash.
- `Incomplete`/`Errors` say a file could not be read. They were added after the
  first releases, and the flag is deliberately inverted (`Incomplete`, not
  `Complete`) so a manifest written by an older build decodes as whole. Keep the
  zero value of these fields meaning "complete and nothing to report".
- A decoded `Index` has a nil mutex (the unexported `Mutex` is omitted from JSON) —
  only call read/diff methods on decoded indexes.
- `dumbsync-index` leaves its own output file out of the manifest by the key it
  has **inside the tree** (`sub/meta.json`), computed from `-index` relative to the
  tree being indexed. `-index` itself is resolved against the current working
  directory, so a manifest written outside the tree excludes nothing.

## Non-obvious constraints

**Keys are not paths.** A key satisfies `fs.ValidPath`: unrooted, slash-separated,
no `..`, no trailing slash. Roots — operating system paths — are accepted only by
`tree.Open`, `os.DirFS`, and the two `cmd` binaries. `keySet` in
`pkg/index/index.go` validates `Options.Exclude`, `writable` in `pkg/tree/tree.go`
checks every write, and `os.Root` checks again in the kernel, so a key cannot land
outside a destination even when a caller above it is careless. Do not add a check
that runs `fs.ValidPath` on a *root*: that was the bug that made `/data/src` fail
while `src` worked.

**The source chooses the hash algorithm.** `DUMBSYNC_HASH` (`MD5` default, `XX`
for xxHash, case-insensitive) is read only where an index is built from scratch,
which today means `dumbsync-index` and a directory source. The destination is
always hashed with the algorithm of the index being synced against. Hashing a
destination with something locally configured instead makes every file look
changed on every run; that is the bug the `Provider` split exists to kill.

**A read that fails is recorded, not hidden.** `walk` still logs and continues,
but it sets `Incomplete` and collects up to 32 reasons. `Sync` then keeps every
file it would have deleted and returns `ErrIncomplete`, and `dumbsync-index`
writes the manifest anyway and exits 1. A flaky server read must never turn into
deletions on clients, and a partial manifest must still be published so the next
run can be compared against it.

**Removing files takes three things**: `-delete`, a source that was read
completely, and `.dumbsync-managed` in the destination. The marker is written by an
earlier run, or by the first one if the directory is empty; `-delete` against a
non-empty directory without it is refused so that a mistyped destination is not
taken apart. Deleting the marker hands a directory back to its owner. What was kept
instead of removed is reported as `Report.Leftover`, and printed.

**Exit statuses are the contract for timers.** `dumbsync-index`: 2 usage, 1 for a
marshal/write failure or an incomplete tree, 0 otherwise. `dumbsync`: 2 for a
complaint about the invocation (bad source scheme, half a certificate pair,
`-delete` on an unclaimed directory), 1 for a manifest that cannot be fetched or
decoded, a file that would not land, or an incomplete source, and otherwise the
`-exec` hook's own status.

**`-exec` runs only when the sync applied changes and nothing failed**, and its
exit status becomes the binary's. 255 means the hook could not be started at all.

**Symlinks are read through at the source.** `fs.WalkDir` does not descend into a
symlinked directory, but `Open` follows a symlink to a file, so a link in a source
tree publishes whatever it points at. Writes cannot follow a link out of a
destination: `OpenWrite` refuses one, and `RemoveAll` takes only the link. Whether
the source should refuse links too is undecided; do not settle it silently.

**Certificates clone `http.DefaultTransport`**, so proxy handling and connection
pooling still apply; setting `TLSClientConfig` on a bare `&http.Transport{}` does
not, and that was a bug. `-timeout` is the whole `http.Client` timeout, body
included, so a large file needs a timeout sized for the transfer, not the request.

**`pkg/sync` is named `sync`.** `cmd/sync` imports it as `dsync`, and its tests
are an external `sync_test` package so they can use the standard library's `sync`.
Do not "fix" the name mid-change.

There is no `io/fs` interface for a writable file system, and no `CopyFS`, which is
why `tree.Tree` exists by hand. Check before inventing another one.

## Testing

Every package but `cmd/` has tests. `pkg/index` covers the diff, keys relative to
a root, `Exclude` (including whole subtrees, and refusing names that cannot be
keys), published hashes winning over content on disk, the bounded pool still
indexing a large tree, and incompleteness surviving the JSON round trip.
`pkg/tree` covers the write/rename/remove round trip and, importantly, that a
symlink inside a destination cannot be written through while removing it leaves the
target alone. `pkg/httpfs` serves a tree over `httptest` and runs `fstest.TestFS`
against it. `pkg/sync` covers the engine, including that a corrupt copy is refused
in both staged and in-place modes, and wraps runs in `doWithDeadline` so a
deadlock fails the test instead of hanging it.

The binaries have no tests; check them by hand. Absolute paths are fine now:

```sh
cd /tmp/srv && dumbsync-index . && python3 -m http.server 8099 &
dumbsync http://127.0.0.1:8099/ /tmp/dst
dumbsync /tmp/srv /tmp/dst           # directory to directory, no server involved
```

## Conventions

- Commits: `<area>: <Summary>`, area is the changed path (`cmd/sync`, `pkg/index`,
  `Dockerfile`, `.github/workflows`). Imperative-ish, capitalized, no scope
  parens, no conventional-commit prefixes. One logical change each; the body says
  why.
- Godoc comments on every exported identifier, full sentences, wrapped around 72
  columns. Comments on unexported helpers are for the reasoning a reader cannot
  rederive, not for what the next line does.
- Standard library first: the only deps are `cespare/xxhash/v2` and
  `google/shlex` (for tokenizing `-exec`), both direct.
- `go.mod` declares `go 1.25` and the Dockerfile builds `golang:1.25-alpine`,
  because `os.Root.MkdirAll`, `os.Root.Rename` and `os.Root.RemoveAll` need it.
- Flags stay package-level `flag.*` vars named `sync*`/`indexFilePath`. Both
  `main` functions are `os.Exit(run())`, which is how exit codes propagate; keep
  the IIFE out.
- Output is plain stdout lines: `[+]` added, `[~]` changed, `[-]` removed, plus a
  `Synchronizing against <source>` header and a `Kept N file(s) ...` line when
  something was not removed. Scripts parse the three marks; keep them.
- Root `.gitignore` covers `dumbsync.json` and `dist/`; `.crush/` and `venv/`
  self-ignore via a `.gitignore` containing `*` inside each. Don't commit a
  generated manifest.
