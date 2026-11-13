package scionic

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// Folders on disk, held open: what is in one is made, listed and opened
// from the folder itself, and a name is one name.

// kinds gives the names in a held folder with what each is, in order.
func kinds(t *testing.T, folder *held) []listedName {
	t.Helper()
	listed := must[[]listedName](t)(folder.list())
	slices.SortFunc(listed, func(left, right listedName) int { return strings.Compare(left.name, right.name) })
	return listed
}

// readIn reads a file in a held folder to its end.
func readIn(t *testing.T, folder *held, name string) string {
	t.Helper()
	file, err := folder.openFile(name)
	if err != nil {
		t.Fatalf("%q was not opened: %v", name, err)
	}
	defer file.Close()
	return string(must[[]byte](t)(io.ReadAll(file)))
}

// writeIn makes a new file in a held folder.
func writeIn(t *testing.T, folder *held, name, content string) {
	t.Helper()
	file, err := folder.createFile(name)
	if err != nil {
		t.Fatalf("%q was not made: %v", name, err)
	}
	if _, err := io.WriteString(file, content); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

// refusedBySystem reports whether it was the system that said no, and not
// this library for what it found there.
func refusedBySystem(err error) bool {
	return err != nil && !errors.Is(err, errRefusedLink) && !errors.Is(err, errRefusedOther) && !errors.Is(err, errNotOneName)
}

// linkToFolder makes a link at link that leads to the folder to, of the
// kind that anyone may make. It says whether the system made it.
func linkToFolder(link, to string) bool {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/C", "mklink", "/J", link, to).Run() == nil
	}
	return os.Symlink(to, link) == nil
}

// linkToFile makes a link at link that leads to the file to. It says
// whether the system made it: on Windows that takes a right that not every
// user has.
func linkToFile(link, to string) bool {
	return os.Symlink(to, link) == nil
}

func TestWhatIsInAHeldFolderIsMadeListedAndOpened(t *testing.T) {
	scratch := t.TempDir()
	place := filepath.Join(scratch, "top")
	top := must[*held](t)(createHeld(place))
	defer top.release()
	if _, err := createHeld(place); err == nil {
		t.Fatal("a folder was made at a place that is taken")
	}
	if top.path != place {
		t.Fatal(top.path)
	}
	if err := top.makeFolder("inner"); err != nil {
		t.Fatal(err)
	}
	inner := must[*held](t)(top.openFolder("inner"))
	defer inner.release()
	writeIn(t, top, "file", "bytes")
	writeIn(t, inner, "deep", "deeper")
	// Nothing is made where something is.
	for _, name := range []string{"file", "inner"} {
		if file, err := top.createFile(name); err == nil {
			file.Close()
			t.Fatalf("a file was made at %q, which is taken", name)
		}
		if err := top.makeFolder(name); err == nil {
			t.Fatalf("a folder was made at %q, which is taken", name)
		}
	}
	if got := kinds(t, top); !slices.Equal(got, []listedName{{"file", foundFile}, {"inner", foundFolder}}) {
		t.Fatalf("%+v", got)
	}
	if readIn(t, top, "file") != "bytes" {
		t.Fatal("the file does not read back")
	}
	again := must[*held](t)(openHeld(place))
	defer again.release()
	below := must[*held](t)(again.openFolder("inner"))
	defer below.release()
	if readIn(t, below, "deep") != "deeper" {
		t.Fatal("the file in the folder inside does not read back")
	}
	// One kind of thing is not opened as the other.
	if _, err := top.openFile("inner"); !errors.Is(err, errRefusedOther) {
		t.Fatal(err)
	}
	if _, err := top.openFolder("file"); !errors.Is(err, errRefusedOther) {
		t.Fatal(err)
	}
	if _, err := top.openFile("absent"); !refusedBySystem(err) || !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err := top.openFolder("absent"); !refusedBySystem(err) || !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	// A folder that is not there is not held, and neither is a file.
	if _, err := openHeld(filepath.Join(scratch, "absent")); err == nil {
		t.Fatal("a folder that is not there was held")
	}
	if _, err := openHeld(filepath.Join(place, "file")); err == nil {
		t.Fatal("a file was held as a folder")
	}
}

func TestANameIsOneName(t *testing.T) {
	scratch := t.TempDir()
	top := must[*held](t)(createHeld(filepath.Join(scratch, "top")))
	defer top.release()
	if err := top.makeFolder("inner"); err != nil {
		t.Fatal(err)
	}
	inner := must[*held](t)(top.openFolder("inner"))
	defer inner.release()
	if err := os.WriteFile(filepath.Join(scratch, "beside"), []byte("beside"), 0o666); err != nil {
		t.Fatal(err)
	}
	moreThanAName := []string{"", ".", "..", "../beside", "inner/new", "a\x00b"}
	if runtime.GOOS == "windows" {
		moreThanAName = append(moreThanAName, `..\beside`, `inner\new`, "file:stream", "C:new")
	}
	for _, name := range moreThanAName {
		if _, err := top.openFile(name); !errors.Is(err, errNotOneName) {
			t.Fatalf("%q: %v", name, err)
		}
		if _, err := inner.openFolder(name); !errors.Is(err, errNotOneName) {
			t.Fatalf("%q: %v", name, err)
		}
		if _, err := top.createFile(name); !errors.Is(err, errNotOneName) {
			t.Fatalf("%q: %v", name, err)
		}
		if err := inner.makeFolder(name); !errors.Is(err, errNotOneName) {
			t.Fatalf("%q: %v", name, err)
		}
	}
	// Nothing was made, in the folders or beside them.
	if got := kinds(t, top); !slices.Equal(got, []listedName{{"inner", foundFolder}}) {
		t.Fatalf("%+v", got)
	}
	if got := kinds(t, inner); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	if beside := must[[]os.DirEntry](t)(os.ReadDir(scratch)); len(beside) != 2 {
		t.Fatalf("%d things are beside the folder", len(beside))
	}
}

func TestAHeldFolderIsLetGoOfWithItsLastUser(t *testing.T) {
	scratch := t.TempDir()
	place := filepath.Join(scratch, "top")
	top := must[*held](t)(createHeld(place))
	writeIn(t, top, "file", "bytes")
	// Kept for two more users, it is still held when the first has done.
	second, third := top.retain(), top.retain()
	top.release()
	if readIn(t, second, "file") != "bytes" {
		t.Fatal("a folder that is still held does not read")
	}
	second.release()
	if got := kinds(t, third); len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	third.release()
	// Let go of by all, it is anyone's: it can be given another name, which
	// Windows refuses for a folder that anything holds.
	if err := os.Rename(place, filepath.Join(scratch, "moved")); err != nil {
		t.Fatal(err)
	}
}
