package index

import (
	"sync"
)

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

	// Subset is a claim made by whoever published the index: it leaves
	// out part of the tree on purpose, and an entry missing from it is
	// not evidence that the file is gone.  Copying from such an index
	// is fine; removing anything on its authority is not, because the
	// omissions look exactly like deletions from here.
	//
	// Like Incomplete, the flag is named in the negative so that a
	// manifest written before it existed decodes as a description of a
	// whole tree and nothing about old clients changes.  Narrowing an
	// index that already claimed to be a subset keeps the claim.
	// Filtering a reader does for its own purposes is not recorded
	// here, because that reader narrows both ends together and can see
	// the result of doing so: only a publisher can leave somebody else
	// holding a list with holes in it.
	Subset bool

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
