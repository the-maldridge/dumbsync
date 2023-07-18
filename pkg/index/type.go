package index

import (
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
	Files map[string]FileData

	*sync.Mutex
	wg sync.WaitGroup
}

// FileData provides a transport for the information about the files.
// It is done as a structure to allow easily adopting different hash
// types in the future.
type FileData struct {
	HashType  HashType
	HashValue []byte
}

// HashType stores what kind of hash was used to validate the data.
type HashType int

const (
	// MD5 is a basic hash type.  Not great, not bad, just a basic
	// hash type.
	MD5 HashType = iota
)
