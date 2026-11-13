package scionic

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// Reading a tree that a store holds, and writing it out to disk.

func TestAStretchIsReadFromAnOpenFileWithoutAPassOverItsList(t *testing.T) {
	store := NewMemoryStore()
	data := corpusBytes("open", 100)
	file := must[Target](t)(NewBuilder(store, smallPieces(t)).FileFromBytes(data))
	open := must[*OpenFile](t)(NewOpenFile(fileListOf(t, store, file)))
	var out bytes.Buffer
	for offset := 0; offset <= 100; offset++ {
		for length := 0; length <= 100-offset; length++ {
			out.Reset()
			if err := ReadOpen(store, open, uint64(offset), uint64(length), &out); err != nil || !bytes.Equal(out.Bytes(), data[offset:offset+length]) {
				t.Fatalf("%d+%d: %v", offset, length, err)
			}
		}
	}
	out.Reset()
	if ReadOpen(store, open, 0, 101, &out) == nil || ReadOpen(store, open, 101, 0, &out) == nil || ReadOpen(store, open, ^uint64(0), 2, &out) == nil {
		t.Fatal("a stretch outside the file was read")
	}
	// A piece that is missing: an error, and no byte.
	piece := open.List().Items()[3]
	store.Remove(StoredPiece, piece.Hash)
	if err := ReadOpen(store, open, 40, 30, &out); !errors.Is(err, ErrMissing) || out.Len() != 0 {
		t.Fatalf("%d bytes (%v)", out.Len(), err)
	}
	// An empty file has one stretch: none of its bytes, from its start.
	empty := must[Target](t)(NewBuilder(store, smallPieces(t)).FileFromBytes(nil))
	open = must[*OpenFile](t)(NewOpenFile(fileListOf(t, store, empty)))
	if err := ReadOpen(store, open, 0, 0, &out); err != nil {
		t.Fatal(err)
	}
	if ReadOpen(store, open, 0, 1, &out) == nil || out.Len() != 0 {
		t.Fatal("a byte of an empty file was read")
	}
}

// limitExceededWith reports whether an error says that a limit was
// exceeded, and by what.
func limitExceededWith(err error, limit, requested uint64) bool {
	var told *Error
	return errors.Is(err, ErrLimitExceeded) && errors.As(err, &told) && told.Limit == limit && told.Requested == requested
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestASmallTreeThatStandsForMoreFoldersThanADiskHoldsIsNotWritten(t *testing.T) {
	store := NewMemoryStore()
	builder := NewBuilder(store, smallPieces(t))
	// An empty folder, and above it 100 folders that each hold the one
	// below under two names.
	folder := must[Target](t)(builder.Folder(nil))
	for range 100 {
		folder = must[Target](t)(builder.Folder([]Entry{plainEntry(t, "a", folder), plainEntry(t, "b", folder)}))
	}
	root := must[Root](t)(builder.Finish(folder, ExtraData{}))
	// To hold and to check, it is a tree like any other: 101 small lists.
	if store.Count(StoredFolderList) != 101 || must[*Summary](t)(Verify(store, root)).Folders != 101 {
		t.Fatal("the tree is not 101 small lists")
	}
	// Written out, it would be more folders than there are numbers for.
	// They are counted, each folder once, before anything is made.
	dest := filepath.Join(t.TempDir(), "written")
	if err := WriteToDisk(store, root, dest); !limitExceededWith(err, DefaultMaxWrittenEntries, ^uint64(0)) {
		t.Fatalf("it was written: %v", err)
	}
	if exists(dest) {
		t.Fatal("something was made before the folders were counted")
	}
	// Twenty levels of it are counted exactly: 2^21 - 1 folders.
	builder = NewBuilder(store, smallPieces(t))
	folder = must[Target](t)(builder.Folder(nil))
	for range 20 {
		folder = must[Target](t)(builder.Folder([]Entry{plainEntry(t, "a", folder), plainEntry(t, "b", folder)}))
	}
	root = must[Root](t)(builder.Finish(folder, ExtraData{}))
	if err := WriteToDisk(store, root, dest); !limitExceededWith(err, DefaultMaxWrittenEntries, 2_097_151) || exists(dest) {
		t.Fatalf("it was written: %v", err)
	}
}

func TestWritingOutStaysWithinWhatItIsGiven(t *testing.T) {
	store, root := sampleTree(t)
	temp := t.TempDir()
	size := topOfTree(t, store, root).Size
	// The tree is seven files and five folders, the top one counted.
	for number, limits := range []struct {
		options WriteOptions
		fits    bool
	}{
		{WriteOptions{MaxEntries: 11}, false},
		{WriteOptions{MaxBytes: size - 1}, false},
		{WriteOptions{MaxEntries: 12, MaxBytes: size}, true},
	} {
		dest := filepath.Join(temp, fmt.Sprintf("written-%d", number))
		err := WriteToDiskWith(store, root, dest, limits.options)
		if limits.fits {
			if err != nil {
				t.Fatal(err)
			}
			if content, err := os.ReadFile(filepath.Join(dest, "weights", "shards", "a.bin")); err != nil || !bytes.Equal(content, corpusBytes("shard-a", 100)) {
				t.Fatal(err)
			}
		} else if !errors.Is(err, ErrLimitExceeded) || exists(dest) {
			t.Fatalf("%+v: nothing may be made before the limits are held: %v", limits.options, err)
		}
	}
	// A file on top is one thing to make, and takes its own size.
	file := targetAt(t, store, root, "weights/model.bin")
	alone := must[Root](t)(NewBuilder(store, smallPieces(t)).Finish(file, ExtraData{}))
	dest := filepath.Join(temp, "alone.bin")
	if err := WriteToDiskWith(store, alone, dest, WriteOptions{MaxBytes: 999}); !errors.Is(err, ErrLimitExceeded) || exists(dest) {
		t.Fatal(err)
	}
	if err := WriteToDiskWith(store, alone, dest, WriteOptions{MaxEntries: 1, MaxBytes: 1000}); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(dest); err != nil || !bytes.Equal(content, corpusBytes("model", 1000)) {
		t.Fatal(err)
	}
}

func TestEveryStretchOfAFileReadsBack(t *testing.T) {
	store := NewMemoryStore()
	data := corpusBytes("stretch", 100)
	file := must[Target](t)(NewBuilder(store, smallPieces(t)).FileFromBytes(data))
	var out bytes.Buffer
	for offset := 0; offset <= 100; offset++ {
		for length := 0; length <= 100-offset; length++ {
			out.Reset()
			if err := ReadRange(store, file, uint64(offset), uint64(length), &out); err != nil || !bytes.Equal(out.Bytes(), data[offset:offset+length]) {
				t.Fatalf("%d+%d: %v", offset, length, err)
			}
		}
	}
	if content := must[[]byte](t)(ReadFileBytes(store, file)); !bytes.Equal(content, data) {
		t.Fatal("the file does not read back")
	}
	out.Reset()
	if ReadRange(store, file, 0, 101, &out) == nil || ReadRange(store, file, 101, 0, &out) == nil || ReadRange(store, file, ^uint64(0), 2, &out) == nil || out.Len() != 0 {
		t.Fatal("a stretch outside the file was read")
	}
}

func TestAFileWithAPieceMissingGivesAnErrorAndNoBytes(t *testing.T) {
	store := NewMemoryStore()
	data := corpusBytes("holed", 100)
	file := must[Target](t)(NewBuilder(store, smallPieces(t)).FileFromBytes(data))
	// Piece 3 holds bytes 48 to 63.
	piece := fileListOf(t, store, file).Items()[3]
	honest := keptIn(t, store, StoredPiece, piece.Hash)
	store.Remove(StoredPiece, piece.Hash)
	var out bytes.Buffer
	if err := ReadFile(store, file, &out); !errors.Is(err, ErrMissing) || out.Len() != 0 {
		t.Fatalf("%d bytes came back, and never a shorter file (%v)", out.Len(), err)
	}
	if _, err := ReadFileBytes(store, file); !errors.Is(err, ErrMissing) {
		t.Fatal(err)
	}
	for _, stretch := range [][2]uint64{{40, 10}, {48, 1}, {63, 1}, {0, 100}, {60, 40}} {
		if err := ReadRange(store, file, stretch[0], stretch[1], &out); err == nil || out.Len() != 0 {
			t.Fatalf("%v: %d bytes (%v)", stretch, out.Len(), err)
		}
	}
	// What does not need the piece is still read.
	if err := errors.Join(ReadRange(store, file, 0, 48, &out), ReadRange(store, file, 64, 36, &out)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), joined(data[:48], data[64:])) {
		t.Fatal("the bytes around the missing piece do not read back")
	}
	// Other bytes under the piece's hash are no piece of the file.
	store.Put(StoredPiece, piece.Hash, corpusBytes("other", 16))
	if _, err := ReadFileBytes(store, file); !errors.Is(err, ErrCorrupt) {
		t.Fatal(err)
	}
	out.Reset()
	if err := ReadFile(store, file, &out); !errors.Is(err, ErrCorrupt) || !bytes.Equal(out.Bytes(), data[:48]) {
		t.Fatalf("%d bytes (%v)", out.Len(), err)
	}
	swapIn(store, StoredPiece, piece.Hash, corpusBytes("other", 5))
	out.Reset()
	if err := ReadFile(store, file, &out); !errors.Is(err, ErrSizeMismatch) || out.Len() != 0 {
		t.Fatalf("%d bytes (%v)", out.Len(), err)
	}
	swapIn(store, StoredPiece, piece.Hash, honest)
	if content := must[[]byte](t)(ReadFileBytes(store, file)); !bytes.Equal(content, data) {
		t.Fatal("the file does not read back")
	}
}

func TestAPathIsFoundOrItIsNot(t *testing.T) {
	store, root := sampleTree(t)
	found := func(path string) Entry {
		t.Helper()
		entry, there, err := Lookup(store, root, path)
		if err != nil || !there {
			t.Fatalf("%q: %v %v", path, there, err)
		}
		return entry
	}
	if found("readme.txt").Kind() != KindFile || found("weights").Kind() != KindFolder ||
		found("weights/shards/b.bin").Size() != 33 || found("nothing-here").Size() != 0 {
		t.Fatal("an entry is not what was built")
	}
	for _, path := range []string{"missing", "weights/missing", "weights/shards/b.bin/deeper", "readme.txt/x", "Readme.txt", "weights/shards/c.bin"} {
		if _, there, err := Lookup(store, root, path); err != nil || there {
			t.Fatalf("%q: %v %v", path, there, err)
		}
	}
	for _, path := range []string{"", "/", "weights/", "/weights", "weights//shards", "weights/../readme.txt", ".", "weights/\x00", "weights/\xff"} {
		if _, _, err := Lookup(store, root, path); err == nil {
			t.Fatalf("%q was looked up", path)
		}
	}
	if _, _, err := Lookup(store, RootOfRecord([]byte("no such tree")), "readme.txt"); err == nil {
		t.Fatal("a path was looked up in a tree that is not held")
	}
}

// fileOnDisk is a file by its path from a folder, with its bytes. A folder
// has a name that ends in a slash, and no bytes.
type fileOnDisk struct {
	name    string
	content string
}

// filesUnder gives every file and folder under a folder.
func filesUnder(t *testing.T, folder string) []fileOnDisk {
	t.Helper()
	var files []fileOnDisk
	type pending struct{ path, prefix string }
	look := []pending{{folder, ""}}
	for len(look) > 0 {
		next := look[len(look)-1]
		look = look[:len(look)-1]
		for _, entry := range must[[]os.DirEntry](t)(os.ReadDir(next.path)) {
			name := next.prefix + entry.Name()
			path := filepath.Join(next.path, entry.Name())
			if entry.IsDir() {
				look = append(look, pending{path, name + "/"})
				files = append(files, fileOnDisk{name + "/", ""})
				continue
			}
			if !entry.Type().IsRegular() {
				t.Fatalf("%s is neither a file nor a folder", path)
			}
			files = append(files, fileOnDisk{name, string(must[[]byte](t)(os.ReadFile(path)))})
		}
	}
	slices.SortFunc(files, func(left, right fileOnDisk) int { return strings.Compare(left.name, right.name) })
	return files
}

func TestATreeWrittenToDiskBuildsToTheSameRoot(t *testing.T) {
	sample := writeTree(t, sampleFiles())
	options := BuildOptions{Cutting: smallPieces(t)}
	store := NewMemoryStore()
	root := must[Root](t)(Build(store, sample, options))
	temp := t.TempDir()
	dest := filepath.Join(temp, "written")
	if err := WriteToDisk(store, root, dest); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(filesUnder(t, dest), filesUnder(t, sample)) || len(filesUnder(t, dest)) != 11 {
		t.Fatal("what was written is not what was built")
	}
	if again := must[Root](t)(Build(NewMemoryStore(), dest, options)); again != root {
		t.Fatal("what was written builds to another root")
	}
	// A place that is there already is never written into.
	before := filesUnder(t, dest)
	if err := WriteToDisk(store, root, dest); !errors.Is(err, ErrUnwritableName) || !slices.Equal(filesUnder(t, dest), before) {
		t.Fatal(err)
	}
	// A tree with a file on top is written as that file.
	file := targetAt(t, store, root, "weights/model.bin")
	alone := must[Root](t)(NewBuilder(store, smallPieces(t)).Finish(file, ExtraData{}))
	destFile := filepath.Join(temp, "written.bin")
	if err := WriteToDisk(store, alone, destFile); err != nil {
		t.Fatal(err)
	}
	if content := must[[]byte](t)(os.ReadFile(destFile)); !bytes.Equal(content, corpusBytes("model", 1000)) {
		t.Fatal("the file that was written is not the file")
	}
	if err := WriteToDisk(store, alone, destFile); !errors.Is(err, ErrIO) {
		t.Fatal(err)
	}
	if content := must[[]byte](t)(os.ReadFile(destFile)); !bytes.Equal(content, corpusBytes("model", 1000)) {
		t.Fatal("a file that was there already was written into")
	}
	// Nothing of what was written is still held open: it can all be moved,
	// which Windows refuses for a folder that anything holds.
	if err := os.Rename(dest, filepath.Join(temp, "moved")); err != nil {
		t.Fatal(err)
	}
}

func TestNothingIsWrittenWhenAPartIsMissing(t *testing.T) {
	store, root := sampleTree(t)
	shard := targetAt(t, store, root, "weights/shards/b.bin")
	piece := fileListOf(t, store, shard).Items()[2]
	store.Remove(StoredPiece, piece.Hash)
	dest := filepath.Join(t.TempDir(), "written")
	if err := WriteToDisk(store, root, dest); !errors.Is(err, ErrMissing) || exists(dest) {
		t.Fatalf("the lack has to be found before anything is written: %v", err)
	}
}

// foldsCase reports whether this system takes Probe and probe for one
// name.
func foldsCase(t *testing.T, folder string) bool {
	t.Helper()
	if err := os.WriteFile(filepath.Join(folder, "Probe"), []byte("x"), 0o666); err != nil {
		t.Fatal(err)
	}
	return exists(filepath.Join(folder, "probe"))
}

func TestTwoNamesThatOneSystemTakesForOneAreRefusedThere(t *testing.T) {
	temp := t.TempDir()
	folds := foldsCase(t, temp)
	store := NewMemoryStore()
	builder := NewBuilder(store, smallPieces(t))
	upper := must[Target](t)(builder.FileFromBytes([]byte("under the capital")))
	lower := must[Target](t)(builder.FileFromBytes([]byte("under the small letter")))
	inner := must[Target](t)(builder.Folder([]Entry{plainEntry(t, "x", upper)}))
	for _, names := range []struct {
		what    string
		entries []Entry
	}{
		{"two-files", []Entry{plainEntry(t, "File", upper), plainEntry(t, "file", lower)}},
		{"file-and-folder", []Entry{plainEntry(t, "Name", inner), plainEntry(t, "name", lower)}},
		{"folder-and-file", []Entry{plainEntry(t, "Name", lower), plainEntry(t, "name", inner)}},
		{"two-folders", []Entry{plainEntry(t, "Dir", inner), plainEntry(t, "dir", inner)}},
	} {
		builder := NewBuilder(store, smallPieces(t))
		top := must[Target](t)(builder.Folder(names.entries))
		// Held in a tree, the two names are two names.
		root := must[Root](t)(builder.Finish(top, ExtraData{}))
		dest := filepath.Join(temp, names.what)
		err := WriteToDisk(store, root, dest)
		if !folds {
			if err != nil {
				t.Fatalf("%s: %v", names.what, err)
			}
			if again := must[Root](t)(Build(NewMemoryStore(), dest, BuildOptions{Cutting: smallPieces(t)})); again != root {
				t.Fatalf("%s: what was written builds to another root", names.what)
			}
			continue
		}
		var told *Error
		if !errors.Is(err, ErrUnwritableName) || !errors.As(err, &told) {
			t.Fatalf("%s: both names were written: %v", names.what, err)
		}
		if !slices.ContainsFunc(names.entries, func(entry Entry) bool { return entry.Name() == told.Name }) {
			t.Fatalf("%s: the name that is refused is %q", names.what, told.Name)
		}
		// What was written before the name was found stays, and nothing of
		// it is held open.
		if err := os.Rename(dest, dest+"-moved"); err != nil {
			t.Fatalf("%s: %v", names.what, err)
		}
	}
}

func TestNamesThatWindowsCannotHoldAreFound(t *testing.T) {
	for _, name := range []string{
		"a", "a.b", ".git", "a b", "com", "com10", "nul1", "console", "aux2", "lpt", "été",
		"model.safetensors", "a.b.c", "con1.txt", "$con",
	} {
		if reason, unwritable := UnwritableOnWindows(name); unwritable {
			t.Errorf("%q is refused: %s", name, reason)
		}
	}
	for _, name := range []string{
		"a:b", "a\\b", "a/b", "a?", "a*", "a<b", "a>b", "a|b", "a\"b", "a\tb", "a\x01", "a.", "a ", "...",
		"CON", "con", "Nul", "nul.txt", "NUL.tar.gz", "NUL .txt", "COM1", "com9.txt", "COM0", "LPT1", "lpt3.x",
		"COM¹", "lpt³", "AUX", "PRN", "CONIN$", "conout$",
	} {
		if reason, unwritable := UnwritableOnWindows(name); !unwritable || reason == "" {
			t.Errorf("%q is taken", name)
		}
	}
	// What the format itself does not allow is no name anywhere.
	for _, name := range []string{"", ".", "..", "a/b", "a\x00b", "\xff", strings.Repeat("n", 256)} {
		if reason, unwritable := Unwritable(name); !unwritable || reason != "the format does not allow it" {
			t.Errorf("%q is taken: %q", name, reason)
		}
	}
	if _, unwritable := Unwritable("nul.txt"); unwritable != (runtime.GOOS == "windows") {
		t.Errorf("nul.txt: %v", unwritable)
	}
}

func TestANameThisSystemCannotHoldIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	store := NewMemoryStore()
	builder := NewBuilder(store, smallPieces(t))
	file := must[Target](t)(builder.FileFromBytes([]byte("content")))
	inner := must[Target](t)(builder.Folder([]Entry{plainEntry(t, "fine", file), plainEntry(t, "nul.txt", file)}))
	top := must[Target](t)(builder.Folder([]Entry{plainEntry(t, "a-fine-name", file), plainEntry(t, "sub", inner)}))
	root := must[Root](t)(builder.Finish(top, ExtraData{}))
	dest := filepath.Join(t.TempDir(), "written")
	err := WriteToDisk(store, root, dest)
	if runtime.GOOS != "windows" {
		if err != nil {
			t.Fatal(err)
		}
		if content := must[[]byte](t)(os.ReadFile(filepath.Join(dest, "sub", "nul.txt"))); string(content) != "content" {
			t.Fatal("the file that was written is not the file")
		}
		return
	}
	var told *Error
	if !errors.Is(err, ErrUnwritableName) || !errors.As(err, &told) || told.Name != "nul.txt" {
		t.Fatalf("a device name was written: %v", err)
	}
	if exists(dest) {
		t.Fatal("the name has to be found before anything is written")
	}
}
