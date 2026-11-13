//go:build !unix && !windows

package scionic

import (
	"io"
	"io/fs"
	"os"
)

// heldInner is nothing: on this system a held folder is its path, and no
// more is promised than the standard library gives.
type heldInner struct{}

func openHeldInner(path string) (heldInner, error) {
	info, err := os.Stat(path)
	if err != nil {
		return heldInner{}, err
	}
	if !info.IsDir() {
		return heldInner{}, &os.PathError{Op: "open", Path: path, Err: fs.ErrInvalid}
	}
	return heldInner{}, nil
}

func createHeldInner(path string) (heldInner, error) {
	return heldInner{}, os.Mkdir(path, 0o777)
}

func (heldInner) close() {}

func foundOf(mode fs.FileMode) found {
	switch {
	case mode&fs.ModeSymlink != 0:
		return foundLink
	case mode.IsDir():
		return foundFolder
	case mode.IsRegular():
		return foundFile
	}
	return foundOther
}

func (heldInner) list(path string) ([]listedName, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	listed := make([]listedName, 0, len(entries))
	for _, entry := range entries {
		listed = append(listed, listedName{name: entry.Name(), found: foundOf(entry.Type())})
	}
	return listed, nil
}

// check: what is at path has to be what was asked for, and no link.
func check(path string, wanted found) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	switch is := foundOf(info.Mode()); {
	case is == foundLink:
		return errRefusedLink
	case is != wanted:
		return errRefusedOther
	}
	return nil
}

func (heldInner) openFolder(_ *held, _, path string) (heldInner, error) {
	return heldInner{}, check(path, foundFolder)
}

func (heldInner) openFile(_, path string) (io.ReadCloser, error) {
	if err := check(path, foundFile); err != nil {
		return nil, err
	}
	return os.Open(path)
}

func (heldInner) makeFolder(_, path string) error {
	return os.Mkdir(path, 0o777)
}

func (heldInner) createFile(_, path string) (io.WriteCloser, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
}
