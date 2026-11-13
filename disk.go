package scionic

import (
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
)

// Folders on disk, held open.
//
// A path is looked up afresh every time it is used. Whoever can write to a
// folder on the way can swap it for a link between two uses, and send the
// second use somewhere else. So nothing here is reached by its whole path
// but the one place a caller names. From there on a folder is held open, and
// what is in it is reached from the folder itself, one name at a time, and
// never through a link.
//
//   - On Unix a held folder is an open descriptor. What is in it is opened
//     from that descriptor, with links refused by the system itself.
//   - On Windows a held folder is a handle that does not share deletion. The
//     folder can be neither renamed nor replaced while it is held, and a held
//     folder keeps the folders above it held. What is in it is opened by its
//     path, as itself, and refused when it turns out to be a link. The path
//     is in the form that Windows takes letter for letter, so a name is the
//     name that was listed and no other.
//   - Elsewhere a held folder is its path, and no more is promised than the
//     standard library gives.
//
// On every system a name is one name: an empty one, a dot name, or one with
// a separator in it is refused before the system sees it.

// found is what a name in a folder is, without following it.
type found uint8

const (
	foundFile found = iota + 1
	foundFolder
	foundLink
	foundOther
)

// Why something in a held folder was not opened, when it was not the system
// that said no.
var (
	// errRefusedLink: a link is there.
	errRefusedLink = errors.New("a link is there")
	// errRefusedOther: something other than what was asked for is there.
	errRefusedOther = errors.New("something other than what was asked for is there")
	// errNotOneName: what was asked for is not named by one name.
	errNotOneName = errors.New("what is opened or made in a folder is named by one name")
)

// listedName is a name in a folder, and what it is.
type listedName struct {
	name  string
	found found
}

// held is a folder that is held open. It is counted: whoever keeps it for
// later retains it, and everyone who has it releases it when done. The
// folder is closed when the last one has.
type held struct {
	// path is where the folder was when it was opened. It is for messages:
	// nothing is opened by it.
	path  string
	inner heldInner
	users atomic.Int32
}

func newHeld(path string, inner heldInner) *held {
	folder := &held{path: path, inner: inner}
	folder.users.Store(1)
	return folder
}

// openHeld holds the folder at path, as the caller names it: a link on the
// way to it, or at its end, is the caller's.
func openHeld(path string) (*held, error) {
	inner, err := openHeldInner(path)
	if err != nil {
		return nil, err
	}
	return newHeld(path, inner), nil
}

// createHeld makes a new folder at path, and holds it. Anything that is
// there already is an error.
func createHeld(path string) (*held, error) {
	inner, err := createHeldInner(path)
	if err != nil {
		return nil, err
	}
	return newHeld(path, inner), nil
}

// retain keeps the folder held for one more user.
func (h *held) retain() *held {
	h.users.Add(1)
	return h
}

// release gives the folder up for one user, and closes it with the last.
func (h *held) release() {
	if h.users.Add(-1) == 0 {
		h.inner.close()
	}
}

// list gives the names in the folder, and what each is. Nothing is
// followed.
func (h *held) list() ([]listedName, error) {
	listed, err := h.inner.list(h.path)
	runtime.KeepAlive(h)
	return listed, err
}

// openFolder holds the folder name in this one. A link is refused.
func (h *held) openFolder(name string) (*held, error) {
	if err := oneName(name); err != nil {
		return nil, err
	}
	path := pathWithin(h.path, name)
	inner, err := h.inner.openFolder(h, name, path)
	runtime.KeepAlive(h)
	if err != nil {
		return nil, err
	}
	return newHeld(path, inner), nil
}

// openFile opens the file name in this folder, to read. A link is refused,
// and so is anything that is not a plain file.
func (h *held) openFile(name string) (io.ReadCloser, error) {
	if err := oneName(name); err != nil {
		return nil, err
	}
	file, err := h.inner.openFile(name, pathWithin(h.path, name))
	runtime.KeepAlive(h)
	return file, err
}

// createFolder makes the new folder name in this one, and holds it.
func (h *held) createFolder(name string) (*held, error) {
	if err := oneName(name); err != nil {
		return nil, err
	}
	path := pathWithin(h.path, name)
	inner, err := h.inner.createFolder(h, name, path)
	runtime.KeepAlive(h)
	if err != nil {
		return nil, err
	}
	return newHeld(path, inner), nil
}

// createFile makes the new file name in this folder, to write. Anything
// that is there already is an error, a link too.
func (h *held) createFile(name string) (io.WriteCloser, error) {
	if err := oneName(name); err != nil {
		return nil, err
	}
	file, err := h.inner.createFile(name, pathWithin(h.path, name))
	runtime.KeepAlive(h)
	return file, err
}

// oneName: a name in a folder is one name. One that could lead out of the
// folder, or to a part of a file, is refused before the system sees it: an
// empty name, a dot name, and a name with a separator or a zero byte in it.
// On Windows a colon too, which names a second stream of a file's bytes.
func oneName(name string) error {
	moreThanAName := name == "" || name == "." || name == ".." ||
		strings.ContainsAny(name, "/\x00") ||
		(runtime.GOOS == "windows" && strings.ContainsAny(name, "\\:"))
	if moreThanAName {
		return errNotOneName
	}
	return nil
}

// pathWithin gives the path of a name in a folder, for a message. Nothing
// reads the name as a path.
func pathWithin(folder, name string) string {
	if folder == "" {
		return name
	}
	if last := folder[len(folder)-1]; os.IsPathSeparator(last) {
		return folder + name
	}
	return folder + string(os.PathSeparator) + name
}

// readOf turns a reader of the usual kind into one that gives no bytes, and
// no error, when it has ended.
func readOf(reader io.Reader) func(buffer []byte) (int, error) {
	return func(buffer []byte) (int, error) {
		for {
			count, err := reader.Read(buffer)
			if count > 0 {
				return count, nil
			}
			if errors.Is(err, io.EOF) {
				return 0, nil
			}
			if err != nil {
				return 0, err
			}
			// No bytes and no error: the reader has nothing yet, and is
			// asked again.
		}
	}
}
