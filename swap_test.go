package scionic

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// A folder is not left by way of a link that takes a file's or a folder's
// place while the folder is read, or while a tree is written into it.
//
// Each test plays the trick at a set moment: a store does it when the
// library puts or asks for a certain thing, which is in the middle of the
// work.

// trickyStore plays a trick in the middle of the library's work: when the
// first chunk is put, or when the thing it watches for is asked for. It has
// the three methods that a store has to have, and no others.
type trickyStore struct {
	inner *MemoryStore
	// watched is the thing whose asking for plays the trick. Without it,
	// the trick is played when a chunk is put.
	watched *storedKey
	lock    sync.Mutex
	// trick is given the bytes that are being put. It says whether it was
	// played, and is then not played again.
	trick func(content []byte) bool
}

func (s *trickyStore) play(content []byte) {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.trick != nil && s.trick(content) {
		s.trick = nil
	}
}

func (s *trickyStore) Put(kind Stored, hash Hash, content []byte) error {
	if s.watched == nil && kind == StoredChunk {
		s.play(content)
	}
	return s.inner.Put(kind, hash, content)
}

func (s *trickyStore) Get(kind Stored, hash Hash) ([]byte, bool, error) {
	if s.watched != nil && *s.watched == (storedKey{kind, hash}) {
		s.play(nil)
	}
	return s.inner.Get(kind, hash)
}

func (s *trickyStore) Remove(kind Stored, hash Hash) error {
	return s.inner.Remove(kind, hash)
}

// linkNow is what the library says of a link that took the place of a file
// or a folder after its folder was read.
const linkNow = "a link now, where a file or a folder was when the build began"

func holdsChunk(store *MemoryStore, content []byte) bool {
	held, _ := store.Has(StoredChunk, HashOf(content))
	return held
}

// removeLink takes away a link that was made to see whether the system
// makes such links.
func removeLink(t *testing.T, link string) {
	t.Helper()
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
}

func TestAFolderThatBecomesALinkWhileItsFolderIsReadIsNotEntered(t *testing.T) {
	temp := t.TempDir()
	secret := corpusBytes("swap-secret", 16)
	writeFiles(t, temp, map[string][]byte{"secret/inside": secret})
	secretFolder := filepath.Join(temp, "secret")
	probe := filepath.Join(temp, "probe")
	if !linkToFolder(probe, secretFolder) {
		t.Skip("this system would not make such a link")
	}
	removeLink(t, probe)
	for _, threads := range []int{1, 4} {
		top := filepath.Join(temp, fmt.Sprintf("top-%d", threads))
		writeFiles(t, top, map[string][]byte{"a-file": corpusBytes("swap-a", 64), "z-dir/inside": corpusBytes("swap-mine", 16)})
		dir, moved := filepath.Join(top, "z-dir"), filepath.Join(top, "moved")
		played := false
		// When the first chunk is kept, the folder is given another name,
		// and its own name is given to a link that leads to the secret.
		store := &trickyStore{inner: NewMemoryStore(), trick: func([]byte) bool {
			if os.Rename(dir, moved) == nil {
				played = linkToFolder(dir, secretFolder)
			}
			return true
		}}
		_, err := Build(store, top, BuildOptions{Chunking: smallChunks(t), Workers: threads})
		// Whatever the timing, the secret is not in the tree.
		if holdsChunk(store.inner, secret) {
			t.Fatalf("%d threads: the secret was read through a link", threads)
		}
		// One thread reads the folder's files before it goes down into the
		// folders, so the link is always there when it does.
		if threads == 1 {
			var told *Error
			if !played || !errors.Is(err, ErrUnsupportedEntry) || !errors.As(err, &told) || told.What != linkNow {
				t.Fatalf("the trick was played: %v, and the build gave: %v", played, err)
			}
		}
	}
}

func TestAFileThatBecomesALinkWhileItsFolderIsReadIsNotRead(t *testing.T) {
	temp := t.TempDir()
	secret := corpusBytes("swap-secret-file", 16)
	secretPath := filepath.Join(temp, "secret-file")
	x, y := corpusBytes("swap-x", 16), corpusBytes("swap-y", 16)
	writeFiles(t, temp, map[string][]byte{"secret-file": secret, "top/x": x, "top/y": y})
	probe := filepath.Join(temp, "probe")
	if !linkToFile(probe, secretPath) {
		t.Skip("this system would not make such a link")
	}
	removeLink(t, probe)
	top := filepath.Join(temp, "top")
	played := false
	// Whichever file is chunked first, the other one is swapped for a link to
	// the secret before its turn comes.
	store := &trickyStore{inner: NewMemoryStore(), trick: func(chunk []byte) bool {
		other := filepath.Join(top, "x")
		if bytes.Equal(chunk, x) {
			other = filepath.Join(top, "y")
		}
		played = os.Remove(other) == nil && linkToFile(other, secretPath)
		return true
	}}
	_, err := Build(store, top, BuildOptions{Chunking: smallChunks(t), Workers: 1})
	var told *Error
	if !played || !errors.Is(err, ErrUnsupportedEntry) || !errors.As(err, &told) || told.What != linkNow {
		t.Fatalf("the trick was played: %v, and the build gave: %v", played, err)
	}
	if holdsChunk(store.inner, secret) {
		t.Fatal("the secret was read through a link")
	}
}

func TestNothingIsWrittenThroughALinkThatTakesANewFoldersPlace(t *testing.T) {
	temp := t.TempDir()
	inner, last := corpusBytes("swap-inner", 16), corpusBytes("swap-last", 16)
	source := filepath.Join(temp, "source")
	writeFiles(t, source, map[string][]byte{"a-file": corpusBytes("swap-first", 16), "z-dir/inner": inner, "zz-last": last})
	outside := filepath.Join(temp, "outside")
	if err := os.Mkdir(outside, 0o777); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(temp, "probe")
	if !linkToFolder(probe, outside) {
		t.Skip("this system would not make such a link")
	}
	removeLink(t, probe)
	options := BuildOptions{Chunking: smallChunks(t)}
	plain := NewMemoryStore()
	root := must[Root](t)(Build(plain, source, options))
	folder := targetAt(t, plain, root, "z-dir")
	// Two moments. "held": the new folder is open and about to be filled,
	// which is when its list is asked for. "waiting": the new folder is
	// made and waits for its turn, while the file after it is written.
	for _, moment := range []struct {
		what    string
		watched storedKey
	}{
		{"held", storedKey{StoredFolderList, folder.Hash}},
		{"waiting", storedKey{StoredChunk, HashOf(last)}},
	} {
		dest := filepath.Join(temp, "dest-"+moment.what)
		dir, moved := filepath.Join(dest, "z-dir"), filepath.Join(dest, "moved")
		played, linked := false, false
		store := &trickyStore{inner: NewMemoryStore(), watched: &moment.watched, trick: func([]byte) bool {
			if is, err := os.Lstat(dir); err != nil || !is.IsDir() {
				return false // nothing of the folder is written yet
			}
			// The folder is given another name where the system lets it
			// be, and its own name is given to a link that leads outside.
			if os.Rename(dir, moved) == nil {
				linked = linkToFolder(dir, outside)
			}
			played = true
			return true
		}}
		if again := must[Root](t)(Build(store.inner, source, options)); again != root {
			t.Fatal("the same folder builds to another root")
		}
		err := WriteToDisk(store, root, dest)
		if !played {
			t.Fatalf("%s: the trick was not played (%v)", moment.what, err)
		}
		if written := must[[]os.DirEntry](t)(os.ReadDir(outside)); len(written) != 0 {
			t.Fatalf("%s: something was written outside (%v)", moment.what, err)
		}
		// A folder that is held is filled wherever its name has gone. One
		// that waited and is now a link is not gone into.
		if moment.what == "held" {
			if !exists(filepath.Join(moved, "inner")) && !exists(filepath.Join(dir, "inner")) {
				t.Fatalf("held: the folder was not filled (%v)", err)
			}
			if content, readErr := os.ReadFile(filepath.Join(moved, "inner")); linked && (readErr != nil || !bytes.Equal(content, inner)) {
				t.Fatalf("held: the folder that was given another name does not hold its file (%v)", readErr)
			}
		} else if err == nil {
			t.Fatal("waiting: a tree was written with a link in a folder's place")
		}
	}
}
