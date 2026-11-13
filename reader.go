package scionic

import "io"

// What the library counts on from a reader and from a writer, whatever
// reader or writer it is handed.
//
// A reader of the usual kind may give bytes and an error at once, may give
// neither for as long as it likes, and is trusted to count its own bytes. A
// writer is trusted to say so when it took fewer bytes than it was given.
// Taken as they come, the first makes a file that failed to arrive look like
// a shorter file that arrived whole, the second makes a build wait for ever,
// and a writer that says nothing leaves a file short on disk. So nothing is
// read or written here but through the helpers below.

// emptyReads is how many times in a row a reader may give no bytes and no
// error before it is given up. It is the number that the standard library's
// buffered reader uses.
const emptyReads = 100

// steady is a reader held to what the library counts on:
//
//   - Bytes and an error never come at once. The bytes are handed on, and
//     the error is told at the next read, and at every read after it. So a
//     reader that fails with its last bytes is never taken for one that
//     ended there.
//   - A reader that has ended, or failed, is not asked again.
//   - A reader that gives neither a byte nor an error is asked again a
//     hundred times, and then given up with io.ErrNoProgress. Nobody waits
//     for it for ever.
//   - A reader that says it gave more bytes than it was asked for, or fewer
//     than none, is an error, and not a crash.
type steady struct {
	inner io.Reader
	// pending is what the reader said last, to be told from now on.
	pending error
}

// steadied gives a reader as a steady one. A reader that is steady already
// is given back as it is, with what it remembers.
func steadied(reader io.Reader) *steady {
	if already, is := reader.(*steady); is {
		return already
	}
	return &steady{inner: reader}
}

func (s *steady) Read(buffer []byte) (int, error) {
	if s.pending != nil {
		return 0, s.pending
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	for range emptyReads {
		count, err := s.inner.Read(buffer)
		if count < 0 || count > len(buffer) {
			s.pending = invariant("a reader gave more bytes than it was asked for")
			return 0, s.pending
		}
		if count > 0 {
			s.pending = err
			return count, nil
		}
		if err != nil {
			s.pending = err
			return 0, err
		}
		// No bytes and no error: the reader has nothing yet, and is asked
		// again.
	}
	s.pending = io.ErrNoProgress
	return 0, s.pending
}

// failed gives the error that the reader has failed with, if it has. That
// it has ended is no failure.
func (s *steady) failed() error {
	// A reader says that it has ended with io.EOF itself. An error that only
	// wraps io.EOF is some other error, and is told as one.
	if s.pending == io.EOF {
		return nil
	}
	return s.pending
}

// readOf turns a reader of the usual kind into one that gives no bytes, and
// no error, when it has ended.
func readOf(reader io.Reader) func(buffer []byte) (int, error) {
	from := steadied(reader)
	return func(buffer []byte) (int, error) {
		count, err := from.Read(buffer)
		// io.EOF itself, and not an error that wraps it: see failed.
		if err == io.EOF {
			return count, nil
		}
		return count, err
	}
}

// writeAll writes all of content, or gives an error. A writer that took
// fewer bytes than it was given and said nothing is not taken at its word:
// what it wrote would be short, and nobody would know.
func writeAll(out io.Writer, content []byte) error {
	count, err := out.Write(content)
	if err != nil {
		return err
	}
	if count != len(content) {
		return io.ErrShortWrite
	}
	return nil
}
