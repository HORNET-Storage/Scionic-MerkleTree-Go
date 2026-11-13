package scionic

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Building a tree, and what the format promises about it: the same things
// have the same hashes wherever they are, and are stored once.

// writeTreeAt writes files under a folder: each name may hold slashes, and
// a name that ends in a slash is an empty folder.
func writeTreeAt(t *testing.T, top string, files map[string][]byte) string {
	t.Helper()
	if err := os.MkdirAll(top, 0o777); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if strings.HasSuffix(name, "/") {
			if err := os.MkdirAll(filepath.Join(top, filepath.FromSlash(name)), 0o777); err != nil {
				t.Fatal(err)
			}
			continue
		}
		writeFiles(t, top, map[string][]byte{name: content})
	}
	return top
}

func smallOptions(t *testing.T) BuildOptions {
	return BuildOptions{Chunking: smallChunks(t)}
}

func TestAFolderHasTheSameRootWhereverItIsAndWhateverItIsCalled(t *testing.T) {
	temp := t.TempDir()
	first := writeTreeAt(t, filepath.Join(temp, "one"), sampleFiles())
	second := writeTreeAt(t, filepath.Join(temp, "deeper", "still", "another-name"), sampleFiles())
	storeA, storeB := NewMemoryStore(), NewMemoryStore()
	rootA := must[Root](t)(Build(storeA, first, smallOptions(t)))
	rootB := must[Root](t)(Build(storeB, second, smallOptions(t)))
	if rootA != rootB || !sameStores(storeA, storeB) {
		t.Fatal("the same folder at another place, under another name, is another tree")
	}
	// The same folder, named by another path to it.
	separator := string(os.PathSeparator)
	roundabout := first + separator + "weights" + separator + ".."
	if root := must[Root](t)(Build(NewMemoryStore(), roundabout, smallOptions(t))); root != rootA {
		t.Fatal("the same folder by another path to it is another tree")
	}
	if text := rootA.String(); !strings.HasPrefix(text, "bafkrei") || len(text) != 59 {
		t.Fatalf("the root is written %s", text)
	}
}

func TestTheRootIsWhatTheFormatSaysItIs(t *testing.T) {
	a, b := []byte("hello"), corpusBytes("b", 40)
	top := writeTree(t, map[string][]byte{"a.txt": a, "sub/b.bin": b, "sub/empty": nil})
	store := NewMemoryStore()
	root := must[Root](t)(Build(store, top, smallOptions(t)))
	// The same tree by the rules, written out by hand.
	aItems := []oracleItem{oracleItemOf(a)}
	bItems := []oracleItem{oracleItemOf(b[:16]), oracleItemOf(b[16:32]), oracleItemOf(b[32:])}
	aFile, bFile, emptyFile := oracleFileHash(aItems), oracleFileHash(bItems), oracleFileHash(nil)
	sub := []oracleEntry{oracleFile("b.bin", bFile.hash, bFile.size), oracleFile("empty", emptyFile.hash, 0)}
	subFolder := oracleFolderHash(sub)
	tree := []oracleEntry{oracleFile("a.txt", aFile.hash, aFile.size), oracleFolder("sub", subFolder.hash, subFolder.size)}
	topFolder := oracleFolderHash(tree)
	if aFile.size != 5 || bFile.size != 40 || subFolder.size != 40 || topFolder.size != 45 {
		t.Fatal("the sizes by the rules are not the sizes of what was written")
	}
	listBytes := uint64(36 + 3*36 + len(oracleFolderListBytes(sub)) + len(oracleFolderListBytes(tree)))
	record := oracleRecordBytes(2, 45, topFolder.hash, [3]uint64{4, 45, listBytes}, oracleChunkingTag("fixed-16"))
	if root.Hash() != oracleSHA(record) {
		t.Fatalf("the root is %x, and by the rules it is %x", root.Hash(), oracleSHA(record))
	}
	for _, want := range []struct {
		what    string
		kind    Stored
		hash    oracleHash
		content []byte
	}{
		{"the record", StoredRecord, root.Hash(), record},
		{"the top folder's list", StoredFolderList, topFolder.hash, oracleFolderListBytes(tree)},
		{"the folder's list", StoredFolderList, subFolder.hash, oracleFolderListBytes(sub)},
		{"a file's list", StoredFileList, bFile.hash, oracleFileListBytes(bItems)},
		{"the empty file's list", StoredFileList, emptyFile.hash, nil},
		{"a file's last chunk", StoredChunk, bItems[2].hash, b[32:]},
		{"a file's only chunk", StoredChunk, aItems[0].hash, a},
	} {
		kept, held, err := store.Get(want.kind, want.hash)
		if err != nil || !held || !bytes.Equal(kept, want.content) {
			t.Fatalf("%s: held: %v, %x (%v)", want.what, held, kept, err)
		}
	}
	// A chunk's hash is the plain SHA-256 of its bytes.
	if got := HashOf(a).Hex(); got != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Fatal(got)
	}
	// Nothing else is in the store: the record, two folders, three files
	// and four chunks.
	if len(contentsOf(store)) != 1+2+3+4 {
		t.Fatalf("the store holds %d things", len(contentsOf(store)))
	}
}

func TestIdenticalThingsAreStoredOnce(t *testing.T) {
	store, root := sampleTree(t)
	// Two names, one file: the same hash, one list, and its chunks once.
	model, _, _ := Lookup(store, root, "weights/model.bin")
	copied, found, err := Lookup(store, root, "weights/copy-of-model.bin")
	if err != nil || !found || model.Hash() != copied.Hash() || model.Target() != copied.Target() {
		t.Fatal("two names of one file are two files")
	}
	summary := must[*Summary](t)(Verify(store, root))
	totals := summary.Record.Totals()
	counts := []uint64{summary.Files, summary.Folders, uint64(store.Count(StoredFileList)), uint64(store.Count(StoredFolderList)), uint64(store.Count(StoredRecord))}
	if !slices.Equal(counts, []uint64{6, 5, 6, 5, 1}) {
		t.Fatalf("%v", counts)
	}
	// 12, 1,000, 100, 33 and 32 bytes in chunks of 16.
	if totals.Chunks != 1+63+7+3+2 || totals.ChunkBytes != 12+1000+100+33+32 {
		t.Fatalf("%+v", totals)
	}
	if uint64(store.Count(StoredChunk)) != totals.Chunks || store.Bytes(StoredChunk) != totals.ChunkBytes ||
		store.Bytes(StoredFileList)+store.Bytes(StoredFolderList) != totals.ListBytes {
		t.Fatal("the record's totals are not what the store holds")
	}
	// The tree's size counts the file under both of its names.
	if top := summary.Record.Top(); top.Size != totals.ChunkBytes+1000 || top.Kind != KindFolder {
		t.Fatalf("%+v", top)
	}
}

func TestASecondTreeAddsOnlyWhatIsNew(t *testing.T) {
	folder := writeTree(t, sampleFiles())
	store := NewMemoryStore()
	first := must[Root](t)(Build(store, folder, smallOptions(t)))
	count := func() [3]int {
		return [3]int{store.Count(StoredChunk), store.Count(StoredFileList), store.Count(StoredFolderList)}
	}
	before := count()
	// One chunk of one file changes, and another file gets a new name.
	model := corpusBytes("model", 1000)
	copy(model[320:336], corpusBytes("changed", 16))
	writeFiles(t, folder, map[string][]byte{"weights/model.bin": model})
	if err := os.Rename(filepath.Join(folder, "readme.txt"), filepath.Join(folder, "notes.txt")); err != nil {
		t.Fatal(err)
	}
	second := must[Root](t)(Build(store, folder, smallOptions(t)))
	if first == second {
		t.Fatal("a folder that changed has the same root")
	}
	// One new chunk. One new list for the changed file: the renamed file is
	// the same file. Two new folders: the one that holds the change, and
	// the top, which holds both.
	if after := count(); after != [3]int{before[0] + 1, before[1] + 1, before[2] + 2} {
		t.Fatalf("%v became %v", before, after)
	}
	must[*Summary](t)(Verify(store, first))
	must[*Summary](t)(Verify(store, second))
	at := func(root Root, path string) Entry {
		t.Helper()
		entry, found, err := Lookup(store, root, path)
		if err != nil || !found {
			t.Fatalf("%q: %v %v", path, found, err)
		}
		return entry
	}
	if at(second, "notes.txt").Hash() != at(first, "readme.txt").Hash() {
		t.Fatal("a file that got another name is another file")
	}
	if _, found, err := Lookup(store, second, "readme.txt"); err != nil || found {
		t.Fatal(found, err)
	}
	// What did not change has the same hash in both trees.
	for _, path := range []string{"weights/shards", "data", "weights/copy-of-model.bin", "nothing-here"} {
		if !at(first, path).Equal(at(second, path)) {
			t.Fatalf("%s did not change, and is another thing in the second tree", path)
		}
	}
}

func TestAFileThatRepeatsAChunkReadsBackWhole(t *testing.T) {
	store := NewMemoryStore()
	builder := NewBuilder(store, smallChunks(t))
	x, y := corpusBytes("x", 16), corpusBytes("y", 16)
	content := joined(x, y, x)
	file := must[Target](t)(builder.FileFromBytes(content))
	list := fileListOf(t, store, file)
	if store.Count(StoredChunk) != 2 || list.Count() != 3 || list.Items()[0] != list.Items()[2] {
		t.Fatal("a chunk that comes twice is not kept once and listed twice")
	}
	if read := must[[]byte](t)(ReadFileBytes(store, file)); !bytes.Equal(read, content) {
		t.Fatal("the file does not read back")
	}
	// Each of the three places is shown on its own, at its own byte.
	for place := range uint64(3) {
		branch := must[*FileBranch](t)(list.Open(place, place+1))
		if at, err := branch.CheckWithChunking(file, smallChunks(t)); err != nil || at != place*16 {
			t.Fatal(place, at, err)
		}
	}
	root := must[Root](t)(builder.Finish(file, ExtraData{}))
	summary := must[*Summary](t)(Verify(store, root))
	if summary.Record.Top().Size != 48 || summary.Record.Totals() != (Totals{Chunks: 2, ChunkBytes: 32, ListBytes: 108}) {
		t.Fatalf("%+v", summary.Record.Totals())
	}
}

func TestTheSameChunkInTwoFilesIsOneChunk(t *testing.T) {
	store := NewMemoryStore()
	builder := NewBuilder(store, smallChunks(t))
	shared := corpusBytes("shared", 16)
	first := must[Target](t)(builder.FileFromBytes(joined(shared, corpusBytes("first", 10))))
	second := must[Target](t)(builder.FileFromBytes(joined(corpusBytes("second", 16), shared)))
	if first.Hash == second.Hash || store.Count(StoredChunk) != 3 {
		t.Fatal("a chunk that two files share is kept twice")
	}
	top := must[Target](t)(builder.Folder([]Entry{plainEntry(t, "second", second), plainEntry(t, "first", first)}))
	root := must[Root](t)(builder.Finish(top, ExtraData{}))
	if totals := must[*Summary](t)(Verify(store, root)).Record.Totals(); totals.Chunks != 3 || totals.ChunkBytes != 42 {
		t.Fatalf("%+v", totals)
	}
	// The builder sorted the entries by name.
	entries := folderListOf(t, store, top).Entries()
	if len(entries) != 2 || entries[0].Name() != "first" || entries[1].Name() != "second" {
		t.Fatal("the entries are not in the order of their names")
	}
	// Two entries with one name are no folder.
	if _, err := builder.Folder([]Entry{plainEntry(t, "same", first), plainEntry(t, "same", second)}); !errors.Is(err, ErrInvalidData) {
		t.Fatal(err)
	}
}

func TestBuildingWithSeveralWorkersGivesTheSameTree(t *testing.T) {
	files := map[string][]byte{}
	for index := range 40 {
		files[fmt.Sprintf("d%d/f%02d.bin", index%5, index)] = corpusBytes(fmt.Sprintf("file-%d", index%30), index*37)
	}
	top := writeTree(t, files)
	var stores []*MemoryStore
	var roots []Root
	for _, workers := range []int{1, 2, 7, 0} {
		store := NewMemoryStore()
		roots = append(roots, must[Root](t)(Build(store, top, BuildOptions{Chunking: smallChunks(t), Workers: workers})))
		stores = append(stores, store)
	}
	for at, store := range stores {
		if roots[at] != roots[0] || !sameStores(store, stores[0]) {
			t.Fatal("the number of workers changed the tree")
		}
		must[*Summary](t)(Verify(store, roots[at]))
	}
}

func TestFilesAreChunkedByContentUnlessATestSaysOtherwise(t *testing.T) {
	// A case of the committed chunking vectors: 3 MiB, cut after 2,118,489.
	data := corpusBytes("scionic-fastcdc-v1:corpus:random_3mib", 3<<20)
	path := filepath.Join(t.TempDir(), "random.bin")
	if err := os.WriteFile(path, data, 0o666); err != nil {
		t.Fatal(err)
	}
	store := NewMemoryStore()
	root := must[Root](t)(Build(store, path, BuildOptions{}))
	record := must[*Record](t)(RecordOf(store, root))
	if tag, _ := record.Extra().Text(ChunkingKey); record.Chunking() != FastCDCV1 || tag != "fastcdc-v1" || record.Extra().Len() != 1 || record.Top().Kind != KindFile {
		t.Fatalf("the record says the tree was chunked as %q", tag)
	}
	var lengths []uint32
	for _, item := range fileListOf(t, store, record.Top()).Items() {
		lengths = append(lengths, item.Length)
	}
	if !slices.Equal(lengths, []uint32{2_118_489, 3<<20 - 2_118_489}) {
		t.Fatalf("the file was split into %v", lengths)
	}
	must[*Summary](t)(VerifyWith(store, root, VerifyOptions{Rechunk: true}))
	// The same bytes, by the builder that is given bytes: the same file.
	if file := must[Target](t)(NewBuilder(NewMemoryStore(), FastCDCV1).FileFromBytes(data)); file != record.Top() {
		t.Fatal("the same bytes in memory are another file than on disk")
	}
}

func TestTheRecordCarriesWhatTheBuilderWasTold(t *testing.T) {
	folder := writeTree(t, sampleFiles())
	plainStore := NewMemoryStore()
	plain := must[Root](t)(Build(plainStore, folder, smallOptions(t)))
	store := NewMemoryStore()
	extra := must[ExtraData](t)(ExtraData{}.WithText("name", "my model"))
	extra = must[ExtraData](t)(extra.WithText(ChunkingKey, "not for a caller to say"))
	root := must[Root](t)(Build(store, folder, BuildOptions{Chunking: smallChunks(t), Extra: extra, Timestamp: true}))
	if root == plain {
		t.Fatal("a record with more in it has the same root")
	}
	record := must[*Record](t)(RecordOf(store, root))
	name, _ := record.Extra().Text("name")
	tag, _ := record.Extra().Text(ChunkingKey)
	stamp, stamped := record.Extra().Text(TimestampKey)
	if name != "my model" || tag != "fixed-16" {
		t.Fatalf("the record says %q and %q", name, tag)
	}
	if !stamped || len(stamp) != 20 || !strings.HasPrefix(stamp, "20") || !strings.HasSuffix(stamp, "Z") {
		t.Fatalf("the time is written %q", stamp)
	}
	// The folder on top is the same folder: only the record differs.
	if record.Top() != topOfTree(t, plainStore, plain) {
		t.Fatal("what the record says changed the folder under it")
	}
}

func TestEveryEntryCanBeGivenExtraData(t *testing.T) {
	folder := writeTree(t, sampleFiles())
	plainStore := NewMemoryStore()
	plain := must[Root](t)(Build(plainStore, folder, smallOptions(t)))
	store := NewMemoryStore()
	var lock sync.Mutex
	var seen []string
	root := must[Root](t)(BuildWith(store, folder, smallOptions(t), func(path, name string, kind Kind) (ExtraData, error) {
		info, err := os.Lstat(path)
		if err != nil || filepath.Base(path) != name || info.IsDir() != (kind == KindFolder) {
			t.Errorf("%s is given as %q, a %v (%v)", path, name, kind, err)
		}
		lock.Lock()
		seen = append(seen, name)
		lock.Unlock()
		if kind == KindFolder {
			return ExtraData{}.WithText("kind", "d")
		}
		return ExtraData{}.WithText("kind", "f")
	}))
	if root == plain {
		t.Fatal("entries with extra data have the same root")
	}
	slices.Sort(seen)
	want := []string{"a.bin", "b.bin", "copy-of-model.bin", "data", "empty.bin", "exactly-two-chunks.bin", "model.bin", "nothing-here", "readme.txt", "shards", "weights"}
	if !slices.Equal(seen, want) {
		t.Fatalf("%v", seen)
	}
	shards, _, _ := Lookup(store, root, "weights/shards")
	model, _, _ := Lookup(store, root, "weights/model.bin")
	ofFolder, _ := shards.Extra().Text("kind")
	ofFile, _ := model.Extra().Text("kind")
	if ofFolder != "d" || ofFile != "f" {
		t.Fatalf("the entries say %q and %q", ofFolder, ofFile)
	}
	// Extra data sits beside the name: the file itself is the same file.
	plainModel, _, _ := Lookup(plainStore, plain, "weights/model.bin")
	if model.Hash() != plainModel.Hash() {
		t.Fatal("extra data changed the file it is said of")
	}
	must[*Summary](t)(Verify(store, root))
	// An error from the one who gives the extra data ends the build, and is
	// what the build gives.
	noMore := errors.New("no more extra data")
	_, err := BuildWith(NewMemoryStore(), folder, smallOptions(t), func(string, string, Kind) (ExtraData, error) {
		return ExtraData{}, noMore
	})
	if !errors.Is(err, noMore) {
		t.Fatal(err)
	}
}

func TestAFileOnItsOwnIsATree(t *testing.T) {
	temp := t.TempDir()
	data := corpusBytes("alone", 70)
	writeFiles(t, temp, map[string][]byte{"whatever-it-is-called.bin": data, "other-name": data, "empty": nil})
	store := NewMemoryStore()
	root := must[Root](t)(Build(store, filepath.Join(temp, "whatever-it-is-called.bin"), smallOptions(t)))
	summary := must[*Summary](t)(Verify(store, root))
	if summary.Record.Top().Kind != KindFile || summary.Files != 1 || summary.Folders != 0 {
		t.Fatalf("%+v", summary)
	}
	if read := must[[]byte](t)(ReadFileBytes(store, summary.Record.Top())); !bytes.Equal(read, data) {
		t.Fatal("the file does not read back")
	}
	// The same bytes under another name are the same tree.
	if other := must[Root](t)(Build(store, filepath.Join(temp, "other-name"), smallOptions(t))); other != root {
		t.Fatal("the same bytes under another name are another tree")
	}
	// An empty file is a tree too.
	empty := must[Root](t)(Build(store, filepath.Join(temp, "empty"), smallOptions(t)))
	if totals := must[*Summary](t)(Verify(store, empty)).Record.Totals(); totals != (Totals{}) {
		t.Fatalf("%+v", totals)
	}
	// What is not there is no tree.
	if _, err := Build(store, filepath.Join(temp, "absent"), smallOptions(t)); !errors.Is(err, ErrIO) {
		t.Fatal(err)
	}
}

func TestALinkIsNeverFollowed(t *testing.T) {
	temp := t.TempDir()
	writeFiles(t, temp, map[string][]byte{"outside-secret.txt": []byte("not part of the folder"), "folder/inside.txt": []byte("part of the folder")})
	folder := filepath.Join(temp, "folder")
	link := filepath.Join(folder, "link.txt")
	if !linkToFile(link, filepath.Join(temp, "outside-secret.txt")) {
		t.Skip("this account cannot make a link")
	}
	secret := HashOf([]byte("not part of the folder"))
	for _, workers := range []int{1, 4} {
		store := NewMemoryStore()
		_, err := Build(store, folder, BuildOptions{Chunking: smallChunks(t), Workers: workers})
		var told *Error
		if !errors.Is(err, ErrUnsupportedEntry) || !errors.As(err, &told) || told.Path != link || told.What != "a link" {
			t.Fatalf("a link was not refused: %v", err)
		}
		root := must[Root](t)(Build(store, folder, BuildOptions{Chunking: smallChunks(t), Workers: workers, Links: LinksSkip}))
		if _, found, err := Lookup(store, root, "link.txt"); err != nil || found {
			t.Fatal(found, err)
		}
		if _, found, err := Lookup(store, root, "inside.txt"); err != nil || !found {
			t.Fatal(found, err)
		}
		if held, _ := store.Has(StoredChunk, secret); held {
			t.Fatal("what the link leads to was read")
		}
	}
	// A link to a folder is not gone into either.
	if !linkToFolder(filepath.Join(folder, "link-to-folder"), temp) {
		return
	}
	store := NewMemoryStore()
	root := must[Root](t)(Build(store, folder, BuildOptions{Chunking: smallChunks(t), Links: LinksSkip}))
	if summary := must[*Summary](t)(Verify(store, root)); summary.Folders != 1 || summary.Files != 1 {
		t.Fatalf("%+v", summary)
	}
	if held, _ := store.Has(StoredChunk, secret); held {
		t.Fatal("what the link leads to was read")
	}
}

func TestAFolderInside128OthersIsTooDeep(t *testing.T) {
	// Made by hand: a path that long does not fit on every system.
	store := NewMemoryStore()
	builder := NewBuilder(store, smallChunks(t))
	file := must[Target](t)(builder.FileFromBytes([]byte("at the bottom")))
	inner := must[Target](t)(builder.Folder([]Entry{plainEntry(t, "file", file)}))
	for range 127 {
		inner = must[Target](t)(builder.Folder([]Entry{plainEntry(t, "d", inner)}))
	}
	// 128 folders, one inside the other, is the most there can be.
	root := must[Root](t)(builder.Finish(inner, ExtraData{}))
	if folders := must[*Summary](t)(Verify(store, root)).Folders; folders != 128 {
		t.Fatal(folders)
	}
	tooDeep := func(err error) bool {
		var told *Error
		return errors.Is(err, ErrTooDeep) && errors.As(err, &told) && told.Limit == 128
	}
	oneMore := must[Target](t)(builder.Folder([]Entry{plainEntry(t, "d", inner)}))
	recordsBefore := store.Count(StoredRecord)
	if _, err := builder.Finish(oneMore, ExtraData{}); !tooDeep(err) || store.Count(StoredRecord) != recordsBefore {
		t.Fatalf("no record may be kept: %v", err)
	}
	// A folder that is reached twice is measured by its deepest place. Here
	// 100 folders sit one inside the other. The top reaches them directly,
	// under near, and again under far, with more folders on the way.
	hundredDeep := func(wrapped int) (Root, error) {
		builder := NewBuilder(store, smallChunks(t))
		hundred := must[Target](t)(builder.Folder([]Entry{plainEntry(t, "file", file)}))
		for range 99 {
			hundred = must[Target](t)(builder.Folder([]Entry{plainEntry(t, "d", hundred)}))
		}
		far := hundred
		for range wrapped {
			far = must[Target](t)(builder.Folder([]Entry{plainEntry(t, "d", far)}))
		}
		top := must[Target](t)(builder.Folder([]Entry{plainEntry(t, "far", far), plainEntry(t, "near", hundred)}))
		return builder.Finish(top, ExtraData{})
	}
	// The top, 27 folders, and the hundred: 128 on the longest way down.
	must[*Summary](t)(Verify(store, must[Root](t)(hundredDeep(27))))
	// One more on the way, and the hundred sit too deep under far, though
	// they are well within the limit under near.
	if _, err := hundredDeep(28); !tooDeep(err) {
		t.Fatal(err)
	}
}

func TestAFolderOnDiskThatIsTooDeepIsNotBuilt(t *testing.T) {
	// 129 folders, one inside the other, with short names so that the path
	// fits: the folder that is built counts as the first.
	top := filepath.Join(t.TempDir(), "t")
	deepest := top + strings.Repeat(string(os.PathSeparator)+"d", 128)
	if err := os.MkdirAll(deepest, 0o777); err != nil {
		t.Skipf("this system would not make a path that long: %v", err)
	}
	for _, workers := range []int{1, 4} {
		_, err := Build(NewMemoryStore(), top, BuildOptions{Chunking: smallChunks(t), Workers: workers})
		var told *Error
		if !errors.Is(err, ErrTooDeep) || !errors.As(err, &told) || told.Limit != 128 {
			t.Fatalf("%d workers: %v", workers, err)
		}
		if err := movable(top); err != nil {
			t.Fatalf("%d workers: the folder is still held after the build failed: %v", workers, err)
		}
	}
	// One folder less, and it is built.
	if err := os.Remove(deepest); err != nil {
		t.Fatal(err)
	}
	store := NewMemoryStore()
	root := must[Root](t)(Build(store, top, smallOptions(t)))
	if folders := must[*Summary](t)(Verify(store, root)).Folders; folders != 128 {
		t.Fatalf("128 folders, one inside the other, are %d folders of a tree", folders)
	}
}
