package scionic

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeTree writes files under a new folder: each name may hold slashes, and
// a name that ends in a slash is an empty folder.
func writeTree(t *testing.T, files map[string][]byte) string {
	t.Helper()
	top := filepath.Join(t.TempDir(), "top")
	if err := os.Mkdir(top, 0o777); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		path := filepath.Join(top, filepath.FromSlash(name))
		if name[len(name)-1] == '/' {
			if err := os.MkdirAll(path, 0o777); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	return top
}

func TestTheTimeIsWrittenInUTCToTheSecond(t *testing.T) {
	for seconds, want := range map[int64]string{
		0:             "1970-01-01T00:00:00Z",
		951_782_400:   "2000-02-29T00:00:00Z",
		1_794_472_200: "2026-11-12T08:30:00Z",
		4_102_444_799: "2099-12-31T23:59:59Z",
		-1:            "1969-12-31T23:59:59Z",
	} {
		if got := formatEpochSeconds(seconds); got != want {
			t.Errorf("%d is written %s, and not %s", seconds, got, want)
		}
	}
}

func TestAFolderIsBuiltTheSameOnOneCoreAndOnMany(t *testing.T) {
	files := map[string][]byte{
		"a.txt":            []byte("alpha"),
		"b/c.txt":          []byte("gamma"),
		"b/d/e.bin":        corpusBytes("build:e", 5000),
		"b/d/same.bin":     corpusBytes("build:e", 5000),
		"empty.txt":        nil,
		"empty-folder/":    nil,
		"long/long.bin":    corpusBytes("build:long", 3<<20),
		"z/y/x/w/deep.txt": []byte("deep"),
	}
	top := writeTree(t, files)
	var roots []Root
	for _, chunking := range []Chunking{FastCDCV1, smallChunks(t)} {
		var first Root
		for index, workers := range []int{1, 2, 0} {
			store := NewMemoryStore()
			root, err := Build(store, top, BuildOptions{Chunking: chunking, Workers: workers})
			if err != nil {
				t.Fatalf("%v with %d workers: %v", chunking, workers, err)
			}
			if index == 0 {
				first = root
			} else if root != first {
				t.Fatalf("%v with %d workers: the root is %v, and not %v", chunking, workers, root, first)
			}
			summary, err := VerifyWith(store, root, VerifyOptions{Rechunk: true})
			if err != nil {
				t.Fatalf("%v with %d workers: %v", chunking, workers, err)
			}
			// The top, b, b/d, empty-folder, long, z, z/y, z/y/x and z/y/x/w.
			if summary.Folders != 9 {
				t.Fatalf("%d folders", summary.Folders)
			}
			// Two of the seven files are the same file.
			if summary.Files != 6 {
				t.Fatalf("%d files", summary.Files)
			}
			if got := summary.Record.Top().Size; got != 5+5+5000+5000+(3<<20)+4 {
				t.Fatalf("the tree holds %d bytes", got)
			}
		}
		roots = append(roots, first)
	}
	if roots[0] == roots[1] {
		t.Fatal("two ways of chunking gave one root")
	}
}

func TestALinkIsRefusedOrLeftOut(t *testing.T) {
	top := writeTree(t, map[string][]byte{"real.txt": []byte("real"), "folder/inside.txt": []byte("inside")})
	if err := os.Symlink("real.txt", filepath.Join(top, "link.txt")); err != nil {
		t.Skipf("this system makes no links here: %v", err)
	}
	store := NewMemoryStore()
	_, err := Build(store, top, BuildOptions{Chunking: smallChunks(t)})
	var told *Error
	if !errors.Is(err, ErrUnsupportedEntry) || !errors.As(err, &told) || told.What != "a link" {
		t.Fatalf("a link was not refused: %v", err)
	}
	without := writeTree(t, map[string][]byte{"real.txt": []byte("real"), "folder/inside.txt": []byte("inside")})
	want, err := Build(NewMemoryStore(), without, BuildOptions{Chunking: smallChunks(t)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Build(store, top, BuildOptions{Chunking: smallChunks(t), Links: LinksSkip})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatal("a tree with a link left out is not the tree without the link")
	}
}
