//go:build unix

package scionic

import (
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// heldInner is an open descriptor of the folder.
type heldInner struct {
	// dir owns the descriptor, and reads the folder's names. Should a
	// folder be forgotten without being closed, it is closed when dir is
	// collected.
	dir *os.File
	// fd is the descriptor, for the system's calls that open what is in
	// the folder from the folder itself.
	fd int
}

// The flags a folder is opened with from the folder it is in: a link is
// refused by the system itself.
const folderFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

// openat opens a name in an open folder, and again when the call was only
// interrupted.
func openat(folder int, name string, flags int, mode uint32) (int, error) {
	for {
		fd, err := unix.Openat(folder, name, flags, mode)
		if err != unix.EINTR {
			return fd, err
		}
	}
}

func heldInnerOf(fd int, path string) heldInner {
	return heldInner{dir: os.NewFile(uintptr(fd), path), fd: fd}
}

func openHeldInner(path string) (heldInner, error) {
	fd, err := openat(unix.AT_FDCWD, path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return heldInner{}, err
	}
	return heldInnerOf(fd, path), nil
}

func createHeldInner(path string) (heldInner, error) {
	if err := mkdirat(unix.AT_FDCWD, path); err != nil {
		return heldInner{}, err
	}
	fd, err := openat(unix.AT_FDCWD, path, folderFlags, 0)
	if err != nil {
		return heldInner{}, err
	}
	return heldInnerOf(fd, path), nil
}

// mkdirat makes a folder that everyone may read, write and enter, less what
// the process's mask takes away.
func mkdirat(folder int, name string) error {
	for {
		err := unix.Mkdirat(folder, name, 0o777)
		if err != unix.EINTR {
			return err
		}
	}
}

func (i heldInner) close() {
	_ = i.dir.Close()
}

func foundOf(mode uint32) found {
	switch mode & unix.S_IFMT {
	case unix.S_IFREG:
		return foundFile
	case unix.S_IFDIR:
		return foundFolder
	case unix.S_IFLNK:
		return foundLink
	}
	return foundOther
}

// look says what name is now, without following it.
func (i heldInner) look(name string) (found, error) {
	var stat unix.Stat_t
	for {
		err := unix.Fstatat(i.fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return 0, err
		}
		return foundOf(uint32(stat.Mode)), nil
	}
}

// refusedBy says why an open that refuses links failed. Systems differ in
// the error they give for a link, so what is there is looked at.
func (i heldInner) refusedBy(name string, err error, wanted found) error {
	switch is, lookErr := i.look(name); {
	case lookErr != nil:
		return err
	case is == foundLink:
		return errRefusedLink
	case is != wanted:
		return errRefusedOther
	}
	return err
}

// list reads the folder's names from the folder itself, and looks at each
// from the folder itself. No path is used.
func (i heldInner) list(string) ([]listedName, error) {
	if _, err := i.dir.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	names, err := i.dir.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	listed := make([]listedName, 0, len(names))
	for _, name := range names {
		is, err := i.look(name)
		if err != nil {
			return nil, &os.PathError{Op: "look at", Path: name, Err: err}
		}
		listed = append(listed, listedName{name: name, found: is})
	}
	return listed, nil
}

func (i heldInner) openFolder(_ *held, name, path string) (heldInner, error) {
	fd, err := openat(i.fd, name, folderFlags, 0)
	if err != nil {
		return heldInner{}, i.refusedBy(name, err, foundFolder)
	}
	return heldInnerOf(fd, path), nil
}

func (i heldInner) openFile(name, _ string) (io.ReadCloser, error) {
	// Not waiting: a pipe that took the file's place would hold a waiting
	// open for ever. A plain file is read the same either way.
	fd, err := openat(i.fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, i.refusedBy(name, err, foundFile)
	}
	// What was opened is what is read, so it is what has to be a file.
	var stat unix.Stat_t
	for {
		err = unix.Fstat(fd, &stat)
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if foundOf(uint32(stat.Mode)) != foundFile {
		_ = unix.Close(fd)
		return nil, errRefusedOther
	}
	return &rawFile{fd: fd}, nil
}

func (i heldInner) createFolder(_ *held, name, path string) (heldInner, error) {
	if err := mkdirat(i.fd, name); err != nil {
		return heldInner{}, err
	}
	fd, err := openat(i.fd, name, folderFlags, 0)
	if err != nil {
		return heldInner{}, err
	}
	return heldInnerOf(fd, path), nil
}

func (i heldInner) createFile(name, _ string) (io.WriteCloser, error) {
	// A link that is there under the name is not gone through: the name is
	// taken, and that is an error.
	fd, err := openat(i.fd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o666)
	if err != nil {
		return nil, err
	}
	return &rawFile{fd: fd}, nil
}

// rawFile is an open file, read or written by the system's calls with
// nothing in between. It is used by one goroutine.
type rawFile struct {
	fd int
}

func (f *rawFile) Read(buffer []byte) (int, error) {
	for {
		count, err := unix.Read(f.fd, buffer)
		switch {
		case err == unix.EINTR:
			continue
		case err != nil:
			return 0, err
		case count == 0 && len(buffer) > 0:
			return 0, io.EOF
		}
		return count, nil
	}
}

func (f *rawFile) Write(content []byte) (int, error) {
	written := 0
	for written < len(content) {
		count, err := unix.Write(f.fd, content[written:])
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return written, err
		}
		if count == 0 {
			return written, io.ErrShortWrite
		}
		written += count
	}
	return written, nil
}

// Close closes the file once. Closing it again does nothing, so that the
// descriptor's number, which the system may have given to another file by
// then, is never closed twice.
func (f *rawFile) Close() error {
	if f.fd < 0 {
		return nil
	}
	fd := f.fd
	f.fd = -1
	return unix.Close(fd)
}
