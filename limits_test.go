package scionic

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"slices"
	"strconv"
	"testing"
)

// How much a receiver sets aside for a peer: no more than the tree it
// trusts calls for. And an intake that has refused a part of its tree does
// not go on as if nothing had happened.

func TestEveryRequestSaysHowLongItsAnswerMayBe(t *testing.T) {
	source, root := sampleTree(t)
	peer := newTestPeer(source)
	must[*Summary](t)(Fetch(NewMemoryStore(), root, peer))
	listBytes := int(must[*Record](t)(RecordOf(source, root)).Totals().ListBytes)
	if len(peer.wire.requests) != len(peer.wire.limits) || len(peer.wire.requests) == 0 {
		t.Fatal("a request went out without a limit")
	}
	for at, request := range peer.wire.requests {
		limit := peer.wire.limits[at]
		asked := must[Request](t)(DecodeRequest(request))
		switch asked.Kind {
		case AskRecord:
			if limit != MaxRecordBytes {
				t.Fatalf("a record may be %d bytes long", limit)
			}
		case AskFolder:
			// A folder's list: no longer than the record leaves room for.
			if limit > listBytes+1 || limit < 1 {
				t.Fatalf("a folder's list may be %d bytes long", limit)
			}
		case AskFile:
			// A file's list: no longer than the file's size allows. These
			// files are split into chunks of 16 bytes, so it is known exactly.
			list := keptIn(t, source, StoredFileList, asked.Hash)
			if limit != max(len(list)+1, 2) {
				t.Fatalf("a file's list of %d bytes may be %d bytes long", len(list), limit)
			}
		case AskChunks:
			// A chunk: no longer than the tree's way of chunking allows.
			if limit != 17 {
				t.Fatalf("a chunk may be %d bytes long", limit)
			}
		default:
			t.Fatalf("%+v", asked)
		}
	}
}

func TestAnAnswerThatIsTooLongIsRefusedByItsLength(t *testing.T) {
	source, root := sampleTree(t)
	// The first answer to each kind of request, with bytes added to it. A
	// connection refuses such a frame by the length that comes before it.
	for _, asked := range []RequestKind{AskRecord, AskFolder, AskFile, AskChunks} {
		done := false
		_, err := fetchTampered(t, source, root, func(request []byte, frames [][]byte) [][]byte {
			if RequestKind(request[0]) == asked && !done {
				done = true
				frames[0] = joined(frames[0], make([]byte, 40_000))
			}
			return frames
		})
		if !errors.Is(err, ErrLimitExceeded) || !done {
			t.Fatalf("%#x: %v", byte(asked), err)
		}
	}
	// The same for one who reads parts of a tree and keeps nothing.
	weights := targetAt(t, source, root, "weights")
	model := targetAt(t, source, root, "weights/model.bin")
	for _, asked := range []RequestKind{AskRecord, AskFolderBranch, AskFolderName, AskFileBytes, AskChunks, AskHave} {
		peer := tamperingPeer(source, func(request []byte, frames [][]byte) [][]byte {
			if RequestKind(request[0]) == asked {
				frames[0] = joined(frames[0], make([]byte, 200_000))
			}
			return frames
		})
		var out bytes.Buffer
		err := func() error {
			remote, err := OpenRemote(peer, root)
			if err != nil {
				return err
			}
			if _, err := remote.Entries(weights, 0, 2); err != nil {
				return err
			}
			if _, _, err := remote.Entry(weights, "model.bin"); err != nil {
				return err
			}
			if err := remote.Read(model, 100, 300, &out); err != nil {
				return err
			}
			_, err = remote.Have(model)
			return err
		}()
		if !errors.Is(err, ErrLimitExceeded) {
			t.Fatalf("%#x: %v", byte(asked), err)
		}
	}
	// What such a reader lets through for a stretch of a file is small: the
	// branch of the list that holds the stretch, and chunks no longer than the
	// branch says they are.
	peer := newTestPeer(source)
	remote := must[*Remote](t)(OpenRemote(peer, root))
	if err := remote.Read(model, 100, 300, io.Discard); err != nil {
		t.Fatal(err)
	}
	must[*HaveMap](t)(remote.Have(model))
	// 300 bytes lie in 20 chunks of 16 bytes at the most, the two at the
	// ends counted: 23 bytes, 20 items, and 80 nodes of 40 bytes. A chunk
	// may be 17 bytes long. And 63 chunks are 63 bits: 8 bytes, after the
	// frame's first byte and the count.
	if want := []int{MaxRecordBytes, 23 + 20*36 + 80*40, 17, 17}; !slices.Equal(peer.wire.limits, want) {
		t.Fatalf("the limits were %v", peer.wire.limits)
	}
}

func TestALongFrameThatNeverComesCostsLittle(t *testing.T) {
	// A stream that claims a frame of 3 GiB and holds five bytes of it. The
	// frame's bytes are set aside as they come, so this is an error at
	// once, and not three gigabytes of memory first.
	stream := joined(binary.BigEndian.AppendUint32(nil, 3<<30), []byte("short"))
	_, err := ReadFrame(bytes.NewReader(stream), maxInt)
	if strconv.IntSize == 64 && !errors.Is(err, ErrInvalidData) {
		t.Fatal(err)
	}
	// Where a frame that long has no place in memory at all, it is refused
	// by its length.
	if strconv.IntSize == 32 && !errors.Is(err, ErrLimitExceeded) {
		t.Fatal(err)
	}
	// Under a limit it is refused by its length, with nothing of it read.
	unread := bytes.NewReader(stream)
	if _, err := ReadFrame(unread, 1<<20); !errors.Is(err, ErrLimitExceeded) || unread.Len() != 5 {
		t.Fatalf("%d bytes are left unread (%v)", unread.Len(), err)
	}
	// A frame that spans several steps of setting aside comes back whole.
	long := corpusBytes("long", 3<<20+7)
	input := bytes.NewReader(withLength(long))
	if frame, err := ReadFrame(input, len(long)); err != nil || !bytes.Equal(frame, long) {
		t.Fatal(err)
	}
	if _, err := ReadFrame(input, 1); err != io.EOF {
		t.Fatal(err)
	}
	// A length of zero is no frame.
	if _, err := ReadFrame(bytes.NewReader(make([]byte, 4)), 100); !errors.Is(err, ErrInvalidData) {
		t.Fatal(err)
	}
	// A stream that ends inside the length.
	if _, err := ReadFrame(bytes.NewReader(make([]byte, 2)), 100); !errors.Is(err, ErrInvalidData) {
		t.Fatal(err)
	}
	// A limit that is no length takes no frame.
	if _, err := ReadFrame(bytes.NewReader(withLength([]byte{1})), -1); !errors.Is(err, ErrLimitExceeded) {
		t.Fatal(err)
	}
}

// treeWithAChunkOfTwoLengths is a tree that is wrong in itself: two of its
// files list the same chunk, the one with 16 bytes and the other with 15.
// Every list of it is well formed and has the hash its parent gives it.
func treeWithAChunkOfTwoLengths(t *testing.T) (record *Record, folder *FolderList, right, wrong *FileList, whole []byte) {
	t.Helper()
	whole = corpusBytes("whole", 16)
	item := must[ChunkRef](t)(ChunkRefOf(whole))
	right = must[*FileList](t)(NewFileList([]ChunkRef{item}))
	wrong = must[*FileList](t)(NewFileList([]ChunkRef{{Hash: item.Hash, Length: 15}}))
	folder = must[*FolderList](t)(NewFolderList([]Entry{plainEntry(t, "a", right.Target()), plainEntry(t, "b", wrong.Target())}))
	totals := Totals{Chunks: 1, ChunkBytes: 16, ListBytes: 72 + folder.EncodedLen()}
	record = must[*Record](t)(NewRecord(folder.Target(), totals, smallChunks(t), ExtraData{}))
	return record, folder, right, wrong, whole
}

func TestAnIntakeThatRefusedAPartOfItsTreeIsDone(t *testing.T) {
	record, folder, right, wrong, whole := treeWithAChunkOfTwoLengths(t)
	root := record.Root()
	dest := NewMemoryStore()
	intake := must[*Intake](t)(NewIntake(dest, root))
	must[Accepted](t)(intake.Accept(record.Encode()))
	// A frame that is refused at the door leaves the intake as it was.
	if _, err := intake.Accept(ChunkFrame([]byte("nothing names this"))); !errors.Is(err, ErrNotWanted) {
		t.Fatal(err)
	}
	if _, err := intake.Accept([]byte{0x25}); err == nil || errors.Is(err, ErrIntakeFailed) {
		t.Fatal(err)
	}
	must[Accepted](t)(intake.Accept(FolderListFrame(folder.Encode())))
	must[Accepted](t)(intake.Accept(FileListFrame(right.Encode())))
	// The second list is named, and it is what its hash says. It fails a
	// check all the same, and that shows the tree itself to be wrong.
	if _, err := intake.Accept(FileListFrame(wrong.Encode())); !errors.Is(err, ErrSizeMismatch) {
		t.Fatal(err)
	}
	if held, _ := dest.Has(StoredFileList, wrong.Hash()); held {
		t.Fatal("a list that failed its checks was kept")
	}
	// From here on the intake takes nothing, asks for nothing, and never
	// says that the tree is whole.
	if _, err := intake.Accept(ChunkFrame(whole)); !errors.Is(err, ErrIntakeFailed) {
		t.Fatal(err)
	}
	if _, err := intake.Accept(FileListFrame(right.Encode())); !errors.Is(err, ErrIntakeFailed) {
		t.Fatal(err)
	}
	if requests := intake.Requests(10); len(requests) != 0 {
		t.Fatalf("an intake that is done asks for %d things", len(requests))
	}
	if _, err := intake.Finish(); !errors.Is(err, ErrIntakeFailed) {
		t.Fatal(err)
	}
	if held, _ := dest.Has(StoredChunk, HashOf(whole)); held {
		t.Fatal("a chunk was kept after the tree was found to be wrong")
	}
	// A new intake goes on from what the store holds, and finds the same.
	again := must[*Intake](t)(NewIntake(dest, root))
	if again.Outstanding() != 2 {
		t.Fatalf("%d things are outstanding, and it is one list and one chunk", again.Outstanding())
	}
	if _, err := again.Accept(FileListFrame(wrong.Encode())); err == nil {
		t.Fatal("the list that is wrong was taken the second time")
	}
	if _, err := again.Finish(); !errors.Is(err, ErrIntakeFailed) {
		t.Fatal(err)
	}
	// And so does one that is given the lists the other way round.
	fresh := NewMemoryStore()
	intake = must[*Intake](t)(NewIntake(fresh, root))
	must[Accepted](t)(intake.Accept(record.Encode()))
	must[Accepted](t)(intake.Accept(FolderListFrame(folder.Encode())))
	must[Accepted](t)(intake.Accept(FileListFrame(wrong.Encode())))
	if _, err := intake.Accept(FileListFrame(right.Encode())); err == nil {
		t.Fatal("two lists that give one chunk two lengths were both taken")
	}
	if held, _ := fresh.Has(StoredFileList, right.Hash()); held {
		t.Fatal("a list that failed its checks was kept")
	}
	if _, err := intake.Finish(); !errors.Is(err, ErrIntakeFailed) {
		t.Fatal(err)
	}
}

func TestATreeNeverTakesMoreRoomThanItsRecordSays(t *testing.T) {
	source, root := sampleTree(t)
	record := must[*Record](t)(RecordOf(source, root))
	right := record.Totals()
	// Records that say less than the tree holds: of lists, of chunks, and
	// of the chunks' bytes.
	for _, totals := range []Totals{
		{right.Chunks, right.ChunkBytes, right.ListBytes - 1},
		{right.Chunks - 1, right.ChunkBytes, right.ListBytes},
		{right.Chunks, right.ChunkBytes - 1, right.ListBytes},
	} {
		forged := must[*Record](t)(NewRecord(record.Top(), totals, record.Chunking(), ExtraData{}))
		source.Put(StoredRecord, forged.Root().Hash(), forged.Encode())
		dest := NewMemoryStore()
		if _, err := Fetch(dest, forged.Root(), newTestPeer(source)); !errors.Is(err, ErrTotalsMismatch) {
			t.Fatalf("%+v: %v", totals, err)
		}
		// The fetch stopped at the list that went over. That list is not
		// kept, so what is kept fits in what the record said.
		lists := dest.Bytes(StoredFileList) + dest.Bytes(StoredFolderList)
		if lists > totals.ListBytes || uint64(dest.Count(StoredChunk)) > totals.Chunks || dest.Bytes(StoredChunk) > totals.ChunkBytes {
			t.Fatalf("%+v: more was kept than the record says the tree holds", totals)
		}
	}
}
