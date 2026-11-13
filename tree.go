package scionic

import (
	"crypto/sha256"
	"encoding/binary"
	"math/bits"
)

// The classic tree over a list.
//
// A file is a list of pieces and a folder is a list of entries. This file
// turns a list into the one hash that its owner is known by, shows a part of
// a list together with the nodes that stand for the rest, and checks such a
// showing.
//
// The rule, in full:
//
//   - a leaf stands for one item of the list: the caller hashes the item, and
//     says how many bytes of file content it holds;
//   - a fork joins two halves: its hash covers each half's hash, its size and
//     the number of its items, and in a folder's tree the first name of its
//     right half; its size is the two sizes added;
//   - a list of more than one item is halved at the largest power of two that
//     is less than its length.
//
// So the shape of a tree follows from the number of its items alone, and the
// hash on top covers that number: a list cannot be passed off as a longer or
// a shorter one. Nothing is padded and nothing is repeated, and a place
// beyond the count does not exist. Forks are hashed from a mark of their
// own, so a fork cannot be shown as a leaf.

// MaxListItems is the most items a list may hold. It bounds how deep any
// walk goes.
const MaxListItems uint64 = 1 << 40

const (
	fileForkMark   = 0x01
	folderForkMark = 0x03
)

// listKind is what a list is a list of.
type listKind uint8

const (
	// fileTree: a file's pieces, in the file's order.
	fileTree listKind = iota
	// folderTree: a folder's entries, sorted by name. Its forks record names.
	folderTree
)

// node is a node of a list's tree: its hash, and the bytes of file content
// under it. The zero node is what an empty list has in place of a tree.
type node struct {
	hash Hash
	size uint64
}

// leaf is one item of a list as its tree sees it: the item's leaf, and for a
// folder's list the entry's name. A file's list has no names.
type leaf struct {
	node node
	name string
}

// carried is a node sent in place of the items under it. In a folder's tree
// it comes with the first name under it.
type carried struct {
	node      node
	firstName string
}

// shown is what an opening shows, once it is rebuilt: the node on top of the
// list, to hold against the hash and size the list is known by, and how many
// bytes of file content come before the first opened item.
type shown struct {
	top    node
	offset uint64
}

// split is where a list of width items is halved: the largest power of two
// below it. The width is 2 at least.
func split(width uint64) uint64 {
	return 1 << (bits.Len64(width-1) - 1)
}

// fork joins two halves. leftItems and rightItems say how many items of the
// list are under each.
func fork(kind listKind, left node, leftItems uint64, right node, rightItems uint64, rightFirstName string) (node, error) {
	size := left.size + right.size
	if size < left.size {
		return node{}, sizeOverflow("list size")
	}
	var room [97 + MaxNameBytes]byte
	message := room[:97]
	message[0] = fileForkMark
	if kind == folderTree {
		message[0] = folderForkMark
	}
	copy(message[1:33], left.hash[:])
	binary.BigEndian.PutUint64(message[33:41], left.size)
	binary.BigEndian.PutUint64(message[41:49], leftItems)
	copy(message[49:81], right.hash[:])
	binary.BigEndian.PutUint64(message[81:89], right.size)
	binary.BigEndian.PutUint64(message[89:97], rightItems)
	if kind == folderTree {
		message = append(message, rightFirstName...)
	}
	return node{hash: sha256.Sum256(message), size: size}, nil
}

// checkList: a whole list has to be short enough, and a folder's has to be
// sorted by name with no name twice.
func checkList(kind listKind, leaves []leaf) error {
	if uint64(len(leaves)) > MaxListItems {
		return invalid("a list holds more items than the format allows")
	}
	if kind == folderTree && !namesAscend(leaves) {
		return invalid("a folder's entries are not sorted by name, or a name is there twice")
	}
	return nil
}

func namesAscend(leaves []leaf) bool {
	for at := 1; at < len(leaves); at++ {
		if leaves[at-1].name >= leaves[at].name {
			return false
		}
	}
	return true
}

// topOf is the node on top of a whole list. An empty list gives the zero
// node.
func topOf(kind listKind, leaves []leaf) (node, error) {
	if err := checkList(kind, leaves); err != nil {
		return node{}, err
	}
	// Two whole trees of one width are joined as soon as they stand side by
	// side, so the stack holds one tree for each 1 bit of the count so far.
	// Each is kept with the place of its first item and its width.
	type standing struct {
		node         node
		first, width int
	}
	var stack []standing
	for index := range leaves {
		right := standing{leaves[index].node, index, 1}
		for len(stack) > 0 {
			left := stack[len(stack)-1]
			if left.width != right.width {
				break
			}
			stack = stack[:len(stack)-1]
			joined, err := fork(kind, left.node, uint64(left.width), right.node, uint64(right.width), leaves[right.first].name)
			if err != nil {
				return node{}, err
			}
			right = standing{joined, left.first, left.width * 2}
		}
		stack = append(stack, right)
	}
	if len(stack) == 0 {
		return node{}, nil
	}
	// What is left joins from the right: each is the left half of all after
	// it.
	top := stack[len(stack)-1]
	for at := len(stack) - 2; at >= 0; at-- {
		left := stack[at]
		joined, err := fork(kind, left.node, uint64(left.width), top.node, uint64(top.width), leaves[top.first].name)
		if err != nil {
			return node{}, err
		}
		top = standing{joined, left.first, left.width + top.width}
	}
	return top.node, nil
}

// listTree is a list's tree kept whole, for showing parts of it. It keeps
// its own copy of the leaves, and of the names in a folder's list.
type listTree struct {
	kind   listKind
	leaves []node
	// names are a folder's names, in the list's order. A file's list has
	// none.
	names []string
	// levels[j] holds, in order, the nodes over 2^(j+1) items each.
	levels [][]node
}

func buildListTree(kind listKind, leaves []leaf) (*listTree, error) {
	if err := checkList(kind, leaves); err != nil {
		return nil, err
	}
	tree := &listTree{kind: kind, leaves: make([]node, len(leaves))}
	for at := range leaves {
		tree.leaves[at] = leaves[at].node
	}
	if kind == folderTree {
		tree.names = make([]string, len(leaves))
		for at := range leaves {
			tree.names[at] = leaves[at].name
		}
	}
	below := tree.leaves
	belowWidth := 1
	for len(below) >= 2 {
		level := make([]node, len(below)/2)
		for pair := range level {
			name := leaves[(pair*2+1)*belowWidth].name
			items := uint64(belowWidth)
			joined, err := fork(kind, below[pair*2], items, below[pair*2+1], items, name)
			if err != nil {
				return nil, err
			}
			level[pair] = joined
		}
		tree.levels = append(tree.levels, level)
		below = level
		belowWidth *= 2
	}
	return tree, nil
}

// name is the name of the entry at index. A file's list has none.
func (t *listTree) name(index int) string {
	if t.kind == folderTree {
		return t.names[index]
	}
	return ""
}

// count is how many items the list holds.
func (t *listTree) count() uint64 {
	return uint64(len(t.leaves))
}

// top is the node on top of the list.
func (t *listTree) top() (node, error) {
	if len(t.leaves) == 0 {
		return node{}, nil
	}
	return t.node(0, len(t.leaves))
}

// node is the node over the width items that start at offset. Every node a
// walk from the top asks for either has a width that is a power of two and
// starts at a multiple of it, and is kept, or it lies on the list's right
// edge and is joined from such nodes.
func (t *listTree) node(offset, width int) (node, error) {
	if width&(width-1) == 0 {
		level := bits.TrailingZeros(uint(width))
		if level == 0 {
			return t.leaves[offset], nil
		}
		return t.levels[level-1][offset>>level], nil
	}
	leftWidth := int(split(uint64(width)))
	left, err := t.node(offset, leftWidth)
	if err != nil {
		return node{}, err
	}
	right, err := t.node(offset+leftWidth, width-leftWidth)
	if err != nil {
		return node{}, err
	}
	return fork(t.kind, left, uint64(leftWidth), right, uint64(width-leftWidth), t.name(offset+leftWidth))
}

// open gives the nodes to send with items start up to, not including, end:
// one for each run of items that is left out, in the order a checker meets
// them.
func (t *listTree) open(start, end uint64) ([]carried, error) {
	if start >= end || end > t.count() {
		return nil, invalid("the items to open are outside the list")
	}
	var nodes []carried
	if err := t.collect(0, len(t.leaves), int(start), int(end), &nodes); err != nil {
		return nil, err
	}
	return nodes, nil
}

func (t *listTree) collect(offset, width, start, end int, nodes *[]carried) error {
	after := offset + width
	if after <= start || end <= offset {
		over, err := t.node(offset, width)
		if err != nil {
			return err
		}
		*nodes = append(*nodes, carried{node: over, firstName: t.name(offset)})
		return nil
	}
	if start <= offset && after <= end {
		return nil
	}
	leftWidth := int(split(uint64(width)))
	if err := t.collect(offset, leftWidth, start, end, nodes); err != nil {
		return err
	}
	return t.collect(offset+leftWidth, width-leftWidth, start, end, nodes)
}

// checkOpening rebuilds the top of a list of count items from the opened
// items, which start at item start, and the nodes carried for the rest.
//
// The caller holds the result against the hash and the size that the list is
// known by. In a folder's list every opened entry must also be where the
// fork names send its name, so that an entry is never shown at a place where
// a search for its name would not find it.
func checkOpening(kind listKind, count, start uint64, opened []leaf, nodes []carried) (shown, error) {
	return runWalk(kind, count, start, opened, nodes, "", false)
}

// checkLookup rebuilds the top of a folder's list from the one entry at the
// place where name can be, and says whether that entry has the name. Each
// fork sends the name left when it sorts before the fork's name and right
// otherwise, so for one folder and one name there is one place, and one
// answer.
func checkLookup(count, index uint64, entry leaf, name string, nodes []carried) (shown, bool, error) {
	result, err := runWalk(folderTree, count, index, []leaf{entry}, nodes, name, true)
	if err != nil {
		return shown{}, false, err
	}
	return result, entry.name == name, nil
}

type listWalk struct {
	kind      listKind
	start     uint64
	end       uint64
	opened    []leaf
	carried   []carried
	next      int
	before    uint64
	lookup    string
	hasLookup bool
}

func runWalk(kind listKind, count, start uint64, opened []leaf, nodes []carried, lookup string, hasLookup bool) (shown, error) {
	if count > MaxListItems {
		return shown{}, invalid("a list holds more items than the format allows")
	}
	length := uint64(len(opened))
	end := start + length
	if length == 0 || end < start || end > count {
		return shown{}, invalid("the opened items are outside the list")
	}
	switch kind {
	case fileTree:
		for at := range nodes {
			if nodes[at].firstName != "" {
				return shown{}, invalid("a node of a file's tree carries a name")
			}
		}
	case folderTree:
		if !namesAscend(opened) {
			return shown{}, invalid("opened entries are not sorted by name, or a name is there twice")
		}
	}
	w := listWalk{
		kind:      kind,
		start:     start,
		end:       end,
		opened:    opened,
		carried:   nodes,
		lookup:    lookup,
		hasLookup: hasLookup,
	}
	top, _, err := w.node(0, count)
	if err != nil {
		return shown{}, err
	}
	if w.next != len(w.carried) {
		return shown{}, invalid("an opening carries more nodes than it needs")
	}
	return shown{top: top, offset: w.before}, nil
}

func (w *listWalk) item(index uint64) *leaf {
	return &w.opened[index-w.start]
}

// node is the node over the width items that start at offset, and the first
// name under it. No more than 41 calls are ever nested, because the list
// holds at most 2^40 items.
func (w *listWalk) node(offset, width uint64) (node, string, error) {
	after := offset + width
	if after <= w.start || w.end <= offset {
		if w.next == len(w.carried) {
			return node{}, "", invalid("an opening carries fewer nodes than it needs")
		}
		over := &w.carried[w.next]
		w.next++
		if after <= w.start {
			before := w.before + over.node.size
			if before < w.before {
				return node{}, "", sizeOverflow("list size")
			}
			w.before = before
		}
		return over.node, over.firstName, nil
	}
	if width == 1 {
		item := w.item(offset)
		return item.node, item.name, nil
	}
	middle := offset + split(width)
	left, firstName, err := w.node(offset, middle-offset)
	if err != nil {
		return node{}, "", err
	}
	right, forkName, err := w.node(middle, after-middle)
	if err != nil {
		return node{}, "", err
	}
	if w.kind == folderTree {
		if err := w.checkNames(offset, middle, after, forkName); err != nil {
			return node{}, "", err
		}
	}
	joined, err := fork(w.kind, left, middle-offset, right, after-middle, forkName)
	if err != nil {
		return node{}, "", err
	}
	return joined, firstName, nil
}

// checkNames, at a fork of a folder's tree: every opened entry on its left
// sorts before the fork's name, and every opened entry on its right sorts
// after it, except the right half's own first entry, which has that name. A
// name that is looked up takes the same turn as the entry it is shown.
func (w *listWalk) checkNames(offset, middle, after uint64, forkName string) error {
	leftEnd := min(w.end, middle)
	if max(w.start, offset) < leftEnd && w.item(leftEnd-1).name >= forkName {
		return invalid("an entry is left of a fork whose name it does not sort before")
	}
	rightStart := max(w.start, middle)
	if rightStart > middle && rightStart < min(w.end, after) && w.item(rightStart).name <= forkName {
		return invalid("an entry is right of a fork whose name it does not sort after")
	}
	if w.hasLookup {
		nameGoesLeft := w.lookup < forkName
		entryIsLeft := w.start < middle
		if nameGoesLeft != entryIsLeft {
			return invalid("the opened entry is not where the name would be")
		}
	}
	return nil
}
