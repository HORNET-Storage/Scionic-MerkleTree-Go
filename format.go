package scionic

import (
	"crypto/sha256"
	"encoding/binary"
	"iter"
	"math/bits"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The things a tree is made of, and their exact bytes.
//
//   - A piece is a run of a file's bytes. Its hash is the SHA-256 of those
//     bytes, and nothing else.
//   - A file is the list of its pieces, in the file's order.
//   - A folder is the list of its entries, sorted by name. An entry holds a
//     name, and the file or folder under it.
//   - The record says what a tree's root stands for.
//
// A thing's hash covers what the thing is, and its parent's hash covers where
// it sits. So a file has no name of its own and a piece has no number of its
// own: the name is in the folder, and the number is the piece's place in the
// file's list.
//
// A value of one of these types is always well formed. It is checked when it
// is made and when it is read from bytes, and reading is strict: bytes that
// could be written in two ways are read in one way only.

const (
	// MaxPieceBytes is the most bytes a piece may hold: 8 MiB.
	MaxPieceBytes = FastCDCMaxSize
	// ListItemBytes is how many bytes one item of a file's list takes: a
	// hash and a length.
	ListItemBytes = 36
	// MaxNameBytes is the most bytes a name may hold.
	MaxNameBytes = 255
	// MaxExtraValues is the most values a set of extra data may hold.
	MaxExtraValues = 32
	// MaxExtraNameBytes is the most bytes the name of an extra value may
	// hold.
	MaxExtraNameBytes = 64
	// MaxExtraValueBytes is the most bytes an extra value may hold.
	MaxExtraValueBytes = 1024
	// MaxFolderDepth is the most folders that may sit inside one another,
	// the top one counted.
	MaxFolderDepth = 128
	// MaxCarriedNodes is the most nodes an opening may carry: 40 on each
	// side of what it shows.
	MaxCarriedNodes = 80
	// MaxExtraBytes is the most bytes a set of extra data may take: how
	// many values it has, and 32 values that are as long as they can be.
	MaxExtraBytes = 2 + MaxExtraValues*(4+MaxExtraNameBytes+MaxExtraValueBytes)
	// MaxRecordBytes is the most bytes a record may take: its fixed part,
	// and extra data that is as full as it can be.
	MaxRecordBytes = 66 + MaxExtraBytes
	// MaxEntryBytes is the most bytes a folder's entry may take: its fixed
	// part, the longest name, and extra data that is as full as it can be.
	MaxEntryBytes = 43 + MaxNameBytes + MaxExtraBytes
	// CuttingKey names the extra value of a record that says how the tree's
	// files were cut.
	CuttingKey = "chunking"
	// TimestampKey names the extra value of a record that says when the
	// tree was built.
	TimestampKey = "timestamp"
	// CuttingFastCDCV1 is how a tree says that its files were cut by
	// content.
	CuttingFastCDCV1 = "fastcdc-v1"

	cuttingFixedPrefix = "fixed-"

	fileLeafMark   = 0x00
	folderLeafMark = 0x02
	fileMark       = 0x10
	folderMark     = 0x11
	recordMark     = 0x20

	// The least bytes an entry is taken to need when a count is held to
	// the bytes that are there.
	leastEntryBytes = 45
)

// reader reads numbers and runs of bytes off the front of a slice. Once a
// read fails, err is set, and every later read gives zero values: a caller
// looks at err before it trusts what it read.
type reader struct {
	bytes []byte
	what  string
	err   error
}

func (r *reader) take(count int) []byte {
	if r.err != nil {
		return nil
	}
	if count < 0 || len(r.bytes) < count {
		r.err = invalid(r.what + " is cut short")
		return nil
	}
	head := r.bytes[:count:count]
	r.bytes = r.bytes[count:]
	return head
}

func (r *reader) u8() uint8 {
	if taken := r.take(1); len(taken) == 1 {
		return taken[0]
	}
	return 0
}

func (r *reader) u16() uint16 {
	if taken := r.take(2); len(taken) == 2 {
		return binary.BigEndian.Uint16(taken)
	}
	return 0
}

func (r *reader) u32() uint32 {
	if taken := r.take(4); len(taken) == 4 {
		return binary.BigEndian.Uint32(taken)
	}
	return 0
}

func (r *reader) u64() uint64 {
	if taken := r.take(8); len(taken) == 8 {
		return binary.BigEndian.Uint64(taken)
	}
	return 0
}

func (r *reader) hash() Hash {
	var hash Hash
	if taken := r.take(len(hash)); len(taken) == len(hash) {
		copy(hash[:], taken)
	}
	return hash
}

func (r *reader) remaining() int {
	return len(r.bytes)
}

func (r *reader) isEmpty() bool {
	return len(r.bytes) == 0
}

// finish: everything must have been read.
func (r *reader) finish() error {
	if r.err != nil {
		return r.err
	}
	if len(r.bytes) != 0 {
		return invalid(r.what + " has bytes after its end")
	}
	return nil
}

// Kind says whether an entry is a file or a folder.
type Kind uint8

const (
	// KindFile is a file: a list of pieces.
	KindFile Kind = 1
	// KindFolder is a folder: a list of entries.
	KindFolder Kind = 2
)

func (k Kind) String() string {
	switch k {
	case KindFile:
		return "file"
	case KindFolder:
		return "folder"
	}
	return "neither a file nor a folder"
}

func kindFromByte(value byte) (Kind, error) {
	if kind := Kind(value); kind == KindFile || kind == KindFolder {
		return kind, nil
	}
	return 0, invalid("an entry is neither a file nor a folder")
}

// Target is a file or a folder as the thing above it names it: its kind,
// its hash, and how many bytes of file content it holds.
type Target struct {
	Kind Kind
	Hash Hash
	Size uint64
}

func (t Target) require(kind Kind) error {
	if t.Kind == kind {
		return nil
	}
	if kind == KindFile {
		return invalid("a folder was given where a file is needed")
	}
	return invalid("a file was given where a folder is needed")
}

// CheckName checks a name: 1 to 255 bytes of UTF-8, no "/", no zero byte,
// and not "." or "..".
func CheckName(name string) error {
	var reason string
	switch {
	case name == "":
		reason = "it is empty"
	case len(name) > MaxNameBytes:
		reason = "it is longer than 255 bytes"
	case name == "." || name == "..":
		reason = "it is a dot name"
	case strings.IndexByte(name, '/') >= 0:
		reason = "it holds a slash"
	case strings.IndexByte(name, 0) >= 0:
		reason = "it holds a zero byte"
	case !utf8.ValidString(name):
		reason = "it is not UTF-8"
	default:
		return nil
	}
	return &Error{kind: kindInvalidName, Name: first80(name), Reason: reason}
}

// ExtraData is a small set of named values, kept beside a name in a folder
// or in a tree's record. It holds at most 32 values. A value's name is 1 to
// 64 bytes of UTF-8, and a value is at most 1,024 bytes.
//
// A set is never changed once it is made: With and Without give another set.
// The zero value is the empty set.
type ExtraData struct {
	// pairs are in the order of their names' bytes, with no name twice.
	pairs []extraPair
}

// A value is kept as a string, which cannot be changed, whatever its bytes
// are.
type extraPair struct {
	name  string
	value string
}

func (e ExtraData) find(name string) (int, bool) {
	at := sort.Search(len(e.pairs), func(index int) bool { return e.pairs[index].name >= name })
	return at, at < len(e.pairs) && e.pairs[at].name == name
}

// With gives the same set with one more value, in place of any value of the
// same name.
func (e ExtraData) With(name string, value []byte) (ExtraData, error) {
	return e.with(name, string(value))
}

// WithText is With for a value that is text.
func (e ExtraData) WithText(name, value string) (ExtraData, error) {
	return e.with(name, value)
}

func (e ExtraData) with(name, value string) (ExtraData, error) {
	if name == "" || len(name) > MaxExtraNameBytes {
		return ExtraData{}, invalid("the name of an extra value is not 1 to 64 bytes")
	}
	if !utf8.ValidString(name) {
		return ExtraData{}, invalid("the name of an extra value is not UTF-8")
	}
	if len(value) > MaxExtraValueBytes {
		return ExtraData{}, invalid("an extra value is longer than 1,024 bytes")
	}
	at, found := e.find(name)
	if !found && len(e.pairs) >= MaxExtraValues {
		return ExtraData{}, invalid("extra data holds more than 32 values")
	}
	pairs := make([]extraPair, 0, len(e.pairs)+1)
	pairs = append(pairs, e.pairs[:at]...)
	pairs = append(pairs, extraPair{name, value})
	if found {
		at++
	}
	pairs = append(pairs, e.pairs[at:]...)
	return ExtraData{pairs}, nil
}

// Without gives the same set without the value of this name.
func (e ExtraData) Without(name string) ExtraData {
	at, found := e.find(name)
	if !found {
		return e
	}
	pairs := make([]extraPair, 0, len(e.pairs)-1)
	pairs = append(pairs, e.pairs[:at]...)
	pairs = append(pairs, e.pairs[at+1:]...)
	return ExtraData{pairs}
}

// Get gives a copy of the value of this name.
func (e ExtraData) Get(name string) ([]byte, bool) {
	at, found := e.find(name)
	if !found {
		return nil, false
	}
	return []byte(e.pairs[at].value), true
}

// Text gives a value as text, when it is UTF-8.
func (e ExtraData) Text(name string) (string, bool) {
	at, found := e.find(name)
	if !found || !utf8.ValidString(e.pairs[at].value) {
		return "", false
	}
	return e.pairs[at].value, true
}

// raw gives a value's bytes as a string, whatever they are.
func (e ExtraData) raw(name string) (string, bool) {
	at, found := e.find(name)
	if !found {
		return "", false
	}
	return e.pairs[at].value, true
}

// Len is how many values the set holds.
func (e ExtraData) Len() int {
	return len(e.pairs)
}

// All gives the values, in the order of their names' bytes. Each value is a
// copy.
func (e ExtraData) All() iter.Seq2[string, []byte] {
	return func(yield func(string, []byte) bool) {
		for _, pair := range e.pairs {
			if !yield(pair.name, []byte(pair.value)) {
				return
			}
		}
	}
}

// Equal reports whether two sets hold the same values under the same names.
func (e ExtraData) Equal(other ExtraData) bool {
	if len(e.pairs) != len(other.pairs) {
		return false
	}
	for at := range e.pairs {
		if e.pairs[at] != other.pairs[at] {
			return false
		}
	}
	return true
}

func (e ExtraData) encodedLen() int {
	length := 2
	for _, pair := range e.pairs {
		length += 4 + len(pair.name) + len(pair.value)
	}
	return length
}

func (e ExtraData) appendTo(out []byte) []byte {
	out = binary.BigEndian.AppendUint16(out, uint16(len(e.pairs)))
	for _, pair := range e.pairs {
		out = binary.BigEndian.AppendUint16(out, uint16(len(pair.name)))
		out = append(out, pair.name...)
		out = binary.BigEndian.AppendUint16(out, uint16(len(pair.value)))
		out = append(out, pair.value...)
	}
	return out
}

func decodeExtraData(r *reader) (ExtraData, error) {
	count := int(r.u16())
	if r.err != nil {
		return ExtraData{}, r.err
	}
	if count > MaxExtraValues {
		return ExtraData{}, invalid("extra data holds more than 32 values")
	}
	if count == 0 {
		return ExtraData{}, nil
	}
	pairs := make([]extraPair, 0, count)
	for index := 0; index < count; index++ {
		nameLen := int(r.u16())
		if r.err != nil {
			return ExtraData{}, r.err
		}
		if nameLen == 0 || nameLen > MaxExtraNameBytes {
			return ExtraData{}, invalid("the name of an extra value is not 1 to 64 bytes")
		}
		name := r.take(nameLen)
		if r.err != nil {
			return ExtraData{}, r.err
		}
		if index > 0 && pairs[index-1].name >= string(name) {
			return ExtraData{}, invalid("extra values are not in the order of their names, or a name is there twice")
		}
		if !utf8.Valid(name) {
			return ExtraData{}, invalid("the name of an extra value is not UTF-8")
		}
		valueLen := int(r.u16())
		if r.err != nil {
			return ExtraData{}, r.err
		}
		if valueLen > MaxExtraValueBytes {
			return ExtraData{}, invalid("an extra value is longer than 1,024 bytes")
		}
		value := r.take(valueLen)
		if r.err != nil {
			return ExtraData{}, r.err
		}
		pairs = append(pairs, extraPair{string(name), string(value)})
	}
	return ExtraData{pairs}, nil
}

// PieceRef is one item of a file's list: a piece's hash, and how many bytes
// it holds.
type PieceRef struct {
	Hash   Hash
	Length uint32
}

// PieceRefOf is the item for a piece with these bytes. A piece holds 1 byte
// to 8 MiB.
func PieceRefOf(bytes []byte) (PieceRef, error) {
	if len(bytes) == 0 || len(bytes) > MaxPieceBytes {
		return PieceRef{}, invalid("a piece does not hold 1 byte to 8 MiB")
	}
	return PieceRef{Hash: HashOf(bytes), Length: uint32(len(bytes))}, nil
}

func (p PieceRef) check() error {
	if p.Length == 0 || p.Length > MaxPieceBytes {
		return invalid("a piece does not hold 1 byte to 8 MiB")
	}
	return nil
}

// Matches reports whether bytes are the piece this item names: of its
// length, and with its hash.
func (p PieceRef) Matches(bytes []byte) bool {
	return uint64(len(bytes)) == uint64(p.Length) && HashOf(bytes) == p.Hash
}

func (p PieceRef) appendTo(out []byte) []byte {
	out = append(out, p.Hash[:]...)
	return binary.BigEndian.AppendUint32(out, p.Length)
}

func decodePieceRef(r *reader) (PieceRef, error) {
	item := PieceRef{Hash: r.hash(), Length: r.u32()}
	if r.err != nil {
		return PieceRef{}, r.err
	}
	if err := item.check(); err != nil {
		return PieceRef{}, err
	}
	return item, nil
}

// leaf is the item's leaf in its file's tree.
func (p PieceRef) leaf() node {
	var message [1 + ListItemBytes]byte
	message[0] = fileLeafMark
	copy(message[1:33], p.Hash[:])
	binary.BigEndian.PutUint32(message[33:], p.Length)
	return node{hash: sha256.Sum256(message[:]), size: uint64(p.Length)}
}

// Entry is one entry of a folder: a name, the file or folder under it, and
// extra data. An entry is made by NewEntry, or read from a folder's list:
// the zero value is not an entry.
type Entry struct {
	name   string
	target Target
	extra  ExtraData
}

// NewEntry makes an entry. The name is checked as CheckName checks it.
func NewEntry(name string, target Target, extra ExtraData) (Entry, error) {
	if err := CheckName(name); err != nil {
		return Entry{}, err
	}
	if _, err := kindFromByte(byte(target.Kind)); err != nil {
		return Entry{}, err
	}
	return Entry{name: name, target: target, extra: extra}, nil
}

// Name is the entry's name.
func (e Entry) Name() string {
	return e.name
}

// Kind says whether a file or a folder is under the entry.
func (e Entry) Kind() Kind {
	return e.target.Kind
}

// Size is how many bytes of file content are under the entry.
func (e Entry) Size() uint64 {
	return e.target.Size
}

// Hash is the hash of the file or folder under the entry.
func (e Entry) Hash() Hash {
	return e.target.Hash
}

// Target is the file or folder under the entry.
func (e Entry) Target() Target {
	return e.target
}

// Extra is the entry's extra data.
func (e Entry) Extra() ExtraData {
	return e.extra
}

// EncodedLen is how many bytes the entry takes in its folder's list.
func (e Entry) EncodedLen() int {
	return 1 + 8 + 32 + 2 + len(e.name) + e.extra.encodedLen()
}

// Equal reports whether two entries are the same in everything.
func (e Entry) Equal(other Entry) bool {
	return e.name == other.name && e.target == other.target && e.extra.Equal(other.extra)
}

func (e Entry) appendTo(out []byte) []byte {
	out = append(out, byte(e.target.Kind))
	out = binary.BigEndian.AppendUint64(out, e.target.Size)
	out = append(out, e.target.Hash[:]...)
	out = binary.BigEndian.AppendUint16(out, uint16(len(e.name)))
	out = append(out, e.name...)
	return e.extra.appendTo(out)
}

func decodeEntry(r *reader) (Entry, error) {
	kindByte := r.u8()
	if r.err != nil {
		return Entry{}, r.err
	}
	kind, err := kindFromByte(kindByte)
	if err != nil {
		return Entry{}, err
	}
	size := r.u64()
	hash := r.hash()
	nameLen := int(r.u16())
	nameBytes := r.take(nameLen)
	if r.err != nil {
		return Entry{}, r.err
	}
	if !utf8.Valid(nameBytes) {
		return Entry{}, invalid("a name is not UTF-8")
	}
	name := string(nameBytes)
	if err := CheckName(name); err != nil {
		return Entry{}, err
	}
	extra, err := decodeExtraData(r)
	if err != nil {
		return Entry{}, err
	}
	return Entry{name: name, target: Target{Kind: kind, Hash: hash, Size: size}, extra: extra}, nil
}

// leafWith is the entry's leaf in its folder's tree. It writes the entry
// into room, and gives room back for the next entry.
func (e Entry) leafWith(room []byte) (node, []byte) {
	room = append(room[:0], folderLeafMark)
	room = e.appendTo(room)
	return node{hash: sha256.Sum256(room), size: e.target.Size}, room
}

// leavesOf gives the leaves of a folder's entries, with their names.
func leavesOf(entries []Entry) []leaf {
	leaves := make([]leaf, len(entries))
	var room []byte
	for at := range entries {
		leaves[at].node, room = entries[at].leafWith(room)
		leaves[at].name = entries[at].name
	}
	return leaves
}

// leavesOfItems gives the leaves of a file's items. They have no names.
func leavesOfItems(items []PieceRef) []leaf {
	leaves := make([]leaf, len(items))
	for at := range items {
		leaves[at].node = items[at].leaf()
	}
	return leaves
}

func ownerHash(mark byte, count, size uint64, list Hash) Hash {
	var message [49]byte
	message[0] = mark
	binary.BigEndian.PutUint64(message[1:9], count)
	binary.BigEndian.PutUint64(message[9:17], size)
	copy(message[17:], list[:])
	return sha256.Sum256(message[:])
}

// FileHash is the hash of a file with count pieces and size bytes, whose
// list's tree has list on top.
func FileHash(count, size uint64, list Hash) Hash {
	return ownerHash(fileMark, count, size, list)
}

// FolderHash is the hash of a folder with count entries and size bytes of
// file content, whose list's tree has list on top.
func FolderHash(count, size uint64, list Hash) Hash {
	return ownerHash(folderMark, count, size, list)
}

// confirm holds a rebuilt top against the file or folder it should be the
// top of.
func confirm(target Target, count uint64, top node) error {
	what := "a file's list"
	hash := FileHash(count, top.size, top.hash)
	if target.Kind == KindFolder {
		what = "a folder's list"
		hash = FolderHash(count, top.size, top.hash)
	}
	if top.size != target.Size {
		return sizeMismatch(what, target.Size, top.size)
	}
	if hash != target.Hash {
		return hashMismatch(what)
	}
	return nil
}

// Cutting is how a tree's files are cut into pieces. The zero value is
// FastCDCV1.
type Cutting struct {
	// fixed is the size of a piece when every piece has one size, and zero
	// when files are cut by content.
	fixed uint32
}

// FastCDCV1 cuts by content: pieces of more than 512 KiB, about 2 MiB on
// average, and 8 MiB at most. Only a file's last piece may be smaller.
var FastCDCV1 = Cutting{}

// FixedCutting cuts at a fixed size, of 1 byte to 8 MiB: every piece but a
// file's last holds exactly this many bytes. It is for tests.
func FixedCutting(size uint32) (Cutting, error) {
	if size == 0 || size > MaxPieceBytes {
		return Cutting{}, invalid("a fixed piece size is not 1 byte to 8 MiB")
	}
	return Cutting{fixed: size}, nil
}

// Fixed gives the size of a piece when every piece has one size.
func (c Cutting) Fixed() (size uint32, fixed bool) {
	return c.fixed, c.fixed != 0
}

// Tag is how a record says this: "fastcdc-v1", or "fixed-" and the size.
func (c Cutting) Tag() string {
	if c.fixed == 0 {
		return CuttingFastCDCV1
	}
	return cuttingFixedPrefix + strconv.FormatUint(uint64(c.fixed), 10)
}

func (c Cutting) String() string {
	return c.Tag()
}

// CuttingFromTag reads what a record says. A size is written in digits with
// no zero in front, so each way of cutting has one spelling.
func CuttingFromTag(tag []byte) (Cutting, error) {
	return cuttingFromTag(string(tag))
}

func cuttingFromTag(tag string) (Cutting, error) {
	unknown := func() (Cutting, error) {
		return Cutting{}, invalid("a record names a way of cutting that is not known")
	}
	if tag == CuttingFastCDCV1 {
		return FastCDCV1, nil
	}
	digits, found := strings.CutPrefix(tag, cuttingFixedPrefix)
	if !found || digits == "" || len(digits) > 7 || digits[0] == '0' {
		return unknown()
	}
	var size uint32
	for at := 0; at < len(digits); at++ {
		if digits[at] < '0' || digits[at] > '9' {
			return unknown()
		}
		size = size*10 + uint32(digits[at]-'0')
	}
	cutting, err := FixedCutting(size)
	if err != nil {
		return unknown()
	}
	return cutting, nil
}

// CheckLength reports whether a piece of length bytes fits, as a file's last
// piece or as one before it.
func (c Cutting) CheckLength(length uint32, isLast bool) error {
	var fits bool
	if c.fixed == 0 {
		fits = length >= 1 && length <= FastCDCMaxSize && (isLast || length > FastCDCMinSize)
	} else if isLast {
		fits = length >= 1 && length <= c.fixed
	} else {
		fits = length == c.fixed
	}
	if !fits {
		return invalid("a piece has a length that the tree's way of cutting does not give")
	}
	return nil
}

// CountBounds gives the fewest and the most pieces that a file of size bytes
// can have.
func (c Cutting) CountBounds(size uint64) (fewest, most uint64) {
	if size == 0 {
		return 0, 0
	}
	if c.fixed == 0 {
		return (size-1)/FastCDCMaxSize + 1, (size-1)/(FastCDCMinSize+1) + 1
	}
	count := (size-1)/uint64(c.fixed) + 1
	return count, count
}

// CheckCount reports whether a file of size bytes can have count pieces.
func (c Cutting) CheckCount(size, count uint64) error {
	fewest, most := c.CountBounds(size)
	if count < fewest || count > most {
		return invalid("a file has a number of pieces that its size does not allow")
	}
	return nil
}

// FileList is a file: the list of its pieces, in the file's order.
type FileList struct {
	items []PieceRef
	size  uint64
	hash  Hash
}

// NewFileList is the file with these pieces, in this order. A piece that
// occurs twice is listed twice. No pieces at all is the empty file.
func NewFileList(items []PieceRef) (*FileList, error) {
	return newFileList(append([]PieceRef(nil), items...))
}

// newFileList is NewFileList for items that nothing else holds.
func newFileList(items []PieceRef) (*FileList, error) {
	for at := range items {
		if err := items[at].check(); err != nil {
			return nil, err
		}
	}
	top, err := topOf(fileTree, leavesOfItems(items))
	if err != nil {
		return nil, err
	}
	return &FileList{
		items: items,
		size:  top.size,
		hash:  FileHash(uint64(len(items)), top.size, top.hash),
	}, nil
}

// itemsOf reads the items of a whole list: 36 bytes for each piece.
func itemsOf(bytes []byte) ([]PieceRef, error) {
	if len(bytes)%ListItemBytes != 0 {
		return nil, invalid("a file's list is not a whole number of items")
	}
	r := reader{bytes: bytes, what: "a file's list"}
	items := make([]PieceRef, 0, len(bytes)/ListItemBytes)
	for !r.isEmpty() {
		item, err := decodePieceRef(&r)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// DecodeFileList reads a whole list: 36 bytes for each piece.
func DecodeFileList(bytes []byte) (*FileList, error) {
	items, err := itemsOf(bytes)
	if err != nil {
		return nil, err
	}
	return newFileList(items)
}

// Encode gives the list's bytes.
func (l *FileList) Encode() []byte {
	out := make([]byte, 0, len(l.items)*ListItemBytes)
	for _, item := range l.items {
		out = item.appendTo(out)
	}
	return out
}

// Items gives the file's pieces, in order. The slice is the list's own, and
// must not be changed.
func (l *FileList) Items() []PieceRef {
	return l.items
}

// Count is how many pieces the file has.
func (l *FileList) Count() uint64 {
	return uint64(len(l.items))
}

// Size is how many bytes the file holds.
func (l *FileList) Size() uint64 {
	return l.size
}

// Hash is the file's hash. It covers the pieces, their order, how many there
// are and the size, and nothing about where the file sits.
func (l *FileList) Hash() Hash {
	return l.hash
}

// Target is the file as the thing above it names it.
func (l *FileList) Target() Target {
	return Target{Kind: KindFile, Hash: l.hash, Size: l.size}
}

// EncodedLen is how many bytes the list takes.
func (l *FileList) EncodedLen() uint64 {
	return uint64(len(l.items)) * ListItemBytes
}

// CheckCutting reports whether every piece has a length that cutting gives.
func (l *FileList) CheckCutting(cutting Cutting) error {
	if err := cutting.CheckCount(l.size, l.Count()); err != nil {
		return err
	}
	last := len(l.items) - 1
	for at := range l.items {
		if err := cutting.CheckLength(l.items[at].Length, at == last); err != nil {
			return err
		}
	}
	return nil
}

// Open shows pieces start up to, not including, end. This hashes the whole
// list each time: keep an OpenFile to show many parts.
func (l *FileList) Open(start, end uint64) (*FileRun, error) {
	tree, err := buildListTree(fileTree, leavesOfItems(l.items))
	if err != nil {
		return nil, err
	}
	return l.openWith(tree, start, end)
}

func (l *FileList) openWith(tree *listTree, start, end uint64) (*FileRun, error) {
	nodes, err := tree.open(start, end)
	if err != nil {
		return nil, err
	}
	return &FileRun{
		count:   l.Count(),
		start:   start,
		items:   append([]PieceRef(nil), l.items[start:end]...),
		carried: nodes,
	}, nil
}

// OpenFile is a file's list with its tree kept, for showing many parts of
// it, and for finding the pieces that hold a stretch of bytes.
type OpenFile struct {
	list *FileList
	tree *listTree
	// starts is where each piece starts, and after the last one the file's
	// size.
	starts []uint64
}

// NewOpenFile keeps the tree of a list.
func NewOpenFile(list *FileList) (*OpenFile, error) {
	tree, err := buildListTree(fileTree, leavesOfItems(list.items))
	if err != nil {
		return nil, err
	}
	return openFileWith(list, tree), nil
}

// DecodeOpenFile reads a whole list as DecodeFileList does, and keeps its
// tree. The list is hashed once for both.
func DecodeOpenFile(bytes []byte) (*OpenFile, error) {
	items, err := itemsOf(bytes)
	if err != nil {
		return nil, err
	}
	tree, err := buildListTree(fileTree, leavesOfItems(items))
	if err != nil {
		return nil, err
	}
	top, err := tree.top()
	if err != nil {
		return nil, err
	}
	list := &FileList{
		items: items,
		size:  top.size,
		hash:  FileHash(uint64(len(items)), top.size, top.hash),
	}
	return openFileWith(list, tree), nil
}

func openFileWith(list *FileList, tree *listTree) *OpenFile {
	starts := make([]uint64, 0, len(list.items)+1)
	var at uint64
	for _, item := range list.items {
		starts = append(starts, at)
		at += uint64(item.Length)
	}
	starts = append(starts, at)
	return &OpenFile{list: list, tree: tree, starts: starts}
}

// List is the file's list.
func (f *OpenFile) List() *FileList {
	return f.list
}

// StartOf gives the byte at which piece index starts. The count gives the
// size.
func (f *OpenFile) StartOf(index uint64) (uint64, bool) {
	if index >= uint64(len(f.starts)) {
		return 0, false
	}
	return f.starts[index], true
}

// PiecesFor gives the pieces that hold the length bytes from offset: the
// first one's number, and the number after the last one. The stretch must
// lie inside the file and hold at least one byte.
func (f *OpenFile) PiecesFor(offset, length uint64) (first, after uint64, err error) {
	end := offset + length
	if length == 0 || end < offset || end > f.list.size {
		return 0, 0, invalid("a stretch of bytes is outside the file")
	}
	// Every piece holds a byte, so the starts ascend and each is found.
	first = uint64(sort.Search(len(f.starts), func(index int) bool { return f.starts[index] > offset }) - 1)
	after = uint64(sort.Search(len(f.starts), func(index int) bool { return f.starts[index] >= end }))
	return first, after, nil
}

// Open shows pieces start up to, not including, end.
func (f *OpenFile) Open(start, end uint64) (*FileRun, error) {
	return f.list.openWith(f.tree, start, end)
}

// OpenBytes shows the pieces that hold the length bytes from offset.
func (f *OpenFile) OpenBytes(offset, length uint64) (*FileRun, error) {
	start, end, err := f.PiecesFor(offset, length)
	if err != nil {
		return nil, err
	}
	return f.Open(start, end)
}

// FileRun is a run of a file's list, with the nodes that stand for the rest
// of it.
type FileRun struct {
	count   uint64
	start   uint64
	items   []PieceRef
	carried []carried
}

// Count is how many pieces the whole file is said to have.
func (r *FileRun) Count() uint64 {
	return r.count
}

// Start is the number of the first piece shown.
func (r *FileRun) Start() uint64 {
	return r.start
}

// Items gives the pieces shown. The slice is the run's own, and must not be
// changed.
func (r *FileRun) Items() []PieceRef {
	return r.items
}

// Encode gives the run's bytes.
func (r *FileRun) Encode() []byte {
	out := make([]byte, 0, 22+len(r.items)*ListItemBytes+len(r.carried)*40)
	out = binary.BigEndian.AppendUint64(out, r.count)
	out = binary.BigEndian.AppendUint64(out, r.start)
	out = binary.BigEndian.AppendUint32(out, uint32(len(r.items)))
	for _, item := range r.items {
		out = item.appendTo(out)
	}
	out = binary.BigEndian.AppendUint16(out, uint16(len(r.carried)))
	for at := range r.carried {
		out = append(out, r.carried[at].node.hash[:]...)
		out = binary.BigEndian.AppendUint64(out, r.carried[at].node.size)
	}
	return out
}

// DecodeFileRun reads a run of a file's list.
func DecodeFileRun(bytes []byte) (*FileRun, error) {
	r := reader{bytes: bytes, what: "a run of a file's list"}
	count, start, shownItems, err := runHeader(&r)
	if err != nil {
		return nil, err
	}
	if shownItems == 0 || r.remaining()/ListItemBytes < shownItems {
		return nil, invalid("a run of a file's list is cut short, or shows nothing")
	}
	items := make([]PieceRef, 0, shownItems)
	for index := 0; index < shownItems; index++ {
		item, err := decodePieceRef(&r)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	carriedNodes, err := carriedCount(&r)
	if err != nil {
		return nil, err
	}
	var nodes []carried
	for index := 0; index < carriedNodes; index++ {
		over := node{hash: r.hash(), size: r.u64()}
		if r.err != nil {
			return nil, r.err
		}
		nodes = append(nodes, carried{node: over})
	}
	if err := r.finish(); err != nil {
		return nil, err
	}
	return &FileRun{count: count, start: start, items: items, carried: nodes}, nil
}

// Check checks the run against the file it is said to be a part of, and
// gives the byte of the file at which the run starts.
func (r *FileRun) Check(file Target) (uint64, error) {
	return r.check(file, nil)
}

// CheckWithCutting is Check, with the pieces' lengths and their number held
// to a way of cutting too.
func (r *FileRun) CheckWithCutting(file Target, cutting Cutting) (uint64, error) {
	return r.check(file, &cutting)
}

func (r *FileRun) check(file Target, cutting *Cutting) (uint64, error) {
	if err := file.require(KindFile); err != nil {
		return 0, err
	}
	if cutting != nil {
		if err := cutting.CheckCount(file.Size, r.count); err != nil {
			return 0, err
		}
		for at := range r.items {
			place := r.start + uint64(at)
			if err := cutting.CheckLength(r.items[at].Length, place+1 == r.count); err != nil {
				return 0, err
			}
		}
	}
	result, err := checkOpening(fileTree, r.count, r.start, leavesOfItems(r.items), r.carried)
	if err != nil {
		return 0, err
	}
	if err := confirm(file, r.count, result.top); err != nil {
		return 0, err
	}
	return result.offset, nil
}

// runHeader reads what every run starts with: how many items the whole list
// has, the place of the first one shown, and how many are shown.
func runHeader(r *reader) (count, start uint64, shownItems int, err error) {
	count = r.u64()
	start = r.u64()
	number := r.u32()
	if r.err != nil {
		return 0, 0, 0, r.err
	}
	end := start + uint64(number)
	if count > MaxListItems || end < start || end > count {
		return 0, 0, 0, invalid("a run lies outside its list")
	}
	if uint64(number) > uint64(maxInt) {
		return 0, 0, 0, invalid("a run lies outside its list")
	}
	return count, start, int(number), nil
}

const maxInt = int(^uint(0) >> 1)

func carriedCount(r *reader) (int, error) {
	count := int(r.u16())
	if r.err != nil {
		return 0, r.err
	}
	if count > MaxCarriedNodes {
		return 0, invalid("a run carries more nodes than any list needs")
	}
	return count, nil
}

// FolderList is a folder: the list of its entries, sorted by name, with no
// name twice.
type FolderList struct {
	entries    []Entry
	size       uint64
	hash       Hash
	encodedLen uint64
}

// NewFolderList is the folder with these entries. They must be in the order
// of their names' bytes, with no name twice. No entries is the empty folder.
func NewFolderList(entries []Entry) (*FolderList, error) {
	return newFolderList(append([]Entry(nil), entries...))
}

// newFolderList is NewFolderList for entries that nothing else holds.
func newFolderList(entries []Entry) (*FolderList, error) {
	var encodedLen uint64
	for at := range entries {
		if entries[at].name == "" {
			return nil, invalid("an entry of a folder was not made as an entry")
		}
		encodedLen += uint64(entries[at].EncodedLen())
	}
	top, err := topOf(folderTree, leavesOf(entries))
	if err != nil {
		return nil, err
	}
	return &FolderList{
		entries:    entries,
		size:       top.size,
		hash:       FolderHash(uint64(len(entries)), top.size, top.hash),
		encodedLen: encodedLen,
	}, nil
}

// entriesOf reads the entries of a whole list, one after another.
func entriesOf(bytes []byte) ([]Entry, error) {
	r := reader{bytes: bytes, what: "a folder's list"}
	var entries []Entry
	for !r.isEmpty() {
		entry, err := decodeEntry(&r)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// DecodeFolderList reads a whole list: its entries, one after another.
func DecodeFolderList(bytes []byte) (*FolderList, error) {
	entries, err := entriesOf(bytes)
	if err != nil {
		return nil, err
	}
	return newFolderList(entries)
}

// Encode gives the list's bytes.
func (l *FolderList) Encode() []byte {
	out := make([]byte, 0, l.encodedLen)
	for at := range l.entries {
		out = l.entries[at].appendTo(out)
	}
	return out
}

// Entries gives the folder's entries, in the order of their names. The slice
// is the list's own, and must not be changed.
func (l *FolderList) Entries() []Entry {
	return l.entries
}

// Count is how many entries the folder has.
func (l *FolderList) Count() uint64 {
	return uint64(len(l.entries))
}

// Size is how many bytes of file content are under the folder.
func (l *FolderList) Size() uint64 {
	return l.size
}

// Hash is the folder's hash. It covers every entry: its name, its kind, its
// size, its hash and its extra data. It does not cover a name for the folder
// itself.
func (l *FolderList) Hash() Hash {
	return l.hash
}

// Target is the folder as the thing above it names it.
func (l *FolderList) Target() Target {
	return Target{Kind: KindFolder, Hash: l.hash, Size: l.size}
}

// EncodedLen is how many bytes the list takes.
func (l *FolderList) EncodedLen() uint64 {
	return l.encodedLen
}

// Get gives the entry with this name.
func (l *FolderList) Get(name string) (Entry, bool) {
	at := sort.Search(len(l.entries), func(index int) bool { return l.entries[index].name >= name })
	if at < len(l.entries) && l.entries[at].name == name {
		return l.entries[at], true
	}
	return Entry{}, false
}

// PlaceOf gives the one place where name can be: that of the last entry
// whose name does not sort after it, or the first place. An empty folder has
// none.
func (l *FolderList) PlaceOf(name string) (uint64, bool) {
	if len(l.entries) == 0 {
		return 0, false
	}
	notAfter := sort.Search(len(l.entries), func(index int) bool { return l.entries[index].name > name })
	if notAfter == 0 {
		return 0, true
	}
	return uint64(notAfter - 1), true
}

// Open shows entries start up to, not including, end. This hashes the whole
// list each time: keep an OpenFolder to show many parts.
func (l *FolderList) Open(start, end uint64) (*FolderRun, error) {
	tree, err := buildListTree(folderTree, leavesOf(l.entries))
	if err != nil {
		return nil, err
	}
	return l.openWith(tree, start, end)
}

// OpenName shows the entry at the one place where name can be. It answers
// whether the folder has the name, whichever way the answer falls.
func (l *FolderList) OpenName(name string) (*FolderRun, error) {
	tree, err := buildListTree(folderTree, leavesOf(l.entries))
	if err != nil {
		return nil, err
	}
	return l.openNameWith(tree, name)
}

func (l *FolderList) openWith(tree *listTree, start, end uint64) (*FolderRun, error) {
	nodes, err := tree.open(start, end)
	if err != nil {
		return nil, err
	}
	return &FolderRun{
		count:   l.Count(),
		start:   start,
		entries: append([]Entry(nil), l.entries[start:end]...),
		carried: nodes,
	}, nil
}

func (l *FolderList) openNameWith(tree *listTree, name string) (*FolderRun, error) {
	if err := CheckName(name); err != nil {
		return nil, err
	}
	place, found := l.PlaceOf(name)
	if !found {
		return &FolderRun{}, nil
	}
	return l.openWith(tree, place, place+1)
}

// OpenFolder is a folder's list with its tree kept, for showing many parts
// of it.
type OpenFolder struct {
	list *FolderList
	tree *listTree
}

// NewOpenFolder keeps the tree of a list.
func NewOpenFolder(list *FolderList) (*OpenFolder, error) {
	tree, err := buildListTree(folderTree, leavesOf(list.entries))
	if err != nil {
		return nil, err
	}
	return &OpenFolder{list: list, tree: tree}, nil
}

// DecodeOpenFolder reads a whole list as DecodeFolderList does, and keeps
// its tree. The list is hashed once for both.
func DecodeOpenFolder(bytes []byte) (*OpenFolder, error) {
	entries, err := entriesOf(bytes)
	if err != nil {
		return nil, err
	}
	tree, err := buildListTree(folderTree, leavesOf(entries))
	if err != nil {
		return nil, err
	}
	top, err := tree.top()
	if err != nil {
		return nil, err
	}
	var encodedLen uint64
	for at := range entries {
		encodedLen += uint64(entries[at].EncodedLen())
	}
	list := &FolderList{
		entries:    entries,
		size:       top.size,
		hash:       FolderHash(uint64(len(entries)), top.size, top.hash),
		encodedLen: encodedLen,
	}
	return &OpenFolder{list: list, tree: tree}, nil
}

// List is the folder's list.
func (f *OpenFolder) List() *FolderList {
	return f.list
}

// Open shows entries start up to, not including, end.
func (f *OpenFolder) Open(start, end uint64) (*FolderRun, error) {
	return f.list.openWith(f.tree, start, end)
}

// OpenName shows the entry at the one place where name can be.
func (f *OpenFolder) OpenName(name string) (*FolderRun, error) {
	return f.list.openNameWith(f.tree, name)
}

// FolderRun is a run of a folder's list, with the nodes that stand for the
// rest of it. A run of an empty folder shows nothing, and carries nothing:
// the zero value is that run.
type FolderRun struct {
	count   uint64
	start   uint64
	entries []Entry
	carried []carried
}

// Count is how many entries the whole folder is said to have.
func (r *FolderRun) Count() uint64 {
	return r.count
}

// Start is the place of the first entry shown.
func (r *FolderRun) Start() uint64 {
	return r.start
}

// Entries gives the entries shown. The slice is the run's own, and must not
// be changed.
func (r *FolderRun) Entries() []Entry {
	return r.entries
}

// Encode gives the run's bytes.
func (r *FolderRun) Encode() []byte {
	var out []byte
	out = binary.BigEndian.AppendUint64(out, r.count)
	out = binary.BigEndian.AppendUint64(out, r.start)
	out = binary.BigEndian.AppendUint32(out, uint32(len(r.entries)))
	for at := range r.entries {
		out = r.entries[at].appendTo(out)
	}
	out = binary.BigEndian.AppendUint16(out, uint16(len(r.carried)))
	for at := range r.carried {
		out = append(out, r.carried[at].node.hash[:]...)
		out = binary.BigEndian.AppendUint64(out, r.carried[at].node.size)
		out = binary.BigEndian.AppendUint16(out, uint16(len(r.carried[at].firstName)))
		out = append(out, r.carried[at].firstName...)
	}
	return out
}

// DecodeFolderRun reads a run of a folder's list.
func DecodeFolderRun(bytes []byte) (*FolderRun, error) {
	r := reader{bytes: bytes, what: "a run of a folder's list"}
	count, start, shownEntries, err := runHeader(&r)
	if err != nil {
		return nil, err
	}
	if (shownEntries == 0) != (count == 0) {
		return nil, invalid("a run of a folder's list shows nothing")
	}
	// An entry takes 45 bytes at least, so a count is held to the bytes.
	if r.remaining()/leastEntryBytes < shownEntries {
		return nil, invalid("a run of a folder's list is cut short")
	}
	entries := make([]Entry, 0, shownEntries)
	for index := 0; index < shownEntries; index++ {
		entry, err := decodeEntry(&r)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	carriedNodes, err := carriedCount(&r)
	if err != nil {
		return nil, err
	}
	var nodes []carried
	for index := 0; index < carriedNodes; index++ {
		over := node{hash: r.hash(), size: r.u64()}
		nameLen := int(r.u16())
		if r.err != nil {
			return nil, r.err
		}
		if nameLen == 0 || nameLen > MaxNameBytes {
			return nil, invalid("a carried name is not 1 to 255 bytes")
		}
		name := r.take(nameLen)
		if r.err != nil {
			return nil, r.err
		}
		nodes = append(nodes, carried{node: over, firstName: string(name)})
	}
	if err := r.finish(); err != nil {
		return nil, err
	}
	if count == 0 && len(nodes) != 0 {
		return nil, invalid("a run of an empty folder carries nodes")
	}
	return &FolderRun{count: count, start: start, entries: entries, carried: nodes}, nil
}

// Check checks the run against the folder it is said to be a part of. Every
// entry shown is then an entry of that folder, at the place where a search
// for its name ends.
func (r *FolderRun) Check(folder Target) error {
	if err := folder.require(KindFolder); err != nil {
		return err
	}
	if r.count == 0 {
		if len(r.entries) != 0 || len(r.carried) != 0 || r.start != 0 {
			return invalid("a run of an empty folder shows something")
		}
		return confirm(folder, 0, node{})
	}
	result, err := checkOpening(folderTree, r.count, r.start, leavesOf(r.entries), r.carried)
	if err != nil {
		return err
	}
	return confirm(folder, r.count, result.top)
}

// CheckName checks the run as the answer to "what is under name in this
// folder". It gives the entry, or false when the folder has no entry of that
// name. Either answer is proven: the run must show the one place where the
// name can be.
func (r *FolderRun) CheckName(folder Target, name string) (Entry, bool, error) {
	if err := CheckName(name); err != nil {
		return Entry{}, false, err
	}
	if err := folder.require(KindFolder); err != nil {
		return Entry{}, false, err
	}
	if r.count == 0 {
		return Entry{}, false, r.Check(folder)
	}
	if len(r.entries) != 1 {
		return Entry{}, false, invalid("a name is answered by one entry, and no more")
	}
	entry := r.entries[0]
	over, _ := entry.leafWith(nil)
	result, present, err := checkLookup(r.count, r.start, leaf{node: over, name: entry.name}, name, r.carried)
	if err != nil {
		return Entry{}, false, err
	}
	if err := confirm(folder, r.count, result.top); err != nil {
		return Entry{}, false, err
	}
	if !present {
		return Entry{}, false, nil
	}
	return entry, true, nil
}

// Totals is what a tree says about itself as a whole: how much a store has
// to keep for it. Each counts a thing once, however often the tree uses it.
type Totals struct {
	// Pieces is how many different pieces the tree has.
	Pieces uint64
	// PieceBytes is how many bytes those pieces hold.
	PieceBytes uint64
	// ListBytes is how many bytes the lists of its different files and
	// folders take.
	ListBytes uint64
}

// Record is a tree's record: the file or folder on top, the tree's totals,
// and its extra data. The hash of the record is the tree's root.
type Record struct {
	top     Target
	totals  Totals
	extra   ExtraData
	cutting Cutting
}

// NewRecord is the record of a tree with top on top. How the tree was cut is
// written into the extra data, in place of anything the caller put under
// that name.
func NewRecord(top Target, totals Totals, cutting Cutting, extra ExtraData) (*Record, error) {
	if _, err := kindFromByte(byte(top.Kind)); err != nil {
		return nil, err
	}
	extra, err := extra.Without(CuttingKey).WithText(CuttingKey, cutting.Tag())
	if err != nil {
		return nil, err
	}
	if err := checkTotals(top, totals); err != nil {
		return nil, err
	}
	return &Record{top: top, totals: totals, extra: extra, cutting: cutting}, nil
}

// DecodeRecord reads a record.
func DecodeRecord(bytes []byte) (*Record, error) {
	r := reader{bytes: bytes, what: "a record"}
	mark := r.u8()
	if r.err != nil {
		return nil, r.err
	}
	if mark != recordMark {
		return nil, invalid("a record does not start with its mark")
	}
	kindByte := r.u8()
	if r.err != nil {
		return nil, r.err
	}
	kind, err := kindFromByte(kindByte)
	if err != nil {
		return nil, err
	}
	size := r.u64()
	hash := r.hash()
	var totals Totals
	totals.Pieces = r.u64()
	totals.PieceBytes = r.u64()
	totals.ListBytes = r.u64()
	if r.err != nil {
		return nil, r.err
	}
	extra, err := decodeExtraData(&r)
	if err != nil {
		return nil, err
	}
	if err := r.finish(); err != nil {
		return nil, err
	}
	tag, found := extra.raw(CuttingKey)
	if !found {
		return nil, invalid("a record does not say how the tree was cut")
	}
	cutting, err := cuttingFromTag(tag)
	if err != nil {
		return nil, err
	}
	top := Target{Kind: kind, Hash: hash, Size: size}
	if err := checkTotals(top, totals); err != nil {
		return nil, err
	}
	return &Record{top: top, totals: totals, extra: extra, cutting: cutting}, nil
}

// Encode gives the record's bytes.
func (r *Record) Encode() []byte {
	out := make([]byte, 0, 66+r.extra.encodedLen())
	out = append(out, recordMark, byte(r.top.Kind))
	out = binary.BigEndian.AppendUint64(out, r.top.Size)
	out = append(out, r.top.Hash[:]...)
	out = binary.BigEndian.AppendUint64(out, r.totals.Pieces)
	out = binary.BigEndian.AppendUint64(out, r.totals.PieceBytes)
	out = binary.BigEndian.AppendUint64(out, r.totals.ListBytes)
	return r.extra.appendTo(out)
}

// Root is the tree's root: the hash of the record.
func (r *Record) Root() Root {
	return RootOfRecord(r.Encode())
}

// Top is the file or folder on top of the tree.
func (r *Record) Top() Target {
	return r.top
}

// Totals gives the tree's totals.
func (r *Record) Totals() Totals {
	return r.totals
}

// Extra is the record's extra data.
func (r *Record) Extra() ExtraData {
	return r.extra
}

// Cutting is how the tree's files were cut.
func (r *Record) Cutting() Cutting {
	return r.cutting
}

// checkTotals holds a record's numbers to what must hold between them in
// any tree. Whether the totals are right is checked against the whole tree.
func checkTotals(top Target, totals Totals) error {
	fits := totals.Pieces <= totals.PieceBytes && totals.PieceBytes <= top.Size
	if high, most := bits.Mul64(totals.Pieces, MaxPieceBytes); high == 0 && totals.PieceBytes > most {
		fits = false
	}
	if high, least := bits.Mul64(totals.Pieces, ListItemBytes); high != 0 || totals.ListBytes < least {
		fits = false
	}
	if top.Kind != KindFolder && totals.ListBytes%ListItemBytes != 0 {
		fits = false
	}
	if !fits {
		return invalid("a record's totals cannot belong to any tree")
	}
	return nil
}
