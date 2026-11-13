package scionic

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"

	"slices"
	"testing"
)

// The shared vectors: what the Rust, Go and Swift libraries are held to,
// byte for byte.
//
// spec/vectors/manifest.json is made by the Rust library, whose tests fail
// when the file and that library disagree. Here the Go library builds the
// same trees from the same descriptions, and has to arrive at the same
// bytes: every root, record, list, chunk, branch, answer and exchange. And it
// has to refuse every input that the manifest says must be refused.

const vectorsManifestSHA256 = "b10f53a722468ee53c69952f2b9ba1421a02608528be6ae4df753a35d0a5cba8"

type vectorsManifest struct {
	Format     string           `json:"format"`
	Bytes      string           `json:"bytes"`
	Trees      []vectorTree     `json:"trees"`
	Branches   []vectorBranch   `json:"branches"`
	Names      []vectorName     `json:"names"`
	Answers    []vectorAnswer   `json:"answers"`
	Connection vectorConnection `json:"connection"`
	Refused    []vectorRefused  `json:"refused"`
}

// vectorNode describes a file as its parts, or a folder as its entries. One
// of File and Folder is there: an empty file has no parts, and an empty
// folder has no entries.
type vectorNode struct {
	Name   string            `json:"name"`
	Extra  map[string]string `json:"extra"`
	File   *[]vectorPart     `json:"file"`
	Folder *[]vectorNode     `json:"folder"`
}

type vectorPart struct {
	Seed   string `json:"seed"`
	Length int    `json:"length"`
}

type vectorTree struct {
	Name         string            `json:"name"`
	Chunking     string            `json:"chunking"`
	Extra        map[string]string `json:"extra"`
	Top          vectorNode        `json:"top"`
	Root         string            `json:"root"`
	RootHash     string            `json:"root_hash"`
	Record       string            `json:"record"`
	Size         uint64            `json:"size"`
	Chunks       uint64            `json:"chunks"`
	ChunkBytes   uint64            `json:"chunk_bytes"`
	ListBytes    uint64            `json:"list_bytes"`
	Lists        []vectorList      `json:"lists"`
	ChunkList    []vectorChunk     `json:"chunk_list"`
	BundleLength int               `json:"bundle_length"`
	BundleSHA256 string            `json:"bundle_sha256"`
}

type vectorList struct {
	Kind  string `json:"kind"`
	Hash  string `json:"hash"`
	Size  uint64 `json:"size"`
	Bytes string `json:"bytes"`
}

type vectorChunk struct {
	Hash   string `json:"hash"`
	Length uint64 `json:"length"`
}

type vectorTarget struct {
	Kind string `json:"kind"`
	Hash string `json:"hash"`
	Size uint64 `json:"size"`
}

type vectorBranch struct {
	Tree         string       `json:"tree"`
	Path         string       `json:"path"`
	Of           vectorTarget `json:"of"`
	Start        uint64       `json:"start"`
	End          uint64       `json:"end"`
	StartsAtByte *uint64      `json:"starts_at_byte"`
	Frame        string       `json:"frame"`
}

type vectorName struct {
	Tree  string       `json:"tree"`
	Path  string       `json:"path"`
	Of    vectorTarget `json:"of"`
	Name  string       `json:"name"`
	Found bool         `json:"found"`
	Frame string       `json:"frame"`
}

type vectorAnswer struct {
	Tree    string   `json:"tree"`
	What    string   `json:"what"`
	Request string   `json:"request"`
	Frames  []string `json:"frames"`
}

type vectorConnection struct {
	Tree     string `json:"tree"`
	Sent     string `json:"sent"`
	Received string `json:"received"`
}

type vectorRefused struct {
	As       string        `json:"as"`
	What     string        `json:"what"`
	Bytes    string        `json:"bytes"`
	Chunking string        `json:"chunking"`
	Against  *vectorTarget `json:"against"`
	Name     string        `json:"name"`
	Text     string        `json:"text"`
}

func readVectors(t *testing.T) vectorsManifest {
	t.Helper()
	var manifest vectorsManifest
	readManifest(t, &manifest, vectorsManifestSHA256, "vectors", "manifest.json")
	if manifest.Format != "scionic-vectors-1" {
		t.Fatalf("the manifest is of the format %q", manifest.Format)
	}
	return manifest
}

func (v vectorTarget) target(t *testing.T) Target {
	t.Helper()
	target := Target{Hash: mustHash(t, v.Hash), Size: v.Size}
	switch v.Kind {
	case "file":
		target.Kind = KindFile
	case "folder":
		target.Kind = KindFolder
	default:
		t.Fatalf("the manifest names a %q", v.Kind)
	}
	return target
}

func extraOf(t *testing.T, values map[string]string) ExtraData {
	t.Helper()
	var extra ExtraData
	for name, value := range values {
		var err error
		if extra, err = extra.WithText(name, value); err != nil {
			t.Fatal(err)
		}
	}
	return extra
}

// content gives the bytes of a file that a vector describes as its parts.
func (n vectorNode) content() []byte {
	var content []byte
	for _, part := range *n.File {
		content = append(content, corpusBytes(part.Seed, part.Length)...)
	}
	return content
}

// build builds what a vector's description says: a file as parts made from
// seeds, or a folder of named files and folders. With fromStream, a file's
// bytes are given as a stream, and not as bytes that lie in memory.
func (n vectorNode) build(t *testing.T, builder *Builder, fromStream bool) Target {
	t.Helper()
	if (n.File == nil) == (n.Folder == nil) {
		t.Fatalf("%q is described as neither a file nor a folder, or as both", n.Name)
	}
	if n.File != nil {
		var target Target
		var err error
		if fromStream {
			target, err = builder.File(iotestOneByteReader{bytes.NewReader(n.content())})
		} else {
			target, err = builder.FileFromBytes(n.content())
		}
		if err != nil {
			t.Fatal(err)
		}
		return target
	}
	var entries []Entry
	for _, child := range *n.Folder {
		entry, err := NewEntry(child.Name, child.build(t, builder, fromStream), extraOf(t, child.Extra))
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
	// The entries are given in the wrong order on purpose: a builder puts
	// them in the order of their names.
	slices.Reverse(entries)
	target, err := builder.Folder(entries)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

// iotestOneByteReader gives its bytes in reads of awkward lengths: one
// byte, then seven, then all that is asked for, again and again.
type iotestOneByteReader struct {
	inner *bytes.Reader
}

func (r iotestOneByteReader) Read(buffer []byte) (int, error) {
	most := []int{1, 7, len(buffer)}[r.inner.Len()%3]
	return r.inner.Read(buffer[:min(most, len(buffer))])
}

// builtVector is a tree of the manifest, built here.
type builtVector struct {
	store *MemoryStore
	root  Root
}

func buildVector(t *testing.T, described vectorTree, fromStream bool) builtVector {
	t.Helper()
	chunking, err := ChunkingFromTag([]byte(described.Chunking))
	if err != nil {
		t.Fatal(err)
	}
	store := NewMemoryStore()
	builder := NewBuilder(store, chunking)
	root, err := builder.Finish(described.Top.build(t, builder, fromStream), extraOf(t, described.Extra))
	if err != nil {
		t.Fatalf("%s: %v", described.Name, err)
	}
	return builtVector{store: store, root: root}
}

// at gives the file or folder at a path of a tree. The empty path is the
// top.
func (b builtVector) at(t *testing.T, path string) Target {
	t.Helper()
	if path == "" {
		record, err := RecordOf(b.store, b.root)
		if err != nil {
			t.Fatal(err)
		}
		return record.Top()
	}
	entry, found, err := Lookup(b.store, b.root, path)
	if err != nil || !found {
		t.Fatalf("%q: %v %v", path, found, err)
	}
	return entry.Target()
}

func TestEveryTreeOfTheSharedVectorsIsBuiltByteForByte(t *testing.T) {
	manifest := readVectors(t)
	if len(manifest.Trees) < 7 {
		t.Fatal("the manifest lists too few trees")
	}
	for _, described := range manifest.Trees {
		built := buildVector(t, described, false)
		name := described.Name
		if got := built.root.String(); got != described.Root {
			t.Fatalf("%s: the root is %s, and not %s", name, got, described.Root)
		}
		if got := built.root.Hash().Hex(); got != described.RootHash {
			t.Fatalf("%s: the root's hash is %s", name, got)
		}
		if parsed, err := ParseRoot(described.Root); err != nil || parsed != built.root {
			t.Fatalf("%s: the root's text is read as %v (%v)", name, parsed, err)
		}
		record, err := RecordOf(built.store, built.root)
		if err != nil {
			t.Fatal(err)
		}
		if got := hex.EncodeToString(record.Encode()); got != described.Record {
			t.Fatalf("%s: the record is %s, and not %s", name, got, described.Record)
		}
		totals := record.Totals()
		if record.Top().Size != described.Size || totals.Chunks != described.Chunks ||
			totals.ChunkBytes != described.ChunkBytes || totals.ListBytes != described.ListBytes {
			t.Fatalf("%s: the totals are %d %+v", name, record.Top().Size, totals)
		}
		if record.Chunking().Tag() != described.Chunking {
			t.Fatalf("%s: the record says it was chunked as %s", name, record.Chunking().Tag())
		}
		// The tree as one stream gives every list and chunk once, parents
		// first.
		var bundle bytes.Buffer
		if err := Export(built.store, built.root, &bundle); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(bundle.Bytes())
		if bundle.Len() != described.BundleLength || hex.EncodeToString(digest[:]) != described.BundleSHA256 {
			t.Fatalf("%s: the bundle holds %d bytes with the SHA-256 %x", name, bundle.Len(), digest)
		}
		var lists []vectorList
		var chunks []vectorChunk
		for rest := bundle.Bytes()[len(BundleMagic):]; len(rest) > 0; {
			length := binary.BigEndian.Uint32(rest)
			frame := rest[4 : 4+length]
			rest = rest[4+length:]
			switch FrameKind(frame[0]) {
			case FrameFileList:
				list, err := DecodeFileList(frame[1:])
				if err != nil {
					t.Fatal(err)
				}
				lists = append(lists, vectorList{"file", list.Hash().Hex(), list.Size(), hex.EncodeToString(frame[1:])})
			case FrameFolderList:
				list, err := DecodeFolderList(frame[1:])
				if err != nil {
					t.Fatal(err)
				}
				lists = append(lists, vectorList{"folder", list.Hash().Hex(), list.Size(), hex.EncodeToString(frame[1:])})
			case FrameChunk:
				chunks = append(chunks, vectorChunk{HashOf(frame[1:]).Hex(), uint64(len(frame) - 1)})
			}
		}
		if !slices.Equal(lists, described.Lists) {
			t.Fatalf("%s: the lists are not the manifest's", name)
		}
		if !slices.Equal(chunks, described.ChunkList) {
			t.Fatalf("%s: the chunks are not the manifest's", name)
		}
		// What is built passes the checks of a tree that is taken in.
		if _, err := VerifyWith(built.store, built.root, VerifyOptions{Rechunk: true}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		// A file's bytes that come as a stream give the same tree.
		if streamed := buildVector(t, described, true); streamed.root != built.root || !sameStores(streamed.store, built.store) {
			t.Fatalf("%s: built from streams, the tree is another one", name)
		}
		// The bundle is taken in, and so is the tree from a holder of it.
		imported := NewMemoryStore()
		if _, err := Import(imported, built.root, bytes.NewReader(bundle.Bytes())); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		fetched := NewMemoryStore()
		if _, err := Fetch(fetched, built.root, NewResponder(built.store)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !sameStores(imported, built.store) || !sameStores(fetched, built.store) {
			t.Fatalf("%s: a tree that was taken in is not the tree that was built", name)
		}
		// Every file reads back as the bytes it was built from.
		var check func(node vectorNode, target Target)
		check = func(node vectorNode, target Target) {
			if node.File != nil {
				content, err := ReadFileBytes(fetched, target)
				if err != nil || !bytes.Equal(content, node.content()) {
					t.Fatalf("%s: %q does not read back (%v)", name, node.Name, err)
				}
				return
			}
			list, err := FolderOf(fetched, target)
			if err != nil {
				t.Fatal(err)
			}
			if int(list.Count()) != len(*node.Folder) {
				t.Fatalf("%s: %q holds %d entries", name, node.Name, list.Count())
			}
			for _, child := range *node.Folder {
				entry, found := list.Get(child.Name)
				if !found || !entry.Extra().Equal(extraOf(t, child.Extra)) {
					t.Fatalf("%s: %q is not in its folder as it was put there", name, child.Name)
				}
				check(child, entry.Target())
			}
		}
		check(described.Top, record.Top())
	}
}

// sampleOf builds the tree that the branches, the names and the answers of the
// manifest are taken from.
func sampleOf(t *testing.T, manifest vectorsManifest, name string) builtVector {
	t.Helper()
	for _, described := range manifest.Trees {
		if described.Name == name {
			return buildVector(t, described, false)
		}
	}
	t.Fatalf("the manifest has no tree named %q", name)
	return builtVector{}
}

func TestEveryBranchOfTheSharedVectorsIsShownAndCheckedByteForByte(t *testing.T) {
	manifest := readVectors(t)
	if len(manifest.Branches) < 9 {
		t.Fatal("the manifest lists too few branches")
	}
	for _, shown := range manifest.Branches {
		sample := sampleOf(t, manifest, shown.Tree)
		of := shown.Of.target(t)
		if sample.at(t, shown.Path) != of {
			t.Fatalf("%q is not what the manifest says is there", shown.Path)
		}
		given, err := DecodeFrame(mustHex(t, shown.Frame))
		if err != nil {
			t.Fatal(err)
		}
		if of.Kind == KindFile {
			list, err := FileOf(sample.store, of)
			if err != nil {
				t.Fatal(err)
			}
			branch, err := list.Open(shown.Start, shown.End)
			if err != nil {
				t.Fatal(err)
			}
			if got := hex.EncodeToString(FileBranchFrame(branch)); got != shown.Frame {
				t.Fatalf("%q %d to %d: the frame is %s", shown.Path, shown.Start, shown.End, got)
			}
			if given.Kind != FrameFileBranch || shown.StartsAtByte == nil {
				t.Fatalf("%q: the manifest's frame is no branch of a file's list", shown.Path)
			}
			for _, checked := range []func() (uint64, error){
				func() (uint64, error) { return given.FileBranch.Check(of) },
				func() (uint64, error) { return given.FileBranch.CheckWithChunking(of, smallChunks(t)) },
			} {
				if at, err := checked(); err != nil || at != *shown.StartsAtByte {
					t.Fatalf("%q %d to %d: starts at byte %d (%v)", shown.Path, shown.Start, shown.End, at, err)
				}
			}
			if given.FileBranch.Start() != shown.Start || uint64(len(given.FileBranch.Items())) != shown.End-shown.Start {
				t.Fatalf("%q: the branch shows other items", shown.Path)
			}
			continue
		}
		list, err := FolderOf(sample.store, of)
		if err != nil {
			t.Fatal(err)
		}
		branch, err := list.Open(shown.Start, shown.End)
		if err != nil {
			t.Fatal(err)
		}
		if got := hex.EncodeToString(FolderBranchFrame(branch)); got != shown.Frame {
			t.Fatalf("%q %d to %d: the frame is %s", shown.Path, shown.Start, shown.End, got)
		}
		if given.Kind != FrameFolderBranch {
			t.Fatalf("%q: the manifest's frame is no branch of a folder's list", shown.Path)
		}
		if err := given.FolderBranch.Check(of); err != nil {
			t.Fatalf("%q %d to %d: %v", shown.Path, shown.Start, shown.End, err)
		}
		if !slices.EqualFunc(given.FolderBranch.Entries(), list.Entries()[shown.Start:shown.End], Entry.Equal) {
			t.Fatalf("%q: the branch shows other entries", shown.Path)
		}
	}
}

func TestEveryNameOfTheSharedVectorsIsAnsweredByteForByte(t *testing.T) {
	manifest := readVectors(t)
	if len(manifest.Names) < 8 {
		t.Fatal("the manifest lists too few names")
	}
	found, absent := 0, 0
	for _, asked := range manifest.Names {
		sample := sampleOf(t, manifest, asked.Tree)
		of := asked.Of.target(t)
		if sample.at(t, asked.Path) != of {
			t.Fatalf("%q is not what the manifest says is there", asked.Path)
		}
		list, err := FolderOf(sample.store, of)
		if err != nil {
			t.Fatal(err)
		}
		branch, err := list.OpenName(asked.Name)
		if err != nil {
			t.Fatal(err)
		}
		if got := hex.EncodeToString(FolderBranchFrame(branch)); got != asked.Frame {
			t.Fatalf("%q in %q: the frame is %s", asked.Name, asked.Path, got)
		}
		given, err := DecodeFrame(mustHex(t, asked.Frame))
		if err != nil || given.Kind != FrameFolderBranch {
			t.Fatal(given.Kind, err)
		}
		entry, has, err := given.FolderBranch.CheckName(of, asked.Name)
		if err != nil || has != asked.Found {
			t.Fatalf("%q in %q: found %v (%v)", asked.Name, asked.Path, has, err)
		}
		wanted, held := list.Get(asked.Name)
		if has != held || (has && !entry.Equal(wanted)) {
			t.Fatalf("%q in %q: the answer is not what the folder holds", asked.Name, asked.Path)
		}
		if has {
			found++
		} else {
			absent++
		}
	}
	if found == 0 || absent == 0 {
		t.Fatal("the manifest has no name that is there, or none that is not")
	}
}

func TestEveryRequestOfTheSharedVectorsIsAnsweredByteForByte(t *testing.T) {
	manifest := readVectors(t)
	if len(manifest.Answers) < 12 {
		t.Fatal("the manifest lists too few answers")
	}
	kinds := map[RequestKind]bool{}
	for _, asked := range manifest.Answers {
		sample := sampleOf(t, manifest, asked.Tree)
		request := mustHex(t, asked.Request)
		decoded, err := DecodeRequest(request)
		if err != nil || !bytes.Equal(decoded.Encode(), request) {
			t.Fatalf("%s: the request does not read back (%v)", asked.What, err)
		}
		kinds[decoded.Kind] = true
		var frames []string
		err = NewResponder(sample.store).Answer(request, func(frame []byte) error {
			frames = append(frames, hex.EncodeToString(frame))
			return nil
		})
		if err != nil {
			t.Fatalf("%s: %v", asked.What, err)
		}
		if !slices.Equal(frames, asked.Frames) {
			t.Fatalf("%s: the answer is %v", asked.What, frames)
		}
		for _, frame := range asked.Frames {
			if _, err := DecodeFrame(mustHex(t, frame)); err != nil {
				t.Fatalf("%s: %v", asked.What, err)
			}
		}
	}
	if len(kinds) != 10 {
		t.Fatalf("the manifest asks %d kinds of request, and there are 10", len(kinds))
	}
}

func TestTheExchangeOfTheSharedVectorsIsTheSameOnAConnection(t *testing.T) {
	manifest := readVectors(t)
	sample := sampleOf(t, manifest, manifest.Connection.Tree)
	sent := mustHex(t, manifest.Connection.Sent)
	want := mustHex(t, manifest.Connection.Received)
	// What a holder of the tree sends back is the manifest's bytes, whether
	// its writer keeps bytes back or not.
	var received bytes.Buffer
	if err := Serve(sample.store, bytes.NewReader(sent), &received); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(received.Bytes(), want) {
		t.Fatalf("the holder sent back %x", received.Bytes())
	}
	received.Reset()
	buffered := bufio.NewWriter(&received)
	if err := Serve(sample.store, iotestOneByteReader{bytes.NewReader(sent)}, buffered); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(received.Bytes(), want) {
		t.Fatal("through a writer that keeps bytes back, the holder sent back other bytes")
	}
	// And an asker on a connection sends the manifest's bytes, and is given
	// the frames that the manifest lists for those requests.
	var requests []Asked
	var expected [][]string
	for rest := sent; len(rest) > 0; {
		length := binary.BigEndian.Uint32(rest)
		request := rest[4 : 4+length]
		rest = rest[4+length:]
		requests = append(requests, Asked{Request: request, Limit: 1 << 20})
		listed := false
		for _, answer := range manifest.Answers {
			if answer.Request == hex.EncodeToString(request) {
				expected = append(expected, answer.Frames)
				listed = true
			}
		}
		if !listed {
			t.Fatal("the connection sends a request that the answers do not list")
		}
	}
	if len(requests) != 3 {
		t.Fatalf("the connection sends %d requests", len(requests))
	}
	// The connection holds on the way to the holder what an asker may send
	// ahead, and on the way back one byte.
	got := make([][]string, len(requests))
	asked, wrote, served := connected(sample.store, RequestsAheadBytes, 1, func(peer *StreamPeer) error {
		return peer.AskAll(requests, func(place int, frame []byte) error {
			got[place] = append(got[place], hex.EncodeToString(frame))
			return nil
		})
	})
	if asked != nil || served != nil {
		t.Fatal(asked, served)
	}
	if !bytes.Equal(wrote.all, sent) {
		t.Fatalf("the asker sent %x", wrote.all)
	}
	for place := range requests {
		if !slices.Equal(got[place], expected[place]) {
			t.Fatalf("request %d was answered with %v", place, got[place])
		}
	}
}

func TestEverythingTheSharedVectorsSayMustBeRefusedIsRefused(t *testing.T) {
	manifest := readVectors(t)
	if len(manifest.Refused) < 76 {
		t.Fatal("the manifest lists too few inputs that must be refused")
	}
	kinds := map[string]int{}
	for _, forged := range manifest.Refused {
		kinds[forged.As]++
		content := mustHex(t, forged.Bytes)
		against := func() Target {
			if forged.Against == nil {
				t.Fatalf("%s: %s: the manifest does not say against what", forged.As, forged.What)
			}
			return forged.Against.target(t)
		}
		var err error
		switch forged.As {
		case "record":
			_, err = DecodeRecord(content)
		case "file-list":
			_, err = DecodeFileList(content)
		case "file-list-chunking":
			// The list is well formed, and does not fit a way of chunking.
			list, readErr := DecodeFileList(content)
			if readErr != nil {
				t.Fatalf("%s: %s: the list does not read: %v", forged.As, forged.What, readErr)
			}
			chunking, readErr := ChunkingFromTag([]byte(forged.Chunking))
			if readErr != nil {
				t.Fatal(readErr)
			}
			err = list.CheckChunking(chunking)
		case "folder-list":
			_, err = DecodeFolderList(content)
		case "file-branch":
			var branch *FileBranch
			if branch, err = DecodeFileBranch(content); err == nil {
				_, err = branch.Check(against())
			}
		case "folder-branch":
			var branch *FolderBranch
			if branch, err = DecodeFolderBranch(content); err == nil {
				err = branch.Check(against())
			}
		case "folder-name":
			var branch *FolderBranch
			if branch, err = DecodeFolderBranch(content); err == nil {
				_, _, err = branch.CheckName(against(), forged.Name)
			}
		case "request":
			_, err = DecodeRequest(content)
		case "frame":
			_, err = DecodeFrame(content)
		case "root":
			_, err = ParseRoot(forged.Text)
		default:
			t.Fatalf("the manifest offers something as %q, which these tests do not know", forged.As)
		}
		if err == nil {
			t.Errorf("taken, and it must be refused: %s: %s", forged.As, forged.What)
		}
	}
	for _, kind := range []string{"record", "file-list", "file-list-chunking", "folder-list", "file-branch", "folder-branch", "folder-name", "request", "frame", "root"} {
		if kinds[kind] == 0 {
			t.Errorf("the manifest offers nothing as %q", kind)
		}
	}
}
