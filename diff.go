package scionic

import (
	"slices"
	"strings"
)

// What changed between two trees.
//
// Two trees are compared folder by folder. Where both have the same hash
// under a name, everything below it is the same, and nothing below it is
// read.

// ChangeKind is the kind of difference between an old tree and a new one.
type ChangeKind uint8

const (
	// ChangeAdded: the new tree has this entry, and the old one has nothing
	// there. A folder that was added is one change: what is in it is not
	// listed.
	ChangeAdded ChangeKind = iota + 1
	// ChangeRemoved: the old tree has this entry, and the new one has
	// nothing there.
	ChangeRemoved
	// ChangeChanged: both trees have an entry here, and it differs: a
	// file's bytes, or an entry's extra data. A folder is listed here only
	// for its extra data. What differs inside it is listed by its own path.
	ChangeChanged
)

func (k ChangeKind) String() string {
	switch k {
	case ChangeAdded:
		return "added"
	case ChangeRemoved:
		return "removed"
	case ChangeChanged:
		return "changed"
	}
	return "no change"
}

// Change is one difference between an old tree and a new one.
type Change struct {
	Kind ChangeKind
	// Path is where in the tree the change is: names with "/" between
	// them.
	Path string
	// Old is the old tree's entry: for ChangeRemoved and ChangeChanged.
	Old Entry
	// New is the new tree's entry: for ChangeAdded and ChangeChanged.
	New Entry
}

// DefaultMaxChanges is the most changes that Diff lists.
const DefaultMaxChanges = 1_000_000

func tooMany(most, found int) error {
	return limitExceeded("the changes between two trees", uint64(max(most, 0)), uint64(found))
}

// Diff gives the differences between two trees that a store holds, sorted
// by path. Both trees must have a folder on top, or be the same tree.
//
// A change is listed at every place it has. A tree keeps a folder once,
// however many places the folder has in it, so two small trees can differ
// at more places than any list holds. More than DefaultMaxChanges changes
// are an error, found as soon as there are that many.
func Diff(store Store, old, new Root) ([]Change, error) {
	return DiffWithin(store, old, new, DefaultMaxChanges)
}

// DiffWithin gives the differences between two trees, as Diff gives them,
// where more than most changes are an error.
func DiffWithin(store Store, old, new Root, most int) ([]Change, error) {
	changes, err := findChanges(store, old, new, most)
	if err != nil {
		return nil, err
	}
	if len(changes) > most {
		return nil, tooMany(most, len(changes))
	}
	return changes, nil
}

// findChanges gives the differences, sorted by path. It stops soon after it
// has found more than most: every pair of folders it looks into holds a
// change, so the work is held by the same number.
func findChanges(store Store, old, new Root, most int) ([]Change, error) {
	oldRecord, err := RecordOf(store, old)
	if err != nil {
		return nil, err
	}
	newRecord, err := RecordOf(store, new)
	if err != nil {
		return nil, err
	}
	if oldRecord.Top() == newRecord.Top() {
		return nil, nil
	}
	if oldRecord.Top().Kind != KindFolder || newRecord.Top().Kind != KindFolder {
		return nil, invalid("a tree with a file on top has no entries to compare")
	}
	type pair struct {
		old, new Target
		prefix   string
		depth    int
	}
	var changes []Change
	pending := []pair{{old: oldRecord.Top(), new: newRecord.Top(), depth: 1}}
	for len(pending) > 0 {
		folders := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if len(changes) > most {
			return nil, tooMany(most, len(changes))
		}
		if folders.depth > MaxFolderDepth {
			return nil, &Error{kind: kindTooDeep, Limit: MaxFolderDepth}
		}
		oldList, err := FolderOf(store, folders.old)
		if err != nil {
			return nil, err
		}
		newList, err := FolderOf(store, folders.new)
		if err != nil {
			return nil, err
		}
		olds, news := oldList.Entries(), newList.Entries()
		left, right := 0, 0
		for left < len(olds) || right < len(news) {
			order := 0
			switch {
			case right == len(news):
				order = -1
			case left == len(olds):
				order = 1
			default:
				order = strings.Compare(olds[left].Name(), news[right].Name())
			}
			switch {
			case order < 0:
				changes = append(changes, Change{Kind: ChangeRemoved, Path: folders.prefix + olds[left].Name(), Old: olds[left]})
				left++
			case order > 0:
				changes = append(changes, Change{Kind: ChangeAdded, Path: folders.prefix + news[right].Name(), New: news[right]})
				right++
			default:
				older, newer := olds[left], news[right]
				path := folders.prefix + older.Name()
				switch {
				case older.Kind() != newer.Kind():
					changes = append(changes,
						Change{Kind: ChangeRemoved, Path: path, Old: older},
						Change{Kind: ChangeAdded, Path: path, New: newer})
				case older.Kind() == KindFile:
					if !older.Equal(newer) {
						changes = append(changes, Change{Kind: ChangeChanged, Path: path, Old: older, New: newer})
					}
				default:
					if older.Hash() != newer.Hash() {
						pending = append(pending, pair{old: older.Target(), new: newer.Target(), prefix: path + "/", depth: folders.depth + 1})
					}
					if !older.Extra().Equal(newer.Extra()) {
						changes = append(changes, Change{Kind: ChangeChanged, Path: path, Old: older, New: newer})
					}
				}
				left++
				right++
			}
		}
	}
	slices.SortStableFunc(changes, func(left, right Change) int { return strings.Compare(left.Path, right.Path) })
	return changes, nil
}
