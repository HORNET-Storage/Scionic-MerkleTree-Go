package scionic

import (
	"bytes"
	"errors"
	"testing"
)

// A tree whose bytes were harmed where they are kept: the harm is found and
// named, it can be removed and fetched again, and nothing that a peer sends
// can make a store give up what is sound.

// namedAs reports whether an error is of one kind, and names one thing.
func namedAs(err error, kind error, what string, hash Hash) bool {
	var told *Error
	return errors.Is(err, kind) && errors.As(err, &told) && told.What == what && told.Hash == hash
}

func TestADamagedPieceIsNamedAndTheStoreIsLeftAsItIs(t *testing.T) {
	store, root := sampleTree(t)
	model := targetAt(t, store, root, "weights/model.bin")
	piece := fileListOf(t, store, model).Items()[3]
	rot := corpusBytes("rot", int(piece.Length))
	swapIn(store, StoredPiece, piece.Hash, rot)
	for _, workers := range []int{0, 1, 4} {
		if _, err := VerifyWith(store, root, VerifyOptions{Workers: workers}); !namedAs(err, ErrCorrupt, "a stored piece", piece.Hash) {
			t.Fatalf("%d workers: %v", workers, err)
		}
	}
	// Checking changes nothing unless it is told to.
	if !bytes.Equal(keptIn(t, store, StoredPiece, piece.Hash), rot) {
		t.Fatal("a check changed the store")
	}
}

func TestDamageIsRemovedWhenAskedAndAFetchMakesTheTreeWhole(t *testing.T) {
	source, root := sampleTree(t)
	dest := NewMemoryStore()
	must[*Summary](t)(Fetch(dest, root, newTestPeer(source)))
	must[*Summary](t)(Verify(dest, root))
	model := targetAt(t, dest, root, "weights/model.bin")
	weights := targetAt(t, dest, root, "weights")
	list := fileListOf(t, dest, model)
	// Two pieces: one harmed at its own length, one at another length.
	same, other := list.Items()[1], list.Items()[4]
	swapIn(dest, StoredPiece, same.Hash, corpusBytes("rot-1", int(same.Length)))
	swapIn(dest, StoredPiece, other.Hash, corpusBytes("rot-2", 7))
	_, err := VerifyWith(dest, root, VerifyOptions{DiscardDamaged: true})
	var told *Error
	if !errors.Is(err, ErrMissing) || !errors.As(err, &told) || told.What != "the piece" {
		t.Fatal(err)
	}
	for _, harmed := range []PieceRef{same, other} {
		if held, _ := dest.Has(StoredPiece, harmed.Hash); held {
			t.Fatal("a harmed piece was not removed")
		}
	}
	peer := newTestPeer(source)
	must[*Summary](t)(Fetch(dest, root, peer))
	must[*Summary](t)(Verify(dest, root))
	if peer.wire.framesOf(FramePiece) != 2 {
		t.Fatalf("%d pieces were fetched to mend two", peer.wire.framesOf(FramePiece))
	}
	// A list and the record: checking names them, and a fetch replaces them
	// without being told to, because it cannot go on with them.
	for _, harm := range []struct {
		kind   Stored
		hash   Hash
		harmed []byte
		what   string
	}{
		{StoredFolderList, weights.Hash, bytes.Repeat([]byte{9}, 50), "a stored folder's list"},
		{StoredFileList, model.Hash, bytes.Repeat([]byte{7}, 72), "a stored file's list"},
		{StoredRecord, root.Hash(), bytes.Repeat([]byte{0x20}, 80), "a stored record"},
	} {
		swapIn(dest, harm.kind, harm.hash, harm.harmed)
		if _, err := Verify(dest, root); !namedAs(err, ErrCorrupt, harm.what, harm.hash) {
			t.Fatalf("%s: %v", harm.what, err)
		}
		must[*Summary](t)(Fetch(dest, root, newTestPeer(source)))
		must[*Summary](t)(Verify(dest, root))
	}
	if !sameStores(dest, source) {
		t.Fatal("the mended store is not the store that was fetched from")
	}
}

func TestAListThatLiesAboutAPieceCannotCostAStoreThePiece(t *testing.T) {
	// A sound piece of 16 bytes, and a file's list that gives it 15.
	whole := corpusBytes("sound", 16)
	item := must[PieceRef](t)(PieceRefOf(whole))
	lying := must[*FileList](t)(NewFileList([]PieceRef{{Hash: item.Hash, Length: 15}}))
	totals := Totals{Pieces: 1, PieceBytes: 15, ListBytes: 36}
	record := must[*Record](t)(NewRecord(lying.Target(), totals, smallPieces(t), ExtraData{}))
	root := RootOfRecord(record.Encode())
	forger := NewMemoryStore()
	forger.Put(StoredRecord, root.Hash(), record.Encode())
	forger.Put(StoredFileList, lying.Hash(), lying.Encode())
	// A holder of the piece is sent the tree. The tree is refused, and the
	// piece stays: bytes that hash to what they are kept under are sound,
	// whatever a list says of them.
	holder := NewMemoryStore()
	holder.Put(StoredPiece, item.Hash, whole)
	if _, err := Fetch(holder, root, newTestPeer(forger)); !errors.Is(err, ErrSizeMismatch) {
		t.Fatal(err)
	}
	if !bytes.Equal(keptIn(t, holder, StoredPiece, item.Hash), whole) {
		t.Fatal("a sound piece was lost to a list that lies about it")
	}
	// The same when the list is in the store already.
	forger.Put(StoredPiece, item.Hash, whole)
	if _, err := NewIntake(forger, root); !errors.Is(err, ErrSizeMismatch) {
		t.Fatal(err)
	}
	if _, err := VerifyWith(forger, root, VerifyOptions{DiscardDamaged: true}); !errors.Is(err, ErrSizeMismatch) {
		t.Fatal(err)
	}
	if !bytes.Equal(keptIn(t, forger, StoredPiece, item.Hash), whole) {
		t.Fatal("a sound piece was removed as damaged")
	}
}
