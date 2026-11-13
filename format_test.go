package scionic

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
)

// What a tree is made of, held to the bytes the format names, and read
// strictly.

// pieceOf is an item of a file's list for tests: a hash that follows from a
// number, and a length.
func pieceOf(seed byte, length uint32) PieceRef {
	return PieceRef{Hash: HashOf([]byte{seed}), Length: length}
}

// smallFile is a file of one piece.
func smallFile(t *testing.T, seed byte) Target {
	t.Helper()
	return must[*FileList](t)(NewFileList([]PieceRef{pieceOf(seed, uint32(seed)+1)})).Target()
}

// folderNamed is a folder with a small file under each of these names.
func folderNamed(t *testing.T, names ...string) *FolderList {
	t.Helper()
	entries := make([]Entry, len(names))
	for index, name := range names {
		entries[index] = plainEntry(t, name, smallFile(t, byte(index)))
	}
	return must[*FolderList](t)(NewFolderList(entries))
}

// entryBytes is an entry's bytes, written by hand.
func entryBytes(kind byte, size uint64, hash Hash, name string, extra []byte) []byte {
	return joined([]byte{kind}, be64(size), hash[:], be16(len(name)), []byte(name), extra)
}

var noExtra = []byte{0, 0}

// refuses fails the test when something that must be refused is taken.
func refuses(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s was not refused", what)
	}
}

// takes fails the test when something that must be taken is refused.
func takes(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s was refused: %v", what, err)
	}
}

func TestEveryHashCoversExactlyTheBytesTheFormatNames(t *testing.T) {
	// A file's list item, and its leaf.
	first, second := pieceOf(1, 5), pieceOf(2, 7)
	item := joined(first.Hash[:], be32(5))
	one := must[*FileList](t)(NewFileList([]PieceRef{first}))
	if !bytes.Equal(one.Encode(), item) {
		t.Fatal("a file's list of one item is not that item's bytes")
	}
	firstLeaf := oracleSHA([]byte{0x00}, item)
	if first.leaf() != (node{hash: firstLeaf, size: 5}) {
		t.Fatal("an item's leaf is not the hash of its mark and its bytes")
	}
	// A file of one piece: the leaf is the top of its list.
	if one.Hash() != oracleSHA([]byte{0x10}, be64(1), be64(5), firstLeaf[:]) {
		t.Fatal("a file of one piece is not hashed as the format says")
	}
	// A file of two pieces: the top is a fork over the two leaves.
	secondLeaf := second.leaf().hash
	top := oracleSHA([]byte{0x01}, firstLeaf[:], be64(5), be64(1), secondLeaf[:], be64(7), be64(1))
	two := must[*FileList](t)(NewFileList([]PieceRef{first, second}))
	twoHash := two.Hash()
	if two.Size() != 12 || twoHash != oracleSHA([]byte{0x10}, be64(2), be64(12), top[:]) {
		t.Fatal("a file of two pieces is not hashed as the format says")
	}
	// An entry with extra data, and its leaf.
	extra := must[ExtraData](t)(ExtraData{}.WithText("mode", "644"))
	extra = must[ExtraData](t)(extra.With("a", []byte{0xff}))
	named := must[Entry](t)(NewEntry("b.txt", two.Target(), extra))
	extraBytes := joined([]byte{0, 2}, []byte{0, 1, 'a', 0, 1, 0xff}, []byte{0, 4, 'm', 'o', 'd', 'e', 0, 3, '6', '4', '4'})
	namedBytes := entryBytes(1, 12, twoHash, "b.txt", extraBytes)
	if !bytes.Equal(named.appendTo(nil), namedBytes) || named.EncodedLen() != len(namedBytes) {
		t.Fatal("an entry is not written as the format says")
	}
	namedLeaf := oracleSHA([]byte{0x02}, namedBytes)
	if leafOfNamed, _ := named.leafWith(nil); leafOfNamed != (node{hash: namedLeaf, size: 12}) {
		t.Fatal("an entry's leaf is not the hash of its mark and its bytes")
	}
	// A folder of an empty folder and that file.
	empty := must[*FolderList](t)(NewFolderList(nil))
	emptyHash := empty.Hash()
	if emptyHash != oracleSHA([]byte{0x11}, be64(0), be64(0), make([]byte, 32)) {
		t.Fatal("the empty folder is not hashed as the format says")
	}
	inner := plainEntry(t, "a", empty.Target())
	innerBytes := entryBytes(2, 0, emptyHash, "a", noExtra)
	innerLeaf := oracleSHA([]byte{0x02}, innerBytes)
	both := must[*FolderList](t)(NewFolderList([]Entry{inner, named}))
	if !bytes.Equal(both.Encode(), joined(innerBytes, namedBytes)) || both.EncodedLen() != uint64(len(innerBytes)+len(namedBytes)) {
		t.Fatal("a folder's list is not its entries' bytes, one after another")
	}
	folderTop := oracleSHA([]byte{0x03}, innerLeaf[:], be64(0), be64(1), namedLeaf[:], be64(12), be64(1), []byte("b.txt"))
	bothHash := both.Hash()
	if bothHash != oracleSHA([]byte{0x11}, be64(2), be64(12), folderTop[:]) {
		t.Fatal("a folder of two entries is not hashed as the format says")
	}
	// The record, and the root.
	totals := Totals{Pieces: 2, PieceBytes: 12, ListBytes: 72 + both.EncodedLen()}
	record := must[*Record](t)(NewRecord(both.Target(), totals, must[Cutting](t)(FixedCutting(5)), ExtraData{}))
	recordBytes := joined([]byte{0x20, 2}, be64(12), bothHash[:], be64(2), be64(12), be64(totals.ListBytes),
		[]byte{0, 1, 0, 8}, []byte("chunking"), []byte{0, 7}, []byte("fixed-5"))
	if !bytes.Equal(record.Encode(), recordBytes) || record.Root().Hash() != HashOf(recordBytes) {
		t.Fatal("a record is not written as the format says, or its root is not its hash")
	}
	read := must[*Record](t)(DecodeRecord(recordBytes))
	if !bytes.Equal(read.Encode(), recordBytes) || read.Top() != both.Target() || read.Totals() != totals || read.Cutting() != record.Cutting() {
		t.Fatal("a record does not read back")
	}
}

func TestTheSameThingHasTheSameHashWhereverItIs(t *testing.T) {
	// A file's hash covers its pieces, and nothing about where it sits.
	list := must[*FileList](t)(NewFileList([]PieceRef{pieceOf(1, 5), pieceOf(2, 7), pieceOf(1, 5)}))
	here := must[*FolderList](t)(NewFolderList([]Entry{plainEntry(t, "a.bin", list.Target())}))
	there := must[*FolderList](t)(NewFolderList([]Entry{plainEntry(t, "other", smallFile(t, 9)), plainEntry(t, "renamed.bin", list.Target())}))
	if here.Entries()[0].Hash() != there.Entries()[1].Hash() || here.Hash() == there.Hash() {
		t.Fatal("a file under another name, in another folder, is another file")
	}
	// A folder's hash covers its entries, and no name for itself.
	outer := must[*FolderList](t)(NewFolderList([]Entry{plainEntry(t, "one", here.Target()), plainEntry(t, "two", here.Target())}))
	if outer.Entries()[0].Hash() != outer.Entries()[1].Hash() || outer.Size() != 2*here.Size() {
		t.Fatal("a folder under two names is two folders")
	}
	// Every empty file is one file, and it is not the empty folder.
	emptyFile := must[*FileList](t)(NewFileList(nil))
	readEmpty := must[*FileList](t)(DecodeFileList(nil))
	if emptyFile.Hash() != FileHash(0, 0, Hash{}) || readEmpty.Hash() != emptyFile.Hash() || readEmpty.Count() != 0 || emptyFile.Size() != 0 {
		t.Fatal("the empty file is not one file")
	}
	emptyFolder := must[*FolderList](t)(DecodeFolderList(nil))
	if emptyFolder.Hash() != FolderHash(0, 0, Hash{}) || emptyFolder.Hash() == emptyFile.Hash() {
		t.Fatal("the empty folder is the empty file")
	}
	// The pieces' order, their number and their lengths are all covered.
	otherOrder := must[*FileList](t)(NewFileList([]PieceRef{pieceOf(2, 7), pieceOf(1, 5), pieceOf(1, 5)}))
	oneLess := must[*FileList](t)(NewFileList([]PieceRef{pieceOf(1, 5), pieceOf(2, 7)}))
	otherLength := must[*FileList](t)(NewFileList([]PieceRef{pieceOf(1, 5), pieceOf(2, 6), pieceOf(1, 6)}))
	if otherLength.Size() != list.Size() {
		t.Fatal(otherLength.Size())
	}
	for _, other := range []*FileList{otherOrder, oneLess, otherLength} {
		if other.Hash() == list.Hash() {
			t.Fatal("a file's hash does not cover its pieces' order, number and lengths")
		}
	}
	// A list keeps its own copy of what it was made from.
	items := []PieceRef{pieceOf(1, 5), pieceOf(2, 7)}
	kept := must[*FileList](t)(NewFileList(items))
	items[0] = pieceOf(9, 9)
	if kept.Hash() != oneLess.Hash() || !bytes.Equal(kept.Encode(), oneLess.Encode()) {
		t.Fatal("a list changed with the slice it was made from")
	}
}

func TestAnEntryDiffersWithItsNameKindSizeHashAndExtraData(t *testing.T) {
	target := smallFile(t, 1)
	of := func(entry Entry) Hash {
		return must[*FolderList](t)(NewFolderList([]Entry{entry})).Hash()
	}
	base := of(plainEntry(t, "a", target))
	others := map[string]Hash{
		"another name":    of(plainEntry(t, "b", target)),
		"a folder":        of(plainEntry(t, "a", Target{Kind: KindFolder, Hash: target.Hash, Size: target.Size})),
		"another size":    of(plainEntry(t, "a", Target{Kind: KindFile, Hash: target.Hash, Size: target.Size + 1})),
		"another file":    of(plainEntry(t, "a", smallFile(t, 2))),
		"with extra data": of(must[Entry](t)(NewEntry("a", target, must[ExtraData](t)(ExtraData{}.WithText("mode", "755"))))),
	}
	seen := map[Hash]string{base: "the entry itself"}
	for what, hash := range others {
		if before, twice := seen[hash]; twice {
			t.Fatalf("%s has the hash of %s", what, before)
		}
		seen[hash] = what
	}
	// An entry that is neither a file nor a folder is no entry.
	for _, kind := range []Kind{0, 3, 0xff} {
		if _, err := NewEntry("a", Target{Kind: kind, Hash: target.Hash, Size: target.Size}, ExtraData{}); err == nil {
			t.Fatalf("an entry of kind %d was made", kind)
		}
	}
	// The zero value is not an entry, and no folder holds it.
	if _, err := NewFolderList([]Entry{{}}); err == nil {
		t.Fatal("a folder was made of an entry that was never made")
	}
}

func TestNamesAreHeldToTheRules(t *testing.T) {
	for _, name := range []string{"a", "A", ".a", "...", "a b", `a\b`, "été", strings.Repeat("x", 255)} {
		takes(t, fmt.Sprintf("the name %q", name), CheckName(name))
	}
	for _, name := range []string{"", ".", "..", "a/b", "/", "a\x00b", strings.Repeat("x", 256), "\xff\xfe", "a\xc3"} {
		err := CheckName(name)
		if !errors.Is(err, ErrInvalidName) {
			t.Fatalf("the name %q: %v", name, err)
		}
		if _, err := NewEntry(name, smallFile(t, 1), ExtraData{}); !errors.Is(err, ErrInvalidName) {
			t.Fatalf("an entry was made under the name %q: %v", name, err)
		}
	}
	// The name an error tells is cut to 80 characters.
	var told *Error
	if err := CheckName(strings.Repeat("x", 300)); !errors.As(err, &told) || len(told.Name) != 80 {
		t.Fatalf("a long name is told in %d bytes", len(told.Name))
	}
	// Names are compared as bytes: these are two names.
	if _, err := NewFolderList([]Entry{plainEntry(t, "File", smallFile(t, 1)), plainEntry(t, "file", smallFile(t, 2))}); err != nil {
		t.Fatal(err)
	}
}

func TestAFolderListIsSortedByItsNamesBytesWithNoNameTwice(t *testing.T) {
	if _, err := NewFolderList([]Entry{plainEntry(t, "b", smallFile(t, 1)), plainEntry(t, "a", smallFile(t, 2))}); err == nil {
		t.Fatal("a folder was made of entries out of order")
	}
	if _, err := NewFolderList([]Entry{plainEntry(t, "a", smallFile(t, 1)), plainEntry(t, "a", smallFile(t, 1))}); err == nil {
		t.Fatal("a folder was made with a name twice")
	}
	// Bytes, not letters: "Z" sorts before "a", and "a" before "a0".
	list := folderNamed(t, "Z", "a", "a0", "b")
	if entry, found := list.Get("a0"); !found || entry.Name() != "a0" {
		t.Fatal("a name that is there was not found")
	}
	for _, name := range []string{"a1", "", "A", "c", "a0 "} {
		if _, found := list.Get(name); found {
			t.Fatalf("%q was found", name)
		}
	}
	if _, err := DecodeFolderList(joined(list.Encode(), folderNamed(t, "B").Encode())); err == nil {
		t.Fatal("a list out of order was read")
	}
	if _, err := DecodeFolderList(joined(folderNamed(t, "a").Encode(), folderNamed(t, "a").Encode())); err == nil {
		t.Fatal("a list with a name twice was read")
	}
	// What is read is what was written.
	read := must[*FolderList](t)(DecodeFolderList(list.Encode()))
	if read.Hash() != list.Hash() || read.Size() != list.Size() || read.Count() != 4 || read.EncodedLen() != list.EncodedLen() || !bytes.Equal(read.Encode(), list.Encode()) {
		t.Fatal("a folder's list does not read back")
	}
}

func TestExtraDataIsHeldToItsLimitsAndReadInOneWayOnly(t *testing.T) {
	var extra ExtraData
	_, err := extra.WithText("", "x")
	refuses(t, "a value with no name", err)
	_, err = extra.WithText(strings.Repeat("n", 65), "x")
	refuses(t, "a name of 65 bytes", err)
	_, err = extra.WithText("\xff", "x")
	refuses(t, "a name that is not UTF-8", err)
	extra = must[ExtraData](t)(extra.With(strings.Repeat("n", 64), make([]byte, 1024)))
	_, err = extra.With("big", make([]byte, 1025))
	refuses(t, "a value of 1,025 bytes", err)
	for index := range 31 {
		extra = must[ExtraData](t)(extra.WithText(fmt.Sprintf("k%02d", index), "v"))
	}
	if extra.Len() != 32 {
		t.Fatal(extra.Len())
	}
	_, err = extra.WithText("one-more", "v")
	refuses(t, "a 33rd value", err)
	// A name that is there is set again, and the set it was taken from is
	// as it was.
	again := must[ExtraData](t)(extra.WithText("k00", "again"))
	if text, _ := again.Text("k00"); text != "again" || again.Len() != 32 {
		t.Fatalf("the value that was set again is %q", text)
	}
	if text, _ := extra.Text("k00"); text != "v" {
		t.Fatalf("a set was changed after it was made: %q", text)
	}
	if without := again.Without("k00"); without.Len() != 31 || again.Len() != 32 {
		t.Fatal("taking a value away changed the set it was taken from")
	}
	if _, found := again.Without("k00").Get("k00"); found {
		t.Fatal("a value that was taken away is still there")
	}
	extra = again
	// A value is bytes: it is text only when it is UTF-8. What is given out
	// is a copy.
	raw := must[ExtraData](t)(ExtraData{}.With("raw", []byte{0xff, 0x00}))
	if _, isText := raw.Text("raw"); isText {
		t.Fatal("bytes that are not UTF-8 were given as text")
	}
	value, _ := raw.Get("raw")
	value[0] = 0
	if value, found := raw.Get("raw"); !found || !bytes.Equal(value, []byte{0xff, 0x00}) {
		t.Fatal("a value was changed through the copy that was given out")
	}
	var names []string
	for name := range extra.All() {
		names = append(names, name)
	}
	if len(names) != 32 || !slices.IsSorted(names) {
		t.Fatal("the values are not given in the order of their names")
	}
	read := func(content []byte) (ExtraData, error) {
		r := reader{bytes: content, what: "extra data"}
		extra, err := decodeExtraData(&r)
		if err != nil {
			return ExtraData{}, err
		}
		return extra, r.finish()
	}
	written := extra.appendTo(nil)
	if len(written) != extra.encodedLen() || len(written) > MaxExtraBytes {
		t.Fatal(len(written))
	}
	if back := must[ExtraData](t)(read(written)); !back.Equal(extra) || !extra.Equal(back) {
		t.Fatal("extra data does not read back")
	}
	if none := must[ExtraData](t)(read([]byte{0, 0})); none.Len() != 0 || !none.Equal(ExtraData{}) {
		t.Fatal("no values were read as some")
	}
	if two := must[ExtraData](t)(read([]byte{0, 2, 0, 1, 'a', 0, 1, 1, 0, 1, 'b', 0, 0})); two.Len() != 2 {
		t.Fatal(two.Len())
	}
	for what, bad := range map[string][]byte{
		"values out of order":      {0, 2, 0, 1, 'b', 0, 0, 0, 1, 'a', 0, 0},
		"a name twice":             {0, 2, 0, 1, 'a', 0, 0, 0, 1, 'a', 0, 0},
		"an empty name":            {0, 1, 0, 0, 0, 0},
		"a name that is not UTF-8": {0, 1, 0, 1, 0xff, 0, 0},
		"a value cut short":        {0, 1, 0, 1, 'a', 0, 5, 1},
		"fewer values than said":   {0, 2, 0, 1, 'a', 0, 0},
		"a byte after the end":     {0, 0, 0},
		"33 values":                {0, 33},
		"nothing":                  {},
		"half a count":             {0},
		"a name of 65 bytes":       joined([]byte{0, 1, 0, 65}, bytes.Repeat([]byte{'n'}, 65), []byte{0, 0}),
		"a value of 1,025 bytes":   joined([]byte{0, 1, 0, 1, 'a', 0x04, 0x01}, make([]byte, 1025)),
	} {
		_, err := read(bad)
		refuses(t, what, err)
	}
}

func TestListsAreReadStrictly(t *testing.T) {
	hash := HashOf([]byte("x"))
	// A file's list: whole items, and pieces of 1 byte to 8 MiB.
	item := func(length uint32) []byte { return joined(hash[:], be32(length)) }
	for what, content := range map[string][]byte{
		"a piece of 1 byte": item(1),
		"a piece of 8 MiB":  item(MaxPieceBytes),
		"two pieces":        joined(item(1), item(2)),
	} {
		_, err := DecodeFileList(content)
		takes(t, what, err)
		_, err = DecodeOpenFile(content)
		takes(t, what, err)
	}
	for what, content := range map[string][]byte{
		"a piece of no bytes":          item(0),
		"a piece of more than 8 MiB":   item(MaxPieceBytes + 1),
		"an item cut short":            item(1)[:35],
		"a byte after the last item":   joined(item(1), []byte{0}),
		"a piece of the widest length": item(math.MaxUint32),
	} {
		_, err := DecodeFileList(content)
		refuses(t, what, err)
		_, err = DecodeOpenFile(content)
		refuses(t, what, err)
	}
	if _, err := PieceRefOf(nil); err == nil {
		t.Fatal("a piece of no bytes was made")
	}
	if _, err := PieceRefOf(make([]byte, MaxPieceBytes+1)); err == nil {
		t.Fatal("a piece of more than 8 MiB was made")
	}
	if whole, err := PieceRefOf(make([]byte, MaxPieceBytes)); err != nil || whole.Length != MaxPieceBytes {
		t.Fatal("a piece of 8 MiB was not made")
	}
	if made := must[PieceRef](t)(PieceRefOf([]byte("x"))); made != (PieceRef{Hash: hash, Length: 1}) || !made.Matches([]byte("x")) || made.Matches([]byte("y")) || made.Matches([]byte("xx")) {
		t.Fatal("a piece's item is not its hash and its length")
	}
	if _, err := NewFileList([]PieceRef{{Hash: hash, Length: 0}}); err == nil {
		t.Fatal("a file was made with a piece of no bytes")
	}
	// A folder's list: every entry whole, of a known kind, validly named.
	for _, kind := range []byte{1, 2} {
		_, err := DecodeFolderList(entryBytes(kind, 3, hash, "a", noExtra))
		takes(t, fmt.Sprintf("an entry of kind %d", kind), err)
	}
	for _, kind := range []byte{0, 3, 0x10, 0xff} {
		_, err := DecodeFolderList(entryBytes(kind, 3, hash, "a", noExtra))
		refuses(t, fmt.Sprintf("an entry of kind %d", kind), err)
	}
	for _, name := range []string{"", ".", "..", "a/b", "a\x00", "\xff\xfe", strings.Repeat("x", 256)} {
		_, err := DecodeFolderList(entryBytes(1, 3, hash, name, noExtra))
		refuses(t, fmt.Sprintf("an entry named %q", name), err)
		_, err = DecodeOpenFolder(entryBytes(1, 3, hash, name, noExtra))
		refuses(t, fmt.Sprintf("an entry named %q, in a list that is kept open", name), err)
	}
	whole := entryBytes(1, 3, hash, "a", noExtra)
	for cut := 1; cut < len(whole); cut++ {
		_, err := DecodeFolderList(whole[:cut])
		refuses(t, fmt.Sprintf("an entry cut at %d", cut), err)
	}
	_, err := DecodeFolderList(joined(whole, []byte{0}))
	refuses(t, "a byte after the last entry", err)
	// A list's sizes are added up, and a sum that does not fit is refused.
	huge := joined(entryBytes(1, math.MaxUint64, hash, "a", noExtra), entryBytes(1, 1, hash, "b", noExtra))
	if _, err := DecodeFolderList(huge); !errors.Is(err, ErrSizeOverflow) {
		t.Fatalf("sizes that do not fit: %v", err)
	}
	if _, err := DecodeOpenFolder(huge); !errors.Is(err, ErrSizeOverflow) {
		t.Fatalf("sizes that do not fit, in a list that is kept open: %v", err)
	}
}

func TestARecordIsReadStrictly(t *testing.T) {
	top := folderNamed(t, "a", "b").Target()
	totals := Totals{Pieces: 2, PieceBytes: 3, ListBytes: 200}
	extra := must[ExtraData](t)(ExtraData{}.WithText(CuttingKey, "a caller cannot set this"))
	extra = must[ExtraData](t)(extra.WithText(TimestampKey, "2026-11-12T00:00:00Z"))
	record := must[*Record](t)(NewRecord(top, totals, FastCDCV1, extra))
	if tag, _ := record.Extra().Text(CuttingKey); tag != "fastcdc-v1" || record.Cutting() != FastCDCV1 || record.Top() != top || record.Totals() != totals {
		t.Fatalf("the record says %q", tag)
	}
	encoded := record.Encode()
	read := must[*Record](t)(DecodeRecord(encoded))
	if !bytes.Equal(read.Encode(), encoded) || read.Top() != top || read.Totals() != totals || read.Cutting() != FastCDCV1 || !read.Extra().Equal(record.Extra()) {
		t.Fatal("a record does not read back")
	}
	if record.Root() != RootOfRecord(encoded) || len(encoded) > MaxRecordBytes {
		t.Fatal("a record's root is not the hash of its bytes")
	}
	for cut := range len(encoded) {
		_, err := DecodeRecord(encoded[:cut])
		refuses(t, fmt.Sprintf("a record cut at %d", cut), err)
	}
	_, err := DecodeRecord(joined(encoded, []byte{0}))
	refuses(t, "a byte after a record", err)
	changed := func(at int, value byte) []byte {
		out := slices.Clone(encoded)
		out[at] = value
		return out
	}
	_, err = DecodeRecord(changed(0, 0x21))
	refuses(t, "a record under another mark", err)
	for _, kind := range []byte{0, 3, 0xff} {
		_, err = DecodeRecord(changed(1, kind))
		refuses(t, fmt.Sprintf("a record of kind %d", kind), err)
	}
	// A record that does not say how its tree was cut is no record.
	_, err = DecodeRecord(joined(encoded[:66], noExtra))
	refuses(t, "a record that does not say how its tree was cut", err)
	_, err = DecodeRecord(joined(encoded[:66], []byte{0, 1, 0, 8}, []byte("chunking"), []byte{0, 8}, []byte("fixed-2m")))
	refuses(t, "a record that names a way of cutting nobody knows", err)
	_, err = DecodeRecord(joined(encoded[:66], []byte{0, 1, 0, 8}, []byte("chunking"), []byte{0, 2}, []byte{0xff, 0xfe}))
	refuses(t, "a record whose way of cutting is not text", err)
	// Totals that no tree can have, made and read.
	for what, bad := range map[string]Totals{
		"more pieces than piece bytes":                {3, 2, 200},
		"more piece bytes than the tree holds":        {1, top.Size + 1, 200},
		"lists too short to name the pieces":          {2, 3, 71},
		"bytes without pieces":                        {0, 1, 0},
		"more pieces than any list of lists can name": {math.MaxUint64, math.MaxUint64, math.MaxUint64},
	} {
		_, err := NewRecord(top, bad, FastCDCV1, ExtraData{})
		refuses(t, what, err)
		written := oracleRecordBytes(2, top.Size, top.Hash, [3]uint64{bad.Pieces, bad.PieceBytes, bad.ListBytes}, oracleCutTag("fastcdc-v1"))
		_, err = DecodeRecord(written)
		refuses(t, what+", in a record that is read", err)
	}
	// The same record with totals that a tree can have is read.
	good := oracleRecordBytes(2, top.Size, top.Hash, [3]uint64{2, 3, 200}, oracleCutTag("fastcdc-v1"))
	takes(t, "a record written by hand", func() error { _, err := DecodeRecord(good); return err }())
	// A file on top has no folders' lists: its lists are whole items.
	fileTop := must[*FileList](t)(NewFileList([]PieceRef{pieceOf(1, 5)})).Target()
	_, err = NewRecord(fileTop, Totals{Pieces: 1, PieceBytes: 5, ListBytes: 37}, FastCDCV1, ExtraData{})
	refuses(t, "a file's lists that are not whole items", err)
	_, err = NewRecord(fileTop, Totals{Pieces: 1, PieceBytes: 5, ListBytes: 36}, FastCDCV1, ExtraData{})
	takes(t, "a file of one piece", err)
	// One piece holds 8 MiB at most.
	bigTop := Target{Kind: KindFile, Hash: fileTop.Hash, Size: MaxPieceBytes + 1}
	_, err = NewRecord(bigTop, Totals{Pieces: 1, PieceBytes: MaxPieceBytes + 1, ListBytes: 36}, FastCDCV1, ExtraData{})
	refuses(t, "one piece of more than 8 MiB", err)
	_, err = NewRecord(bigTop, Totals{Pieces: 2, PieceBytes: MaxPieceBytes + 1, ListBytes: 72}, FastCDCV1, ExtraData{})
	takes(t, "two pieces of more than 8 MiB together", err)
	// A record is neither a file's nor a folder's when its top is neither.
	_, err = NewRecord(Target{Kind: 3, Hash: top.Hash, Size: top.Size}, totals, FastCDCV1, ExtraData{})
	refuses(t, "a record of a top that is neither a file nor a folder", err)
}

func TestAWayOfCuttingHasOneSpellingAndHoldsPiecesToItsLengths(t *testing.T) {
	fixed := func(size uint32) Cutting { return must[Cutting](t)(FixedCutting(size)) }
	if FastCDCV1.Tag() != "fastcdc-v1" || fixed(16).Tag() != "fixed-16" || (Cutting{}) != FastCDCV1 {
		t.Fatal("a way of cutting is spelled another way")
	}
	for _, cutting := range []Cutting{FastCDCV1, fixed(1), fixed(16), fixed(MaxPieceBytes)} {
		if read, err := CuttingFromTag([]byte(cutting.Tag())); err != nil || read != cutting {
			t.Fatalf("%s does not read back: %v", cutting.Tag(), err)
		}
	}
	for _, tag := range []string{
		"", "fastcdc-v2", "FASTCDC-V1", "fastcdc-v1 ", " fastcdc-v1", "fixed-", "fixed-0", "fixed-016", "fixed-+16", "fixed--16",
		"fixed-16 ", "fixed-2m", "fixed-1e3", "fixed-8388609", "fixed-99999999", "fixed-4294967297", "fixed-18446744073709551617", "Fixed-16",
	} {
		if _, err := CuttingFromTag([]byte(tag)); err == nil {
			t.Fatalf("the tag %q was read", tag)
		}
	}
	if _, err := FixedCutting(0); err == nil {
		t.Fatal("pieces of no bytes")
	}
	if _, err := FixedCutting(MaxPieceBytes + 1); err == nil {
		t.Fatal("pieces of more than 8 MiB")
	}
	if size, isFixed := fixed(16).Fixed(); size != 16 || !isFixed {
		t.Fatal(size, isFixed)
	}
	if _, isFixed := FastCDCV1.Fixed(); isFixed {
		t.Fatal("cutting by content has a fixed size")
	}
	const least, most = FastCDCMinSize, FastCDCMaxSize
	for _, check := range []struct {
		cutting Cutting
		length  uint32
		isLast  bool
		fits    bool
	}{
		{FastCDCV1, least + 1, false, true},
		{FastCDCV1, most, false, true},
		// The cutter never stops there.
		{FastCDCV1, least, false, false},
		{FastCDCV1, 1, false, false},
		{FastCDCV1, most + 1, false, false},
		{FastCDCV1, 0, false, false},
		{FastCDCV1, 1, true, true},
		{FastCDCV1, least, true, true},
		{FastCDCV1, most, true, true},
		{FastCDCV1, most + 1, true, false},
		{FastCDCV1, 0, true, false},
		{fixed(16), 16, false, true},
		{fixed(16), 15, false, false},
		{fixed(16), 17, false, false},
		{fixed(16), 0, false, false},
		{fixed(16), 1, true, true},
		{fixed(16), 16, true, true},
		{fixed(16), 17, true, false},
		{fixed(16), 0, true, false},
	} {
		if err := check.cutting.CheckLength(check.length, check.isLast); (err == nil) != check.fits {
			t.Fatalf("%s, a piece of %d, the last: %v: %v", check.cutting.Tag(), check.length, check.isLast, err)
		}
	}
}

func TestAFilesSizeBoundsHowManyPiecesItCanHave(t *testing.T) {
	const step, most = uint64(FastCDCMinSize) + 1, uint64(FastCDCMaxSize)
	content := FastCDCV1
	for _, bound := range []struct{ size, fewest, most uint64 }{
		{0, 0, 0},
		{1, 1, 1},
		{step, 1, 1},
		{step + 1, 1, 2},
		{most, 1, 16},
		{most + 1, 2, 16},
		{2*step + 1, 1, 3},
		// A terabyte: at most about two million pieces, never a billion.
		// The most is 2,097,148 pieces of 524,289 bytes, and a last one
		// of 4.
		{1 << 40, 131_072, 2_097_149},
	} {
		if fewest, most := content.CountBounds(bound.size); fewest != bound.fewest || most != bound.most {
			t.Fatalf("a file of %d bytes has %d to %d pieces", bound.size, fewest, most)
		}
	}
	if 2_097_148*step+4 != 1<<40 {
		t.Fatal("the sum is wrong")
	}
	takes(t, "a terabyte in 500,000 pieces", content.CheckCount(1<<40, 500_000))
	takes(t, "a terabyte in the most pieces", content.CheckCount(1<<40, 2_097_149))
	takes(t, "a terabyte in the fewest pieces", content.CheckCount(1<<40, 131_072))
	refuses(t, "a terabyte in one piece more than the most", content.CheckCount(1<<40, 2_097_150))
	refuses(t, "a terabyte in one piece fewer than the fewest", content.CheckCount(1<<40, 131_071))
	refuses(t, "an empty file of one piece", content.CheckCount(0, 1))
	refuses(t, "a file of one byte in no pieces", content.CheckCount(1, 0))
	// The widest size there is does not wrap.
	if fewest, most := content.CountBounds(math.MaxUint64); fewest != (math.MaxUint64-1)/most0()+1 || most <= fewest {
		t.Fatalf("the widest size has %d to %d pieces", fewest, most)
	}
	// What the bounds say is what lists of fitting pieces can add up to.
	fixed := must[Cutting](t)(FixedCutting(4))
	for size := range uint64(40) {
		pieces := uint64(len(fixed.Cut(make([]byte, size))))
		if fewest, most := fixed.CountBounds(size); fewest != pieces || most != pieces {
			t.Fatalf("%d bytes in pieces of 4 are %d pieces, and the bounds say %d to %d", size, pieces, fewest, most)
		}
	}
	list := must[*FileList](t)(NewFileList([]PieceRef{pieceOf(1, 4), pieceOf(2, 4), pieceOf(3, 3)}))
	takes(t, "pieces of 4, 4 and 3 as cut at 4", list.CheckCutting(fixed))
	refuses(t, "pieces of 4, 4 and 3 as cut at 5", list.CheckCutting(must[Cutting](t)(FixedCutting(5))))
	refuses(t, "pieces of 4, 4 and 3 as cut by content", list.CheckCutting(content))
	shortInTheMiddle := must[*FileList](t)(NewFileList([]PieceRef{pieceOf(1, 4), pieceOf(2, 3), pieceOf(3, 4)}))
	refuses(t, "a short piece in the middle", shortInTheMiddle.CheckCutting(fixed))
	shortAtTheStart := must[*FileList](t)(NewFileList([]PieceRef{pieceOf(1, 3), pieceOf(2, 4)}))
	refuses(t, "a short piece at the start", shortAtTheStart.CheckCutting(fixed))
	takes(t, "the empty file", must[*FileList](t)(NewFileList(nil)).CheckCutting(content))
	takes(t, "the empty file, cut at 4", must[*FileList](t)(NewFileList(nil)).CheckCutting(fixed))
}

// most0 is the most bytes a piece cut by content holds.
func most0() uint64 { return FastCDCMaxSize }

func TestEveryRunOfAFileChecksAndGivesTheByteItStartsAt(t *testing.T) {
	for count := 1; count <= 17; count++ {
		items := make([]PieceRef, count)
		for index := range items {
			items[index] = pieceOf(byte(index), uint32(index%5)+1)
		}
		list := must[*FileList](t)(NewFileList(items))
		open := must[*OpenFile](t)(NewOpenFile(list))
		read := must[*OpenFile](t)(DecodeOpenFile(list.Encode()))
		if read.List().Hash() != list.Hash() || open.List() != list {
			t.Fatal("a list that is kept open is another list")
		}
		target := list.Target()
		var before uint64
		for start := range uint64(count) {
			for end := start + 1; end <= uint64(count); end++ {
				what := fmt.Sprintf("%d pieces, %d up to %d", count, start, end)
				run := must[*FileRun](t)(open.Open(start, end))
				encoded := run.Encode()
				if !bytes.Equal(encoded, must[*FileRun](t)(list.Open(start, end)).Encode()) || !bytes.Equal(encoded, must[*FileRun](t)(read.Open(start, end)).Encode()) {
					t.Fatalf("%s: a list that is kept open shows another run", what)
				}
				back := must[*FileRun](t)(DecodeFileRun(encoded))
				if !bytes.Equal(back.Encode(), encoded) || back.Count() != uint64(count) || back.Start() != start {
					t.Fatalf("%s: the run does not read back", what)
				}
				if at, err := back.Check(target); err != nil || at != before {
					t.Fatalf("%s: the run starts at byte %d, and not %d (%v)", what, at, before, err)
				}
				if !slices.Equal(run.Items(), items[start:end]) {
					t.Fatalf("%s: the run shows other pieces", what)
				}
			}
			if at, found := open.StartOf(start); !found || at != before {
				t.Fatalf("piece %d of %d starts at %d", start, count, at)
			}
			before += uint64(items[start].Length)
		}
		if at, found := open.StartOf(uint64(count)); !found || at != list.Size() || before != list.Size() {
			t.Fatalf("the end of %d pieces is at %d", count, at)
		}
		if _, found := open.StartOf(uint64(count) + 1); found {
			t.Fatal("a piece after the end has a start")
		}
	}
}

func TestARunOfAFileIsRefusedForAnyOtherFileCountOrPlace(t *testing.T) {
	items := make([]PieceRef, 9)
	for index := range items {
		items[index] = pieceOf(byte(index), 4)
	}
	list := must[*FileList](t)(NewFileList(items))
	target := list.Target()
	run := must[*FileRun](t)(list.Open(3, 5))
	if at, err := run.Check(target); err != nil || at != 12 {
		t.Fatal(at, err)
	}
	// Another file, another size, and a folder.
	if _, err := run.Check(smallFile(t, 1)); !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("another file: %v", err)
	}
	if _, err := run.Check(Target{Kind: KindFile, Hash: HashOf([]byte("other")), Size: target.Size}); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("another hash: %v", err)
	}
	if _, err := run.Check(Target{Kind: KindFile, Hash: target.Hash, Size: target.Size + 1}); !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("another size: %v", err)
	}
	if _, err := run.Check(Target{Kind: KindFolder, Hash: target.Hash, Size: target.Size}); err == nil {
		t.Fatal("a run of a file was taken for a folder")
	}
	// The same bytes with another count, another place, or one item less.
	encoded := run.Encode()
	with := func(at int, value byte) error {
		changed := slices.Clone(encoded)
		changed[at] = value
		run, err := DecodeFileRun(changed)
		if err != nil {
			return err
		}
		_, err = run.Check(target)
		return err
	}
	last := len(encoded) - 1
	refuses(t, "the run as of a file of 10 pieces", with(7, 10))
	refuses(t, "the run as of a file of 8 pieces", with(7, 8))
	refuses(t, "the run one place on", with(15, 4))
	refuses(t, "the run one place back", with(15, 2))
	refuses(t, "the run with one item less", with(19, 1))
	refuses(t, "the run with no items", with(19, 0))
	refuses(t, "the run with another piece", with(20, encoded[20]^1))
	refuses(t, "the run with another length for a piece", with(20+35, encoded[20+35]^1))
	refuses(t, "the run with another size for a carried node", with(last, encoded[last]^1))
	takes(t, "the run as it is", with(7, 9))
	// Every single byte of it counts.
	for at := range encoded {
		refuses(t, fmt.Sprintf("the run with byte %d changed", at), with(at, encoded[at]^0x80))
	}
	for cut := range len(encoded) {
		_, err := DecodeFileRun(encoded[:cut])
		refuses(t, fmt.Sprintf("a run cut at %d", cut), err)
	}
	_, err := DecodeFileRun(joined(encoded, []byte{0}))
	refuses(t, "a byte after a run", err)
	// The pieces' lengths are held to the way of cutting, when one is given.
	if _, err := run.CheckWithCutting(target, must[Cutting](t)(FixedCutting(4))); err != nil {
		t.Fatal(err)
	}
	if _, err := run.CheckWithCutting(target, must[Cutting](t)(FixedCutting(5))); err == nil {
		t.Fatal("pieces of 4 were taken as cut at 5")
	}
	if _, err := run.CheckWithCutting(target, FastCDCV1); err == nil {
		t.Fatal("pieces of 4 were taken as cut by content")
	}
	// A run that claims more carried nodes than any list needs.
	crowded := joined(encoded[:20+2*ListItemBytes], be16(81), make([]byte, 81*40))
	_, err = DecodeFileRun(crowded)
	refuses(t, "a run that carries 81 nodes", err)
	// A run that claims more items than it has bytes for.
	hollow := joined(encoded[:16], be32(math.MaxUint32))
	_, err = DecodeFileRun(hollow)
	refuses(t, "a run that claims more items than it has bytes for", err)
	// And one that claims a list longer than any list.
	endless := joined(be64(MaxListItems+1), be64(3), be32(2), encoded[20:])
	_, err = DecodeFileRun(endless)
	refuses(t, "a run of a list longer than any list", err)
}

func TestThePiecesThatHoldAStretchOfBytesAreFound(t *testing.T) {
	lengths := []uint32{3, 1, 4, 1, 5, 9, 2, 6}
	items := make([]PieceRef, len(lengths))
	// Which piece holds each byte, worked out the long way.
	var holder []uint64
	for index, length := range lengths {
		items[index] = pieceOf(byte(index), length)
		for range length {
			holder = append(holder, uint64(index))
		}
	}
	list := must[*FileList](t)(NewFileList(items))
	open := must[*OpenFile](t)(NewOpenFile(list))
	size := list.Size()
	if size != 31 {
		t.Fatal(size)
	}
	for offset := range size {
		for length := uint64(1); length <= size-offset; length++ {
			first, after, err := open.PiecesFor(offset, length)
			if err != nil || first != holder[offset] || after != holder[offset+length-1]+1 {
				t.Fatalf("%d bytes from %d are in pieces %d up to %d (%v)", length, offset, first, after, err)
			}
			run := must[*FileRun](t)(open.OpenBytes(offset, length))
			startsAt := must[uint64](t)(run.Check(list.Target()))
			var shown uint64
			for _, item := range run.Items() {
				shown += uint64(item.Length)
			}
			if startsAt > offset || startsAt+shown < offset+length {
				t.Fatalf("%d bytes from %d: the run shows %d bytes from %d", length, offset, shown, startsAt)
			}
			// No piece is shown that holds none of the bytes.
			if first != run.Start() || after-first != uint64(len(run.Items())) {
				t.Fatalf("%d bytes from %d: the run shows more pieces than hold them", length, offset)
			}
		}
	}
	for _, outside := range [][2]uint64{{0, 0}, {size, 1}, {size - 1, 2}, {math.MaxUint64, 2}, {1, math.MaxUint64}, {size, 0}} {
		if _, _, err := open.PiecesFor(outside[0], outside[1]); err == nil {
			t.Fatalf("%d bytes from %d were found", outside[1], outside[0])
		}
		if _, err := open.OpenBytes(outside[0], outside[1]); err == nil {
			t.Fatalf("%d bytes from %d were shown", outside[1], outside[0])
		}
	}
	empty := must[*OpenFile](t)(NewOpenFile(must[*FileList](t)(NewFileList(nil))))
	if _, _, err := empty.PiecesFor(0, 1); err == nil {
		t.Fatal("a byte of the empty file was found")
	}
	if at, found := empty.StartOf(0); !found || at != 0 {
		t.Fatal("the empty file does not end at byte 0")
	}
}

func TestEveryRunOfAFolderChecksAndReadsBack(t *testing.T) {
	names := make([]string, 13)
	for index := range names {
		names[index] = fmt.Sprintf("n%02d", index)
	}
	other := folderNamed(t, "other").Target()
	for count := 1; count <= len(names); count++ {
		list := folderNamed(t, names[:count]...)
		open := must[*OpenFolder](t)(NewOpenFolder(list))
		read := must[*OpenFolder](t)(DecodeOpenFolder(list.Encode()))
		if read.List().Hash() != list.Hash() || open.List() != list {
			t.Fatal("a list that is kept open is another list")
		}
		target := list.Target()
		for start := range uint64(count) {
			for end := start + 1; end <= uint64(count); end++ {
				what := fmt.Sprintf("%d entries, %d up to %d", count, start, end)
				run := must[*FolderRun](t)(open.Open(start, end))
				encoded := run.Encode()
				if !bytes.Equal(encoded, must[*FolderRun](t)(list.Open(start, end)).Encode()) || !bytes.Equal(encoded, must[*FolderRun](t)(read.Open(start, end)).Encode()) {
					t.Fatalf("%s: a list that is kept open shows another run", what)
				}
				back := must[*FolderRun](t)(DecodeFolderRun(encoded))
				if !bytes.Equal(back.Encode(), encoded) || back.Count() != uint64(count) || back.Start() != start {
					t.Fatalf("%s: the run does not read back", what)
				}
				takes(t, what, back.Check(target))
				if !slices.EqualFunc(back.Entries(), list.Entries()[start:end], Entry.Equal) {
					t.Fatalf("%s: the run shows other entries", what)
				}
				refuses(t, what+", for another folder", back.Check(other))
				refuses(t, what+", for a file", back.Check(Target{Kind: KindFile, Hash: target.Hash, Size: target.Size}))
				refuses(t, what+", for a folder of another size", back.Check(Target{Kind: KindFolder, Hash: target.Hash, Size: target.Size + 1}))
			}
		}
	}
	// A run is read whole, or not at all.
	list := folderNamed(t, names[:9]...)
	encoded := must[*FolderRun](t)(list.Open(3, 5)).Encode()
	for cut := range len(encoded) {
		_, err := DecodeFolderRun(encoded[:cut])
		refuses(t, fmt.Sprintf("a run cut at %d", cut), err)
	}
	_, err := DecodeFolderRun(joined(encoded, []byte{0}))
	refuses(t, "a byte after a run", err)
	// The carried node that begins the list comes with no name, and every
	// other one with a name: a run is read in one way only. Here each entry
	// takes 48 bytes, and a carried node's name follows its 40 bytes.
	nameAt := 20 + 2*48 + 2 + 40
	if !bytes.Equal(encoded[nameAt:nameAt+2], []byte{0, 0}) {
		t.Fatalf("the node that begins the list is carried with a name of %d bytes", encoded[nameAt+1])
	}
	_, err = DecodeFolderRun(joined(encoded[:nameAt], []byte{0, 3}, []byte("n00"), encoded[nameAt+2:]))
	refuses(t, "a name for the carried node that begins the list", err)
	fromTheStart := must[*FolderRun](t)(list.Open(0, 2)).Encode()
	if !bytes.Equal(fromTheStart[nameAt:nameAt+5], joined([]byte{0, 3}, []byte("n02"))) {
		t.Fatalf("the first carried node of a run from the start is carried as % x", fromTheStart[nameAt:nameAt+5])
	}
	_, err = DecodeFolderRun(joined(fromTheStart[:nameAt], []byte{0, 0}, fromTheStart[nameAt+5:]))
	refuses(t, "no name for a carried node that is not the one that begins the list", err)
}

func TestANameIsShownToBeThereOrNotThereByItsOnePlace(t *testing.T) {
	names := []string{"b", "d", "f", "h", "j", "l", "n"}
	asked := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o", "b0", "B"}
	for count := 1; count <= len(names); count++ {
		list := folderNamed(t, names[:count]...)
		open := must[*OpenFolder](t)(NewOpenFolder(list))
		target := list.Target()
		for _, name := range asked {
			what := fmt.Sprintf("%d entries, %q", count, name)
			place, hasPlace := list.PlaceOf(name)
			truth, there := list.Get(name)
			if !hasPlace {
				t.Fatalf("%s has no place", what)
			}
			// The answer the folder gives.
			answer := must[*FolderRun](t)(list.OpenName(name))
			encoded := answer.Encode()
			if !bytes.Equal(encoded, must[*FolderRun](t)(open.OpenName(name)).Encode()) {
				t.Fatalf("%s: a list that is kept open gives another answer", what)
			}
			back := must[*FolderRun](t)(DecodeFolderRun(encoded))
			if got, found, err := back.CheckName(target, name); err != nil || found != there || (there && !got.Equal(truth)) {
				t.Fatalf("%s: found: %v, and it is there: %v (%v)", what, found, there, err)
			}
			// Any other entry shown in its place is refused, so there is
			// one answer, and it is the true one.
			for other := range uint64(count) {
				shown := must[*FolderRun](t)(list.Open(other, other+1))
				got, found, err := shown.CheckName(target, name)
				if other != place {
					refuses(t, fmt.Sprintf("%s, answered at %d", what, other), err)
				} else if err != nil || found != there || (there && !got.Equal(truth)) {
					t.Fatalf("%s at its place %d: found: %v, and it is there: %v (%v)", what, other, found, there, err)
				}
			}
			// A run of two entries is no answer for one name.
			if count >= 2 {
				_, _, err := must[*FolderRun](t)(list.Open(0, 2)).CheckName(target, name)
				refuses(t, what+", answered by two entries", err)
			}
			// The true answer for another folder is no answer for this one.
			_, _, err := back.CheckName(folderNamed(t, "other").Target(), name)
			refuses(t, what+", answered for another folder", err)
			_, _, err = back.CheckName(Target{Kind: KindFile, Hash: target.Hash, Size: target.Size}, name)
			refuses(t, what+", answered for a file", err)
		}
		_, err := list.OpenName("a/b")
		refuses(t, "a name with a slash", err)
		_, _, err = must[*FolderRun](t)(list.Open(0, 1)).CheckName(target, "")
		refuses(t, "the empty name", err)
	}
}

func TestAnEmptyFolderShowsThatItHoldsNoName(t *testing.T) {
	empty := must[*FolderList](t)(NewFolderList(nil))
	if _, hasPlace := empty.PlaceOf("a"); hasPlace {
		t.Fatal("the empty folder has a place for a name")
	}
	answer := must[*FolderRun](t)(empty.OpenName("a"))
	encoded := answer.Encode()
	if len(encoded) != 22 {
		t.Fatalf("the answer of an empty folder takes %d bytes", len(encoded))
	}
	back := must[*FolderRun](t)(DecodeFolderRun(encoded))
	if !bytes.Equal(back.Encode(), encoded) {
		t.Fatal("the answer does not read back")
	}
	if _, found, err := back.CheckName(empty.Target(), "a"); err != nil || found {
		t.Fatal(found, err)
	}
	takes(t, "the run of an empty folder", back.Check(empty.Target()))
	// The same answer for a folder that is not empty is refused.
	full := folderNamed(t, "a")
	_, _, err := back.CheckName(full.Target(), "a")
	refuses(t, "an empty folder's answer for a folder with entries", err)
	refuses(t, "an empty folder's run for a folder with entries", back.Check(full.Target()))
	// And a run that shows nothing of a folder with entries is no run.
	_, err = DecodeFolderRun(joined(be64(3), make([]byte, 8+4+2)))
	refuses(t, "a run that shows nothing of a folder with entries", err)
	_, err = DecodeFolderRun(joined(be64(0), make([]byte, 8+4), []byte{0, 1}, make([]byte, 40), []byte{0, 1, 'a'}))
	refuses(t, "a run of an empty folder that carries a node", err)
	// A run of an empty folder that starts somewhere is no run either.
	_, err = DecodeFolderRun(joined(be64(0), be64(1), make([]byte, 4+2)))
	refuses(t, "a run of an empty folder that starts at a place", err)
}
