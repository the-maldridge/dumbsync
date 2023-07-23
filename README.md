# dumbsync

Dumbsync is a really dumb file synchronization solution.  It works by
generating an index of file checksums and then comparing this to a
locally generated index.  Files that are not present or have changed
are downloaded, and files that are no longer in the index are removed.

The intent is that your files are served via a webserver,
security/authentication of this webserver is beyond the scope of
dumbsync.  The index, which is named `dumbsync.json` by default, must
be at the root of the file tree on the webserver.  A typical
deployment will run `dumbsync-index` either on a watcher or a timer to
capture changes, and `dumbsync` on a timer to periodically capture
changes contained in the index.

### You should probably be using something else!

This software solves an extremely specific problem, and you almost
certainly should be using csync2 or syncthing instead.  Dumbsync is
designed to solve a specific problem where extremely low dependency
architecture is important.
