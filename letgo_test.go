package scionic

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Whatever is held open on disk is let go of, however the work ends. In Go
// nothing does that for a folder or a file that is forgotten, so every way
// out is held to it here: an error, and a store that falls over.

func TestAStoreThatFallsOverDoesNotLeaveWhatWasWrittenHeld(t *testing.T) {
	store, root := sampleTree(t)
	dest := filepath.Join(t.TempDir(), "written")
	// The store falls over when the first chunk is read, which is when the
	// first file is open and being written into its new folder.
	raised := endsByFalling(t, "writing a tree out", func() {
		WriteToDisk(fallingOnGet{store}, root, dest)
	})
	if raised != "this store falls over" {
		t.Fatalf("writing a tree out fell over with %v", raised)
	}
	if !exists(dest) {
		t.Fatal("the store fell over before anything was written")
	}
	if err := movable(dest); err != nil {
		t.Fatalf("what was written is still held after the store fell over: %v", err)
	}
	if err := os.RemoveAll(dest); err != nil {
		t.Fatal(err)
	}
	// The same for a tree that is one file.
	file := targetAt(t, store, root, "weights/model.bin")
	alone := must[Root](t)(NewBuilder(store, smallChunks(t)).Finish(file, ExtraData{}))
	destFile := filepath.Join(t.TempDir(), "written.bin")
	raised = endsByFalling(t, "writing a file out", func() {
		WriteToDisk(fallingOnGet{store}, alone, destFile)
	})
	if raised != "this store falls over" || !exists(destFile) {
		t.Fatalf("writing a file out fell over with %v", raised)
	}
	if err := os.Remove(destFile); err != nil {
		t.Fatalf("the file that was being written is still open: %v", err)
	}
}

// brokenWriter fails after it has taken so many bytes.
type brokenWriter struct {
	left int
}

var errBrokenWriter = errors.New("this writer is broken")

func (w *brokenWriter) Write(content []byte) (int, error) {
	if len(content) > w.left {
		taken := w.left
		w.left = 0
		return taken, errBrokenWriter
	}
	w.left -= len(content)
	return len(content), nil
}

func TestAWriterThatFailsIsToldAsItFailed(t *testing.T) {
	store, root := sampleTree(t)
	model := targetAt(t, store, root, "weights/model.bin")
	for _, left := range []int{0, 1, 16, 17, 999} {
		err := ReadFile(store, model, &brokenWriter{left: left})
		var told *Error
		if !errors.Is(err, ErrStream) || !errors.Is(err, errBrokenWriter) || !errors.As(err, &told) || told.Operation != "write a file's bytes" {
			t.Fatalf("after %d bytes: %v", left, err)
		}
	}
	if err := ReadFile(store, model, &brokenWriter{left: 1000}); err != nil {
		t.Fatal(err)
	}
	// The same for one who reads from a peer and keeps nothing.
	remote := must[*Remote](t)(OpenRemote(newTestPeer(store), root))
	if err := remote.Read(model, 0, 1000, &brokenWriter{left: 500}); !errors.Is(err, ErrStream) || !errors.Is(err, errBrokenWriter) {
		t.Fatal(err)
	}
	// And a bundle that cannot be written.
	if err := Export(store, root, &brokenWriter{left: 100}); !errors.Is(err, ErrStream) || !errors.Is(err, errBrokenWriter) {
		t.Fatal(err)
	}
}
