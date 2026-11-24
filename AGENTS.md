# AGENTS.md

dumbsync: one-way file sync over HTTP. An indexer publishes a flat JSON manifest
of file hashes; a client fetches the manifest, hashes its own tree, and downloads
adds/changes and deletes anything the manifest no longer lists. No auth, no
conflict resolution, no bidirectionality — see README, which tells people to use
syncthing/csync2 instead. Keep changes minimal and dependency-free; the whole
point is a tiny static binary set.

## Commands

```sh
# Build (two binaries, neither named after its package dir — go build ./... gives
# you "index" and "sync", which is wrong)
CGO_ENABLED=0 go build -o dumbsync-index ./cmd/index/main.go
CGO_ENABLED=0 go build -o dumbsync      ./cmd/sync/main.go

go test ./...                 # stdlib testing only, no test files in cmd/
go vet ./...
gofmt -l .                    # tree is gofmt-clean; keep it that way

docker build -t dumbsync .    # scratch image, tini + both binaries, ENTRYPOINT /dumbsync
goreleaser release --clean    # on tag push only (v*), .goreleaser.yaml
```

CI (`.github/workflows/build.yml`) fires only on `v*` tags: builds/pushes a
container to `ghcr.io/<repo>:<tag>` and runs goreleaser. Nothing lints or tests on
PRs, so run `go test ./...` and `gofmt -l .` yourself.

## Layout

- `pkg/index/` — all real logic. `type.go` = `Indexer`, `Index`, `HashType`;
  `index.go` = walking, hashing, diffing. Only exported surface used by callers:
  `IndexPath`, `PruneFile`, `ComputeDifference`.
- `cmd/index/main.go` → `dumbsync-index <path>`: walks `path`, writes manifest
  (`-index`, default `dumbsync.json`). Runs on the server side, on a timer/watcher.
- `cmd/sync/main.go` → `dumbsync <url> <path>`: fetches `<url>/<index>`, indexes
  `<path>`, applies the diff. Runs on the client side, on a timer.

Flow in `cmd/sync`: GET manifest → decode into `index.Index` → `IndexPath(local)` →
`ComputeDifference` → sort all three lists → download `added` (goroutines, `-threads`
semaphore) → `wg.Wait` → download `changed` (same pattern, temp-file path when
`-delay-changes`) → `wg.Wait` → `os.Rename` queued temp files → `os.RemoveAll`
`removed` → optional `-exec` hook (only if something changed).

## Manifest format

`dumbsync.json` is `json.Marshal` of `Index` with **no struct tags**, so keys are
Go field names: `{"Files": {...}, "HashType": 1}`.

- `Files` keys are slash-separated paths **relative to the indexed root**
  (`sub/b.txt`), never absolute, never rooted with `./`.
- `Files` values are base64 of the **raw digest bytes**, not hex: 16 bytes for
  MD5, 8 for xxHash64. Anything generating or comparing manifests by hand must use
  raw-digest base64 or nothing will match.
- `HashType`: `0` = MD5, `1` = XXHash. Unexported `Mutex`/`wg`/`fs` fields are
  omitted from JSON, so a decoded `Index` has a nil mutex — only call
  read/diff methods on decoded indexes.
- `dumbsync-index` drops its own output file from the manifest by
  `filepath.Base(-index)` name, so manifests never index themselves.

## Non-obvious constraints

**All paths go through `fs.ValidPath` (`pkg/index/index.go:21`).** A path starting
or ending with `/`, containing `..`, or beginning with `./` is rejected with
`path must be unrooted...`. This applies to the path argument of **both** binaries,
so callers must `cd` into the parent and pass a relative path like `src`, not
`/data/src` or `src/`. Anything downstream of that call assumes relative keys.

**Hash algorithm comes from the `DUMBSYNC_HASH` env var, read at index time**
(`MD5` default, `XX` for xxHash; case-insensitive). `cmd/sync` calls the same
`IndexPath` for the local tree, so server and client must have the same env value.
The decoded remote `HashType` is never consulted when hashing locally — a mismatch
silently re-downloads the entire tree on every run rather than erroring.

**`dumbsync-index` always exits 0**, including on usage errors, indexing errors and
write failures (`cmd/index/main.go` has no `os.Exit`). Timer wrappers cannot detect
failure from it; check the manifest's mtime or content instead. `dumbsync` (sync)
does return non-zero (2 usage/cert problems, 1 fetch/decode failure, 255 `-exec`).

**The remote index is authoritative and sync is destructive.** Anything present in
the destination but absent from the manifest is `os.RemoveAll`'d, including files
that were transiently unreadable when the manifest was generated: `walkDir` and
`handleFile` log walk/open errors and continue, leaving entries out rather than
failing (`pkg/index/index.go:46`, `:61`). A single flaky read on the server turns
into deletions on every client. Preserve this "log and skip" behavior unless
deliberately changing it, and never point `<path>` at a tree you don't own.

**`PruneFile` takes a manifest-relative key.** `cmd/sync/main.go:87` passes
`filepath.Join(<local path>, -index)`, which can never match a relative key, so the
prune is a no-op: a `dumbsync.json` sitting in a destination gets deleted as
"removed from remote". Verified empirically. Fixing this means passing just
`*syncFileName`.

**`-delay-changes` hangs if any temp file fails.** In the `changed` goroutine
(`cmd/sync/main.go:118`), the error path from `syncCmdGetFileTemp` `return`s
without releasing the `-threads` semaphore or calling `wg.Done()`, so `wg.Wait()`
blocks forever (confirmed: run never exits under `timeout`). Any early return added
in that loop needs the same care.

Other rough edges to keep in mind when touching this code (all current behavior,
verified where noted):

- Download failures in `syncCmdGetFile` only print and return; the file is retried
  on the next run and the process still exits 0.
- `tempfiles = append(...)` inside the `changed` goroutines is unsynchronized —
  `-race` flags it; collects rename targets from `-delay-changes`.
- `walkDir` starts one goroutine per file with no cap, so large trees open many
  fds at once. `handleFile` closes the file explicitly in addition to its `defer`
  (see `pkg/index: Aggressively close files`) to keep the fd count down.
- `-exec` exit codes are never propagated: `errors.Is(err, &exec.ExitError{})`
  (`cmd/sync/main.go:156`) can't match, so the exit-code branch is dead code and a
  failing hook always yields 255 (confirmed: hook exiting 37 gives 255).
- `-cert`/`-key` must both be set to have any effect, and setting them replaces
  `Transport` wholesale (no proxy/keepalive defaults from `http.DefaultTransport`).

## Testing

One unit test: `pkg/index/index_test.go` exercises the unexported
`computeDifference` on hand-built `Index` values with fake hash bytes. It is the
pattern for diff logic — construct `Index{Files: ...}` directly (no mutex needed;
the diff path doesn't lock) and call the unexported method to bypass `Indexer`
state. There are no fixtures, golden files, HTTP mocks, or `cmd/` tests.

To test end-to-end, index a scratch dir and serve it with
`python3 -m http.server`, then run the sync binary with a relative destination:

```sh
cd /tmp/srv && dumbsync-index . && python3 -m http.server 8099 &
cd /tmp && dumbsync http://127.0.0.1:8099/ dst     # relative target, or ValidPath fails
```

## Conventions

- Commits: `<area>: <Summary>`, area is the changed path (`cmd/sync`, `pkg/index`,
  `Dockerfile`, `.github/workflows`). Imperative-ish, capitalized, no scope
  parens, no conventional-commit prefixes.
- Godoc comments on every exported identifier, full sentences, wrapping around 72
  columns; unexported helpers (`syncCmdGetFile*`, `walkDir`, `handleFile`) are
  uncommented.
- Standard library first: only two deps (`cespare/xxhash/v2`, `google/shlex`, the
  latter for tokenizing `-exec`). Both are marked `// indirect` in `go.mod`, which
  gopls/`go mod tidy` flag; leave alone unless tidying deliberately.
- `go.mod` declares `go 1.20`; the Dockerfile builds with Go 1.24 and CI uses
  `setup-go@v4 go-version: stable`, so don't rely on post-1.20 language features
  in committed code.
- Flags are package-level `flag.*` vars named `sync*`/`indexFilePath`; the sync
  `main` body is an IIFE returning an int passed to `os.Exit` (that's how error
  codes propagate), and `cmd/index` does not follow that pattern.
- Output is plain stdout lines: `[+]` added, `[~]` changed, `[-]` removed, plus a
  `Synchronizing against <url>` header. Scripts may parse these; keep the format.
- Root `.gitignore` covers `dumbsync.json` and `dist/`; `.crush/` and `venv/`
  self-ignore via a `.gitignore` containing `*` inside each. Don't commit a
  generated manifest.
