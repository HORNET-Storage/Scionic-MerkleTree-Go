//go:build windows

package scionic

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestAFolderThatIsHeldCanBeNeitherRenamedNorReplaced(t *testing.T) {
	root := t.TempDir()
	at := func(names ...string) string {
		return filepath.Join(append([]string{root}, names...)...)
	}
	writeFiles(t, root, map[string][]byte{"top/a/b/file": []byte("mine")})
	if err := os.Mkdir(at("top", "a", "empty"), 0o777); err != nil {
		t.Fatal(err)
	}
	top := must[*held](t)(openHeld(at("top")))
	a := must[*held](t)(top.openFolder("a"))
	b := must[*held](t)(a.openFolder("b"))
	empty := must[*held](t)(a.openFolder("empty"))
	// The lower folders keep the ones above them held.
	a.release()
	top.release()
	for _, move := range [][2]string{
		{at("top", "a", "b"), at("top", "a", "c")},
		{at("top", "a"), at("top", "z")},
		{at("top"), at("gone")},
	} {
		if err := os.Rename(move[0], move[1]); err == nil {
			t.Fatalf("%s was given another name while it was held", move[0])
		}
	}
	if err := os.Remove(at("top", "a", "empty")); err == nil {
		t.Fatal("a folder was removed while it was held")
	}
	if readIn(t, b, "file") != "mine" {
		t.Fatal("the file in a held folder does not read")
	}
	// Let go of, they are anyone's again.
	empty.release()
	b.release()
	if err := os.Remove(at("top", "a", "empty")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(at("top", "a"), at("top", "z")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(at("top"), at("gone")); err != nil {
		t.Fatal(err)
	}
}

func TestALinkBetweenFoldersIsRefused(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string][]byte{"secret/key": []byte("secret")})
	if err := os.MkdirAll(filepath.Join(root, "top", "real"), 0o777); err != nil {
		t.Fatal(err)
	}
	// A link from one folder to another, of the kind that anyone may make.
	if !linkToFolder(filepath.Join(root, "top", "to-folder"), filepath.Join(root, "secret")) {
		t.Skip("this system would not make one")
	}
	top := must[*held](t)(openHeld(filepath.Join(root, "top")))
	if got := kinds(t, top); !slices.Equal(got, []listedName{{"real", foundFolder}, {"to-folder", foundLink}}) {
		t.Fatalf("%+v", got)
	}
	if _, err := top.openFolder("to-folder"); !errors.Is(err, errRefusedLink) {
		t.Fatal(err)
	}
	if _, err := top.openFile("to-folder"); !errors.Is(err, errRefusedLink) {
		t.Fatal(err)
	}
	if file, err := top.createFile("to-folder"); err == nil {
		file.Close()
		t.Fatal("a file was made in the place of a link")
	}
	if err := top.makeFolder("to-folder"); err == nil {
		t.Fatal("a folder was made in the place of a link")
	}
	// The link is taken away as a link: what it leads to stays.
	top.release()
	if err := os.Remove(filepath.Join(root, "top", "to-folder")); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(root, "secret", "key")) {
		t.Fatal("what the link led to went with the link")
	}
}

func TestANameIsOpenedAsItIsListed(t *testing.T) {
	scratch := t.TempDir()
	// A path that begins with \\?\ is taken letter for letter, so it can
	// make the names that Windows changes when it looks an ordinary path
	// up: a dot or a space at the end goes, and some names are devices.
	whole := must[string](t)(filepath.Abs(scratch))
	if len(whole) < 3 || whole[1] != ':' {
		t.Skipf("%s is not on a drive with a letter", whole)
	}
	exact := `\\?\` + whole + `\top`
	if err := os.Mkdir(exact, 0o777); err != nil {
		t.Fatal(err)
	}
	files := []string{"a", "a.", "a ", "only.", "aux", "nul", "con", "com1", "nul.txt"}
	folders := []string{"d", "d.", "d ", "...", ".. "}
	for _, name := range files {
		if err := os.WriteFile(exact+`\`+name, []byte(fmt.Sprintf("the file [%s]", name)), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range folders {
		if err := os.Mkdir(exact+`\`+name, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(exact+`\`+name+`\inside`, []byte(fmt.Sprintf("in [%s]", name)), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	// Whatever is left of them is taken away by their exact names: the
	// usual way of removing a folder does not find them all.
	defer func() {
		for _, name := range folders {
			os.RemoveAll(exact + `\` + name)
		}
		for _, name := range append(files, "made.") {
			os.RemoveAll(exact + `\` + name)
		}
	}()
	top := must[*held](t)(openHeld(filepath.Join(scratch, "top")))
	defer top.release()
	listed := kinds(t, top)
	if len(listed) != len(files)+len(folders) {
		t.Fatalf("the folder lists %+v", listed)
	}
	for _, name := range files {
		if !slices.Contains(listed, listedName{name, foundFile}) {
			t.Fatalf("%q is not listed as a file: %+v", name, listed)
		}
		if got := readIn(t, top, name); got != fmt.Sprintf("the file [%s]", name) {
			t.Fatalf("%q was read as %q", name, got)
		}
		if file, err := top.createFile(name); err == nil {
			file.Close()
			t.Fatalf("%q is taken, and a file was made there", name)
		}
	}
	for _, name := range folders {
		if !slices.Contains(listed, listedName{name, foundFolder}) {
			t.Fatalf("%q is not listed as a folder: %+v", name, listed)
		}
		folder, err := top.openFolder(name)
		if err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		if got := kinds(t, folder); !slices.Equal(got, []listedName{{"inside", foundFile}}) {
			t.Fatalf("%q: %+v", name, got)
		}
		if got := readIn(t, folder, "inside"); got != fmt.Sprintf("in [%s]", name) {
			t.Fatalf("in %q, the file was read as %q", name, got)
		}
		// It is this folder that is held, and not the one that Windows
		// would have made of its name.
		if err := os.Rename(exact+`\`+name, exact+`\moved`); err == nil {
			t.Fatalf("%q was given another name while it was held", name)
		}
		folder.release()
		if err := os.Rename(exact+`\`+name, exact+`\moved`); err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		if err := os.Rename(exact+`\moved`, exact+`\`+name); err != nil {
			t.Fatalf("%q: %v", name, err)
		}
	}
	// What is made is made under the name it is given, and under no other.
	if err := top.makeFolder("made."); err != nil {
		t.Fatal(err)
	}
	made := must[*held](t)(top.openFolder("made."))
	writeIn(t, made, "file ", "new")
	if got := kinds(t, made); !slices.Equal(got, []listedName{{"file ", foundFile}}) {
		t.Fatalf("%+v", got)
	}
	made.release()
	if content := must[[]byte](t)(os.ReadFile(exact + `\made.\file `)); string(content) != "new" {
		t.Fatalf("the file that was made holds %q", content)
	}
	if exists(exact + `\made`) {
		t.Fatal("a folder was made under a name it was not given")
	}
}

func TestNothingIsMadeThroughALinkThatLeadsNowhere(t *testing.T) {
	root := t.TempDir()
	top, elsewhere := filepath.Join(root, "top"), filepath.Join(root, "elsewhere")
	for _, folder := range []string{top, elsewhere} {
		if err := os.Mkdir(folder, 0o777); err != nil {
			t.Fatal(err)
		}
	}
	// Links between folders, of the kind that anyone may make, to places
	// where nothing is: each is made to a folder, which is then taken away.
	var links []string
	for _, name := range []string{"one", "two"} {
		to := filepath.Join(elsewhere, name)
		if err := os.Mkdir(to, 0o777); err != nil {
			t.Fatal(err)
		}
		made := linkToFolder(filepath.Join(top, name), to)
		if err := os.Remove(to); err != nil {
			t.Fatal(err)
		}
		if made {
			links = append(links, name)
		}
	}
	// Links of the other kind, which take a right that not every user has:
	// one to a file that is not there, and one to a folder that is then
	// taken away.
	if linkToFile(filepath.Join(top, "three"), filepath.Join(elsewhere, "three")) {
		links = append(links, "three")
	}
	four := filepath.Join(elsewhere, "four")
	if err := os.Mkdir(four, 0o777); err != nil {
		t.Fatal(err)
	}
	if os.Symlink(four, filepath.Join(top, "four")) == nil {
		links = append(links, "four")
	}
	if err := os.Remove(four); err != nil {
		t.Fatal(err)
	}
	if len(links) == 0 {
		t.Skip("this system would not make a link")
	}
	folder := must[*held](t)(openHeld(top))
	defer folder.release()
	listed := kinds(t, folder)
	if len(listed) != len(links) {
		t.Fatalf("%+v", listed)
	}
	for _, name := range links {
		if !slices.Contains(listed, listedName{name, foundLink}) {
			t.Fatalf("%s is not listed as a link: %+v", name, listed)
		}
		if file, err := folder.createFile(name); err == nil {
			file.Close()
			t.Fatalf("a file was made through %s", name)
		}
		if err := folder.makeFolder(name); err == nil {
			t.Fatalf("a folder was made through %s", name)
		}
		if _, err := folder.openFile(name); !errors.Is(err, errRefusedLink) {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := folder.openFolder(name); !errors.Is(err, errRefusedLink) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if left := must[[]os.DirEntry](t)(os.ReadDir(elsewhere)); len(left) != 0 {
		t.Fatalf("%d things were made where the links lead", len(left))
	}
}

// handleOn opens a file as the standard library does, and gives its handle.
func handleOn(t *testing.T, path string) (windows.Handle, func()) {
	t.Helper()
	file := must[*os.File](t)(os.Open(path))
	return windows.Handle(file.Fd()), func() { file.Close() }
}

func TestTwoHandlesAreKnownToBeOnOneFileOrNot(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string][]byte{"one": []byte("one"), "two": []byte("one"), "folder/one": []byte("one")})
	at := func(name string) string { return filepath.Join(root, filepath.FromSlash(name)) }
	same := func(left, right string) bool {
		t.Helper()
		first, closeFirst := handleOn(t, at(left))
		defer closeFirst()
		second, closeSecond := handleOn(t, at(right))
		defer closeSecond()
		for _, identityOf := range []func(windows.Handle) (fileIdentity, error){longIdentity, shortIdentity} {
			firstIs, firstErr := identityOf(first)
			secondIs, secondErr := identityOf(second)
			if firstErr == nil && secondErr == nil && (firstIs == secondIs) != (left == right || strings.HasPrefix(right, "link-to-"+left)) {
				t.Fatalf("%s and %s: one way of telling files apart says otherwise", left, right)
			}
		}
		return must[bool](t)(sameFile(first, second))
	}
	if !same("one", "one") {
		t.Fatal("a file is not known to be itself")
	}
	// Two files with the same bytes are two files, and so are two files
	// with the same name in two folders.
	if same("one", "two") || same("one", "folder/one") {
		t.Fatal("two files are taken for one")
	}
	// A second name of a file is the file.
	from := must[*uint16](t)(windows.UTF16PtrFromString(at("link-to-one-hard")))
	to := must[*uint16](t)(windows.UTF16PtrFromString(at("one")))
	if err := windows.CreateHardLink(from, to, 0); err == nil && !same("one", "link-to-one-hard") {
		t.Fatal("a second name of a file is taken for another file")
	}
	// What a link leads to is the file it leads to, and no other: a link
	// that took a file's place is found by this.
	if linkToFile(at("link-to-one"), at("one")) {
		if !same("one", "link-to-one") || same("two", "link-to-one") {
			t.Fatal("what a link leads to is not known to be the file it leads to")
		}
	}
}

// standInFor makes something stand in for a file, as a system does that
// fetches a file's bytes when they are read. Nothing on this machine knows
// what to do with this one, which is what makes it safe to put there.
func standInFor(t *testing.T, path string) bool {
	t.Helper()
	pointer := must[*uint16](t)(windows.UTF16PtrFromString(path))
	handle, err := windows.CreateFile(pointer, windows.GENERIC_WRITE, shareAll, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	// A tag that is nobody's, with its 16 bytes of identity and four bytes
	// of data: the tag, the data's length, two bytes that are kept free,
	// the identity, and the data.
	buffer := binary.LittleEndian.AppendUint32(nil, 0x0000_7C51)
	buffer = binary.LittleEndian.AppendUint16(buffer, 4)
	buffer = binary.LittleEndian.AppendUint16(buffer, 0)
	buffer = append(buffer, []byte("scionic-test-tag")...)
	buffer = append(buffer, 1, 2, 3, 4)
	var returned uint32
	return windows.DeviceIoControl(handle, windows.FSCTL_SET_REPARSE_POINT, &buffer[0], uint32(len(buffer)), nil, 0, &returned, nil) == nil
}

func TestAFileThatSomethingStandsInForIsOpenedAsThatFileOrNotAtAll(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string][]byte{"top/plain": []byte("plain"), "top/stood-in": []byte("stood in for")})
	if !standInFor(t, filepath.Join(root, "top", "stood-in")) {
		t.Skip("this system would not let anything stand in for a file")
	}
	top := must[*held](t)(openHeld(filepath.Join(root, "top")))
	defer top.release()
	// It is a file, and no link: nothing says that it leads anywhere.
	if got := kinds(t, top); !slices.Equal(got, []listedName{{"plain", foundFile}, {"stood-in", foundFile}}) {
		t.Fatalf("%+v", got)
	}
	if readIn(t, top, "plain") != "plain" {
		t.Fatal("the plain file does not read")
	}
	// Nothing on this machine reads it, so it is not opened. It is the
	// system that says so, and nothing is read by another way.
	file, err := top.openFile("stood-in")
	if err == nil {
		file.Close()
		t.Fatal("a file that nothing on this machine reads was opened")
	}
	if !refusedBySystem(err) {
		t.Fatal(err)
	}
	// It is not built into a tree either, and the error names it.
	_, err = Build(NewMemoryStore(), filepath.Join(root, "top"), BuildOptions{Cutting: smallPieces(t)})
	var told *Error
	if !errors.Is(err, ErrIO) || !errors.As(err, &told) || !strings.HasSuffix(told.Path, "stood-in") {
		t.Fatal(err)
	}
	// And it is still a file that can be taken away.
	if err := os.Remove(filepath.Join(root, "top", "stood-in")); err != nil {
		t.Fatal(err)
	}
}
