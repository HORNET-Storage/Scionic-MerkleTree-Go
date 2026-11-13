package scionic

import (
	"crypto/sha256"
	"encoding/binary"
)

// The format, written a second time and as plainly as it can be.
//
// Nothing here is shared with the library: not a type, not a function, and
// the hash is the standard library's. Each function is the rule as
// spec/FORMAT.md states it, with no shortcut: a tree is hashed by halving,
// an opening is found by walking. It does two jobs. It says what the
// library's hashes have to be, from another hand. And it forges: it hashes
// lists that the library refuses to make, such as a folder that is out of
// order on purpose, the way an author with code of his own could.

type oracleHash = [32]byte

func oracleSHA(parts ...[]byte) oracleHash {
	hasher := sha256.New()
	for _, part := range parts {
		hasher.Write(part)
	}
	var out oracleHash
	hasher.Sum(out[:0])
	return out
}

func be16(value int) []byte    { return binary.BigEndian.AppendUint16(nil, uint16(value)) }
func be32(value uint32) []byte { return binary.BigEndian.AppendUint32(nil, value) }
func be64(value uint64) []byte { return binary.BigEndian.AppendUint64(nil, value) }

// joined gives parts one after another, in bytes of its own.
func joined(parts ...[]byte) []byte {
	var out []byte
	for _, part := range parts {
		out = append(out, part...)
	}
	return out
}

// oracleItem is one item of a file's list.
type oracleItem struct {
	hash   oracleHash
	length uint32
}

func oracleItemOf(content []byte) oracleItem {
	return oracleItem{hash: oracleSHA(content), length: uint32(len(content))}
}

func (i oracleItem) bytes() []byte {
	return joined(i.hash[:], be32(i.length))
}

// oracleValue is one named value of extra data.
type oracleValue struct {
	name, value []byte
}

// oracleEntry is one entry of a folder's list.
type oracleEntry struct {
	// kind is 1 for a file, 2 for a folder.
	kind byte
	size uint64
	hash oracleHash
	name []byte
	// extra is the named values, in the order they are to be written.
	extra []oracleValue
}

func oracleFile(name string, hash oracleHash, size uint64) oracleEntry {
	return oracleEntry{kind: 1, size: size, hash: hash, name: []byte(name)}
}

func oracleFolder(name string, hash oracleHash, size uint64) oracleEntry {
	return oracleEntry{kind: 2, size: size, hash: hash, name: []byte(name)}
}

func (e oracleEntry) bytes() []byte {
	return joined([]byte{e.kind}, be64(e.size), e.hash[:], be16(len(e.name)), e.name, oracleExtraBytes(e.extra))
}

func oracleExtraBytes(extra []oracleValue) []byte {
	out := be16(len(extra))
	for _, value := range extra {
		out = joined(out, be16(len(value.name)), value.name, be16(len(value.value)), value.value)
	}
	return out
}

// oracleEntryOf gives an entry of the library as the oracle writes one,
// with no extra data.
func oracleEntryOf(entry Entry) oracleEntry {
	kind := byte(2)
	if entry.Kind() == KindFile {
		kind = 1
	}
	return oracleEntry{kind: kind, size: entry.Size(), hash: entry.Hash(), name: []byte(entry.Name())}
}

func oracleEntriesOf(entries []Entry) []oracleEntry {
	out := make([]oracleEntry, len(entries))
	for at, entry := range entries {
		out[at] = oracleEntryOf(entry)
	}
	return out
}

func oracleItemsOf(items []PieceRef) []oracleItem {
	out := make([]oracleItem, len(items))
	for at, item := range items {
		out[at] = oracleItem{hash: item.Hash, length: item.Length}
	}
	return out
}

// oracleNode is a node of a list's tree: its hash, and the bytes of content
// under it.
type oracleNode struct {
	hash oracleHash
	size uint64
}

// oracleLeaves is a list as its tree sees it: a leaf for each item, and for
// a folder each entry's name.
type oracleLeaves struct {
	// forkMark is 0x01 for a file's tree, 0x03 for a folder's.
	forkMark byte
	nodes    []oracleNode
	names    [][]byte
}

func oracleFileLeaves(items []oracleItem) oracleLeaves {
	leaves := oracleLeaves{forkMark: 0x01}
	for _, item := range items {
		leaves.nodes = append(leaves.nodes, oracleNode{oracleSHA([]byte{0x00}, item.bytes()), uint64(item.length)})
	}
	return leaves
}

func oracleFolderLeaves(entries []oracleEntry) oracleLeaves {
	leaves := oracleLeaves{forkMark: 0x03}
	for _, entry := range entries {
		leaves.nodes = append(leaves.nodes, oracleNode{oracleSHA([]byte{0x02}, entry.bytes()), entry.size})
		leaves.names = append(leaves.names, entry.name)
	}
	return leaves
}

// oracleLeftHalf is the largest power of two that is less than width.
func oracleLeftHalf(width int) int {
	half := 1
	for half*2 < width {
		half *= 2
	}
	return half
}

func (l oracleLeaves) firstName(offset int) []byte {
	if l.forkMark == 0x03 {
		return l.names[offset]
	}
	return nil
}

// node gives the node over the width items from offset, by the rule.
func (l oracleLeaves) node(offset, width int) oracleNode {
	if width == 1 {
		return l.nodes[offset]
	}
	half := oracleLeftHalf(width)
	left := l.node(offset, half)
	right := l.node(offset+half, width-half)
	return oracleFork(l.forkMark, left, uint64(half), right, uint64(width-half), l.firstName(offset+half))
}

// top gives the node on top of the list. An empty list has 32 zero bytes.
func (l oracleLeaves) top() oracleNode {
	if len(l.nodes) == 0 {
		return oracleNode{}
	}
	return l.node(0, len(l.nodes))
}

// oracleCarried is a node that stands for a run that is left out, with the
// first name under it.
type oracleCarried struct {
	node oracleNode
	name []byte
}

// carried gives the nodes to carry when items start up to end are shown:
// one for each run that is left out, in the order a checker meets them,
// each with the first name under it.
func (l oracleLeaves) carried(start, end int) []oracleCarried {
	var out []oracleCarried
	l.collect(0, len(l.nodes), start, end, &out)
	return out
}

func (l oracleLeaves) collect(offset, width, start, end int, out *[]oracleCarried) {
	if offset+width <= start || end <= offset {
		*out = append(*out, oracleCarried{l.node(offset, width), l.firstName(offset)})
	} else if width > 1 {
		half := oracleLeftHalf(width)
		l.collect(offset, half, start, end, out)
		l.collect(offset+half, width-half, start, end, out)
	}
}

func oracleFork(mark byte, left oracleNode, leftItems uint64, right oracleNode, rightItems uint64, name []byte) oracleNode {
	if mark != 0x03 {
		name = nil
	}
	hash := oracleSHA([]byte{mark},
		left.hash[:], be64(left.size), be64(leftItems),
		right.hash[:], be64(right.size), be64(rightItems),
		name)
	return oracleNode{hash, left.size + right.size}
}

// oracleFileHash gives a file's hash and size, from its list.
func oracleFileHash(items []oracleItem) oracleNode {
	top := oracleFileLeaves(items).top()
	return oracleNode{oracleSHA([]byte{0x10}, be64(uint64(len(items))), be64(top.size), top.hash[:]), top.size}
}

// oracleFolderHash gives a folder's hash and size, from its list as it is
// given: nothing is sorted, and nothing is checked.
func oracleFolderHash(entries []oracleEntry) oracleNode {
	top := oracleFolderLeaves(entries).top()
	return oracleNode{oracleSHA([]byte{0x11}, be64(uint64(len(entries))), be64(top.size), top.hash[:]), top.size}
}

func oracleFileListBytes(items []oracleItem) []byte {
	var out []byte
	for _, item := range items {
		out = append(out, item.bytes()...)
	}
	return out
}

func oracleFolderListBytes(entries []oracleEntry) []byte {
	var out []byte
	for _, entry := range entries {
		out = append(out, entry.bytes()...)
	}
	return out
}

// oracleRecordBytes gives a record's bytes. Its hash is the root. The
// totals are the pieces, the pieces' bytes and the lists' bytes.
func oracleRecordBytes(kind byte, size uint64, hash oracleHash, totals [3]uint64, extra []oracleValue) []byte {
	return joined([]byte{0x20, kind}, be64(size), hash[:], be64(totals[0]), be64(totals[1]), be64(totals[2]), oracleExtraBytes(extra))
}

// oracleCutTag gives the extra data of a record that says only how the
// tree was cut.
func oracleCutTag(tag string) []oracleValue {
	return []oracleValue{{[]byte("chunking"), []byte(tag)}}
}

// oracleFileRunFrame gives the frame that shows items start up to end of a
// file's list.
func oracleFileRunFrame(items []oracleItem, start, end int) []byte {
	return oracleFileRunFrameAs(uint64(len(items)), uint64(start), items[start:end], oracleFileLeaves(items).carried(start, end))
}

// oracleFileRunFrameAs gives a file-run frame from its parts, whatever
// they say.
func oracleFileRunFrameAs(count, start uint64, shown []oracleItem, carried []oracleCarried) []byte {
	out := joined([]byte{0x23}, be64(count), be64(start), be32(uint32(len(shown))), oracleFileListBytes(shown), be16(len(carried)))
	for _, node := range carried {
		out = joined(out, node.node.hash[:], be64(node.node.size))
	}
	return out
}

// oracleFolderRunFrame gives the frame that shows entries start up to end
// of a folder's list.
func oracleFolderRunFrame(entries []oracleEntry, start, end int) []byte {
	return oracleFolderRunFrameAs(uint64(len(entries)), uint64(start), entries[start:end], oracleFolderLeaves(entries).carried(start, end))
}

// oracleFolderRunFrameAs gives a folder-run frame from its parts, whatever
// they say.
func oracleFolderRunFrameAs(count, start uint64, shown []oracleEntry, carried []oracleCarried) []byte {
	out := joined([]byte{0x24}, be64(count), be64(start), be32(uint32(len(shown))), oracleFolderListBytes(shown), be16(len(carried)))
	for _, node := range carried {
		out = joined(out, node.node.hash[:], be64(node.node.size), be16(len(node.name)), node.name)
	}
	return out
}

// oracleFrame gives a frame: its first byte, and its body.
func oracleFrame(tag byte, body []byte) []byte {
	return joined([]byte{tag}, body)
}
