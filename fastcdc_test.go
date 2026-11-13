package scionic

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math/bits"
	"slices"
	"testing"
)

func TestTheGearTableIsTheFrozenOne(t *testing.T) {
	// The two entries that the specification writes out.
	if fastcdcGear[0] != 0x947e4e1b8bedab5d || fastcdcGear[1] != 0xd7d7f340329c706e {
		t.Fatalf("the gear table starts %#x %#x", fastcdcGear[0], fastcdcGear[1])
	}
	if fastcdcGear[255] != 0x628f935ad9cce8d5 {
		t.Fatalf("the gear table ends %#x", fastcdcGear[255])
	}
	// All 256 entries, as 8 bytes each, big-endian, have the SHA-256 that
	// the Rust library's table has, which is written out there as numbers.
	var table []byte
	for _, entry := range fastcdcGear {
		table = binary.BigEndian.AppendUint64(table, entry)
	}
	digest := sha256.Sum256(table)
	const want = "4a751e8c3b43e7190540627d2edf56679d6e46b66ce471999c914e94e77ac647"
	if got := hex.EncodeToString(digest[:]); got != want {
		t.Fatalf("the gear table has the SHA-256 %s, and not %s", got, want)
	}
}

func TestTheConstantsAreFrozen(t *testing.T) {
	if FastCDCMinSize != 512*1024 || FastCDCTargetSize != 2048*1024 || FastCDCMaxSize != 8192*1024 {
		t.Fatal("a size changed")
	}
	allOnes := ^uint64(0)
	if FastCDCStrictMask != allOnes<<41 || bits.OnesCount64(FastCDCStrictMask) != 23 {
		t.Fatal("the strict mask must be the top 23 bits")
	}
	if FastCDCLooseMask != allOnes<<45 || bits.OnesCount64(FastCDCLooseMask) != 19 {
		t.Fatal("the loose mask must be the top 19 bits")
	}
	if FastCDCStrictMask&FastCDCLooseMask != FastCDCLooseMask {
		t.Fatal("the loose mask must be a subset of the strict mask")
	}
	if FastCDCGearSeedPrefix != "scionic-fastcdc-v1:gear:" {
		t.Fatal("the seed changed")
	}
}

func TestCutBoundsAndDeterminism(t *testing.T) {
	data := corpusBytes("scionic-fastcdc-v1:test:bounds", 40<<20)
	first := FastCDCV1.Cut(data)
	second := FastCDCV1.Cut(data)
	if !piecesEqual(first, second) {
		t.Fatal("two cuts of identical data must agree")
	}
	if len(first) == 0 {
		t.Fatal("no pieces")
	}
	total := 0
	for index, piece := range first {
		if index < len(first)-1 && len(piece) <= FastCDCMinSize {
			t.Fatalf("piece %d is %d bytes; every piece but the last must exceed the floor", index, len(piece))
		}
		if len(piece) > FastCDCMaxSize {
			t.Fatalf("piece %d exceeds the ceiling", index)
		}
		total += len(piece)
	}
	if total != len(data) {
		t.Fatal("the pieces must cover the input exactly")
	}
	if mean := total / len(first); mean < 1<<20 || mean > 4<<20 {
		t.Fatalf("the mean piece size %d is outside 1 MiB to 4 MiB", mean)
	}
}

// stutterReader gives data in awkward partial reads, to go through every way
// the stream cutter fills its window.
type stutterReader struct {
	data   []byte
	offset int
	step   int
}

func (r *stutterReader) read(buffer []byte) (int, error) {
	take := min(r.step, len(buffer), len(r.data)-r.offset)
	copy(buffer, r.data[r.offset:r.offset+take])
	r.offset += take
	return take, nil
}

func TestCuttingAStreamIsCuttingTheWholeOfIt(t *testing.T) {
	lengths := []int{
		0,
		100,
		FastCDCMinSize,
		FastCDCMinSize + 1,
		FastCDCTargetSize,
		FastCDCMaxSize,
		FastCDCMaxSize + 1,
		3 << 20,
		20 << 20,
	}
	corpus := corpusBytes("scionic-fastcdc-v1:test:stream", 20<<20)
	fixed, err := FixedCutting(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	var cutter streamCutter
	for _, cutting := range []Cutting{FastCDCV1, fixed} {
		for _, length := range lengths {
			data := corpus[:length]
			expected := cutting.Cut(data)
			// The smallest window, one that is filled several times, and
			// one that the whole stream fits into.
			for _, room := range []int{0, 2 * FastCDCMaxSize, 5*FastCDCMaxSize + 3} {
				var streamed [][]byte
				source := &stutterReader{data: data, step: 64*1024 + 17}
				err := cutter.cut(cutting, room, source.read, func(pieces [][]byte) error {
					for _, piece := range pieces {
						streamed = append(streamed, bytes.Clone(piece))
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				if !piecesEqual(streamed, expected) {
					t.Fatalf("%v, length %d, room %d: a stream is cut at other places", cutting, length, room)
				}
			}
		}
	}
}

func TestTheFastBoundaryIsThePlainOne(t *testing.T) {
	corpus := corpusBytes("scionic-fastcdc-v1:test:boundary", 24<<20)
	strict, loose := 0, 0
	steps := map[int]bool{}
	check := func(data []byte) {
		t.Helper()
		cut := fastcdcBoundary(data)
		if plain := fastcdcBoundaryPlain(data); cut != plain {
			t.Fatalf("length %d: cut at %d, and the rule says %d", len(data), cut, plain)
		}
		if cut > FastCDCMinSize && cut < min(len(data), FastCDCMaxSize) {
			steps[(cut-FastCDCMinSize)%4] = true
			if cut <= FastCDCTargetSize {
				strict++
			} else {
				loose++
			}
		}
	}
	// Whole windows, from many starts.
	for start := 0; start < len(corpus)-FastCDCMaxSize; start += 262_139 {
		check(corpus[start:])
	}
	// Windows that end at every kind of place: around the floor, around the
	// switch of masks, around the ceiling, and a few bytes on.
	for _, edge := range []int{FastCDCMinSize, FastCDCTargetSize, FastCDCMaxSize} {
		for length := edge - 5; length <= edge+9; length++ {
			check(corpus[1_000_003 : 1_000_003+length])
		}
	}
	// Bytes that never match are cut at the ceiling, or where they end.
	zeros := make([]byte, FastCDCMaxSize+7)
	check(zeros)
	check(zeros[:FastCDCTargetSize+2])
	if len(steps) != 4 {
		t.Fatal("no cut at every place within a step of four")
	}
	if strict == 0 || loose == 0 {
		t.Fatal("no cuts under both masks")
	}
}

func TestSmallInputsAreSinglePieces(t *testing.T) {
	for _, length := range []int{1, 100, FastCDCMinSize - 1, FastCDCMinSize} {
		data := corpusBytes("scionic-fastcdc-v1:test:small", length)
		pieces := FastCDCV1.Cut(data)
		if len(pieces) != 1 || !bytes.Equal(pieces[0], data) {
			t.Fatalf("length %d must be a single piece", length)
		}
	}
	if len(FastCDCV1.Cut(nil)) != 0 {
		t.Fatal("no bytes gave pieces")
	}
}

func TestConstantBytesStayBounded(t *testing.T) {
	// 20 MiB of zeros never matches either mask: every piece is cut at the
	// ceiling, with the rest as the last one.
	var lengths []int
	for _, piece := range FastCDCV1.Cut(make([]byte, 20<<20)) {
		lengths = append(lengths, len(piece))
	}
	if !slices.Equal(lengths, []int{8 << 20, 8 << 20, 4 << 20}) {
		t.Fatal(lengths)
	}
	letters := bytes.Repeat([]byte{'a'}, 20<<20)
	pieces := FastCDCV1.Cut(letters)
	total := 0
	for index, piece := range pieces {
		if len(piece) > FastCDCMaxSize || (index < len(pieces)-1 && len(piece) <= FastCDCMinSize) {
			t.Fatalf("piece %d holds %d bytes", index, len(piece))
		}
		total += len(piece)
	}
	if total != len(letters) {
		t.Fatal("the pieces do not cover the input")
	}
}

func TestAnInsertReusesMostPieces(t *testing.T) {
	base := corpusBytes("scionic-fastcdc-v1:test:reuse", 32<<20)
	payload := corpusBytes("scionic-fastcdc-v1:test:reuse-payload", 4096)
	edited := slices.Concat(base[:16<<20], payload, base[16<<20:])
	inBase := map[Hash]bool{}
	for _, piece := range FastCDCV1.Cut(base) {
		inBase[HashOf(piece)] = true
	}
	pieces := FastCDCV1.Cut(edited)
	fresh := 0
	for _, piece := range pieces {
		if !inBase[HashOf(piece)] {
			fresh++
		}
	}
	if fresh < 1 || fresh > 3 {
		t.Fatalf("an insert of 4 KiB gave %d new pieces of %d", fresh, len(pieces))
	}
}

func TestCuttingAtAFixedSize(t *testing.T) {
	data := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	fixed := func(size uint32) Cutting {
		t.Helper()
		cutting, err := FixedCutting(size)
		if err != nil {
			t.Fatal(err)
		}
		return cutting
	}
	if len(fixed(4).Cut(nil)) != 0 {
		t.Fatal("no bytes gave pieces")
	}
	if !piecesEqual(fixed(4).Cut(data), [][]byte{data[0:4], data[4:8], data[8:10]}) {
		t.Fatal("pieces of 4")
	}
	if !piecesEqual(fixed(5).Cut(data), [][]byte{data[0:5], data[5:10]}) {
		t.Fatal("pieces of 5")
	}
	if !piecesEqual(fixed(16).Cut(data), [][]byte{data}) {
		t.Fatal("pieces of 16")
	}
	if !piecesEqual(FastCDCV1.Cut(data), [][]byte{data}) {
		t.Fatal("cut by content")
	}
	if _, err := FixedCutting(0); err == nil {
		t.Fatal("a size of 0 was taken")
	}
	if _, err := FixedCutting(FastCDCMaxSize); err != nil {
		t.Fatal(err)
	}
	if _, err := FixedCutting(FastCDCMaxSize + 1); err == nil {
		t.Fatal("a size over 8 MiB was taken")
	}
}

// The fastcdc-v1 vectors: where files are cut.
//
// spec/fastcdc-vectors/manifest.json says, for inputs of several kinds,
// where each piece ends and what each piece hashes to. The inputs are not in
// the repository: they are made again from the seeds the manifest gives.
//
// Two builders give the same file the same hash only when both cut at the
// same bytes, so one wrong gear entry, mask or off-by-one would make trees
// that look valid and never share a piece with anyone. These vectors are
// where that shows.

const fastcdcManifestSHA256 = "7624a7eb87315adc6920aac0cec2155ed7e28b3271396c6ad242312ee6b1abc5"

type fastcdcManifest struct {
	Spec  string        `json:"spec"`
	Cases []fastcdcCase `json:"cases"`
}

type fastcdcCase struct {
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Seed        string   `json:"seed"`
	Length      int      `json:"length"`
	Boundaries  []int    `json:"boundaries"`
	ChunkSHA256 []string `json:"chunk_sha256"`
	FileHash    string   `json:"file_hash"`
	Base        string   `json:"base"`
	Op          string   `json:"op"`
	Offset      int      `json:"offset"`
	EditLength  int      `json:"edit_length"`
	PayloadSeed string   `json:"payload_seed"`
	Reused      []bool   `json:"reused"`
}

func readFastcdcManifest(t *testing.T) fastcdcManifest {
	t.Helper()
	var manifest fastcdcManifest
	readManifest(t, &manifest, fastcdcManifestSHA256, "fastcdc-vectors", "manifest.json")
	if manifest.Spec != "fastcdc-v1" {
		t.Fatalf("the manifest is for %q", manifest.Spec)
	}
	if len(manifest.Cases) < 11 {
		t.Fatal("the manifest lists too few cases")
	}
	return manifest
}

// input makes the input of a case from what the manifest says about it.
func (m fastcdcManifest) input(t *testing.T, name string) []byte {
	t.Helper()
	for _, entry := range m.Cases {
		if entry.Name != name {
			continue
		}
		switch entry.Kind {
		case "corpus":
			return corpusBytes(entry.Seed, entry.Length)
		case "zeros":
			return make([]byte, entry.Length)
		case "edit":
			base := m.input(t, entry.Base)
			switch entry.Op {
			case "insert":
				payload := corpusBytes(entry.PayloadSeed, entry.EditLength)
				return slices.Concat(base[:entry.Offset], payload, base[entry.Offset:])
			case "delete":
				return slices.Concat(base[:entry.Offset], base[entry.Offset+entry.EditLength:])
			}
			t.Fatalf("case %s has the unknown op %q", name, entry.Op)
		}
		t.Fatalf("case %s has the unknown kind %q", name, entry.Kind)
	}
	t.Fatalf("the manifest has no case named %s", name)
	return nil
}

func TestEveryCaseIsCutWhereTheManifestSays(t *testing.T) {
	manifest := readFastcdcManifest(t)
	for _, entry := range manifest.Cases {
		data := manifest.input(t, entry.Name)
		if len(data) != entry.Length {
			t.Fatalf("%s: the input holds %d bytes", entry.Name, len(data))
		}
		// Cut in memory.
		var ends []int
		var hashes []string
		var items []PieceRef
		end := 0
		for _, piece := range FastCDCV1.Cut(data) {
			end += len(piece)
			ends = append(ends, end)
			hashes = append(hashes, HashOf(piece).Hex())
			item, err := PieceRefOf(piece)
			if err != nil {
				t.Fatal(err)
			}
			items = append(items, item)
		}
		if !slices.Equal(ends, entry.Boundaries) {
			t.Fatalf("%s: the pieces end at %v, and not at %v", entry.Name, ends, entry.Boundaries)
		}
		if !slices.Equal(hashes, entry.ChunkSHA256) {
			t.Fatalf("%s: the pieces' hashes", entry.Name)
		}
		// And so the file has the hash that the manifest gives it.
		list, err := NewFileList(items)
		if err != nil {
			t.Fatal(err)
		}
		if err := list.CheckCutting(FastCDCV1); err != nil {
			t.Fatalf("%s: %v", entry.Name, err)
		}
		if list.Size() != uint64(len(data)) {
			t.Fatalf("%s: the file's size", entry.Name)
		}
		if got := list.Hash().Hex(); got != entry.FileHash {
			t.Fatalf("%s: the file's hash is %s, and not %s", entry.Name, got, entry.FileHash)
		}
	}
}

// A case that edits another one must share with it exactly the pieces the
// manifest says it shares. What the base has is worked out here by cutting
// the base again, and not taken from the manifest.
func TestAnEditChangesOnlyThePiecesAroundIt(t *testing.T) {
	manifest := readFastcdcManifest(t)
	edits := 0
	for _, entry := range manifest.Cases {
		if entry.Kind != "edit" {
			continue
		}
		edits++
		inBase := map[Hash]bool{}
		for _, piece := range FastCDCV1.Cut(manifest.input(t, entry.Base)) {
			inBase[HashOf(piece)] = true
		}
		var shared []bool
		fresh := 0
		for _, piece := range FastCDCV1.Cut(manifest.input(t, entry.Name)) {
			isShared := inBase[HashOf(piece)]
			shared = append(shared, isShared)
			if !isShared {
				fresh++
			}
		}
		if !slices.Equal(shared, entry.Reused) {
			t.Fatalf("%s: which pieces are shared with the base: %v, and not %v", entry.Name, shared, entry.Reused)
		}
		if fresh < 1 || fresh > 4 {
			t.Fatalf("%s: %d new pieces", entry.Name, fresh)
		}
	}
	if edits < 4 {
		t.Fatal("the manifest lists too few edits")
	}
}

func BenchmarkCutting(b *testing.B) {
	data := corpusBytes("scionic-fastcdc-v1:bench", 256<<20)
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for range b.N {
		for rest := data; len(rest) > 0; {
			rest = rest[fastcdcBoundary(rest):]
		}
	}
}

func BenchmarkSHA256(b *testing.B) {
	data := corpusBytes("scionic-fastcdc-v1:bench", 64<<20)
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for range b.N {
		sha256.Sum256(data)
	}
}
