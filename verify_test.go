package scionic

import (
	"bytes"
	"errors"
	"testing"
)

// Checking a whole tree that a store holds.

// swapIn puts other bytes under a hash that the store holds already.
func swapIn(store *MemoryStore, kind Stored, hash Hash, content []byte) {
	store.Remove(kind, hash)
	store.Put(kind, hash, content)
}

func TestAWholeTreeChecksAndAMissingPartIsNamed(t *testing.T) {
	store, root := sampleTree(t)
	must[*Summary](t)(Verify(store, root))
	must[*Summary](t)(VerifyWith(store, root, VerifyOptions{Rechunk: true}))
	model := targetAt(t, store, root, "weights/model.bin")
	weights := targetAt(t, store, root, "weights")
	chunk := fileListOf(t, store, model).Items()[5]
	for _, part := range []struct {
		kind Stored
		hash Hash
		what string
	}{
		{StoredChunk, chunk.Hash, "the chunk"},
		{StoredFileList, model.Hash, "the list of the file"},
		{StoredFolderList, weights.Hash, "the list of the folder"},
		{StoredRecord, root.Hash(), "the record of"},
	} {
		kept := keptIn(t, store, part.kind, part.hash)
		store.Remove(part.kind, part.hash)
		for _, options := range []VerifyOptions{{}, {Rechunk: true}, {Workers: 1}} {
			_, err := VerifyWith(store, root, options)
			var told *Error
			if !errors.Is(err, ErrMissing) || !errors.As(err, &told) || told.What != part.what || told.Hash != part.hash {
				t.Fatalf("%s was not missed: %v", part.what, err)
			}
		}
		store.Put(part.kind, part.hash, kept)
		must[*Summary](t)(Verify(store, root))
	}
}

func TestBytesThatChangedInTheStoreAreFound(t *testing.T) {
	store, root := sampleTree(t)
	model := targetAt(t, store, root, "weights/model.bin")
	shard := targetAt(t, store, root, "weights/shards/a.bin")
	weights := targetAt(t, store, root, "weights")
	data := targetAt(t, store, root, "data")
	chunk := fileListOf(t, store, model).Items()[5]
	for _, change := range []struct {
		kind   Stored
		hash   Hash
		forged []byte
		what   string
	}{
		{StoredChunk, chunk.Hash, corpusBytes("other", 16), "a chunk's bytes"},
		{StoredChunk, chunk.Hash, corpusBytes("other", 15), "a chunk cut short"},
		{StoredChunk, chunk.Hash, corpusBytes("other", 17), "a chunk made longer"},
		{StoredFileList, model.Hash, keptIn(t, store, StoredFileList, shard.Hash), "another file's list"},
		{StoredFileList, model.Hash, bytes.Repeat([]byte{7}, 35), "no list at all"},
		{StoredFolderList, weights.Hash, keptIn(t, store, StoredFolderList, data.Hash), "another folder's list"},
		{StoredFolderList, weights.Hash, bytes.Repeat([]byte{9}, 50), "no list at all"},
		{StoredRecord, root.Hash(), bytes.Repeat([]byte{0x20}, 80), "another record"},
	} {
		honest := keptIn(t, store, change.kind, change.hash)
		swapIn(store, change.kind, change.hash, change.forged)
		_, err := Verify(store, root)
		if !errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrSizeMismatch) && !errors.Is(err, ErrInvalidData) {
			t.Fatalf("%s: %v", change.what, err)
		}
		swapIn(store, change.kind, change.hash, honest)
		must[*Summary](t)(Verify(store, root))
	}
}

func TestARecordWithWrongTotalsIsRefused(t *testing.T) {
	store, root := sampleTree(t)
	record := must[*Record](t)(RecordOf(store, root))
	right := record.Totals()
	for _, wrong := range []struct {
		field  string
		totals Totals
	}{
		{"chunks", Totals{right.Chunks - 1, right.ChunkBytes, right.ListBytes}},
		{"chunks", Totals{right.Chunks + 1, right.ChunkBytes, right.ListBytes}},
		{"chunk bytes", Totals{right.Chunks, right.ChunkBytes - 1, right.ListBytes}},
		{"chunk bytes", Totals{right.Chunks, right.ChunkBytes + 1, right.ListBytes}},
		{"list bytes", Totals{right.Chunks, right.ChunkBytes, right.ListBytes - 1}},
		{"list bytes", Totals{right.Chunks, right.ChunkBytes, right.ListBytes + 1}},
	} {
		forged := must[*Record](t)(NewRecord(record.Top(), wrong.totals, record.Chunking(), ExtraData{}))
		forgedRoot := forged.Root()
		if forgedRoot == root {
			t.Fatal("a record with other totals has the same root")
		}
		store.Put(StoredRecord, forgedRoot.Hash(), forged.Encode())
		_, err := Verify(store, forgedRoot)
		var told *Error
		if !errors.Is(err, ErrTotalsMismatch) || !errors.As(err, &told) || told.What != wrong.field {
			t.Fatalf("%+v passed: %v", wrong.totals, err)
		}
	}
}

func TestAnEntryCannotGiveWhatIsUnderItAnotherSize(t *testing.T) {
	store := NewMemoryStore()
	builder := NewBuilder(store, smallChunks(t))
	file := must[Target](t)(builder.FileFromBytes(corpusBytes("sized", 70)))
	folder := must[Target](t)(builder.Folder([]Entry{plainEntry(t, "f", file)}))
	grown := func(target Target) Target {
		target.Size++
		return target
	}
	for _, entries := range [][]Entry{
		{plainEntry(t, "f", grown(file))},
		{plainEntry(t, "d", grown(folder))},
		// The same file under two names, once with its size and once not.
		{plainEntry(t, "a", file), plainEntry(t, "b", grown(file))},
		{plainEntry(t, "a", grown(file)), plainEntry(t, "b", file)},
	} {
		builder := NewBuilder(store, smallChunks(t))
		top := must[Target](t)(builder.Folder(entries))
		if _, err := builder.Finish(top, ExtraData{}); !errors.Is(err, ErrSizeMismatch) {
			t.Fatalf("%v: %v", entries, err)
		}
	}
	builder = NewBuilder(store, smallChunks(t))
	top := must[Target](t)(builder.Folder([]Entry{plainEntry(t, "d", folder), plainEntry(t, "f", file)}))
	root := must[Root](t)(builder.Finish(top, ExtraData{}))
	if size := must[*Summary](t)(Verify(store, root)).Record.Top().Size; size != 140 {
		t.Fatalf("the tree holds %d bytes", size)
	}
}

// fileByHand puts a file into a store by hand, with whatever chunks it is
// given.
func fileByHand(t *testing.T, store Store, chunks ...[]byte) Target {
	t.Helper()
	var items []ChunkRef
	for _, chunk := range chunks {
		item := must[ChunkRef](t)(ChunkRefOf(chunk))
		if err := store.Put(StoredChunk, item.Hash, chunk); err != nil {
			t.Fatal(err)
		}
		items = append(items, item)
	}
	list := must[*FileList](t)(NewFileList(items))
	if err := store.Put(StoredFileList, list.Hash(), list.Encode()); err != nil {
		t.Fatal(err)
	}
	return list.Target()
}

func TestEveryFileIsHeldToTheWayItsTreeWasChunked(t *testing.T) {
	store := NewMemoryStore()
	first, short, last := corpusBytes("p0", 16), corpusBytes("p1", 9), corpusBytes("p2", 16)
	// A chunk in the middle that is shorter than the tree's chunks.
	uneven := fileByHand(t, store, first, short, last)
	// A last chunk that is longer than the tree's chunks.
	longTail := fileByHand(t, store, first, corpusBytes("tail", 17))
	for _, file := range []Target{uneven, longTail} {
		builder := NewBuilder(store, smallChunks(t))
		top := must[Target](t)(builder.Folder([]Entry{plainEntry(t, "f", file)}))
		if _, err := builder.Finish(top, ExtraData{}); !errors.Is(err, ErrInvalidData) {
			t.Fatal(err)
		}
		// A tree of the file alone is refused as well.
		if _, err := NewBuilder(store, smallChunks(t)).Finish(file, ExtraData{}); err == nil {
			t.Fatal("a file that was chunked another way was taken as a tree")
		}
	}
	// Small chunks are no chunks of a tree that is chunked by content.
	even := fileByHand(t, store, first, last, short)
	must[Root](t)(NewBuilder(store, smallChunks(t)).Finish(even, ExtraData{}))
	if _, err := NewBuilder(store, FastCDCV1).Finish(even, ExtraData{}); err == nil {
		t.Fatal("small chunks were taken as chunks that were cut by content")
	}
	// A file of one small chunk fits either way.
	single := fileByHand(t, store, short)
	for _, chunking := range []Chunking{smallChunks(t), FastCDCV1} {
		root := must[Root](t)(NewBuilder(store, chunking).Finish(single, ExtraData{}))
		must[*Summary](t)(Verify(store, root))
	}
}

func TestChunkingAgainFindsAFileThatWasChunkedAnotherWay(t *testing.T) {
	data := corpusBytes("scionic-fastcdc-v1:corpus:random_3mib", 3<<20)
	store := NewMemoryStore()
	builder := NewBuilder(store, FastCDCV1)
	honest := must[Target](t)(builder.FileFromBytes(data))
	root := must[Root](t)(builder.Finish(honest, ExtraData{}))
	must[*Summary](t)(VerifyWith(store, root, VerifyOptions{Rechunk: true}))
	// The same bytes, cut in the middle. Each half is long enough to pass
	// for a chunk that the chunker made, so its lengths alone give nothing
	// away. It is another file: the same bytes under another hash.
	forged := fileByHand(t, store, data[:3<<19], data[3<<19:])
	if forged.Size != honest.Size || forged.Hash == honest.Hash {
		t.Fatal("the forgery is not the same bytes under another hash")
	}
	forgedRoot := must[Root](t)(NewBuilder(store, FastCDCV1).Finish(forged, ExtraData{}))
	must[*Summary](t)(Verify(store, forgedRoot))
	for _, workers := range []int{0, 1} {
		if _, err := VerifyWith(store, forgedRoot, VerifyOptions{Rechunk: true, Workers: workers}); !errors.Is(err, ErrInvalidData) {
			t.Fatal(err)
		}
	}
}

func TestAChunkCannotBeListedWithTwoLengths(t *testing.T) {
	store := NewMemoryStore()
	whole := corpusBytes("whole", 16)
	right := fileByHand(t, store, whole)
	item := must[ChunkRef](t)(ChunkRefOf(whole))
	shorter := must[*FileList](t)(NewFileList([]ChunkRef{{Hash: item.Hash, Length: 15}}))
	store.Put(StoredFileList, shorter.Hash(), shorter.Encode())
	// The chunk is held with 16 bytes, so a list that gives it 15 is wrong
	// on its own, and wrong beside a list that gives it 16.
	for _, entries := range [][]Entry{
		{plainEntry(t, "wrong", shorter.Target())},
		{plainEntry(t, "a-right", right), plainEntry(t, "b-wrong", shorter.Target())},
		{plainEntry(t, "a-wrong", shorter.Target()), plainEntry(t, "b-right", right)},
	} {
		builder := NewBuilder(store, smallChunks(t))
		top := must[Target](t)(builder.Folder(entries))
		if _, err := builder.Finish(top, ExtraData{}); !errors.Is(err, ErrSizeMismatch) {
			t.Fatalf("%v: %v", entries, err)
		}
	}
	// Without the chunk's bytes, two lists that disagree are still refused.
	bare := NewMemoryStore()
	for _, list := range []*FileList{shorter, must[*FileList](t)(NewFileList([]ChunkRef{item}))} {
		bare.Put(StoredFileList, list.Hash(), list.Encode())
	}
	builder := NewBuilder(bare, smallChunks(t))
	top := must[Target](t)(builder.Folder([]Entry{plainEntry(t, "a", right), plainEntry(t, "b", shorter.Target())}))
	if _, err := builder.Finish(top, ExtraData{}); !errors.Is(err, ErrSizeMismatch) {
		t.Fatal(err)
	}
}

func TestATreeIsCheckedByItsRootAndByNothingElse(t *testing.T) {
	store, root := sampleTree(t)
	// A root that the store has no record for.
	unknown := RootOfRecord([]byte("nothing is kept under this"))
	_, err := Verify(store, unknown)
	var told *Error
	if !errors.Is(err, ErrMissing) || !errors.As(err, &told) || told.What != "the record of" {
		t.Fatal(err)
	}
	// The record of one tree, kept under the root of another.
	store.Put(StoredRecord, unknown.Hash(), keptIn(t, store, StoredRecord, root.Hash()))
	if _, err := Verify(store, unknown); !errors.Is(err, ErrCorrupt) {
		t.Fatal(err)
	}
	// A folder is checked once, however many names it has.
	weights := targetAt(t, store, root, "weights")
	builder := NewBuilder(store, smallChunks(t))
	top := must[Target](t)(builder.Folder([]Entry{plainEntry(t, "one", weights), plainEntry(t, "two", weights)}))
	twice := must[Root](t)(builder.Finish(top, ExtraData{}))
	summary := must[*Summary](t)(Verify(store, twice))
	// The top, weights and shards.
	if summary.Folders != 3 || summary.Record.Top().Size != 2*weights.Size || folderListOf(t, store, top).Count() != 2 {
		t.Fatalf("%+v", summary)
	}
}
