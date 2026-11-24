package index

import (
	"sync"
)

// An Indexer handles indexing.
type Indexer struct {
	idx *Index
}

// Index is a listing of files and their hashes which allows you to
// determine whether a file is out of date.  The Index is flat, and
// you must parse paths to do subdirectories.
type Index struct {
	Files    map[string][]byte
	HashType HashType

	// Incomplete is set when part of the tree could not be read while
	// the index was being built.  A missing entry in an incomplete
	// index does not prove that the file is gone, so such an index must
	// never be used to decide what to delete.  Indexes written before
	// this field existed decode as complete, which leaves their
	// behaviour unchanged.
	Incomplete bool

	// Errors holds a short description of the read failures that made
	// the index incomplete.
	Errors []string

	*sync.Mutex
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
