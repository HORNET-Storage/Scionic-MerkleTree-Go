package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	scionic "github.com/HORNET-Storage/Scionic-Merkle-Tree/v2"
)

// The example is run, so that it cannot fall behind the library: a folder
// goes its whole way round through the store on disk, and comes back as
// the same tree.
func TestAFolderGoesItsWholeWayRound(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "my-model")
	files := map[string][]byte{
		"config.json":        []byte("{\"layers\": 12}\n"),
		"weights/model.bin":  bytes.Repeat([]byte{7}, 3<<20),
		"weights/again.bin":  bytes.Repeat([]byte{7}, 3<<20),
		"notes/empty.txt":    nil,
		"notes/deep/more.md": []byte("# Notes\n"),
	}
	for name, content := range files {
		path := filepath.Join(folder, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	work := filepath.Join(t.TempDir(), "work")
	var out bytes.Buffer
	root, err := roundtrip(folder, work, &out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	printed := out.String()
	for _, want := range []string{
		"root         " + root.String(),
		// The two files of 3 MiB are one file, kept once.
		"the tree     6291479 bytes; kept once: 3 chunks of 3145751 bytes",
		"4 different files in 4 different folders",
		"4096 bytes from the middle of a file of 3145728 bytes",
		"the copy has the same root",
	} {
		if !strings.Contains(printed, want) {
			t.Fatalf("the example did not print %q:\n%s", want, printed)
		}
	}
	// What was written out is what was built from, byte for byte.
	for name, content := range files {
		copied, err := os.ReadFile(filepath.Join(work, "written", filepath.FromSlash(name)))
		if err != nil || !bytes.Equal(copied, content) {
			t.Fatalf("%s did not come back as it was (%v)", name, err)
		}
	}
	// A second run needs a folder of its own.
	if _, err := roundtrip(folder, work, &out); err == nil {
		t.Fatal("a work folder that is there already was used again")
	}
}

func TestTheStoreOnDiskKeepsAndGivesBack(t *testing.T) {
	store := &dirStore{dir: t.TempDir()}
	content := []byte("a chunk")
	hash := scionic.HashOf(content)
	if _, found, err := store.Get(scionic.StoredChunk, hash); err != nil || found {
		t.Fatal(found, err)
	}
	if _, found, err := store.Size(scionic.StoredChunk, hash); err != nil || found {
		t.Fatal(found, err)
	}
	// Kept twice, it is kept once.
	for range 2 {
		if err := store.Put(scionic.StoredChunk, hash, content); err != nil {
			t.Fatal(err)
		}
	}
	kept, found, err := store.Get(scionic.StoredChunk, hash)
	if err != nil || !found || !bytes.Equal(kept, content) {
		t.Fatal(found, err)
	}
	if size, found, err := store.Size(scionic.StoredChunk, hash); err != nil || !found || size != 7 {
		t.Fatal(size, found, err)
	}
	// A thing is kept by its kind: the same hash as another kind is not it.
	if _, found, err := store.Get(scionic.StoredFileList, hash); err != nil || found {
		t.Fatal(found, err)
	}
	// Removed twice, it is gone, and that is no error.
	for range 2 {
		if err := store.Remove(scionic.StoredChunk, hash); err != nil {
			t.Fatal(err)
		}
	}
	if _, found, err := store.Get(scionic.StoredChunk, hash); err != nil || found {
		t.Fatal(found, err)
	}
}
