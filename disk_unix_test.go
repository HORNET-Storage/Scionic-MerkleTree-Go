//go:build unix

package scionic

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/sys/unix"
)

func TestALinkIsRefusedWhereverItStands(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string][]byte{"secret/key": []byte("secret"), "top/real/file": []byte("mine")})
	at := func(names ...string) string {
		return filepath.Join(append([]string{root}, names...)...)
	}
	for link, to := range map[string]string{
		at("top", "to-folder"):  at("secret"),
		at("top", "to-file"):    at("secret", "key"),
		at("top", "to-nothing"): at("nowhere"),
	} {
		if err := os.Symlink(to, link); err != nil {
			t.Fatal(err)
		}
	}
	top := must[*held](t)(openHeld(at("top")))
	defer top.release()
	want := []listedName{{"real", foundFolder}, {"to-file", foundLink}, {"to-folder", foundLink}, {"to-nothing", foundLink}}
	if got := kinds(t, top); !slices.Equal(got, want) {
		t.Fatalf("%+v", got)
	}
	if _, err := top.openFolder("to-folder"); !errors.Is(err, errRefusedLink) {
		t.Fatal(err)
	}
	for _, name := range []string{"to-file", "to-folder", "to-nothing"} {
		if _, err := top.openFile(name); !errors.Is(err, errRefusedLink) {
			t.Fatalf("%s: %v", name, err)
		}
		// Nothing is made through a link, or in the place of one.
		if file, err := top.createFile(name); err == nil {
			file.Close()
			t.Fatalf("a file was made at %s", name)
		}
		if err := top.makeFolder(name); err == nil {
			t.Fatalf("a folder was made at %s", name)
		}
	}
	if exists(at("nowhere")) {
		t.Fatal("something was made through a link")
	}
	// A folder that is held stays the folder it was, whatever becomes of
	// its name: here the name is given to a link that leads elsewhere.
	real := must[*held](t)(top.openFolder("real"))
	defer real.release()
	if err := os.Rename(at("top", "real"), at("top", "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(at("secret"), at("top", "real")); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, root, map[string][]byte{"secret/file": []byte("not mine")})
	if readIn(t, real, "file") != "mine" {
		t.Fatal("a held folder was left by way of its name")
	}
	if _, err := top.openFolder("real"); !errors.Is(err, errRefusedLink) {
		t.Fatal(err)
	}
	// What is made in it lands in it, and not where the link leads.
	writeIn(t, real, "new", "new")
	if err := real.makeFolder("new-folder"); err != nil {
		t.Fatal(err)
	}
	if !exists(at("top", "moved", "new")) || !exists(at("top", "moved", "new-folder")) || exists(at("secret", "new")) || exists(at("secret", "new-folder")) {
		t.Fatal("what was made in a held folder landed somewhere else")
	}
	// It lists what is in it, and not what the link leads to.
	if got := kinds(t, real); !slices.Equal(got, []listedName{{"file", foundFile}, {"new", foundFile}, {"new-folder", foundFolder}}) {
		t.Fatalf("%+v", got)
	}
	// The place a caller names is the caller's, link or not.
	if err := os.Symlink(at("top"), at("alias")); err != nil {
		t.Fatal(err)
	}
	alias := must[*held](t)(openHeld(at("alias")))
	defer alias.release()
	must[*held](t)(alias.openFolder("moved")).release()
	// A pipe that took a file's place is refused, and not waited on.
	if err := unix.Mkfifo(at("top", "pipe"), 0o600); err == nil {
		if _, err := top.openFile("pipe"); !errors.Is(err, errRefusedOther) {
			t.Fatal(err)
		}
		if got := kinds(t, top); !slices.Contains(got, listedName{"pipe", foundOther}) {
			t.Fatalf("%+v", got)
		}
	}
}

func TestAFileThatIsNotReadIsStillAFile(t *testing.T) {
	// A file that this user may not read is listed as a file, and opening
	// it is the system's refusal: it is not taken for a link or for
	// something else.
	if os.Getuid() == 0 {
		t.Skip("the first user reads every file")
	}
	root := t.TempDir()
	writeFiles(t, root, map[string][]byte{"top/closed": []byte("closed")})
	if err := os.Chmod(filepath.Join(root, "top", "closed"), 0); err != nil {
		t.Fatal(err)
	}
	top := must[*held](t)(openHeld(filepath.Join(root, "top")))
	defer top.release()
	if got := kinds(t, top); !slices.Equal(got, []listedName{{"closed", foundFile}}) {
		t.Fatalf("%+v", got)
	}
	if _, err := top.openFile("closed"); !refusedBySystem(err) || !errors.Is(err, os.ErrPermission) {
		t.Fatal(err)
	}
}
