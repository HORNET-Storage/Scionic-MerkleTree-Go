//go:build windows

package scionic

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestOnWindowsANameIsReadAsTheFolderListsIt(t *testing.T) {
	// Windows drops a dot or a space from the end of a name when it looks a
	// path up, and takes nul for a device wherever it stands. So a file
	// "model." beside a file "model" would be read as "model". Such names
	// are made by a path that begins with \\?\, which is taken as it is.
	scratch := t.TempDir()
	whole := must[string](t)(filepath.Abs(scratch))
	if len(whole) < 3 || whole[1] != ':' {
		t.Skipf("%s is not on a drive with a letter", whole)
	}
	exact := `\\?\` + whole + `\tree`
	files := []string{"model", "model.", "model ", "nul", "dir/inside", "dir./inside", ".../inside"}
	contentOf := func(name string) []byte { return corpusBytes("names-"+name, 16) }
	if err := os.Mkdir(exact, 0o777); err != nil {
		t.Fatal(err)
	}
	// Whatever is left of them is taken away by their exact names: the
	// usual way of removing a folder does not find them all.
	defer func() {
		for _, name := range files {
			os.RemoveAll(exact + `\` + strings.Split(name, "/")[0])
		}
	}()
	for _, name := range files {
		parts := strings.Split(name, "/")
		if len(parts) == 2 {
			if err := os.Mkdir(exact+`\`+parts[0], 0o777); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(exact+`\`+strings.Join(parts, `\`), contentOf(name), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	for _, workers := range []int{1, 4} {
		store := NewMemoryStore()
		root := must[Root](t)(Build(store, filepath.Join(scratch, "tree"), BuildOptions{Chunking: smallChunks(t), Workers: workers}))
		for _, name := range files {
			entry, found, err := Lookup(store, root, name)
			if err != nil || !found {
				t.Fatalf("%q with %d workers: found: %v (%v)", name, workers, found, err)
			}
			if read := must[[]byte](t)(ReadFileBytes(store, entry.Target())); !bytes.Equal(read, contentOf(name)) {
				t.Fatalf("%q with %d workers was read as another file", name, workers)
			}
		}
		// Four files and three folders under the top, and nothing else.
		if summary := must[*Summary](t)(Verify(store, root)); summary.Files != 7 || summary.Folders != 4 {
			t.Fatalf("%d workers: %d files in %d folders", workers, summary.Files, summary.Folders)
		}
		// Windows cannot hold these names as they are, so here the tree is
		// not written out: nothing is made, and the error names the name.
		out := filepath.Join(scratch, fmt.Sprintf("out-%d", workers))
		err := WriteToDisk(store, root, out)
		var told *Error
		if !errors.Is(err, ErrUnwritableName) || !errors.As(err, &told) {
			t.Fatalf("%d workers: %v", workers, err)
		}
		if !slices.Contains([]string{"model.", "model ", "nul", "dir.", "..."}, told.Name) {
			t.Fatalf("%d workers: the name that is told is %q", workers, told.Name)
		}
		if exists(out) {
			t.Fatalf("%d workers: something was written", workers)
		}
	}
}
