package scionic

import (
	"fmt"
)

// errorKind says what kind of thing went wrong.
type errorKind uint8

const (
	kindIO errorKind = iota + 1
	kindStream
	kindInvalidData
	kindInvalidBase32
	kindInvalidHex
	kindInvalidRoot
	kindInvalidName
	kindHashMismatch
	kindSizeMismatch
	kindTotalsMismatch
	kindIntakeFailed
	kindNotWanted
	kindCorrupt
	kindMissing
	kindTooDeep
	kindUnwritableName
	kindUnsupportedEntry
	kindNonUTF8FileName
	kindPeer
	kindLimitExceeded
	kindSystemTimeBeforeUnixEpoch
	kindSizeOverflow
	kindInvariant
)

// Error is every error this package returns. What went wrong is told by
// matching it with [errors.Is] against one of the Err values of this
// package, and the fields say more about it where they apply. Read them
// with [errors.As].
type Error struct {
	kind errorKind
	// Operation is what was being done: for ErrIO and ErrStream.
	Operation string
	// Path is a path on disk: for ErrIO, ErrUnsupportedEntry and
	// ErrNonUTF8FileName.
	Path string
	// Detail says more: for ErrInvalidData, ErrInvalidRoot, ErrPeer and
	// ErrInvariant.
	Detail string
	// Name is the name in question: for ErrInvalidName and
	// ErrUnwritableName. It is cut to 80 characters.
	Name string
	// Reason is why a name is refused.
	Reason string
	// What is the thing in question: "a piece", "a stored file's list".
	What string
	// Hash names the thing: for ErrCorrupt and ErrMissing.
	Hash Hash
	// Expected and Actual are sizes in bytes: for ErrSizeMismatch. For
	// ErrTotalsMismatch, Expected is what the record says and Actual is
	// what the tree has.
	Expected, Actual uint64
	// Limit and Requested: for ErrLimitExceeded. ErrTooDeep has Limit.
	Limit, Requested uint64
	// Err is the system's own error: for ErrIO and ErrStream.
	Err error
}

// One value for each kind of error, to match with [errors.Is].
var (
	// ErrIO: the file system refused something.
	ErrIO = &Error{kind: kindIO}
	// ErrStream: reading from a stream or writing to one failed.
	ErrStream = &Error{kind: kindStream}
	// ErrInvalidData: bytes that are not a well-formed thing of the format.
	ErrInvalidData = &Error{kind: kindInvalidData}
	// ErrInvalidBase32: text that is not lowercase base32.
	ErrInvalidBase32 = &Error{kind: kindInvalidBase32}
	// ErrInvalidHex: text that is not lowercase hex of the right length.
	ErrInvalidHex = &Error{kind: kindInvalidHex}
	// ErrInvalidRoot: text that is not a root.
	ErrInvalidRoot = &Error{kind: kindInvalidRoot}
	// ErrInvalidName: a name that the format does not allow.
	ErrInvalidName = &Error{kind: kindInvalidName}
	// ErrHashMismatch: a thing does not hash to what it has to.
	ErrHashMismatch = &Error{kind: kindHashMismatch}
	// ErrSizeMismatch: a thing does not have the size it has to.
	ErrSizeMismatch = &Error{kind: kindSizeMismatch}
	// ErrTotalsMismatch: a record's totals are not those of its tree.
	ErrTotalsMismatch = &Error{kind: kindTotalsMismatch}
	// ErrIntakeFailed: an intake was used again after a part of its tree
	// was refused.
	ErrIntakeFailed = &Error{kind: kindIntakeFailed}
	// ErrNotWanted: something arrived that is not tied to the root, or was
	// not asked for.
	ErrNotWanted = &Error{kind: kindNotWanted}
	// ErrCorrupt: a store holds, under a hash, bytes that are not what the
	// hash says. Removing them from the store and fetching the tree again
	// mends it.
	ErrCorrupt = &Error{kind: kindCorrupt}
	// ErrMissing: a store does not hold something that the tree needs.
	ErrMissing = &Error{kind: kindMissing}
	// ErrTooDeep: folders sit inside one another more deeply than the
	// format allows.
	ErrTooDeep = &Error{kind: kindTooDeep}
	// ErrUnwritableName: a name that this system cannot hold as it is.
	ErrUnwritableName = &Error{kind: kindUnwritableName}
	// ErrUnsupportedEntry: a link or a special file, where only files and
	// folders are stored.
	ErrUnsupportedEntry = &Error{kind: kindUnsupportedEntry}
	// ErrNonUTF8FileName: a file's name on disk is not UTF-8.
	ErrNonUTF8FileName = &Error{kind: kindNonUTF8FileName}
	// ErrPeer: a peer could not answer, or stopped early.
	ErrPeer = &Error{kind: kindPeer}
	// ErrLimitExceeded: something needs more than its limit allows.
	ErrLimitExceeded = &Error{kind: kindLimitExceeded}
	// ErrSystemTimeBeforeUnixEpoch: the system clock is before 1970.
	ErrSystemTimeBeforeUnixEpoch = &Error{kind: kindSystemTimeBeforeUnixEpoch}
	// ErrSizeOverflow: a size or a count does not fit.
	ErrSizeOverflow = &Error{kind: kindSizeOverflow}
	// ErrInvariant: the library found itself in a state it rules out.
	ErrInvariant = &Error{kind: kindInvariant}
)

func (e *Error) Error() string {
	switch e.kind {
	case kindIO:
		return fmt.Sprintf("failed to %s %s: %v", e.Operation, e.Path, e.Err)
	case kindStream:
		return fmt.Sprintf("failed to %s: %v", e.Operation, e.Err)
	case kindInvalidData:
		return "malformed: " + e.Detail
	case kindInvalidBase32:
		return "not lowercase base32"
	case kindInvalidHex:
		return "not lowercase hex of the right length"
	case kindInvalidRoot:
		return fmt.Sprintf("not a root: %q", e.Detail)
	case kindInvalidName:
		return fmt.Sprintf("the name %q is not allowed: %s", e.Name, e.Reason)
	case kindHashMismatch:
		return e.What + " does not hash to what it has to"
	case kindSizeMismatch:
		return fmt.Sprintf("%s holds %d bytes where %d are due", e.What, e.Actual, e.Expected)
	case kindTotalsMismatch:
		return fmt.Sprintf("the record says %d for %s, and the tree has %d", e.Expected, e.What, e.Actual)
	case kindIntakeFailed:
		return "an intake is done once a part of its tree was refused: start a new one"
	case kindNotWanted:
		return e.What + " is not tied to the root, or was not asked for"
	case kindCorrupt:
		return fmt.Sprintf("%s %s is damaged: its bytes are not what its hash says", e.What, e.Hash)
	case kindMissing:
		return fmt.Sprintf("%s %s is not held", e.What, e.Hash)
	case kindTooDeep:
		return fmt.Sprintf("folders sit inside one another more than %d deep", e.Limit)
	case kindUnwritableName:
		return fmt.Sprintf("the name %q cannot be written on this system: %s", e.Name, e.Reason)
	case kindUnsupportedEntry:
		return fmt.Sprintf("%s is %s, and only files and folders are stored", e.Path, e.What)
	case kindNonUTF8FileName:
		return fmt.Sprintf("path has a non-UTF-8 file name: %q", e.Path)
	case kindPeer:
		return "the peer " + e.Detail
	case kindLimitExceeded:
		return fmt.Sprintf("%s needs %d, over the limit of %d", e.What, e.Requested, e.Limit)
	case kindSystemTimeBeforeUnixEpoch:
		return "the system clock is before the Unix epoch"
	case kindSizeOverflow:
		return e.What + " exceeds the supported size"
	case kindInvariant:
		return "internal invariant failed: " + e.Detail
	}
	return "unknown error"
}

// Unwrap gives the system's own error, for the kinds that have one.
func (e *Error) Unwrap() error {
	return e.Err
}

// Is reports whether target is an [*Error] of the same kind, so that
// errors.Is(err, ErrCorrupt) matches every error of that kind.
func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	return ok && other.kind == e.kind
}

func invalid(detail string) error {
	return &Error{kind: kindInvalidData, Detail: detail}
}

func ioError(operation, path string, err error) error {
	return &Error{kind: kindIO, Operation: operation, Path: path, Err: err}
}

func streamError(operation string, err error) error {
	return &Error{kind: kindStream, Operation: operation, Err: err}
}

func invariant(detail string) error {
	return &Error{kind: kindInvariant, Detail: detail}
}

func sizeOverflow(what string) error {
	return &Error{kind: kindSizeOverflow, What: what}
}

func limitExceeded(what string, limit, requested uint64) error {
	return &Error{kind: kindLimitExceeded, What: what, Limit: limit, Requested: requested}
}

func hashMismatch(what string) error {
	return &Error{kind: kindHashMismatch, What: what}
}

func sizeMismatch(what string, expected, actual uint64) error {
	return &Error{kind: kindSizeMismatch, What: what, Expected: expected, Actual: actual}
}

func notWanted(what string) error {
	return &Error{kind: kindNotWanted, What: what}
}

func peerError(detail string) error {
	return &Error{kind: kindPeer, Detail: detail}
}

func corrupt(what string, hash Hash) error {
	return &Error{kind: kindCorrupt, What: what, Hash: hash}
}

func missing(what string, hash Hash) error {
	return &Error{kind: kindMissing, What: what, Hash: hash}
}

// first80 cuts text to 80 characters, for a name in an error.
func first80(text string) string {
	count := 0
	for at := range text {
		if count == 80 {
			return text[:at]
		}
		count++
	}
	return text
}
