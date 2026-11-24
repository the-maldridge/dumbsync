# dumbsync

Dumbsync is a really dumb file synchronization solution.  It works by
generating an index of file checksums and then comparing this to a
locally generated index.  Files that are not present or have changed
are downloaded, and files that are no longer in the index are removed
only when `-delete` is asked for, and only in a directory dumbsync
wrote its `.dumbsync-managed` marker into.

The intent is that your files are served via a webserver,
security/authentication of this webserver is beyond the scope of
dumbsync.  The index, which is named `dumbsync.json` by default, must
be at the root of the file tree on the webserver.  A typical
deployment will run `dumbsync-index` either on a watcher or a timer to
capture changes, and `dumbsync` on a timer to periodically capture
changes contained in the index.

A webserver is not required either: a directory on the same machine,
given as a path or a `file://` URL, is a source like any other.

### You should probably be using something else!

This software solves an extremely specific problem, and you almost
certainly should be using csync2 or syncthing instead.  Dumbsync is
designed to solve a specific problem where extremely low dependency
architecture is important.
