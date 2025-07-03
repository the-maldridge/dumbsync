package index

import (
	"io/fs"
	"sync"
)

// An Indexer handles indexing.
type Indexer struct {
	idx *Index

	basepath string
}

// Index is a listing of files and their hashes which allows you to
// determine whether a file is out of date.  The Index is flat, and
// you must parse paths to do subdirectories.
type Index struct {
	Files    map[string][]byte
	HashType HashType

	*sync.Mutex
	wg sync.WaitGroup
	fs fs.FS
}

// HashType stores what kind of hash was used to validate the data.
type HashType int

const (
	// MD5 is a basic hash type.  Not great, not bad, just a basic
	// hash type.
	MD5 HashType = iota

	// XXHash is a very fast non-crypto hash.  It is great for
	// detecting file differences but is not as resistant to
	// tampering.
	XXHash
)
