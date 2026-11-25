# dumbsync

Dumbsync is a really dumb file synchronization solution.  It works by
generating an index of file checksums and then comparing that to an
index of the destination.  Files that are not present or have changed
are copied, and files that are no longer in the index are removed only
when `-delete` is asked for, and only in a directory dumbsync wrote its
`.dumbsync-managed` marker into.

There are two binaries.  `dumbsync-index` runs wherever the files live
and writes a manifest of checksums.  `dumbsync` runs wherever the files
are wanted, reads that manifest, and brings the destination in line with
it.  Everything else in this document is about configuring those two.

## Install

Release archives for linux, darwin and windows, with `checksums.txt`,
are on the releases page.  Each release also publishes a container, so
that the version tag is the release tag and there is no floating
`latest`: `ghcr.io/the-maldridge/dumbsync:v0.0.9`.

Building from source needs a Go toolchain at least as new as the `go`
line in `go.mod`:

```sh
CGO_ENABLED=0 go build -o dumbsync-index ./cmd/index/main.go
CGO_ENABLED=0 go build -o dumbsync      ./cmd/sync/main.go
```

Both are static, single-file binaries with no runtime dependencies.

## Quick start

On the machine holding the files, index them and serve the tree with any
web server.  The manifest has to be reachable at the root of the tree it
describes:

```sh
cd /srv/www
dumbsync-index .          # writes ./dumbsync.json, naming files relative to /srv/www
# then serve /srv/www over HTTP, with nginx or anything else
```

On the machine that wants the files:

```sh
dumbsync http://files.example.com/ /var/www
```

That prints one line per file it touched and exits.  Cron both ends and
you have a sync:

```cron
*/5 * * * *  cd /srv/www && /usr/local/bin/dumbsync-index .
*/5 * * * *  /usr/local/bin/dumbsync http://files.example.com/ /var/www
```

A source does not have to be a web server.  A directory on the same
machine, given as a path or a `file://` URL, is a source like any other,
which is the shortest way to mirror one disk to another:

```sh
dumbsync /srv/www /backup/www
dumbsync file:///srv/www /backup/www
```

## dumbsync-index

```
dumbsync-index [-index filename] <path>
```

| Flag | Default | Meaning |
|---|---|---|
| `-index` | `dumbsync.json` | Where to write the manifest.  Resolved against the current working directory, which is not necessarily inside `<path>`. |

`<path>` is a directory on the local system and may be absolute or
relative.  The manifest describes files by their names inside `<path>`,
so it never depends on where it was written from.  The manifest leaves
itself out of its own listing; when `-index` points somewhere outside
`<path>` there is nothing for it to leave out.

Environment:

| Variable | Values | Meaning |
|---|---|---|
| `DUMBSYNC_HASH` | `MD5` (default), `XX` | Digest algorithm.  Anything unrecognized means MD5.  Case-insensitive. |

Exit status: `0` the manifest is complete, `1` it could not be written
or a file could not be read, `2` bad invocation.

If a file cannot be read, the manifest is still written, with
`"Incomplete": true` and up to 32 reasons in `"Errors"`, and the exit
status is 1.  That is deliberate: a partial manifest published so that
it can be recognized is far safer than no manifest, or yesterday's.

The indexer changes nothing else about the tree it indexes: aside from
the manifest, it writes nothing.

## dumbsync

```
dumbsync [flags] <source> <destination>
```

The source is `http://<host>/`, `https://<host>/`, a directory on this
machine, or a `file://<path>` URL.  The destination is a directory; it
is created if it does not exist.  Each file named by the manifest is fetched from the
same URL the manifest came from, so the manifest has to sit at the root
of the tree it describes.

| Flag | Default | Meaning |
|---|---|---|
| `-index` | `dumbsync.json` | Manifest name, looked for at the root of the source. |
| `-threads` | `10` | Files copied at once. |
| `-timeout` | `10s` | Per-request HTTP timeout, body included.  Raise it for large files. |
| `-delay-changes` | off | Write every file beside its final name and move it into place once all transfers finish. |
| `-delete` | off | Remove destination files the source no longer lists. |
| `-exec` | none | Command to run when the run changed something. |
| `-cert`, `-key` | none | Client certificate and key, for a server that checks them.  Both or neither. |

The name given by `-index` is left out of the comparison on both sides,
so a manifest sitting in the destination is neither overwritten nor
counted as an extra file, and `.dumbsync-managed` is left alone for the
same reason.

Exit status: `0` nothing needed doing, or everything that did landed;
`1` a file would not land, the manifest could not be fetched or decoded,
the source could not be read completely, or an `-exec` line that cannot
be parsed; `2` the invocation is wrong (bad source, half a certificate
pair, `-delete` on a directory dumbsync has not taken over); otherwise
the exit status of the `-exec` command, or `255` if it could not be
started.

Output is one line per action, which scripts parse.  Keep to it if you
are writing one.  Failures and warnings go to stderr, the rest to
stdout:

```
Synchronizing against http://files.example.com/dumbsync.json
[+] docs/index.html
[~] app/config.yaml
[-] old/thing.tar
Failed to synchronize app/cache.bin: copied content does not match the source
Kept 3 file(s) the source no longer has, pass -delete to remove them
```

### Removing files

Nothing at the destination is ever removed unless all three of these are
true:

- `-delete` was given,
- the source's manifest says it was read completely, and
- the destination holds a `.dumbsync-managed` file that dumbsync wrote.

The marker is what stops `-delete` aimed at a mistyped path, or at a
directory that happens to hold other work, from taking it apart.  The
first run into an empty directory writes the marker.  A run into a
non-empty directory that has no marker refuses `-delete` outright and
says so; run once without `-delete` to write the marker and take the
directory over.  To hand a directory back, delete `.dumbsync-managed`
from it.

Files kept rather than removed are reported as `Kept N file(s) …`, and
the run still exits 0.  A manifest that admits to being incomplete
suppresses removals on its own, even with `-delete`, because a file the
server failed to read and a file that was deleted look identical from
here.

### Atomic updates

`-delay-changes` writes each file to a scratch name beside its final
one, `.dumbsync-<hex>-<name>`, and moves them all into place only after
every transfer has finished.  Use it when something is reading the
destination while it is being synced: a reader never sees a half written
file.  Without it, files are written straight into place as they
arrive.

### Running a command afterwards

`-exec` takes a command line, split the way a shell would split it.
Its output goes to the terminal, its exit status becomes dumbsync's,
and it runs only when the run actually changed files and nothing failed.
It runs in the directory dumbsync was started from, not the destination:

```sh
dumbsync -exec '/usr/bin/systemctl reload nginx' http://files.example.com/ /var/www
dumbsync -exec 'sh -c "cd /var/www && ./build.sh"' http://files.example.com/ /var/www
```

### In a container

The image's entrypoint is `dumbsync`, so mount the destination and pass
the source:

```sh
docker run --rm -v /var/www:/var/www \
    ghcr.io/the-maldridge/dumbsync:v0.0.9 http://files.example.com/ /var/www
```

`dumbsync-index` is in the same image and needs the entrypoint
overridden:

```sh
docker run --rm -v /srv/www:/srv/www --entrypoint /dumbsync-index \
    ghcr.io/the-maldridge/dumbsync:v0.0.9 /srv/www
```

A container is a poor way to run the indexer if the web server cannot
see files the container wrote as the same user, so most deployments run
only the client end in a container.

## What dumbsync does not do

- It syncs content.  Permissions, ownership and timestamps are not
  carried across: files are created with the destination's umask and
  directories `0755`, and their mtime is when they were written.
- Removals leave directories behind once they are empty.
- There are no include or exclude patterns.  Anything in the source
  tree, including a `.git` directory or editor droppings, is indexed and
  copied.  Index a subtree that contains only what you want published.
- It is one way.  The destination is expected to be quiet.  A file
  changed at the destination is overwritten on the next run, and with
  `-delete` a file created at the destination is removed.
- Symlinks in a source are published as the content they point at;
  symlinked directories are not descended into.  At the destination, a
  symlink that leads out of the tree is refused rather than written
  through.
- Authentication, TLS termination, and everything else about the web
  server are outside dumbsync.  Client certificates are supported;
  bearer tokens and similar are not.
- There is no resume, compression, bandwidth limit, or encryption of the
  transfer.  Put it behind TLS if that matters.

## The manifest

`dumbsync.json` is JSON with Go field names, which is worth knowing the
one time you have to debug it by hand:

```json
{
  "Files": {"greeting.txt": "sZRqySSS0jR8YjW00mERhA=="},
  "HashType": 0,
  "Incomplete": false,
  "Errors": null
}
```

`Files` keys are slash-separated paths relative to the root that was
indexed, never absolute.  The values are base64 of the raw digest bytes,
not hex: 16 bytes for MD5, 8 for xxHash64 (`HashType` `0` and `1`).  The
entry above is a file holding `hello` and a newline, so the digest is the
base64 of `b1946ac92492d2347c6235b4d2611184` rather than the hex string
most tools print.

`Incomplete` and `Errors` were added later, and a manifest from an older
build decodes as complete, which is the point of naming the flag in the
negative.

## You should probably be using something else!

This software solves an extremely specific problem, and you almost
certainly should be using csync2 or syncthing instead.  Dumbsync is
designed to solve a specific problem where extremely low dependency
architecture is important.
