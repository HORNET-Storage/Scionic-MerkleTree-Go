package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The example is run, so that it cannot fall behind the library. The roots
// it prints are the ones that the Rust library's example prints for a file
// of the same size: both make the same bytes, and both have to chunk and hash
// them the same way.
func TestTheExamplePrintsTheRootsThatTheRustLibrarysDoes(t *testing.T) {
	folder := t.TempDir()
	if err := os.WriteFile(filepath.Join(folder, "notes.md"), []byte("# Notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, sized := range []struct {
		megabytes int
		printed   []string
	}{
		{8, []string{" 5 chunks\n", "root bafkreihlmar6at4go4edps64trfo7nmddh6i3lyd6ibuhiqwjvxohnorqu (74)\n"}},
		{0, []string{" 0 chunks\n", "root bafkreihfnyssmxyxarhaixuy77kzg4u6dkyc55az74s5npkmsb3xdh6ryu (00)\n"}},
	} {
		var out bytes.Buffer
		root, err := speed(sized.megabytes, folder, &out)
		if err != nil {
			t.Fatalf("%d MiB: %v\n%s", sized.megabytes, err, out.String())
		}
		printed := out.String()
		wanted := append(sized.printed, "chunked ", "hashed ", "built ", "checked ", "root "+root.String(),
			"fetched ", "bundled ", "built from disk ", " 1 different files, 8 bytes\n")
		for _, want := range wanted {
			if !strings.Contains(printed, want) {
				t.Fatalf("%d MiB: the example did not print %q:\n%s", sized.megabytes, want, printed)
			}
		}
	}
}

func TestTheFillerIsTheSameBytesEveryTime(t *testing.T) {
	long := filler(1000)
	if len(long) != 1000 || len(filler(0)) != 0 {
		t.Fatal(len(long))
	}
	// A shorter filling is the start of a longer one, wherever it is cut off.
	for _, length := range []int{1, 7, 8, 9, 999} {
		if !bytes.Equal(filler(length), long[:length]) {
			t.Fatalf("%d bytes of filler are not the first %d of 1000", length, length)
		}
	}
	// The generator's first step, low byte first, as it was worked out by
	// hand: 0x9E3779B97F4A7C15 through its three shifts.
	if !bytes.Equal(long[:8], []byte{0xad, 0x4d, 0xf3, 0x0b, 0xae, 0x77, 0x1b, 0xdc}) {
		t.Fatalf("the filler starts % x", long[:8])
	}
}
