package scionic

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// More of what a forger can try: against the shape of a tree. A file that
// comes back short, a name at two places, a place beyond the last, a tree
// that holds more than its record says or goes deeper than a tree may, and
// bytes that are nothing at all.

func TestAFileNeverComesBackShort(t *testing.T) {
	source, root := sampleTree(t)
	model := targetAt(t, source, root, "weights/model.bin")
	list := fileListOf(t, source, model)
	last := list.Items()[list.Count()-1]
	isLast := func(frame []byte) bool {
		return FrameKind(frame[0]) == FrameChunk && HashOf(frame[1:]) == last.Hash
	}
	// A peer that never sends the file's last chunk.
	dest, err := fetchTampered(t, source, root, func(_ []byte, frames [][]byte) [][]byte {
		return slices.DeleteFunc(frames, isLast)
	})
	if !errors.Is(err, ErrPeer) {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := ReadFile(dest, model, &out); !errors.Is(err, ErrMissing) || out.Len() != 0 {
		t.Fatalf("%d bytes came back, and it has to be an error, never a shorter file (%v)", out.Len(), err)
	}
	if _, err := Verify(dest, root); err == nil {
		t.Fatal("a tree with a chunk missing counts as whole")
	}
	// The last chunk, one byte short.
	_, err = fetchTampered(t, source, root, func(_ []byte, frames [][]byte) [][]byte {
		for at, frame := range frames {
			if isLast(frame) {
				frames[at] = frame[:len(frame)-1]
			}
		}
		return frames
	})
	if err == nil {
		t.Fatal("a chunk that is a byte short was taken")
	}
	// A made-up file that is the real one without its last chunk. It is
	// whole and well formed, it is shorter, and it is another file.
	shorter := oracleItemsOf(list.Items()[:62])
	shorterList := oracleFrame(byte(FrameFileList), oracleFileListBytes(shorter))
	_, err = fetchTampered(t, source, root, func(request []byte, frames [][]byte) [][]byte {
		asked := must[Request](t)(DecodeRequest(request))
		if asked.Kind == AskFile && asked.Hash == model.Hash {
			return [][]byte{shorterList}
		}
		return frames
	})
	if !errors.Is(err, ErrNotWanted) {
		t.Fatal(err)
	}
	peer := tamperingPeer(source, answering(AskFileBytes, oracleFileBranchFrame(shorter, 0, 62)))
	remote := must[*Remote](t)(OpenRemote(peer, root))
	out.Reset()
	if err := remote.Read(model, 0, 1000, &out); err == nil || out.Len() != 0 {
		t.Fatalf("%d bytes of a shorter file were handed out (%v)", out.Len(), err)
	}
	// A peer that sends fewer chunks than a stretch needs, and then none.
	sent := 0
	peer = tamperingPeer(source, func(request []byte, frames [][]byte) [][]byte {
		if RequestKind(request[0]) != AskChunks {
			return frames
		}
		keep := min(len(frames), 3-sent)
		sent += keep
		return frames[:keep]
	})
	remote = must[*Remote](t)(OpenRemote(peer, root))
	out.Reset()
	if err := remote.Read(model, 0, 1000, &out); !errors.Is(err, ErrPeer) {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), corpusBytes("model", 1000)[:out.Len()]) {
		t.Fatal("bytes were handed out that are not the file's")
	}
}

func TestOneNameHasOnePlaceInAFolder(t *testing.T) {
	// A folder made by an author who put its entries out of order on
	// purpose, with one name twice. The library makes no such folder, so it
	// is hashed here by hand, to the letter of the format.
	for _, names := range [][]string{
		{"b", "a", "b", "c"},
		{"a", "b", "b", "c"},
		{"c", "b", "a"},
		{"b", "b"},
		{"a", "c", "b", "d", "a", "e", "b"},
	} {
		var entries []oracleEntry
		for place, name := range names {
			entries = append(entries, oracleFile(name, oracleSHA([]byte(fmt.Sprintf("%s%d", name, place))), 1))
		}
		hashed := oracleFolderHash(entries)
		folder := Target{Kind: KindFolder, Hash: hashed.hash, Size: hashed.size}
		listBytes := oracleFolderListBytes(entries)
		record := oracleRecordBytes(2, hashed.size, hashed.hash, [3]uint64{0, 0, uint64(len(listBytes))}, oracleChunkingTag("fixed-16"))
		root := RootOfRecord(record)
		// Whole, the list is refused as soon as it is read.
		dest := NewMemoryStore()
		intake := must[*Intake](t)(NewIntake(dest, root))
		if _, err := intake.Accept(record); err != nil {
			t.Fatal(err)
		}
		if _, err := intake.Accept(oracleFrame(byte(FrameFolderList), listBytes)); !errors.Is(err, ErrInvalidData) {
			t.Fatalf("%v: %v", names, err)
		}
		if dest.Count(StoredFolderList) != 0 {
			t.Fatalf("%v: a folder that is out of order was kept", names)
		}
		// In part, the author shows whichever place suits him. For each
		// name at most one place is ever accepted, so two entries under one
		// name are never both shown.
		for _, name := range []string{"a", "b", "c", "d", "e", "f"} {
			var accepted []int
			for shown := range entries {
				peer := scriptedPeer(func(request []byte) [][]byte {
					if RequestKind(request[0]) == AskRecord {
						return [][]byte{record}
					}
					return [][]byte{oracleFolderBranchFrame(entries, shown, shown+1)}
				})
				remote := must[*Remote](t)(OpenRemote(peer, root))
				if _, _, err := remote.Entry(folder, name); err == nil {
					accepted = append(accepted, shown)
				}
			}
			if len(accepted) > 1 {
				t.Fatalf("%v: %s was answered at the places %v", names, name, accepted)
			}
		}
		// A branch of several entries is accepted only where each of them is
		// at its name's one place: never a branch that shows a name twice.
		for start := range entries {
			for end := start + 1; end <= len(entries); end++ {
				frame, err := DecodeFrame(oracleFolderBranchFrame(entries, start, end))
				if err != nil || frame.Kind != FrameFolderBranch || frame.FolderBranch.Check(folder) != nil {
					continue
				}
				shown := frame.FolderBranch.Entries()
				for at := 1; at < len(shown); at++ {
					if shown[at-1].Name() >= shown[at].Name() {
						t.Fatalf("%v: a branch that shows %q and then %q was taken", names, shown[at-1].Name(), shown[at].Name())
					}
				}
			}
		}
	}
}

func TestNoPlaceExistsBeyondTheCountAndAForkIsNoLeaf(t *testing.T) {
	var items []oracleItem
	for index := range 5 {
		items = append(items, oracleItemOf(corpusBytes(fmt.Sprintf("p%d", index), 16)))
	}
	hashed := oracleFileHash(items)
	file := Target{Kind: KindFile, Hash: hashed.hash, Size: hashed.size}
	check := func(against Target, frame []byte) (uint64, error) {
		branch, err := DecodeFileBranch(frame[1:])
		if err != nil {
			return 0, err
		}
		return branch.Check(against)
	}
	if at, err := check(file, oracleFileBranchFrame(items, 4, 5)); err != nil || at != 64 {
		t.Fatal(at, err)
	}
	// The last chunk, shown again at the place after the last. A tree that
	// fills up a level by repeating its last leaf would take this.
	carried := oracleFileLeaves(items).carried(4, 5)
	for _, place := range [][2]uint64{{5, 5}, {6, 5}, {6, 4}, {4, 4}, {8, 7}} {
		forged := oracleFileBranchFrameAs(place[0], place[1], items[4:], carried)
		if _, err := check(file, forged); err == nil {
			t.Fatalf("the last chunk was taken as one of %d chunks, at %d", place[0], place[1])
		}
	}
	// A file of six chunks whose sixth is the fifth again is another file.
	six := append(slices.Clone(items), items[4])
	if oracleFileHash(six).hash == hashed.hash || oracleFileLeaves(six).top().hash == oracleFileLeaves(items).top().hash {
		t.Fatal("a file with its last chunk twice hashes as the file")
	}
	for _, part := range [][2]int{{0, 6}, {5, 6}, {4, 5}} {
		if _, err := check(file, oracleFileBranchFrame(six, part[0], part[1])); err == nil {
			t.Fatalf("%v of a file with its last chunk twice was taken", part)
		}
	}
	// A fork passed off as a chunk: a file of two "chunks" that are the
	// two forks under the top of a file of four.
	four := items[:4]
	leaves := oracleFileLeaves(four)
	fourHashed := oracleFileHash(four)
	fourFile := Target{Kind: KindFile, Hash: fourHashed.hash, Size: fourHashed.size}
	var forks []oracleItem
	for _, fork := range []oracleNode{leaves.node(0, 2), leaves.node(2, 2)} {
		forks = append(forks, oracleItem{hash: fork.hash, length: uint32(fork.size)})
	}
	if _, err := check(fourFile, oracleFileBranchFrame(forks, 0, 2)); err == nil {
		t.Fatal("two forks were taken for two chunks")
	}
	if oracleFileHash(forks).hash == fourHashed.hash {
		t.Fatal("a file of two forks hashes as the file of four chunks")
	}
	// And a leaf passed off as a fork: the top of a file of one chunk is
	// never the top of a file of two.
	if oracleFileLeaves(items[:1]).top().hash == oracleFileLeaves(items[:2]).top().hash {
		t.Fatal("a leaf hashes as a fork")
	}
	// The library and the rule written a second time agree on what the
	// honest file is.
	built := must[*FileList](t)(DecodeFileList(oracleFileListBytes(items)))
	if built.Hash() != hashed.hash || built.Size() != hashed.size {
		t.Fatal("the library hashes a file otherwise than the rule")
	}
}

func TestATreeThatHoldsMoreThanItsRecordSaysIsStopped(t *testing.T) {
	source, root := sampleTree(t)
	record := must[*Record](t)(RecordOf(source, root))
	right := record.Totals()
	for _, wrong := range []struct {
		field  string
		totals Totals
	}{
		{"chunks", Totals{right.Chunks - 1, right.ChunkBytes, right.ListBytes}},
		{"chunk bytes", Totals{right.Chunks, right.ChunkBytes - 16, right.ListBytes}},
		{"list bytes", Totals{right.Chunks, right.ChunkBytes, right.ListBytes - 1}},
	} {
		forged := must[*Record](t)(NewRecord(record.Top(), wrong.totals, record.Chunking(), ExtraData{}))
		forgedRoot := forged.Root()
		source.Put(StoredRecord, forgedRoot.Hash(), forged.Encode())
		dest := NewMemoryStore()
		_, err := Fetch(dest, forgedRoot, newTestPeer(source))
		var told *Error
		if !errors.Is(err, ErrTotalsMismatch) || !errors.As(err, &told) || told.What != wrong.field {
			t.Fatalf("%+v: %v", wrong.totals, err)
		}
		assertClean(t, dest)
	}
}

func TestATreeThatIsTooDeepIsRefusedHoweverItArrives(t *testing.T) {
	source := NewMemoryStore()
	builder := NewBuilder(source, smallChunks(t))
	file := must[Target](t)(builder.FileFromBytes([]byte("at the bottom")))
	inner := must[Target](t)(builder.Folder([]Entry{plainEntry(t, "file", file)}))
	for range 127 {
		inner = must[Target](t)(builder.Folder([]Entry{plainEntry(t, "d", inner)}))
	}
	deepestAllowed := must[Root](t)(builder.Finish(inner, ExtraData{}))
	totals := must[*Summary](t)(Verify(source, deepestAllowed)).Record.Totals()
	// One folder more, and a record for it written without the builder.
	over := must[*FolderList](t)(NewFolderList([]Entry{plainEntry(t, "d", inner)}))
	source.Put(StoredFolderList, over.Hash(), over.Encode())
	totals.ListBytes += over.EncodedLen()
	forged := must[*Record](t)(NewRecord(over.Target(), totals, smallChunks(t), ExtraData{}))
	forgedRoot := forged.Root()
	source.Put(StoredRecord, forgedRoot.Hash(), forged.Encode())
	tooDeep := func(err error) bool {
		var told *Error
		return errors.Is(err, ErrTooDeep) && errors.As(err, &told) && told.Limit == 128
	}
	if _, err := Verify(source, forgedRoot); !tooDeep(err) {
		t.Fatal(err)
	}
	if _, err := Fetch(NewMemoryStore(), forgedRoot, newTestPeer(source)); !tooDeep(err) {
		t.Fatal(err)
	}
	var bundle bytes.Buffer
	if err := Export(source, forgedRoot, &bundle); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(NewMemoryStore(), forgedRoot, &bundle); !tooDeep(err) {
		t.Fatal(err)
	}
	never := filepath.Join(t.TempDir(), "scionic-never-written")
	if err := WriteToDisk(source, forgedRoot, never); !tooDeep(err) {
		t.Fatal(err)
	}
	if _, _, err := Lookup(source, forgedRoot, strings.Repeat("d/", 129)+"d"); !tooDeep(err) {
		t.Fatal(err)
	}
	// The deepest tree that a tree may be is taken, and written out.
	if _, err := Fetch(NewMemoryStore(), deepestAllowed, newTestPeer(source)); err != nil {
		t.Fatal(err)
	}
	if _, found, err := Lookup(source, deepestAllowed, strings.Repeat("d/", 127)+"file"); err != nil || !found {
		t.Fatal(found, err)
	}
}

func TestMalformedInputIsAnErrorAndNeverACrash(t *testing.T) {
	source, root := sampleTree(t)
	top := topOfTree(t, source, root)
	model := targetAt(t, source, root, "weights/model.bin")
	weights := targetAt(t, source, root, "weights")
	record := keptIn(t, source, StoredRecord, root.Hash())
	honest := [][]byte{
		record,
		oracleFrame(byte(FrameFolderList), keptIn(t, source, StoredFolderList, top.Hash)),
		oracleFrame(byte(FrameFileList), keptIn(t, source, StoredFileList, model.Hash)),
		oracleFrame(byte(FrameChunk), []byte("a small file")),
		oracleFrame(byte(FrameFileBranch), must[*FileBranch](t)(fileListOf(t, source, model).Open(3, 9)).Encode()),
		oracleFrame(byte(FrameFolderBranch), must[*FolderBranch](t)(folderListOf(t, source, weights).Open(1, 2)).Encode()),
		oracleFrame(0x26, []byte{0, 0, 0, 0, 0, 0, 0, 9, 0xff, 0x80}),
		{0x27, 1},
	}
	if FrameKind(honest[0][0]) != FrameRecord {
		t.Fatal("a record does not start as a record does")
	}
	// Every one of them reads, as it is.
	for _, frame := range honest {
		if _, err := DecodeFrame(frame); err != nil {
			t.Fatalf("%x: %v", frame[:1], err)
		}
	}
	// Every way of chunking a frame short, and every single byte of it
	// changed in three ways.
	var forged [][]byte
	for _, frame := range honest {
		step := max(len(frame)/400, 1)
		for cut := 0; cut < len(frame); cut += step {
			forged = append(forged, bytes.Clone(frame[:cut]))
		}
		for at := 0; at < len(frame); at += step {
			for _, change := range []byte{1, 0x80, 0xff} {
				changed := bytes.Clone(frame)
				changed[at] ^= change
				forged = append(forged, changed)
			}
		}
		forged = append(forged, joined(frame, []byte{0}))
	}
	// And bytes that are nothing at all.
	for _, length := range []int{1, 2, 20, 36, 37, 45, 66, 100, 500} {
		for seed := range 8 {
			for _, tag := range []byte{0x20, 0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x30, 0x33, 0x36, 0xff} {
				noise := corpusBytes(fmt.Sprintf("noise-%d", seed), length)
				noise[0] = tag
				forged = append(forged, noise)
			}
		}
	}
	if len(forged) <= 5000 {
		t.Fatalf("only %d inputs were made", len(forged))
	}
	responder := NewResponder(source)
	dest := NewMemoryStore()
	if _, err := must[*Intake](t)(NewIntake(dest, root)).Accept(record); err != nil {
		t.Fatal(err)
	}
	nothing := func([]byte) error { return nil }
	for _, frame := range forged {
		// None of these may panic, whatever they are given. What they
		// answer is held to account by the tests beside this one.
		DecodeFrame(frame)
		DecodeRequest(frame)
		DecodeRecord(frame)
		DecodeFileList(frame)
		DecodeFolderList(frame)
		DecodeOpenFile(frame)
		DecodeOpenFolder(frame)
		DecodeHaveMap(frame)
		if branch, err := DecodeFileBranch(frame); err == nil {
			branch.Check(model)
			branch.CheckWithChunking(model, smallChunks(t))
		}
		if branch, err := DecodeFolderBranch(frame); err == nil {
			branch.Check(weights)
			branch.CheckName(weights, "model.bin")
		}
		ParseRoot(string(frame))
		ChunkingFromTag(frame)
		CheckName(string(frame))
		responder.Answer(frame, nothing)
		must[*Intake](t)(NewIntake(dest, root)).Accept(frame)
		bundle := joined([]byte(BundleMagic), withLength(frame))
		Import(NewMemoryStore(), root, bytes.NewReader(bundle))
		Import(NewMemoryStore(), root, bytes.NewReader(frame))
		ReadFrame(bytes.NewReader(frame), 1<<20)
		Serve(source, bytes.NewReader(frame), &bytes.Buffer{})
		Serve(source, bytes.NewReader(withLength(frame)), &bytes.Buffer{})
	}
	// Whatever got through was a true part of the tree.
	assertOnlyFrom(t, dest, source)
	if _, err := Verify(source, root); err != nil {
		t.Fatal(err)
	}
}
