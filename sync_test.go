package scionic

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Syncing by file hash and chunk number: a whole tree into a store, parts
// of a tree straight from a peer, and a tree as one stream.

func TestATreeIsFetchedWholeIntoAnEmptyStore(t *testing.T) {
	source, root := sampleTree(t)
	dest := NewMemoryStore()
	peer := newTestPeer(source)
	summary := must[*Summary](t)(Fetch(dest, root, peer))
	if !sameSummary(summary, must[*Summary](t)(Verify(dest, root))) || !sameStores(dest, source) {
		t.Fatal("what was fetched is not what was held")
	}
	// Each thing came once: the record, 5 folders, 6 files, 76 chunks.
	got := []int{peer.wire.framesOf(FrameRecord), peer.wire.framesOf(FrameFolderList), peer.wire.framesOf(FrameFileList), peer.wire.framesOf(FrameChunk), len(peer.wire.frames)}
	if !slices.Equal(got, []int{1, 5, 6, 76, 88}) || summary.Folders != 5 || summary.Files != 6 {
		t.Fatalf("the frames that came: %v", got)
	}
	// From the top down. The record is asked for first, a list only once
	// the list that names it is in, and chunks only by their file's hash
	// and their numbers in it.
	known := []Hash{root.Hash()}
	askedChunks := uint32(0)
	for _, request := range peer.wire.requests {
		asked := must[Request](t)(DecodeRequest(request))
		if asked.Kind != AskRecord && !slices.Contains(known, asked.Hash) {
			t.Fatalf("%v was asked for before anything named it", asked.Hash)
		}
		switch asked.Kind {
		case AskRecord:
			if asked.Hash != root.Hash() {
				t.Fatal("another record was asked for")
			}
		case AskFolder:
			list := must[*FolderList](t)(DecodeFolderList(keptIn(t, dest, StoredFolderList, asked.Hash)))
			for _, entry := range list.Entries() {
				known = append(known, entry.Hash())
			}
		case AskFile:
		case AskChunks:
			askedChunks += asked.Count
		default:
			t.Fatalf("a whole tree is not fetched by %#x", byte(asked.Kind))
		}
		if len(known) == 1 {
			known = append(known, topOfTree(t, dest, root).Hash)
		}
	}
	if askedChunks != 76 {
		t.Fatalf("%d chunks were asked for", askedChunks)
	}
}

func TestChunksAreAskedForByTheirFileAndTheirNumbers(t *testing.T) {
	source, root := sampleTree(t)
	peer := newTestPeer(source)
	must[*Summary](t)(Fetch(NewMemoryStore(), root, peer))
	model := targetAt(t, source, root, "weights/model.bin")
	// The file has 63 chunks, and two names. All of its chunks are asked
	// for once, in one request of 45 bytes.
	var forModel [][]byte
	for _, request := range peer.wire.requests {
		if asked := must[Request](t)(DecodeRequest(request)); asked.Kind == AskChunks && asked.Hash == model.Hash {
			forModel = append(forModel, request)
		}
	}
	if len(forModel) != 1 || len(forModel[0]) != 45 {
		t.Fatalf("the file's chunks were asked for in %d requests", len(forModel))
	}
	if asked := must[Request](t)(DecodeRequest(forModel[0])); asked != RequestChunks(model.Hash, 0, 63) {
		t.Fatalf("%+v", asked)
	}
}

func TestWhatAReceiverHoldsIsNotAskedForAgain(t *testing.T) {
	folder := writeTree(t, sampleFiles())
	options := BuildOptions{Chunking: smallChunks(t)}
	source := NewMemoryStore()
	first := must[Root](t)(Build(source, folder, options))
	dest := NewMemoryStore()
	must[*Summary](t)(Fetch(dest, first, newTestPeer(source)))
	// A second tree: one chunk of one file changed, a file that is there
	// already under another name, and one new file of two chunks.
	model := corpusBytes("model", 1000)
	copy(model[320:336], corpusBytes("changed", 16))
	for name, content := range map[string][]byte{
		"weights/model.bin":   model,
		"weights/a-again.bin": corpusBytes("shard-a", 100),
		"new.txt":             corpusBytes("new", 20),
	} {
		if err := os.WriteFile(filepath.Join(folder, filepath.FromSlash(name)), content, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	second := must[Root](t)(Build(source, folder, options))
	peer := newTestPeer(source)
	must[*Summary](t)(Fetch(dest, second, peer))
	// The record, the two folders on the way to the changes, the lists of
	// the changed file and the new one, and three chunks.
	got := []int{peer.wire.framesOf(FrameRecord), peer.wire.framesOf(FrameFolderList), peer.wire.framesOf(FrameFileList), peer.wire.framesOf(FrameChunk)}
	if !slices.Equal(got, []int{1, 2, 2, 3}) {
		t.Fatalf("the frames that came: %v", got)
	}
	// The one changed chunk of the file was asked for by its number.
	changed := targetAt(t, source, second, "weights/model.bin")
	if !slices.ContainsFunc(peer.wire.requests, func(request []byte) bool {
		return bytes.Equal(request, RequestChunks(changed.Hash, 20, 1).Encode())
	}) {
		t.Fatal("the changed chunk was not asked for by its number")
	}
	must[*Summary](t)(Verify(dest, first))
	must[*Summary](t)(Verify(dest, second))
	// A tree that is held whole is not asked about at all.
	idle := newTestPeer(source)
	must[*Summary](t)(Fetch(dest, second, idle))
	if len(idle.wire.requests) != 0 {
		t.Fatalf("%d requests were sent for a tree that is held whole", len(idle.wire.requests))
	}
}

func TestChunksHeldFromAnywhereElseAreSkippedByTheirHash(t *testing.T) {
	source, root := sampleTree(t)
	model := targetAt(t, source, root, "weights/model.bin")
	dest := NewMemoryStore()
	// The receiver has the file's bytes from somewhere else, as bare
	// chunks.
	for _, item := range fileListOf(t, source, model).Items() {
		dest.Put(StoredChunk, item.Hash, keptIn(t, source, StoredChunk, item.Hash))
	}
	peer := newTestPeer(source)
	must[*Summary](t)(Fetch(dest, root, peer))
	if got := peer.wire.framesOf(FrameChunk); got != 76-63 {
		t.Fatalf("%d chunks came", got)
	}
	must[*Summary](t)(Verify(dest, root))
}

func TestAFetchThatStopsGoesOnFromWhatIsHeld(t *testing.T) {
	source, root := sampleTree(t)
	dest := NewMemoryStore()
	// A peer that goes silent after 30 frames.
	left := 30
	flaky := tamperingPeer(source, func(_ []byte, frames [][]byte) [][]byte {
		keep := min(len(frames), left)
		left -= keep
		return frames[:keep]
	})
	if _, err := Fetch(dest, root, flaky); !errors.Is(err, ErrPeer) || len(flaky.wire.frames) != 30 {
		t.Fatalf("%d frames came (%v)", len(flaky.wire.frames), err)
	}
	if _, err := Verify(dest, root); !errors.Is(err, ErrMissing) {
		t.Fatal(err)
	}
	peer := newTestPeer(source)
	must[*Summary](t)(Fetch(dest, root, peer))
	if len(peer.wire.frames) != 88-30 || peer.wire.framesOf(FrameRecord) != 0 {
		t.Fatalf("%d frames came, and nothing may come twice", len(peer.wire.frames))
	}
	must[*Summary](t)(Verify(dest, root))
}

func TestOneNameIsLookedUpWithoutTheFolder(t *testing.T) {
	source, root := sampleTree(t)
	peer := newTestPeer(source)
	remote := must[*Remote](t)(OpenRemote(peer, root))
	if !bytes.Equal(remote.Record().Encode(), must[*Record](t)(RecordOf(source, root)).Encode()) {
		t.Fatal("the record that was read is not the tree's")
	}
	for _, path := range []string{"readme.txt", "weights", "weights/model.bin", "weights/shards/a.bin", "nothing-here"} {
		shown, found, err := remote.Lookup(path)
		held, there, heldErr := Lookup(source, root, path)
		if err != nil || heldErr != nil || !found || !there || !shown.Equal(held) {
			t.Fatalf("%s: %v %v %v %v", path, found, there, err, heldErr)
		}
	}
	for _, path := range []string{"a", "zzz", "readme.txs", "weights/shards/aa.bin", "weights/model.bin/x", "nothing-here/x", "Weights"} {
		if _, found, err := remote.Lookup(path); err != nil || found {
			t.Fatalf("%s: %v %v", path, found, err)
		}
		if _, found, err := Lookup(source, root, path); err != nil || found {
			t.Fatalf("%s: %v %v", path, found, err)
		}
	}
	if _, _, err := remote.Lookup("weights/../readme.txt"); err == nil {
		t.Fatal("a path with two dots in it was looked up")
	}
	// The record, and one small frame for each name on the way: no folder's
	// whole list, no file's list, and no chunk.
	if peer.wire.framesOf(FrameRecord) != 1 || peer.wire.framesOf(FrameFolderBranch) != len(peer.wire.frames)-1 {
		t.Fatal("something else than one entry for each name came")
	}
	for _, frame := range peer.wire.frames {
		if len(frame) >= 600 {
			t.Fatalf("a frame of %d bytes came", len(frame))
		}
	}
}

func TestAStretchOfAFileIsReadWithoutTheRest(t *testing.T) {
	source, root := sampleTree(t)
	model := targetAt(t, source, root, "weights/model.bin")
	content := corpusBytes("model", 1000)
	peer := newTestPeer(source)
	remote := must[*Remote](t)(OpenRemote(peer, root))
	var out bytes.Buffer
	if err := remote.Read(model, 100, 333, &out); err != nil || !bytes.Equal(out.Bytes(), content[100:433]) {
		t.Fatal(err)
	}
	// Bytes 100 to 432 lie in chunks 6 to 27. The record, one branch of the
	// file's list, and those 22 chunks came, and nothing else.
	got := []int{peer.wire.framesOf(FrameFileBranch), peer.wire.framesOf(FrameChunk), len(peer.wire.frames)}
	if !slices.Equal(got, []int{1, 22, 24}) {
		t.Fatalf("the frames that came: %v", got)
	}
	if !slices.ContainsFunc(peer.wire.requests, func(request []byte) bool {
		return bytes.Equal(request, RequestChunks(model.Hash, 6, 22).Encode())
	}) {
		t.Fatal("the chunks were not asked for by their numbers")
	}
	remote = must[*Remote](t)(OpenRemote(newTestPeer(source), root))
	for _, stretch := range [][2]uint64{{0, 1}, {0, 16}, {15, 2}, {16, 16}, {999, 1}, {992, 8}, {0, 1000}, {500, 0}, {1000, 0}} {
		out.Reset()
		if err := remote.Read(model, stretch[0], stretch[1], &out); err != nil || !bytes.Equal(out.Bytes(), content[stretch[0]:stretch[0]+stretch[1]]) {
			t.Fatalf("%v: %v", stretch, err)
		}
	}
	out.Reset()
	weights := targetAt(t, source, root, "weights")
	if remote.Read(model, 0, 1001, &out) == nil || remote.Read(model, 1001, 0, &out) == nil || remote.Read(weights, 0, 1, &out) == nil || out.Len() != 0 {
		t.Fatal("a stretch outside the file, or of a folder, was read")
	}
	if err := remote.Read(model, ^uint64(0), 2, &out); err == nil {
		t.Fatal("a stretch that wraps around was read")
	}
}

func TestEveryStretchOfASmallFileReadsBackFromAPeer(t *testing.T) {
	source, root := sampleTree(t)
	shard := targetAt(t, source, root, "weights/shards/a.bin")
	content := corpusBytes("shard-a", 100)
	remote := must[*Remote](t)(OpenRemote(newTestPeer(source), root))
	var out bytes.Buffer
	for offset := 0; offset < 100; offset++ {
		for length := 1; length <= 100-offset; length++ {
			out.Reset()
			if err := remote.Read(shard, uint64(offset), uint64(length), &out); err != nil || !bytes.Equal(out.Bytes(), content[offset:offset+length]) {
				t.Fatalf("%d+%d: %v", offset, length, err)
			}
		}
	}
}

func TestABranchOfAFoldersEntriesIsListed(t *testing.T) {
	source, root := sampleTree(t)
	weights := targetAt(t, source, root, "weights")
	nothing := targetAt(t, source, root, "nothing-here")
	model := targetAt(t, source, root, "weights/model.bin")
	all := folderListOf(t, source, weights)
	if all.Count() != 3 {
		t.Fatal(all.Count())
	}
	remote := must[*Remote](t)(OpenRemote(newTestPeer(source), root))
	listed := must[*Entries](t)(remote.Entries(weights, 0, 10))
	if listed.Total != 3 || listed.Start != 0 || !slices.EqualFunc(listed.Entries, all.Entries(), Entry.Equal) {
		t.Fatalf("%+v", listed)
	}
	one := must[*Entries](t)(remote.Entries(weights, 1, 1))
	if one.Total != 3 || one.Start != 1 || len(one.Entries) != 1 || one.Entries[0].Name() != "model.bin" || !one.Entries[0].Equal(all.Entries()[1]) {
		t.Fatalf("%+v", one)
	}
	if _, err := remote.Entries(weights, 3, 1); !errors.Is(err, ErrPeer) {
		t.Fatal(err)
	}
	empty := must[*Entries](t)(remote.Entries(nothing, 0, 5))
	if empty.Total != 0 || len(empty.Entries) != 0 {
		t.Fatalf("%+v", empty)
	}
	if _, err := remote.Entries(model, 0, 1); err == nil {
		t.Fatal("a file's entries were listed")
	}
	if _, _, err := remote.Entry(model, "x"); err == nil {
		t.Fatal("a name was looked up in a file")
	}
	if _, found, err := remote.Entry(nothing, "x"); err != nil || found {
		t.Fatal(found, err)
	}
}

func TestAPeerSaysWhichChunksItHolds(t *testing.T) {
	source, root := sampleTree(t)
	model := targetAt(t, source, root, "weights/model.bin")
	list := fileListOf(t, source, model)
	for _, gone := range []int{3, 7, 62} {
		source.Remove(StoredChunk, list.Items()[gone].Hash)
	}
	remote := must[*Remote](t)(OpenRemote(newTestPeer(source), root))
	held := must[*HaveMap](t)(remote.Have(model))
	if held.Count() != 63 || held.Held() != 60 {
		t.Fatal(held.Count(), held.Held())
	}
	var missing []uint64
	for number := range uint64(63) {
		if !held.Get(number) {
			missing = append(missing, number)
		}
	}
	// 63 bits take 8 bytes, after the count.
	if !slices.Equal(missing, []uint64{3, 7, 62}) || len(held.Encode()) != 16 {
		t.Fatal(missing, len(held.Encode()))
	}
	// A responder that is told not to say answers as for a file it does
	// not hold.
	quiet := NewResponder(source).Have(false)
	if _, err := must[*Remote](t)(OpenRemote(quiet, root)).Have(model); !errors.Is(err, ErrPeer) {
		t.Fatal(err)
	}
}

func TestATreeGoesThroughABundleAndBack(t *testing.T) {
	source, root := sampleTree(t)
	var exported bytes.Buffer
	if err := Export(source, root, &exported); err != nil {
		t.Fatal(err)
	}
	bundle := exported.Bytes()
	if string(bundle[:8]) != BundleMagic {
		t.Fatal("a bundle does not start as a bundle does")
	}
	// Each thing once: 88 frames, each with four bytes for its length, and
	// all but the record with one byte for its kind.
	var kept uint64
	for _, kind := range []Stored{StoredChunk, StoredFileList, StoredFolderList, StoredRecord} {
		kept += source.Bytes(kind)
	}
	if uint64(len(bundle)) != 8+kept+88*4+87 {
		t.Fatalf("the bundle holds %d bytes", len(bundle))
	}
	dest := NewMemoryStore()
	summary := must[*Summary](t)(Import(dest, root, bytes.NewReader(bundle)))
	if !sameSummary(summary, must[*Summary](t)(Verify(dest, root))) || !sameStores(dest, source) {
		t.Fatal("what was taken from the bundle is not what was put into it")
	}
	// Into a store that holds it all already: nothing is kept twice.
	must[*Summary](t)(Import(dest, root, bytes.NewReader(bundle)))
	if !sameStores(dest, source) {
		t.Fatal("a second import changed the store")
	}
	// A bundle as a file on disk.
	path := filepath.Join(t.TempDir(), "tree.bundle")
	file := must[*os.File](t)(os.Create(path))
	if err := Export(source, root, file); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	file = must[*os.File](t)(os.Open(path))
	defer file.Close()
	fromDisk := NewMemoryStore()
	must[*Summary](t)(Import(fromDisk, root, file))
	if !sameStores(fromDisk, source) {
		t.Fatal("a bundle on disk gave another tree")
	}
	// A bundle that stops early leaves a tree that is not whole, and says
	// so.
	for _, cut := range []int{0, 7, 8, 9, 200, len(bundle) - 1} {
		if _, err := Import(NewMemoryStore(), root, bytes.NewReader(bundle[:cut])); err == nil {
			t.Fatalf("a bundle that was cut at %d was taken", cut)
		}
	}
	// A stream that is no bundle.
	other := bytes.Clone(bundle)
	other[0] ^= 1
	if _, err := Import(NewMemoryStore(), root, bytes.NewReader(other)); err == nil {
		t.Fatal("a stream that is no bundle was taken")
	}
	// A bundle of one tree is not taken for another.
	wrong := RootOfRecord([]byte("another tree"))
	if _, err := Import(NewMemoryStore(), wrong, bytes.NewReader(bundle)); !errors.Is(err, ErrHashMismatch) {
		t.Fatal(err)
	}
}

func TestAResponderSaysWhatItDoesNotHoldAndWhatLiesOutside(t *testing.T) {
	source, root := sampleTree(t)
	model := targetAt(t, source, root, "weights/model.bin")
	weights := targetAt(t, source, root, "weights")
	unknown := HashOf([]byte("nobody holds this"))
	responder := NewResponder(source)
	answer := func(request Request) []Frame {
		t.Helper()
		var frames []Frame
		err := responder.Answer(request.Encode(), func(frame []byte) error {
			frames = append(frames, must[Frame](t)(DecodeFrame(bytes.Clone(frame))))
			return nil
		})
		if err != nil {
			t.Fatalf("%+v: %v", request, err)
		}
		return frames
	}
	refused := func(request Request, why Refusal) {
		t.Helper()
		frames := answer(request)
		if len(frames) != 1 || frames[0].Kind != FrameRefused || frames[0].Refusal != why {
			t.Fatalf("%+v was answered with %+v", request, frames)
		}
	}
	for _, request := range []Request{
		RequestRecord(Root(unknown)),
		RequestFolder(unknown),
		RequestFile(unknown),
		RequestChunk(unknown),
		RequestHave(unknown),
		RequestFolderBranch(unknown, 0, 1),
		RequestFolderName(unknown, "a"),
		RequestFileBranch(unknown, 0, 1),
		RequestFileBytes(unknown, 0, 1),
		RequestChunks(unknown, 0, 1),
		// A folder is not found as a file, nor a file as a folder.
		RequestFile(weights.Hash),
		RequestFolder(model.Hash),
	} {
		refused(request, RefusalNotHeld)
	}
	for _, request := range []Request{
		RequestFolderBranch(weights.Hash, 3, 1),
		RequestFileBranch(model.Hash, 63, 1),
		RequestFileBytes(model.Hash, 1000, 1),
		RequestChunks(model.Hash, 63, 1),
	} {
		refused(request, RefusalOutOfRange)
	}
	// What reaches past the end is cut off at the end.
	if frames := answer(RequestChunks(model.Hash, 60, 10)); len(frames) != 3 {
		t.Fatalf("%d chunks came", len(frames))
	}
	frames := answer(RequestFileBytes(model.Hash, 990, 10_000))
	if len(frames) != 1 || frames[0].Kind != FrameFileBranch {
		t.Fatalf("%+v", frames)
	}
	branch := frames[0].FileBranch
	if at, err := branch.Check(model); err != nil || at != 976 || branch.Start() != 61 || len(branch.Items()) != 2 {
		t.Fatal(at, err, branch.Start(), len(branch.Items()))
	}
	// Chunks come in order, up to the first one that is not held.
	source.Remove(StoredChunk, fileListOf(t, source, model).Items()[12].Hash)
	frames = answer(RequestChunks(model.Hash, 10, 5))
	if len(frames) != 3 || frames[0].Kind != FrameChunk || frames[1].Kind != FrameChunk ||
		frames[2].Kind != FrameRefused || frames[2].Refusal != RefusalNotHeld {
		t.Fatalf("%+v", frames)
	}
	// A request that is malformed is an error, and gets no frame.
	came := 0
	for _, request := range [][]byte{nil, {0x30}, bytes.Repeat([]byte{0x3a}, 33), bytes.Repeat([]byte{0x37}, 44)} {
		err := responder.Answer(request, func([]byte) error {
			came++
			return nil
		})
		if err == nil {
			t.Fatalf("%x was answered", request)
		}
	}
	if came != 0 || weights.Kind != KindFolder {
		t.Fatalf("%d frames came for requests that are malformed", came)
	}
}
