package scionic

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"testing"
)

// What a forger can try, and that each try is refused.
//
// The forger here is a peer that answers a receiver, a bundle that a
// receiver reads, or whoever wrote into a receiver's store. Each test makes
// the forgery out of bytes, as a forger would, and holds the receiver to
// two things: it says no, and it has kept nothing that is not a true part
// of the tree it asked for.

func targetAt(t *testing.T, store Store, root Root, path string) Target {
	t.Helper()
	entry, found, err := Lookup(store, root, path)
	if err != nil || !found {
		t.Fatalf("%q: %v %v", path, found, err)
	}
	return entry.Target()
}

func topOfTree(t *testing.T, store Store, root Root) Target {
	t.Helper()
	record, err := RecordOf(store, root)
	if err != nil {
		t.Fatal(err)
	}
	return record.Top()
}

// keptIn gives a copy of what a store holds under a hash.
func keptIn(t *testing.T, store Store, kind Stored, hash Hash) []byte {
	t.Helper()
	kept, held, err := store.Get(kind, hash)
	if err != nil || !held {
		t.Fatalf("%v %v: %v %v", kind, hash, held, err)
	}
	return bytes.Clone(kept)
}

func fileListOf(t *testing.T, store Store, file Target) *FileList {
	t.Helper()
	list, err := FileOf(store, file)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func folderListOf(t *testing.T, store Store, folder Target) *FolderList {
	t.Helper()
	list, err := FolderOf(store, folder)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// plainEntry gives an entry with no extra data.
func plainEntry(t *testing.T, name string, target Target) Entry {
	t.Helper()
	entry, err := NewEntry(name, target, ExtraData{})
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

// must gives a value that has to be there.
func must[T any](t *testing.T) func(value T, err error) T {
	return func(value T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
}

// assertClean: everything a store holds has to be what its hash says it
// is.
func assertClean(t *testing.T, store *MemoryStore) {
	t.Helper()
	for _, thing := range contentsOf(store) {
		content := []byte(thing.content)
		switch thing.kind {
		case StoredChunk:
			if HashOf(content) != thing.hash {
				t.Fatalf("a chunk is kept under %v, which is not its hash", thing.hash)
			}
		case StoredFileList:
			if list, err := DecodeFileList(content); err != nil || list.Hash() != thing.hash {
				t.Fatalf("a file's list is kept under %v, which is not its hash (%v)", thing.hash, err)
			}
		case StoredFolderList:
			if list, err := DecodeFolderList(content); err != nil || list.Hash() != thing.hash {
				t.Fatalf("a folder's list is kept under %v, which is not its hash (%v)", thing.hash, err)
			}
		case StoredRecord:
			if _, err := DecodeRecord(content); err != nil || RootOfRecord(content).Hash() != thing.hash {
				t.Fatalf("a record is kept under %v, which is not its hash (%v)", thing.hash, err)
			}
		}
	}
}

// assertOnlyFrom: everything dest holds has to be a part of what source
// holds.
func assertOnlyFrom(t *testing.T, dest, source *MemoryStore) {
	t.Helper()
	assertClean(t, dest)
	honest := contentsOf(source)
	for _, thing := range contentsOf(dest) {
		if !slices.Contains(honest, thing) {
			t.Fatalf("%v %v was kept", thing.kind, thing.hash)
		}
	}
}

// tamper changes the frames of an answer on their way to a receiver.
type tamper = func(request []byte, frames [][]byte) [][]byte

// fetchTampered fetches a tree from a peer that changes its answers on the
// way.
func fetchTampered(t *testing.T, source *MemoryStore, root Root, change tamper) (*MemoryStore, error) {
	t.Helper()
	dest := NewMemoryStore()
	_, err := Fetch(dest, root, tamperingPeer(source, change))
	assertOnlyFrom(t, dest, source)
	return dest, err
}

// answering gives a tamper that answers every request of one kind with the
// frames it is given, and leaves every other answer as it is.
func answering(kind RequestKind, with ...[]byte) tamper {
	return func(request []byte, frames [][]byte) [][]byte {
		if RequestKind(request[0]) == kind {
			return with
		}
		return frames
	}
}

func TestAnyChangedByteOfAnyFrameIsRefused(t *testing.T) {
	source, root := sampleTree(t)
	// How many frames an honest fetch of the sample takes.
	honest := newTestPeer(source)
	if _, err := Fetch(NewMemoryStore(), root, honest); err != nil {
		t.Fatal(err)
	}
	if len(honest.wire.frames) != 88 {
		t.Fatalf("an honest fetch takes %d frames", len(honest.wire.frames))
	}
	for target := range 88 {
		for place := range 3 {
			seen := 0
			dest, err := fetchTampered(t, source, root, func(_ []byte, frames [][]byte) [][]byte {
				for _, frame := range frames {
					if seen == target {
						at := []int{0, len(frame) / 2, len(frame) - 1}[place]
						frame[at] ^= 1
					}
					seen++
				}
				return frames
			})
			if err == nil {
				t.Fatalf("frame %d, place %d: a changed byte was taken", target, place)
			}
			if _, err := Verify(dest, root); err == nil {
				t.Fatalf("frame %d, place %d: the tree counts as whole", target, place)
			}
		}
	}
}

func TestEveryByteOfABranchIsCoveredByTheHashItIsHeldAgainst(t *testing.T) {
	// A branch shows a part of a list, and carries nodes for the rest. If one
	// of its bytes could change and the branch still pass, two different branches
	// would show the same thing, and that byte would be nobody's to vouch
	// for. So every byte of every branch of a list of nine is changed here, one
	// at a time, in its lowest bit and in its highest.
	store := NewMemoryStore()
	builder := NewBuilder(store, smallChunks(t))
	file := must[Target](t)(builder.FileFromBytes(corpusBytes("covered", 9*16)))
	entries := make([]Entry, 9)
	for index := range entries {
		entries[index] = plainEntry(t, fmt.Sprintf("n%02d", index), file)
	}
	folder := must[Target](t)(builder.Folder(entries))
	fileList := fileListOf(t, store, file)
	folderList := folderListOf(t, store, folder)
	if fileList.Count() != 9 || folderList.Count() != 9 {
		t.Fatal(fileList.Count(), folderList.Count())
	}
	changes := 0
	eachChange := func(branch []byte, what string, taken func(branch []byte) bool) {
		t.Helper()
		if !taken(branch) {
			t.Fatalf("%s is refused as it was made", what)
		}
		for at := range branch {
			for _, bit := range []byte{0x01, 0x80} {
				changed := slices.Clone(branch)
				changed[at] ^= bit
				if taken(changed) {
					t.Fatalf("%s: byte %d of %d was changed, and the branch was taken", what, at, len(branch))
				}
				changes++
			}
		}
	}
	for start := range uint64(9) {
		for end := start + 1; end <= 9; end++ {
			branch := must[*FolderBranch](t)(folderList.Open(start, end)).Encode()
			eachChange(branch, fmt.Sprintf("entries %d up to %d", start, end), func(branch []byte) bool {
				read, err := DecodeFolderBranch(branch)
				return err == nil && read.Check(folder) == nil
			})
			branch = must[*FileBranch](t)(fileList.Open(start, end)).Encode()
			eachChange(branch, fmt.Sprintf("chunks %d up to %d", start, end), func(branch []byte) bool {
				read, err := DecodeFileBranch(branch)
				if err != nil {
					return false
				}
				_, err = read.Check(file)
				return err == nil
			})
		}
	}
	// The answer to a name is a branch too, whether the name is there or not.
	for _, name := range []string{"n00", "n04", "n08", "a", "n04x", "z"} {
		answer := must[*FolderBranch](t)(folderList.OpenName(name)).Encode()
		eachChange(answer, fmt.Sprintf("the answer for %q", name), func(branch []byte) bool {
			read, err := DecodeFolderBranch(branch)
			if err != nil {
				return false
			}
			_, _, err = read.CheckName(folder, name)
			return err == nil
		})
	}
	if changes < 30_000 {
		t.Fatalf("only %d changes were tried", changes)
	}
}

func TestAMadeUpChildUnderAFolderOrAFileIsRefused(t *testing.T) {
	source, root := sampleTree(t)
	weights := targetAt(t, source, root, "weights")
	model := targetAt(t, source, root, "weights/model.bin")
	madeUp := oracleFile("zz-made-up", oracleSHA([]byte("made up")), 7)
	madeUpItem := oracleItemOf([]byte("a made-up chunk"))
	// One more entry at the end of a folder's list, one more item at the
	// end of a file's list, and one less of each.
	forges := []struct {
		what  string
		forge func(frame []byte) []byte
	}{
		{"an entry more", func(frame []byte) []byte {
			if FrameKind(frame[0]) != FrameFolderList {
				return nil
			}
			return joined(frame, madeUp.bytes())
		}},
		{"an item more", func(frame []byte) []byte {
			if FrameKind(frame[0]) != FrameFileList || len(frame) <= 1 {
				return nil
			}
			return joined(frame, madeUpItem.bytes())
		}},
		{"an entry less", func(frame []byte) []byte {
			if FrameKind(frame[0]) != FrameFolderList {
				return nil
			}
			list, err := DecodeFolderList(frame[1:])
			if err != nil || list.Count() == 0 {
				return nil
			}
			shorter, err := NewFolderList(slices.Clone(list.Entries()[:list.Count()-1]))
			if err != nil {
				t.Fatal(err)
			}
			return oracleFrame(byte(FrameFolderList), shorter.Encode())
		}},
		{"an item less", func(frame []byte) []byte {
			if FrameKind(frame[0]) != FrameFileList || len(frame) <= 37 {
				return nil
			}
			return bytes.Clone(frame[:len(frame)-36])
		}},
	}
	for _, forgery := range forges {
		done := false
		_, err := fetchTampered(t, source, root, func(_ []byte, frames [][]byte) [][]byte {
			for at, frame := range frames {
				if forged := forgery.forge(frame); forged != nil && !done {
					done = true
					frames[at] = forged
				}
			}
			return frames
		})
		if !done {
			t.Fatalf("%s: nothing was forged", forgery.what)
		}
		// It is refused as something nothing names, or sooner, by its
		// length: a file's list may be no longer than the file's size
		// allows.
		if !errors.Is(err, ErrNotWanted) && !errors.Is(err, ErrLimitExceeded) {
			t.Fatalf("%s: %v", forgery.what, err)
		}
	}
	// A part of the folder, from a peer that has made up a folder of four
	// entries which holds the three real ones. It is whole and well formed.
	// It is another folder, and the receiver asked about this one.
	four := append(oracleEntriesOf(folderListOf(t, source, weights).Entries()), madeUp)
	for _, part := range [][2]int{{0, 4}, {3, 4}, {0, 1}} {
		forged, err := DecodeFrame(oracleFolderBranchFrame(four, part[0], part[1]))
		if err != nil || forged.Kind != FrameFolderBranch {
			t.Fatalf("%v: the forgery does not read as a branch: %v", part, err)
		}
		if err := forged.FolderBranch.Check(weights); err == nil {
			t.Fatalf("%v: a branch of another folder was taken", part)
		}
		if _, _, err := forged.FolderBranch.CheckName(weights, "zz-made-up"); err == nil {
			t.Fatalf("%v: a name of another folder was taken", part)
		}
	}
	peer := tamperingPeer(source, answering(AskFolderName, oracleFolderBranchFrame(four, 3, 4)))
	remote := must[*Remote](t)(OpenRemote(peer, root))
	if _, _, err := remote.Entry(weights, "zz-made-up"); err == nil {
		t.Fatal("a made-up entry was shown as the folder's")
	}
	if _, _, err := remote.Lookup("weights/zz-made-up"); err == nil {
		t.Fatal("a made-up entry was found by its path")
	}
	// The same for a file: a made-up list of 64 chunks that holds the real
	// 63 is another file.
	longer := append(oracleItemsOf(fileListOf(t, source, model).Items()), madeUpItem)
	for _, part := range [][2]int{{0, 64}, {63, 64}, {5, 6}} {
		branch, err := DecodeFileBranch(oracleFileBranchFrame(longer, part[0], part[1])[1:])
		if err != nil {
			t.Fatalf("%v: the forgery does not read as a branch: %v", part, err)
		}
		if _, err := branch.Check(model); err == nil {
			t.Fatalf("%v: a branch of another file was taken", part)
		}
	}
}

func TestARealChildsBranchDoesNotCarryAnotherChild(t *testing.T) {
	source, root := sampleTree(t)
	weights := targetAt(t, source, root, "weights")
	model := targetAt(t, source, root, "weights/model.bin")
	list := folderListOf(t, source, weights)
	if place, held := list.PlaceOf("model.bin"); !held || place != 1 {
		t.Fatal(place, held)
	}
	shown := must[*FolderBranch](t)(list.OpenName("model.bin"))
	if entry, found, err := shown.CheckName(weights, "model.bin"); err != nil || !found || entry.Name() != "model.bin" {
		t.Fatal(entry, found, err)
	}
	honest := shown.Encode()
	// The entry that is shown starts 20 bytes into the branch: its kind, its
	// size, its hash, and then its name.
	with := func(at int, value byte) error {
		forged := bytes.Clone(honest)
		forged[at] = value
		branch, err := DecodeFolderBranch(forged)
		if err != nil {
			return err
		}
		_, _, err = branch.CheckName(weights, "model.bin")
		return err
	}
	for _, change := range []struct {
		what  string
		at    int
		value byte
	}{
		{"the file shown as a folder", 20, 2},
		{"another size", 28, honest[28] ^ 1},
		{"another hash", 29, honest[29] ^ 1},
		{"another hash", 60, honest[60] ^ 1},
		{"another name", 63, 'n'},
		{"another place", 15, honest[15] ^ 1},
		{"another count", 7, honest[7] + 1},
	} {
		if honest[change.at] == change.value {
			t.Fatalf("%s: byte %d is that already", change.what, change.at)
		}
		if with(change.at, change.value) == nil {
			t.Fatalf("%s was taken", change.what)
		}
	}
	// The neighbour's branch, with the neighbour passed off under the name
	// that was asked for, and the branch of the wrong place as proof that
	// the name is not there.
	entries := oracleEntriesOf(list.Entries())
	leaves := oracleFolderLeaves(entries)
	record := keptIn(t, source, StoredRecord, root.Hash())
	for place := range 3 {
		for _, name := range []string{"model.bin", "copy-of-model.bin", "shards", "absent", "a", "zz"} {
			// As the place really is: right only when it is the name's
			// place.
			plain := oracleFolderBranchFrame(entries, place, place+1)
			// With the entry renamed to what was asked for.
			renamed := entries[place]
			renamed.name = []byte(name)
			passedOff := oracleFolderBranchFrameAs(3, uint64(place), []oracleEntry{renamed}, leaves.carried(place, place+1))
			truth, there := list.Get(name)
			// A name has one place where it can be, whether it is there or
			// not. The entry at that place is the proof, either way.
			namePlace, hasPlace := list.PlaceOf(name)
			isItsPlace := hasPlace && namePlace == uint64(place)
			for _, forged := range []struct {
				frame   []byte
				isPlain bool
			}{{plain, true}, {passedOff, false}} {
				peer := scriptedPeer(func(request []byte) [][]byte {
					if RequestKind(request[0]) == AskRecord {
						return [][]byte{record}
					}
					return [][]byte{forged.frame}
				})
				remote := must[*Remote](t)(OpenRemote(peer, root))
				answer, found, err := remote.Entry(weights, name)
				unchanged := !forged.isPlain && string(entries[place].name) == name
				if (forged.isPlain || unchanged) && isItsPlace {
					if err != nil || found != there || !answer.Equal(truth) {
						t.Fatalf("%s at %d: the true answer was not taken: %v %v", name, place, found, err)
					}
				} else if err == nil {
					t.Fatalf("%s at %d, plain: %v: taken, and found: %v", name, place, forged.isPlain, found)
				}
			}
		}
	}
	// A file's branch with one item changed, and the branch of other chunks than
	// those that hold the bytes asked for.
	fileList := fileListOf(t, source, model)
	branch := must[*FileBranch](t)(fileList.Open(6, 9)).Encode()
	if at, err := must[*FileBranch](t)(DecodeFileBranch(branch)).Check(model); err != nil || at != 96 {
		t.Fatal(at, err)
	}
	for _, at := range []int{20, 51, 52, 55} {
		forged := bytes.Clone(branch)
		forged[at] ^= 1
		changed, err := DecodeFileBranch(forged)
		if err == nil {
			_, err = changed.Check(model)
		}
		if err == nil {
			t.Fatalf("byte %d of a branch was changed, and the branch was taken", at)
		}
	}
	elsewhere := oracleFrame(byte(FrameFileBranch), must[*FileBranch](t)(fileList.Open(0, 3)).Encode())
	peer := tamperingPeer(source, answering(AskFileBytes, elsewhere))
	remote := must[*Remote](t)(OpenRemote(peer, root))
	var out bytes.Buffer
	if err := remote.Read(model, 100, 40, &out); !errors.Is(err, ErrNotWanted) || out.Len() != 0 {
		t.Fatalf("%d bytes were handed out (%v)", out.Len(), err)
	}
}

func TestAFileIsNeverTakenForAFolderNorAListForAChunk(t *testing.T) {
	source, root := sampleTree(t)
	weights := targetAt(t, source, root, "weights")
	model := targetAt(t, source, root, "weights/model.bin")
	fileFrame := oracleFrame(byte(FrameFileList), keptIn(t, source, StoredFileList, model.Hash))
	folderFrame := oracleFrame(byte(FrameFolderList), keptIn(t, source, StoredFolderList, weights.Hash))
	chunkFrame := oracleFrame(byte(FrameChunk), corpusBytes("model", 16))
	// Each kind of question, answered with a true part of the tree that is
	// of another kind.
	for _, swap := range []struct {
		asked   RequestKind
		answers [][]byte
	}{
		{AskFolder, [][]byte{fileFrame, chunkFrame}},
		{AskFile, [][]byte{folderFrame, chunkFrame}},
		{AskChunks, [][]byte{fileFrame, folderFrame}},
	} {
		for _, answer := range swap.answers {
			done := false
			_, err := fetchTampered(t, source, root, func(request []byte, frames [][]byte) [][]byte {
				if RequestKind(request[0]) == swap.asked && !done {
					done = true
					return [][]byte{answer}
				}
				return frames
			})
			if err == nil || !done {
				t.Fatalf("%#x was answered with a part of another kind, and it was taken", byte(swap.asked))
			}
		}
	}
	// A file's entry, shown as a folder's: the kind is part of the entry.
	top := topOfTree(t, source, root)
	flipped := keptIn(t, source, StoredFolderList, top.Hash)
	list := folderListOf(t, source, top)
	before := list.Entries()[0].EncodedLen()
	if list.Entries()[1].Name() != "empty.bin" || flipped[before] != 1 {
		t.Fatal("the second entry of the sample is not the empty file")
	}
	flipped[before] = 2
	if _, err := DecodeFolderList(flipped); err != nil {
		t.Fatal(err)
	}
	_, err := fetchTampered(t, source, root, answering(AskFolder, oracleFrame(byte(FrameFolderList), flipped)))
	if !errors.Is(err, ErrNotWanted) {
		t.Fatal(err)
	}
	// A file whose bytes are another file's list, to the letter. It is kept
	// as a chunk, under the plain hash of its bytes, and never as a list.
	listBytes := keptIn(t, source, StoredFileList, model.Hash)
	store := NewMemoryStore()
	builder := NewBuilder(store, must[Chunking](t)(FixedChunking(4096)))
	odd := must[Target](t)(builder.FileFromBytes(listBytes))
	oddRoot := must[Root](t)(builder.Finish(odd, ExtraData{}))
	dest := NewMemoryStore()
	if _, err := Fetch(dest, oddRoot, newTestPeer(store)); err != nil {
		t.Fatal(err)
	}
	plain := HashOf(listBytes)
	asChunk, _ := dest.Has(StoredChunk, plain)
	asList, _ := dest.Has(StoredFileList, plain)
	asModel, _ := dest.Has(StoredFileList, model.Hash)
	if !asChunk || asList || asModel || plain == model.Hash {
		t.Fatal("a file's bytes that read as a list were kept as a list")
	}
}

func TestAFileIsNotGivenAnotherFilesChunks(t *testing.T) {
	source, root := sampleTree(t)
	model := targetAt(t, source, root, "weights/model.bin")
	shard := targetAt(t, source, root, "weights/shards/a.bin")
	var shardChunks [][]byte
	for _, item := range fileListOf(t, source, shard).Items() {
		shardChunks = append(shardChunks, oracleFrame(byte(FrameChunk), keptIn(t, source, StoredChunk, item.Hash)))
	}
	// A whole fetch: the chunks of one file, sent when another file's were
	// asked for. They are true chunks of the tree, and they are not what
	// was asked for.
	_, err := fetchTampered(t, source, root, func(request []byte, frames [][]byte) [][]byte {
		asked := must[Request](t)(DecodeRequest(request))
		if asked.Kind == AskChunks && asked.Hash == model.Hash {
			return slices.Clone(shardChunks)
		}
		return frames
	})
	if !errors.Is(err, ErrNotWanted) {
		t.Fatal(err)
	}
	// A stretch of one file, answered with another file's chunks, and with
	// the file's own chunks in another order.
	for _, swap := range []bool{false, true} {
		peer := tamperingPeer(source, func(request []byte, frames [][]byte) [][]byte {
			if RequestKind(request[0]) != AskChunks {
				return frames
			}
			if swap {
				frames[0], frames[1] = frames[1], frames[0]
				return frames
			}
			return slices.Clone(shardChunks)
		})
		remote := must[*Remote](t)(OpenRemote(peer, root))
		var out bytes.Buffer
		if err := remote.Read(model, 0, 64, &out); err == nil || out.Len() != 0 {
			t.Fatalf("swap: %v: %d bytes of another chunk were handed out (%v)", swap, out.Len(), err)
		}
	}
	// A stretch of one file, answered with the branch of another file's list.
	otherBranch := oracleFrame(byte(FrameFileBranch), must[*FileBranch](t)(fileListOf(t, source, shard).Open(0, 4)).Encode())
	peer := tamperingPeer(source, answering(AskFileBytes, otherBranch))
	remote := must[*Remote](t)(OpenRemote(peer, root))
	var out bytes.Buffer
	if err := remote.Read(model, 0, 64, &out); err == nil || out.Len() != 0 {
		t.Fatalf("%d bytes were handed out (%v)", out.Len(), err)
	}
}

func TestARecordForAnotherRootIsRefused(t *testing.T) {
	source, root := sampleTree(t)
	builder := NewBuilder(source, smallChunks(t))
	otherFile := must[Target](t)(builder.FileFromBytes([]byte("another tree")))
	other := must[Root](t)(builder.Finish(otherFile, ExtraData{}))
	otherRecord := keptIn(t, source, StoredRecord, other.Hash())
	dest, err := fetchTampered(t, source, root, answering(AskRecord, otherRecord))
	if !errors.Is(err, ErrHashMismatch) || len(contentsOf(dest)) != 0 {
		t.Fatalf("%d things were kept (%v)", len(contentsOf(dest)), err)
	}
	everything := func(with []byte) tamper {
		return func([]byte, [][]byte) [][]byte { return [][]byte{with} }
	}
	if _, err := OpenRemote(tamperingPeer(source, everything(otherRecord)), root); !errors.Is(err, ErrHashMismatch) {
		t.Fatal(err)
	}
	// The record that was asked for, with one of its totals changed.
	changed := keptIn(t, source, StoredRecord, root.Hash())
	changed[45] ^= 1
	if _, err := OpenRemote(tamperingPeer(source, everything(changed)), root); err == nil {
		t.Fatal("a record with another total was taken")
	}
}

func TestNothingIsKeptBeforeItIsTiedToTheRoot(t *testing.T) {
	source, root := sampleTree(t)
	top := topOfTree(t, source, root)
	readme := targetAt(t, source, root, "readme.txt")
	shard := targetAt(t, source, root, "weights/shards/a.bin")
	record := keptIn(t, source, StoredRecord, root.Hash())
	topList := oracleFrame(byte(FrameFolderList), keptIn(t, source, StoredFolderList, top.Hash))
	readmeList := oracleFrame(byte(FrameFileList), keptIn(t, source, StoredFileList, readme.Hash))
	readmeChunk := oracleFrame(byte(FrameChunk), []byte("a small file"))
	deepList := oracleFrame(byte(FrameFileList), keptIn(t, source, StoredFileList, shard.Hash))
	madeUpChunk := oracleFrame(byte(FrameChunk), []byte("no part of this tree"))
	dest := NewMemoryStore()
	refused := func(frame []byte, what string) {
		t.Helper()
		before := contentsOf(dest)
		intake := must[*Intake](t)(NewIntake(dest, root))
		if _, err := intake.Accept(frame); !errors.Is(err, ErrNotWanted) {
			t.Fatalf("%s: %v", what, err)
		}
		if !slices.Equal(contentsOf(dest), before) {
			t.Fatalf("%s: something was kept", what)
		}
	}
	taken := func(frame []byte, what string) {
		t.Helper()
		intake := must[*Intake](t)(NewIntake(dest, root))
		if accepted, err := intake.Accept(frame); err != nil || !accepted.Kept {
			t.Fatalf("%s: %v %v", what, accepted, err)
		}
		// The same thing again is passed over, and not kept twice.
		before := contentsOf(dest)
		if accepted, err := intake.Accept(frame); err != nil || accepted.Kept {
			t.Fatalf("%s, again: %v %v", what, accepted, err)
		}
		if !slices.Equal(contentsOf(dest), before) {
			t.Fatalf("%s: kept twice", what)
		}
	}
	// Before the record, nothing is tied to anything.
	refused(topList, "the top folder")
	refused(readmeList, "a file's list")
	refused(readmeChunk, "a chunk")
	if must[*Intake](t)(NewIntake(dest, root)).Record() != nil {
		t.Fatal("an intake that was given nothing has a record")
	}
	taken(record, "the record")
	// The record's totals are there to be looked at before anything more is
	// taken.
	intake := must[*Intake](t)(NewIntake(dest, root))
	if intake.Record() == nil || intake.Record().Totals().Chunks != 76 {
		t.Fatal("the record's totals are not there to be looked at")
	}
	if intake.Outstanding() != 1 {
		t.Fatalf("%d things are outstanding, and only the top folder is named", intake.Outstanding())
	}
	// After the record, only the top folder is named.
	refused(readmeList, "a file's list before its folder")
	refused(readmeChunk, "a chunk before its file's list")
	refused(deepList, "a list from further down")
	taken(topList, "the top folder")
	// Now the top folder's files are named, and their chunks are not yet.
	refused(readmeChunk, "a chunk before its file's list")
	refused(deepList, "a list from further down")
	refused(madeUpChunk, "a made-up chunk")
	taken(readmeList, "a file's list")
	refused(madeUpChunk, "a made-up chunk")
	taken(readmeChunk, "a chunk")
	assertOnlyFrom(t, dest, source)
	if _, err := must[*Intake](t)(NewIntake(dest, root)).Finish(); !errors.Is(err, ErrMissing) {
		t.Fatal(err)
	}
	// A bundle with a made-up chunk in it is refused where the chunk comes,
	// be the chunk as long as a chunk of this tree or longer.
	var bundle bytes.Buffer
	if err := Export(source, root, &bundle); err != nil {
		t.Fatal(err)
	}
	withLengthOf := func(frame []byte) []byte {
		return joined(binary.BigEndian.AppendUint32(nil, uint32(len(frame))), frame)
	}
	shortChunk := oracleFrame(byte(FrameChunk), []byte("not of this tree"))
	for _, chunk := range [][]byte{shortChunk, madeUpChunk} {
		extra := joined(bundle.Bytes(), withLengthOf(chunk))
		fresh := NewMemoryStore()
		if _, err := Import(fresh, root, bytes.NewReader(extra)); !errors.Is(err, ErrNotWanted) {
			t.Fatal(err)
		}
		assertOnlyFrom(t, fresh, source)
	}
	// And one whose first list comes before its record.
	early := joined(bundle.Bytes()[:8], withLengthOf(topList), bundle.Bytes()[8:])
	fresh := NewMemoryStore()
	if _, err := Import(fresh, root, bytes.NewReader(early)); err == nil || len(contentsOf(fresh)) != 0 {
		t.Fatalf("%d things were kept (%v)", len(contentsOf(fresh)), err)
	}
}

func TestAPeerWhoseOwnStoreIsWrongIsFoundOut(t *testing.T) {
	source, root := sampleTree(t)
	model := targetAt(t, source, root, "weights/model.bin")
	shard := targetAt(t, source, root, "weights/shards/a.bin")
	weights := targetAt(t, source, root, "weights")
	data := targetAt(t, source, root, "data")
	chunk := fileListOf(t, source, model).Items()[9]
	for _, wrong := range []storedThing{
		{StoredChunk, chunk.Hash, string(corpusBytes("wrong", 16))},
		{StoredFileList, model.Hash, string(keptIn(t, source, StoredFileList, shard.Hash))},
		{StoredFolderList, weights.Hash, string(keptIn(t, source, StoredFolderList, data.Hash))},
	} {
		honest := keptIn(t, source, wrong.kind, wrong.hash)
		source.Remove(wrong.kind, wrong.hash)
		source.Put(wrong.kind, wrong.hash, []byte(wrong.content))
		// One set of checks: the same forgery is refused in the store that
		// holds it, and by whoever fetches from that store.
		if _, err := Verify(source, root); err == nil {
			t.Fatalf("%v: a store that holds a forgery counts as whole", wrong.kind)
		}
		dest := NewMemoryStore()
		if _, err := Fetch(dest, root, newTestPeer(source)); err == nil {
			t.Fatalf("%v: a forgery was fetched", wrong.kind)
		}
		assertClean(t, dest)
		if held, _ := dest.Has(wrong.kind, wrong.hash); held {
			t.Fatalf("%v: the forged part was kept", wrong.kind)
		}
		source.Remove(wrong.kind, wrong.hash)
		source.Put(wrong.kind, wrong.hash, honest)
	}
	if _, err := Verify(source, root); err != nil {
		t.Fatal(err)
	}
}
