package scionic

import (
	"errors"
	"slices"
	"sync/atomic"
	"testing"
)

func TestAStoreKeepsEachKindApartAndEachThingOnce(t *testing.T) {
	store := NewMemoryStore()
	hash := HashOf([]byte("x"))
	for range 2 {
		if err := store.Put(StoredChunk, hash, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if store.Count(StoredChunk) != 1 || store.Bytes(StoredChunk) != 1 {
		t.Fatal("the chunk is not kept once")
	}
	if held, _ := storeHas(store, StoredChunk, hash); !held {
		t.Fatal("the chunk is not held")
	}
	if held, _ := storeHas(store, StoredFileList, hash); held {
		t.Fatal("a chunk is held as a file's list")
	}
	if size, held, _ := storeSize(store, StoredChunk, hash); !held || size != 1 {
		t.Fatal(size, held)
	}
	if _, held, _ := storeSize(store, StoredRecord, hash); held {
		t.Fatal("a chunk is held as a record")
	}
	if kept, held, _ := store.Get(StoredChunk, hash); !held || string(kept) != "x" {
		t.Fatal(kept, held)
	}
	if !slices.Equal(store.Hashes(StoredChunk), []Hash{hash}) {
		t.Fatal("the hashes")
	}
	if err := store.Remove(StoredChunk, hash); err != nil {
		t.Fatal(err)
	}
	if _, held, _ := store.Get(StoredChunk, hash); held {
		t.Fatal("the chunk is still held")
	}
}

func TestAStoreKeepsItsOwnCopy(t *testing.T) {
	store := NewMemoryStore()
	content := []byte("lent")
	hash := HashOf(content)
	if err := store.Put(StoredChunk, hash, content); err != nil {
		t.Fatal(err)
	}
	content[0] = 'b'
	if kept, _, _ := store.Get(StoredChunk, hash); string(kept) != "lent" {
		t.Fatalf("the store kept the caller's bytes: %q", kept)
	}
	// No bytes are kept as no bytes, and are found.
	empty := HashOf(nil)
	if err := store.Put(StoredFileList, empty, nil); err != nil {
		t.Fatal(err)
	}
	if kept, held, _ := store.Get(StoredFileList, empty); !held || len(kept) != 0 {
		t.Fatal(kept, held)
	}
}

// plainStore is a store with the three methods every store has, and no more.
type plainStore struct {
	inner *MemoryStore
}

func (s plainStore) Put(kind Stored, hash Hash, content []byte) error {
	return s.inner.Put(kind, hash, content)
}

func (s plainStore) Get(kind Stored, hash Hash) ([]byte, bool, error) {
	return s.inner.Get(kind, hash)
}

func (s plainStore) Remove(kind Stored, hash Hash) error {
	return s.inner.Remove(kind, hash)
}

func TestAStoreNeedsNoMoreThanThreeMethods(t *testing.T) {
	store := plainStore{NewMemoryStore()}
	hash := HashOf([]byte("abc"))
	if held, err := storeHas(store, StoredChunk, hash); held || err != nil {
		t.Fatal(held, err)
	}
	if err := store.Put(StoredChunk, hash, []byte("abc")); err != nil {
		t.Fatal(err)
	}
	if held, err := storeHas(store, StoredChunk, hash); !held || err != nil {
		t.Fatal(held, err)
	}
	if size, held, err := storeSize(store, StoredChunk, hash); size != 3 || !held || err != nil {
		t.Fatal(size, held, err)
	}
}

// sizedStore is a store that says how long a thing is, and counts how often
// a thing is read from it.
type sizedStore struct {
	plainStore
	read *atomic.Int64
}

func (s sizedStore) Get(kind Stored, hash Hash) ([]byte, bool, error) {
	s.read.Add(1)
	return s.inner.Get(kind, hash)
}

func (s sizedStore) Size(kind Stored, hash Hash) (uint64, bool, error) {
	return s.inner.Size(kind, hash)
}

func TestAStoreThatSaysHowLongAThingIsIsNotReadToSeeWhatItHolds(t *testing.T) {
	store := sizedStore{plainStore{NewMemoryStore()}, new(atomic.Int64)}
	content := corpusBytes("sized", 5000)
	builder := NewBuilder(store, smallChunks(t))
	first := must[Target](t)(builder.FileFromBytes(content))
	// Built again, every chunk and the list are there already. Seeing that
	// takes no read: for a store on disk, a read is the whole of a chunk.
	again := must[Target](t)(builder.FileFromBytes(content))
	if again != first || store.inner.Count(StoredChunk) == 0 {
		t.Fatal(again, first)
	}
	if read := store.read.Load(); read != 0 {
		t.Fatalf("the store was read %d times to see what it holds", read)
	}
	// And it is still asked rightly.
	hash := HashOf([]byte("not there"))
	if held, err := storeHas(store, StoredChunk, hash); held || err != nil {
		t.Fatal(held, err)
	}
	if held, err := storeHas(store, StoredFileList, first.Hash); !held || err != nil {
		t.Fatal(held, err)
	}
}

func TestWhatIsReadFromAStoreIsCheckedBeforeItIsUsed(t *testing.T) {
	store := NewMemoryStore()
	item, err := ChunkRefOf([]byte("right"))
	if err != nil {
		t.Fatal(err)
	}
	put := func(kind Stored, hash Hash, content []byte) {
		t.Helper()
		if err := store.Remove(kind, hash); err != nil {
			t.Fatal(err)
		}
		if err := store.Put(kind, hash, content); err != nil {
			t.Fatal(err)
		}
	}
	// A store that holds other bytes under a chunk's hash.
	put(StoredChunk, item.Hash, []byte("wrong"))
	if _, _, err := loadChunk(store, item); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("other bytes of the same length: %v", err)
	}
	put(StoredChunk, item.Hash, []byte("longer"))
	if _, _, err := loadChunk(store, item); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("other bytes of another length: %v", err)
	}
	// The chunk itself, named by an item that gives it another length.
	put(StoredChunk, item.Hash, []byte("right"))
	if _, _, err := loadChunk(store, ChunkRef{Hash: item.Hash, Length: 4}); !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("an item that gives another length: %v", err)
	}
	if kept, held, err := loadChunk(store, item); err != nil || !held || string(kept) != "right" {
		t.Fatal(kept, held, err)
	}
	// A list kept under another list's hash.
	list, err := NewFileList([]ChunkRef{item})
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewFileList([]ChunkRef{item, item})
	if err != nil {
		t.Fatal(err)
	}
	put(StoredFileList, list.Hash(), other.Encode())
	if _, err := loadFile(store, list.Target()); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("a list under another list's hash: %v", err)
	}
	// The right list, asked for with another size than it has.
	put(StoredFileList, other.Hash(), other.Encode())
	resized := other.Target()
	resized.Size++
	if _, err := loadFile(store, resized); !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("a list asked for with another size: %v", err)
	}
	if held, err := loadFile(store, other.Target()); err != nil || held == nil {
		t.Fatal(held, err)
	}
	// A file's list is not found as a folder's.
	asFolder := other.Target()
	asFolder.Kind = KindFolder
	if held, err := loadFolder(store, asFolder); err != nil || held != nil {
		t.Fatal(held, err)
	}
	if _, err := loadFile(store, asFolder); err == nil {
		t.Fatal("a folder was read as a file")
	}
	// A record kept under another root.
	root := RootOfRecord([]byte("one"))
	put(StoredRecord, root.Hash(), []byte("two"))
	if _, err := loadRecord(store, root); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("a record under another root: %v", err)
	}
}
