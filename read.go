package scionic

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"runtime"
	"strings"
)

// Reading a tree that a store holds: finding a path, reading a file or a
// stretch of it, and writing the tree out to disk.
//
// Everything read from the store is checked again before it is used: a list
// against the hash it was asked for by, and a piece against the hash and the
// length its file gives it. A file's bytes are handed out only when every
// piece they need is there. Otherwise it is an error, never a shorter file.

// RecordOf gives the record of the tree with this root.
func RecordOf(store Store, root Root) (*Record, error) {
	record, err := loadRecord(store, root)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, missing("the record of", root.Hash())
	}
	return record, nil
}

// FolderOf gives the entries of a folder.
func FolderOf(store Store, folder Target) (*FolderList, error) {
	list, err := loadFolder(store, folder)
	if err != nil {
		return nil, err
	}
	if list == nil {
		return nil, missing("the list of the folder", folder.Hash)
	}
	return list, nil
}

// FileOf gives the list of a file's pieces.
func FileOf(store Store, file Target) (*FileList, error) {
	list, err := loadFile(store, file)
	if err != nil {
		return nil, err
	}
	if list == nil {
		return nil, missing("the list of the file", file.Hash)
	}
	return list, nil
}

// Lookup gives what is at path in a tree: names with "/" between them, as
// in "weights/model.safetensors". Nothing is there when a name on the way
// is missing, or is a file. A path that leads through more folders than a
// tree may have is an error.
func Lookup(store Store, root Root, path string) (Entry, bool, error) {
	record, err := RecordOf(store, root)
	if err != nil {
		return Entry{}, false, err
	}
	current := record.Top()
	var found Entry
	for foldersPassed, name := range strings.Split(path, "/") {
		if err := CheckName(name); err != nil {
			return Entry{}, false, err
		}
		if current.Kind != KindFolder {
			return Entry{}, false, nil
		}
		if foldersPassed >= MaxFolderDepth {
			return Entry{}, false, &Error{kind: kindTooDeep, Limit: MaxFolderDepth}
		}
		list, err := FolderOf(store, current)
		if err != nil {
			return Entry{}, false, err
		}
		entry, held := list.Get(name)
		if !held {
			return Entry{}, false, nil
		}
		current = entry.Target()
		found = entry
	}
	return found, true, nil
}

// ReadFile writes all of a file's bytes to out.
func ReadFile(store Store, file Target, out io.Writer) error {
	return ReadRange(store, file, 0, file.Size, out)
}

// ReadFileBytes gives a file's bytes, in memory. Room for them is taken
// only once every piece of the file is found.
func ReadFileBytes(store Store, file Target) ([]byte, error) {
	if file.Size > uint64(maxInt) {
		return nil, sizeOverflow("a file read into memory")
	}
	list, err := FileOf(store, file)
	if err != nil {
		return nil, err
	}
	wanted := piecesOfStretch(list, 0, list.Size())
	if err := requireHeld(store, wanted); err != nil {
		return nil, err
	}
	content := make([]byte, 0, list.Size())
	for _, piece := range wanted {
		kept, err := heldPiece(store, piece.item)
		if err != nil {
			return nil, err
		}
		content = append(content, kept...)
	}
	return content, nil
}

// wantedPiece is a piece that holds a part of a stretch, with the byte of
// the file it starts at.
type wantedPiece struct {
	item  PieceRef
	start uint64
}

// piecesOfStretch gives the pieces that hold the bytes from offset up to
// end, each with the byte it starts at.
func piecesOfStretch(list *FileList, offset, end uint64) []wantedPiece {
	var wanted []wantedPiece
	var start uint64
	for _, item := range list.Items() {
		if start >= end {
			break
		}
		after := start + uint64(item.Length)
		if after > offset {
			wanted = append(wanted, wantedPiece{item: item, start: start})
		}
		start = after
	}
	return wanted
}

// ReadRange writes length bytes of a file, from byte offset, to out.
//
// Before the first byte goes out, every piece of the stretch is looked for.
// If one is missing, nothing is written. Each piece is then checked against
// its hash as it is written.
//
// The file's list is read and checked on every call. For many stretches of
// one long file, open its list once and use ReadOpen.
func ReadRange(store Store, file Target, offset, length uint64, out io.Writer) error {
	list, err := FileOf(store, file)
	if err != nil {
		return err
	}
	end := offset + length
	if end < offset || end > list.Size() {
		return invalid("a stretch of bytes is outside the file")
	}
	if length == 0 {
		return nil
	}
	return writeStretch(store, piecesOfStretch(list, offset, end), offset, end, out)
}

// ReadOpen writes length bytes of a file whose list is open, from byte
// offset, to out. It does what ReadRange does, and finds the pieces without
// a pass over the list.
func ReadOpen(store Store, file *OpenFile, offset, length uint64, out io.Writer) error {
	if length == 0 {
		if offset > file.List().Size() {
			return invalid("a stretch of bytes is outside the file")
		}
		return nil
	}
	first, after, err := file.PiecesFor(offset, length)
	if err != nil {
		return err
	}
	items := file.List().Items()
	wanted := make([]wantedPiece, 0, after-first)
	for number := first; number < after; number++ {
		start, known := file.StartOf(number)
		if !known || number >= uint64(len(items)) {
			return invariant("an open file names a piece it does not have")
		}
		wanted = append(wanted, wantedPiece{item: items[number], start: start})
	}
	return writeStretch(store, wanted, offset, offset+length, out)
}

// requireHeld: every piece has to be held, with the length its list gives
// it. The pieces' bytes are counted here, not read.
func requireHeld(store Store, wanted []wantedPiece) error {
	for _, piece := range wanted {
		if err := requirePiece(store, piece.item); err != nil {
			return err
		}
	}
	return nil
}

func requirePiece(store Store, item PieceRef) error {
	size, held, err := storeSize(store, StoredPiece, item.Hash)
	switch {
	case err != nil:
		return err
	case !held:
		return missing("the piece", item.Hash)
	case size != uint64(item.Length):
		return sizeMismatch("a stored piece", uint64(item.Length), size)
	}
	return nil
}

// heldPiece gives the bytes of a piece that has to be there, checked.
func heldPiece(store Store, item PieceRef) ([]byte, error) {
	kept, held, err := loadPiece(store, item)
	if err != nil {
		return nil, err
	}
	if !held {
		return nil, missing("the piece", item.Hash)
	}
	return kept, nil
}

// writeStretch writes the bytes from offset up to end out of the pieces
// that hold them.
func writeStretch(store Store, wanted []wantedPiece, offset, end uint64, out io.Writer) error {
	if err := requireHeld(store, wanted); err != nil {
		return err
	}
	for _, piece := range wanted {
		kept, err := heldPiece(store, piece.item)
		if err != nil {
			return err
		}
		var from uint64
		if offset > piece.start {
			from = offset - piece.start
		}
		to := min(end, piece.start+uint64(piece.item.Length)) - piece.start
		if _, err := out.Write(kept[from:to]); err != nil {
			return streamError("write a file's bytes", err)
		}
	}
	return nil
}

// Unwritable says why this system cannot hold a name as it is, if it
// cannot.
//
// A tree can hold a name that one system cannot. What is checked here is
// what would make a name land somewhere else than it says: on another file,
// on a device, or outside the folder.
func Unwritable(name string) (reason string, unwritable bool) {
	if CheckName(name) != nil {
		return "the format does not allow it", true
	}
	if runtime.GOOS == "windows" {
		return UnwritableOnWindows(name)
	}
	return "", false
}

// UnwritableOnWindows says why Windows cannot hold a name as it is, if it
// cannot.
func UnwritableOnWindows(name string) (reason string, unwritable bool) {
	for _, letter := range name {
		if letter < ' ' || strings.ContainsRune(`<>:"|?*\/`, letter) {
			return "it holds a character that Windows does not allow in a name", true
		}
	}
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return "Windows drops a dot or a space at the end of a name", true
	}
	// A device name is one whatever follows its first dot: NUL.txt is NUL.
	stem, _, _ := strings.Cut(name, ".")
	stem = asciiUpper(strings.TrimRight(stem, " "))
	numbered := func(port string) bool {
		number, found := strings.CutPrefix(stem, port)
		if !found {
			return false
		}
		switch number {
		case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³":
			return true
		}
		return false
	}
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return "Windows takes it for a device", true
	}
	if numbered("COM") || numbered("LPT") {
		return "Windows takes it for a device", true
	}
	return "", false
}

// asciiUpper makes the letters a to z capitals, and leaves every other
// letter as it is.
func asciiUpper(text string) string {
	upper := []byte(text)
	for at, letter := range upper {
		if letter >= 'a' && letter <= 'z' {
			upper[at] = letter - 'a' + 'A'
		}
	}
	return string(upper)
}

func unwritableError(name, reason string) error {
	return &Error{kind: kindUnwritableName, Name: first80(name), Reason: reason}
}

// DefaultMaxWrittenEntries is the most files and folders that WriteToDisk
// makes unless it is told otherwise.
const DefaultMaxWrittenEntries uint64 = 1_000_000

// WriteOptions says how far writing a tree out may go.
//
// A tree keeps each folder once, however many places the folder has in it,
// and writing it out makes the folder at every one of those places. So a
// tree of a few small lists can stand for more folders than any disk holds:
// a folder with two names for one folder, a hundred deep. Both limits are
// held before anything is written.
type WriteOptions struct {
	// MaxEntries is the most files and folders that may be made, the top
	// one counted. Zero is DefaultMaxWrittenEntries.
	MaxEntries uint64
	// MaxBytes is the most bytes that may be written. A tree's record says
	// how many it takes: the size of what is on top, with a file or a piece
	// that the tree uses twice counted twice. Zero is as many bytes as the
	// tree has.
	MaxBytes uint64
}

// WriteToDisk writes a tree out to disk: its top folder as the new folder
// dest, or its top file as the new file dest.
//
// Writing is stricter than holding. dest must not exist. Before anything is
// written, the whole tree is looked through: every list and every piece
// must be there, no folder may sit too deep, every name must be one that
// this system can hold as it is, and what would be made must fit within the
// limits of WriteOptions. Every file and folder is then created as new.
// Where two names of one folder would land on the same file, as "File" and
// "file" do on Windows and macOS, the second one is refused, and so is a
// name that the system keeps differently from how it was given. A name is
// never changed to make it fit: the error says which name it was.
//
// A tree holds no links, and every folder written into is one this call
// created, so nothing is written outside dest. After an error, what was
// written so far stays.
func WriteToDisk(store Store, root Root, dest string) error {
	return WriteToDiskWith(store, root, dest, WriteOptions{})
}

// folderToFill is a folder that was made and is still to be filled: its
// name in its folder, and what it is.
type folderToFill struct {
	name   string
	folder Target
}

// WriteToDiskWith writes a tree out to disk as WriteToDisk does, within
// other limits.
func WriteToDiskWith(store Store, root Root, dest string, options WriteOptions) error {
	record, err := RecordOf(store, root)
	if err != nil {
		return err
	}
	top := record.Top()
	// First: find everything, count what would be made, and write nothing.
	entries := uint64(1)
	if top.Kind == KindFile {
		list, err := FileOf(store, top)
		if err != nil {
			return err
		}
		if err := requirePieces(store, list); err != nil {
			return err
		}
	} else if entries, err = survey(store, top); err != nil {
		return err
	}
	maxEntries := options.MaxEntries
	if maxEntries == 0 {
		maxEntries = DefaultMaxWrittenEntries
	}
	if entries > maxEntries {
		return limitExceeded("the files and folders that writing the tree out makes", maxEntries, entries)
	}
	if options.MaxBytes != 0 && top.Size > options.MaxBytes {
		return limitExceeded("the bytes that writing the tree out takes", options.MaxBytes, top.Size)
	}
	if top.Kind == KindFile {
		// The place a caller names is the caller's.
		out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
		if err != nil {
			return ioError("create the file", dest, err)
		}
		return fillFile(store, top, out, dest)
	}
	// Then: write. From here on nothing is reached by its path. The new
	// folder is held open, and so is every folder under it while it is
	// filled, so no folder on the way can be swapped for a link.
	//
	// A folder is made with its folder's other entries, and filled when its
	// turn comes. The folders on the way down to the one that is filled
	// stay open, and no others.
	made, err := createHeld(dest)
	if err != nil {
		return notMade(dest, err)
	}
	type onTheWay struct {
		folder *held
		inside []folderToFill
	}
	var way []onTheWay
	// next is the folder that is filled next, held for the filling.
	next, nextFolder := made, top
	// However the writing ends, by its end, by an error or by a store that
	// falls over, every folder it holds is let go of. Nothing does that for
	// a folder that is forgotten: it would stay held for as long as the
	// program runs.
	defer func() {
		if next != nil {
			next.release()
		}
		for _, step := range way {
			step.folder.release()
		}
	}()
	for {
		if next != nil {
			inside, err := fillFolder(store, next, nextFolder)
			if err != nil {
				return err
			}
			way = append(way, onTheWay{folder: next, inside: inside})
			next = nil
		}
		if len(way) == 0 {
			return nil
		}
		last := &way[len(way)-1]
		if len(last.inside) == 0 {
			last.folder.release()
			way = way[:len(way)-1]
			continue
		}
		below := last.inside[len(last.inside)-1]
		last.inside = last.inside[:len(last.inside)-1]
		opened, err := last.folder.openFolder(below.name)
		if err != nil {
			return reopened(pathWithin(last.folder.path, below.name), err)
		}
		next, nextFolder = opened, below.folder
	}
}

// fillFolder fills a new folder that is held open: writes its files, makes
// the folders inside it, and holds what it then lists against what it was
// given. It gives the folders inside it, which are still to be filled.
func fillFolder(store Store, folder *held, target Target) ([]folderToFill, error) {
	list, err := FolderOf(store, target)
	if err != nil {
		return nil, err
	}
	var inside []folderToFill
	for _, entry := range list.Entries() {
		name := entry.Name()
		if oneName(name) != nil {
			return nil, unwritableError(name, "it would not land inside its folder")
		}
		child := pathWithin(folder.path, name)
		if entry.Kind() == KindFile {
			out, err := folder.createFile(name)
			if err != nil {
				if taken(child, err) {
					return nil, unwritableError(name, "another name of its folder lands on the same place")
				}
				return nil, ioError("create the file", child, err)
			}
			if err := fillFile(store, entry.Target(), out, child); err != nil {
				return nil, err
			}
			continue
		}
		// Made now, with the names beside it. It is opened when its turn
		// comes, so that a folder of many folders does not keep them all
		// open.
		if err := folder.makeFolder(name); err != nil {
			return nil, notMade(child, err)
		}
		inside = append(inside, folderToFill{name: name, folder: entry.Target()})
	}
	// The folder must now hold exactly the names it was given.
	if err := confirmNames(folder, list); err != nil {
		return nil, err
	}
	return inside, nil
}

// survey looks through a whole tree before it is written out, each folder
// once, however many places it has. Every list and every piece must be
// there, every name must be one this system can hold, and no folder may sit
// too deep. It gives how many files and folders writing the tree makes, the
// top one counted, and a folder with several places counted at each.
func survey(store Store, top Target) (uint64, error) {
	tooDeep := func() error {
		return &Error{kind: kindTooDeep, Limit: MaxFolderDepth}
	}
	// For each folder that is done: how many files and folders are below
	// it, and how many folders deep it goes, itself counted.
	type tally struct {
		below uint64
		deep  int
	}
	done := make(map[Hash]tally)
	checkedFiles := make(map[Hash]bool)
	// The folders on the way down: each with its list, and with the place
	// of the entry that is looked at next.
	type step struct {
		hash Hash
		list *FolderList
		next int
	}
	first, err := FolderOf(store, top)
	if err != nil {
		return 0, err
	}
	if err := checkNames(first); err != nil {
		return 0, err
	}
	path := []step{{hash: top.Hash, list: first}}
	for len(path) > 0 {
		last := &path[len(path)-1]
		entries := last.list.Entries()
		if last.next < len(entries) {
			entry := entries[last.next]
			last.next++
			if entry.Kind() == KindFile {
				if !checkedFiles[entry.Hash()] {
					checkedFiles[entry.Hash()] = true
					list, err := FileOf(store, entry.Target())
					if err != nil {
						return 0, err
					}
					if err := requirePieces(store, list); err != nil {
						return 0, err
					}
				}
				continue
			}
			if _, counted := done[entry.Hash()]; counted {
				continue
			}
			if len(path) >= MaxFolderDepth {
				return 0, tooDeep()
			}
			inside, err := FolderOf(store, entry.Target())
			if err != nil {
				return 0, err
			}
			if err := checkNames(inside); err != nil {
				return 0, err
			}
			path = append(path, step{hash: entry.Hash(), list: inside})
			continue
		}
		// Every entry is looked at, and every folder inside is done.
		var below uint64
		deep := 1
		for _, entry := range entries {
			below = addSaturating(below, 1)
			if entry.Kind() == KindFolder {
				inside, counted := done[entry.Hash()]
				if !counted {
					return 0, invariant("a folder is counted before the folders inside it")
				}
				below = addSaturating(below, inside.below)
				deep = max(deep, inside.deep+1)
			}
		}
		if deep > MaxFolderDepth {
			return 0, tooDeep()
		}
		done[last.hash] = tally{below: below, deep: deep}
		path = path[:len(path)-1]
	}
	total, counted := done[top.Hash]
	if !counted {
		return 0, invariant("the top folder is not counted")
	}
	return addSaturating(total.below, 1), nil
}

// addSaturating adds, and gives the largest number where the sum does not
// fit.
func addSaturating(left, right uint64) uint64 {
	sum := left + right
	if sum < left {
		return ^uint64(0)
	}
	return sum
}

// checkNames: every name of a folder has to be one that this system can
// hold as it is.
func checkNames(folder *FolderList) error {
	for _, entry := range folder.Entries() {
		if reason, unwritable := Unwritable(entry.Name()); unwritable {
			return unwritableError(entry.Name(), reason)
		}
	}
	return nil
}

// requirePieces: every piece of a file has to be held, with the length its
// list gives it.
func requirePieces(store Store, list *FileList) error {
	for _, item := range list.Items() {
		if err := requirePiece(store, item); err != nil {
			return err
		}
	}
	return nil
}

// taken reports whether something is at path, be it a file, a folder or a
// link. Systems do not agree on the error they give for making a thing
// where another one is, so the place is looked at.
func taken(path string, err error) bool {
	if errors.Is(err, fs.ErrExist) {
		return true
	}
	_, lookErr := os.Lstat(path)
	return lookErr == nil
}

// lastName gives the last name of a path.
func lastName(path string) string {
	for at := len(path) - 1; at >= 0; at-- {
		if os.IsPathSeparator(path[at]) {
			return path[at+1:]
		}
	}
	return path
}

// notMade says why a folder was not made at path.
func notMade(path string, err error) error {
	if name := lastName(path); name != "" && taken(path, err) {
		return unwritableError(name, "something is there already under that name")
	}
	return ioError("create the folder", path, err)
}

// reopened: a folder that was made has to be that folder still when its
// turn comes.
func reopened(path string, err error) error {
	if errors.Is(err, errRefusedLink) || errors.Is(err, errRefusedOther) {
		return invalid("a folder that was written was swapped for something else before it was filled")
	}
	return ioError("open the folder", path, err)
}

// fillFile writes a file's bytes into a file that was just made, and closes
// it. The path is for messages.
func fillFile(store Store, file Target, out io.WriteCloser, path string) error {
	// The file is closed however this ends, also when the store falls
	// over: nothing else would close it.
	closed := false
	defer func() {
		if !closed {
			_ = out.Close()
		}
	}()
	err := ReadFile(store, file, out)
	closed = true
	closeErr := out.Close()
	if err != nil {
		if told := (*Error)(nil); errors.As(err, &told) && told.kind == kindStream {
			return ioError("write", path, told.Err)
		}
		return err
	}
	if closeErr != nil {
		return ioError("write", path, closeErr)
	}
	return nil
}

// confirmNames: a folder that was written must list exactly the names it
// was given, byte for byte. Where the system changed a name, the tree was
// not written as it is.
func confirmNames(folder *held, list *FolderList) error {
	listed, err := folder.list()
	if err != nil {
		return ioError("list", folder.path, err)
	}
	written := make(map[string]bool, len(listed))
	for _, entry := range listed {
		written[entry.name] = true
	}
	for _, entry := range list.Entries() {
		if !written[entry.Name()] {
			return unwritableError(entry.Name(), "this system keeps it under another name")
		}
	}
	if len(written) != len(list.Entries()) {
		return invalid("a folder that was written holds a name it was not given")
	}
	return nil
}
