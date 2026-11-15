package scionic

import (
	"bytes"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
)

// A responder keeps the lists it was asked about, so that a busy one does
// not read and hash a long list again for every request.

// countingStore counts how often a list is read from it.
type countingStore struct {
	*MemoryStore
	listsRead atomic.Int64
}

func (s *countingStore) Get(kind Stored, hash Hash) ([]byte, bool, error) {
	if kind == StoredFileList || kind == StoredFolderList {
		s.listsRead.Add(1)
	}
	return s.MemoryStore.Get(kind, hash)
}

// listsReadFor asks about two files in turn, as two peers at once would,
// and says how often a list was read from the store for it.
func listsReadFor(t *testing.T, store *countingStore, responder *Responder, files [2]Hash) int64 {
	t.Helper()
	before := store.listsRead.Load()
	for round := range uint64(20) {
		for _, file := range files {
			for _, request := range []Request{
				RequestChunks(file, round, 2),
				RequestFileBranch(file, round, 3),
				RequestFileBytes(file, round*16, 20),
			} {
				frames := 0
				err := responder.Answer(request.Encode(), func([]byte) error {
					frames++
					return nil
				})
				if err != nil || frames == 0 {
					t.Fatalf("%+v: %d frames (%v)", request, frames, err)
				}
			}
		}
	}
	return store.listsRead.Load() - before
}

func TestListsThatWereAskedAboutAreNotReadAgain(t *testing.T) {
	store := &countingStore{MemoryStore: NewMemoryStore()}
	builder := NewBuilder(store, smallChunks(t))
	first := must[Target](t)(builder.FileFromBytes(corpusBytes("responder-a", 1600)))
	second := must[Target](t)(builder.FileFromBytes(corpusBytes("responder-b", 1600)))
	files := [2]Hash{first.Hash, second.Hash}
	// Each of the two lists is read once, for 120 requests.
	if read := listsReadFor(t, store, NewResponder(store), files); read != 2 {
		t.Fatalf("the lists were read %d times", read)
	}
	// A responder that keeps one list only reads again at every change.
	if read := listsReadFor(t, store, NewResponder(store).Recent(0), files); read != 40 {
		t.Fatalf("a responder that keeps one list read the lists %d times", read)
	}
	// A list that was harmed in the store is named, not served.
	store.Remove(StoredFileList, first.Hash)
	store.Put(StoredFileList, first.Hash, bytes.Repeat([]byte{7}, 72))
	for _, request := range []Request{
		RequestFileBranch(first.Hash, 0, 1),
		RequestFileBytes(first.Hash, 0, 1),
		RequestChunks(first.Hash, 0, 1),
		RequestHave(first.Hash),
	} {
		err := NewResponder(store).Answer(request.Encode(), func([]byte) error { return nil })
		var told *Error
		if !errors.Is(err, ErrCorrupt) || !errors.As(err, &told) || told.Hash != first.Hash {
			t.Fatalf("%+v: %v", request, err)
		}
	}
}

func TestAResponderCanKeepToItselfWhichChunksItHolds(t *testing.T) {
	store := NewMemoryStore()
	builder := NewBuilder(store, smallChunks(t))
	file := must[Target](t)(builder.FileFromBytes(corpusBytes("responder-have", 160)))
	request := RequestHave(file.Hash).Encode()
	answer := func(responder *Responder) Frame {
		t.Helper()
		var frames []Frame
		err := responder.Answer(request, func(frame []byte) error {
			frames = append(frames, must[Frame](t)(DecodeFrame(bytes.Clone(frame))))
			return nil
		})
		if err != nil || len(frames) != 1 {
			t.Fatalf("%d frames (%v)", len(frames), err)
		}
		return frames[0]
	}
	told := answer(NewResponder(store))
	if told.Kind != FrameHave || told.Have.Count() != 10 || told.Have.Held() != 10 {
		t.Fatalf("%+v", told)
	}
	kept := answer(NewResponder(store).Have(false))
	if kept.Kind != FrameRefused || kept.Refusal != RefusalNotHeld {
		t.Fatalf("%+v", kept)
	}
}

// A responder fills one frame again for every chunk it sends, from one
// request to the next. What it sends is the chunk that was asked for and
// nothing of a longer one before it, and a frame that is lent out is never
// filled again: a request that is answered inside the answer to another is
// given a frame of its own.
func TestAFrameThatIsFilledAgainHoldsTheChunkAskedForAlone(t *testing.T) {
	store := NewMemoryStore()
	long := corpusBytes("frame-long", 4000)
	short := corpusBytes("frame-short", 10)
	for _, chunk := range [][]byte{long, short} {
		if err := store.Put(StoredChunk, HashOf(chunk), chunk); err != nil {
			t.Fatal(err)
		}
	}
	ask := func(chunk []byte) []byte { return RequestChunk(HashOf(chunk)).Encode() }
	responder := NewResponder(store)
	sent := func(request []byte, inside func() error) []byte {
		t.Helper()
		var frames [][]byte
		err := responder.Answer(request, func(frame []byte) error {
			if err := inside(); err != nil {
				return err
			}
			// Taken after the answer inside, which left this frame as it was.
			frames = append(frames, bytes.Clone(frame))
			return nil
		})
		if err != nil || len(frames) != 1 {
			t.Fatalf("%d frames (%v)", len(frames), err)
		}
		return frames[0]
	}
	nothing := func() error { return nil }
	for _, chunk := range [][]byte{long, short, long, short} {
		if frame := sent(ask(chunk), nothing); !bytes.Equal(frame, ChunkFrame(chunk)) {
			t.Fatalf("a chunk of %d bytes was sent in a frame of %d", len(chunk), len(frame))
		}
	}
	var inner []byte
	outer := sent(ask(long), func() error {
		inner = sent(ask(short), nothing)
		return nil
	})
	if !bytes.Equal(outer, ChunkFrame(long)) || !bytes.Equal(inner, ChunkFrame(short)) {
		t.Fatalf("frames of %d and %d bytes", len(outer), len(inner))
	}
}

func TestAListReadWithItsTreeIsTheListReadAlone(t *testing.T) {
	for _, count := range []int{0, 1, 2, 3, 5, 8, 13, 100} {
		var items []ChunkRef
		for number := range count {
			items = append(items, must[ChunkRef](t)(ChunkRefOf(corpusBytes(fmt.Sprintf("chunk-%d", number), 16))))
		}
		list := must[*FileList](t)(NewFileList(items))
		open := must[*OpenFile](t)(DecodeOpenFile(list.Encode()))
		if open.List().Hash() != list.Hash() || !bytes.Equal(open.List().Encode(), list.Encode()) {
			t.Fatalf("%d chunks: the list read with its tree is another list", count)
		}
		if count > 0 {
			end := uint64(min(count, 3))
			read := must[*FileBranch](t)(open.Open(0, end))
			built := must[*FileBranch](t)(must[*OpenFile](t)(NewOpenFile(list)).Open(0, end))
			alone := must[*FileBranch](t)(list.Open(0, end))
			if !bytes.Equal(read.Encode(), built.Encode()) || !bytes.Equal(read.Encode(), alone.Encode()) {
				t.Fatalf("%d chunks: a branch is shown otherwise", count)
			}
		}
	}
	file := must[*FileList](t)(NewFileList([]ChunkRef{must[ChunkRef](t)(ChunkRefOf([]byte("some bytes")))})).Target()
	entry := func(number int) Entry {
		return plainEntry(t, fmt.Sprintf("name-%03d", number), file)
	}
	for _, count := range []int{0, 1, 2, 3, 7, 50} {
		var entries []Entry
		for number := range count {
			entries = append(entries, entry(number))
		}
		list := must[*FolderList](t)(NewFolderList(entries))
		open := must[*OpenFolder](t)(DecodeOpenFolder(list.Encode()))
		if open.List().Hash() != list.Hash() || !bytes.Equal(open.List().Encode(), list.Encode()) {
			t.Fatalf("%d entries: the list read with its tree is another list", count)
		}
		for _, name := range []string{"name-000", "name-025", "not-there"} {
			read := must[*FolderBranch](t)(open.OpenName(name))
			built := must[*FolderBranch](t)(must[*OpenFolder](t)(NewOpenFolder(list)).OpenName(name))
			alone := must[*FolderBranch](t)(list.OpenName(name))
			if !bytes.Equal(read.Encode(), built.Encode()) || !bytes.Equal(read.Encode(), alone.Encode()) {
				t.Fatalf("%d entries: %s is shown otherwise", count, name)
			}
		}
	}
	// Entries out of order are no list, read either way. A list's bytes are
	// its entries' bytes, one after another.
	unsorted := joined(
		must[*FolderList](t)(NewFolderList([]Entry{entry(2)})).Encode(),
		must[*FolderList](t)(NewFolderList([]Entry{entry(1)})).Encode())
	if _, err := DecodeFolderList(unsorted); err == nil {
		t.Fatal("entries out of order were read as a list")
	}
	if _, err := DecodeOpenFolder(unsorted); err == nil {
		t.Fatal("entries out of order were read as a list with its tree")
	}
}
