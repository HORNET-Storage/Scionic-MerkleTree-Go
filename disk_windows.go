//go:build windows

package scionic

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// heldInner is the folder's exact path, with the folder pinned where it is.
type heldInner struct {
	// exact is the folder's path in the form that Windows takes letter for
	// letter. Everything in the folder is reached through it.
	exact string
	// pin is open for as long as the folder is held. It shares reading and
	// writing and not deleting, so the folder can be neither renamed nor
	// replaced meanwhile.
	pin windows.Handle
	// above is the folder this one is in, held as long as this one is.
	above *held
}

// A reparse point whose tag has this bit stands for another name: a link
// between files or folders, or a junction.
const nameSurrogateBit = 0x2000_0000

// All may read, write and delete while a file is open, as the standard
// library opens files.
const shareAll = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE

// shareNoDelete keeps what is open where it is: nobody may rename or delete
// it meanwhile.
const shareNoDelete = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE

// exactPath gives path in the form that Windows takes letter for letter.
//
// Windows changes an ordinary path when it looks it up: a dot or a space at
// the end of a name is dropped, and some names are devices wherever they
// stand. So a file "a." would be read as the file "a" beside it. A path that
// begins with \\?\ is looked up as it is written.
//
// What the caller wrote is first read the way Windows reads it, which is
// what the caller means by it. The names that are added to it afterwards
// are the ones that a folder listed, and are taken as they are.
func exactPath(path string) (string, error) {
	const verbatim = `\\?\`
	if strings.HasPrefix(path, verbatim) {
		// It is in that form already.
		return path, nil
	}
	whole, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	volume := filepath.VolumeName(whole)
	rest := whole[len(volume):]
	switch {
	case strings.HasPrefix(volume, verbatim):
		return whole, nil
	case len(volume) == 2 && volume[1] == ':':
		// C:\folder is \\?\C:\folder.
		return verbatim + volume + rest, nil
	case strings.HasPrefix(volume, `\\.\`):
		// \\.\C:\folder is \\?\C:\folder.
		return verbatim + volume[4:] + rest, nil
	case strings.HasPrefix(volume, `\\`):
		// \\server\share\folder is \\?\UNC\server\share\folder.
		return verbatim + `UNC\` + volume[2:] + rest, nil
	}
	return whole, nil
}

// within gives the exact path of name in this folder. The name is added as
// it is: nothing reads it as a path.
func (i heldInner) within(name string) string {
	if strings.HasSuffix(i.exact, `\`) {
		return i.exact + name
	}
	return i.exact + `\` + name
}

// attributesOf gives the attributes of what a handle is open on, and the
// tag of what stands in for it, if anything does.
func attributesOf(handle windows.Handle) (attributes, tag uint32, err error) {
	var info [8]byte
	if err := windows.GetFileInformationByHandleEx(handle, windows.FileAttributeTagInfo, &info[0], uint32(len(info))); err != nil {
		return 0, 0, err
	}
	return binary.LittleEndian.Uint32(info[0:4]), binary.LittleEndian.Uint32(info[4:8]), nil
}

func isLink(attributes, tag uint32) bool {
	return attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 && tag&nameSurrogateBit != 0
}

func isFolder(attributes, tag uint32) bool {
	return !isLink(attributes, tag) && attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
}

// open opens what is at an exact path.
func open(path string, access, share, disposition, flags uint32) (windows.Handle, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return windows.CreateFile(pointer, access, share, nil, disposition, flags, 0)
}

// refusedByPath says why opening path failed, for one that had to be
// wanted.
func refusedByPath(path string, err error, wanted found) error {
	handle, lookErr := open(path, 0, shareAll, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if lookErr != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	attributes, tag, lookErr := attributesOf(handle)
	switch {
	case lookErr != nil:
		return err
	case isLink(attributes, tag):
		return errRefusedLink
	case isFolder(attributes, tag) != (wanted == foundFolder):
		return errRefusedOther
	}
	return err
}

// pinFolder opens the folder at path so that it stays what and where it is.
// Unless follow is set, a link is opened as itself, and refused.
func pinFolder(path string, follow bool) (windows.Handle, error) {
	flags := uint32(windows.FILE_FLAG_BACKUP_SEMANTICS)
	if !follow {
		flags |= windows.FILE_FLAG_OPEN_REPARSE_POINT
	}
	pin, err := open(path, windows.GENERIC_READ, shareNoDelete, windows.OPEN_EXISTING, flags)
	if err != nil {
		return windows.InvalidHandle, refusedByPath(path, err, foundFolder)
	}
	attributes, tag, err := attributesOf(pin)
	switch {
	case err != nil:
	case isLink(attributes, tag):
		err = errRefusedLink
	case !isFolder(attributes, tag):
		err = errRefusedOther
	default:
		return pin, nil
	}
	_ = windows.CloseHandle(pin)
	return windows.InvalidHandle, err
}

// asSystemError turns a refusal into an error of the usual kind, where the
// caller is told nothing else.
func asSystemError(err error) error {
	switch {
	case errors.Is(err, errRefusedLink):
		return errors.New("a link is there, where a folder has to be")
	case errors.Is(err, errRefusedOther):
		return errors.New("something that is no folder is there, where a folder has to be")
	}
	return err
}

func openHeldInner(path string) (heldInner, error) {
	exact, err := exactPath(path)
	if err != nil {
		return heldInner{}, err
	}
	pin, err := pinFolder(exact, true)
	if err != nil {
		return heldInner{}, asSystemError(err)
	}
	return heldInner{exact: exact, pin: pin}, nil
}

func makeFolder(exact string) error {
	pointer, err := windows.UTF16PtrFromString(exact)
	if err != nil {
		return err
	}
	return windows.CreateDirectory(pointer, nil)
}

func createHeldInner(path string) (heldInner, error) {
	exact, err := exactPath(path)
	if err != nil {
		return heldInner{}, err
	}
	if err := makeFolder(exact); err != nil {
		return heldInner{}, err
	}
	pin, err := pinFolder(exact, false)
	if err != nil {
		return heldInner{}, asSystemError(err)
	}
	return heldInner{exact: exact, pin: pin}, nil
}

func (i heldInner) close() {
	_ = windows.CloseHandle(i.pin)
	if i.above != nil {
		i.above.release()
	}
}

// list reads the folder's names, and what each is, as the folder itself
// gives them. Nothing is opened.
func (i heldInner) list(string) ([]listedName, error) {
	pattern, err := windows.UTF16PtrFromString(i.within("*"))
	if err != nil {
		return nil, err
	}
	var entry windows.Win32finddata
	search, err := windows.FindFirstFile(pattern, &entry)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return nil, nil
		}
		return nil, err
	}
	defer windows.FindClose(search)
	var listed []listedName
	for {
		// A name that is not UTF-16 is given in a form that is not UTF-8,
		// and is refused as such by whoever reads the list.
		name := windows.UTF16ToString(entry.FileName[:])
		if name != "." && name != ".." {
			is := foundFile
			switch {
			case isLink(entry.FileAttributes, entry.Reserved0):
				is = foundLink
			case isFolder(entry.FileAttributes, entry.Reserved0):
				is = foundFolder
			}
			listed = append(listed, listedName{name: name, found: is})
		}
		if err := windows.FindNextFile(search, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				return listed, nil
			}
			return nil, err
		}
	}
}

func (i heldInner) openFolder(above *held, name, _ string) (heldInner, error) {
	exact := i.within(name)
	pin, err := pinFolder(exact, false)
	if err != nil {
		return heldInner{}, err
	}
	return heldInner{exact: exact, pin: pin, above: above.retain()}, nil
}

// openAsItself opens path as itself, and says whether it is a plain file
// with nothing standing in for it. A link is refused, and so is a folder.
func openAsItself(path string, share uint32) (windows.Handle, bool, error) {
	handle, err := open(path, windows.GENERIC_READ, share, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if err != nil {
		return windows.InvalidHandle, false, refusedByPath(path, err, foundFile)
	}
	attributes, tag, err := attributesOf(handle)
	switch {
	case err != nil:
	case isLink(attributes, tag):
		err = errRefusedLink
	case isFolder(attributes, tag):
		err = errRefusedOther
	default:
		return handle, attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0, nil
	}
	_ = windows.CloseHandle(handle)
	return windows.InvalidHandle, false, err
}

func (i heldInner) openFile(name, _ string) (io.ReadCloser, error) {
	exact := i.within(name)
	handle, plain, err := openAsItself(exact, shareAll)
	if err != nil {
		return nil, err
	}
	if plain {
		// For a plain file, opening it as itself is opening it.
		return os.NewFile(uintptr(handle), exact), nil
	}
	// Something stands in for this file, as for one that is fetched when
	// it is read, and opened as itself it may not read as the file does.
	// So it is held where it is, looked at again, and opened in the usual
	// way while it cannot be swapped.
	_ = windows.CloseHandle(handle)
	kept, _, err := openAsItself(exact, shareNoDelete)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(kept)
	return os.Open(exact)
}

func (i heldInner) createFolder(above *held, name, _ string) (heldInner, error) {
	exact := i.within(name)
	if err := makeFolder(exact); err != nil {
		return heldInner{}, err
	}
	pin, err := pinFolder(exact, false)
	if err != nil {
		return heldInner{}, asSystemError(err)
	}
	return heldInner{exact: exact, pin: pin, above: above.retain()}, nil
}

func (i heldInner) createFile(name, _ string) (io.WriteCloser, error) {
	// A link that is there under the name is not gone through: the name is
	// taken, and that is an error.
	exact := i.within(name)
	handle, err := open(exact, windows.GENERIC_WRITE, shareAll, windows.CREATE_NEW,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), exact), nil
}
