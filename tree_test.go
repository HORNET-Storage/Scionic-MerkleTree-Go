package scionic

import (
	"crypto/sha256"
	"fmt"
	"math"
	"slices"
	"testing"
)

// The classic tree over a list, held to its rule.

// leafNode is a leaf for tests: a hash and a size that follow from a number.
func leafNode(seed int) node {
	return node{hash: HashOf(be64(uint64(seed))), size: uint64(seed%7) + 1}
}

// fileLeaves is a file's list of count items.
func fileLeaves(count int) []leaf {
	leaves := make([]leaf, count)
	for index := range leaves {
		leaves[index] = leaf{node: leafNode(index)}
	}
	return leaves
}

// folderLeaves is a folder's list with these names, in this order.
func folderLeaves(names ...string) []leaf {
	leaves := make([]leaf, len(names))
	for index, name := range names {
		leaves[index] = leaf{node: leafNode(index), name: name}
	}
	return leaves
}

// sortedFolder is a folder's list of count entries, sorted by name.
func sortedFolder(count int) []leaf {
	leaves := make([]leaf, count)
	for index := range leaves {
		leaves[index] = leaf{node: leafNode(index), name: fmt.Sprintf("name-%04d", index)}
	}
	return leaves
}

// byTheRule is the rule as it is written, with no stack and no kept levels.
func byTheRule(t *testing.T, kind listKind, leaves []leaf, offset, width int) node {
	t.Helper()
	if width == 1 {
		return leaves[offset].node
	}
	leftWidth := 1
	for leftWidth*2 < width {
		leftWidth *= 2
	}
	left := byTheRule(t, kind, leaves, offset, leftWidth)
	right := byTheRule(t, kind, leaves, offset+leftWidth, width-leftWidth)
	return must[node](t)(fork(kind, left, uint64(leftWidth), right, uint64(width-leftWidth), leaves[offset+leftWidth].name))
}

// numbers is a small generator of numbers that repeats from run to run.
type numbers uint64

func (n *numbers) next(below int) int {
	*n ^= *n << 13
	*n ^= *n >> 7
	*n ^= *n << 17
	return int(uint64(*n) % uint64(below))
}

// sizeBefore is the bytes of content before the item at start.
func sizeBefore(leaves []leaf, start int) uint64 {
	var before uint64
	for _, leaf := range leaves[:start] {
		before += leaf.node.size
	}
	return before
}

var bothKinds = []struct {
	kind listKind
	name string
	list func(count int) []leaf
}{
	{fileTree, "a file", fileLeaves},
	{folderTree, "a folder", sortedFolder},
}

func TestAListIsHalvedAtTheLargestPowerOfTwoBelowItsLength(t *testing.T) {
	for _, pair := range [][2]uint64{{2, 1}, {3, 2}, {4, 2}, {5, 4}, {6, 4}, {7, 4}, {8, 4}, {9, 8}, {16, 8}, {17, 16}} {
		if got := split(pair[0]); got != pair[1] {
			t.Fatalf("a list of %d is halved at %d", pair[0], got)
		}
	}
	if split(MaxListItems) != MaxListItems/2 || split(MaxListItems+1) != MaxListItems {
		t.Fatal("the longest list is not halved at half its length")
	}
	if split(math.MaxUint64) != 1<<63 {
		t.Fatal("the widest number is not halved at the largest power of two")
	}
}

func TestAForkHashesExactlyTheBytesTheFormatNames(t *testing.T) {
	left, right := leafNode(1), leafNode(2)
	message := joined([]byte{0x01}, left.hash[:], be64(left.size), be64(4), right.hash[:], be64(right.size), be64(3))
	if len(message) != 97 {
		t.Fatal(len(message))
	}
	file := must[node](t)(fork(fileTree, left, 4, right, 3, "ignored"))
	if file.hash != sha256.Sum256(message) || file.size != left.size+right.size {
		t.Fatal("a file's fork is not the hash of the bytes the format names")
	}
	// The same two halves over another number of items are another fork.
	if other := must[node](t)(fork(fileTree, left, 4, right, 2, "ignored")); other.hash == file.hash {
		t.Fatal("a fork does not cover the number of its items")
	}
	// The two halves the other way round are another fork too.
	if other := must[node](t)(fork(fileTree, right, 3, left, 4, "ignored")); other.hash == file.hash {
		t.Fatal("a fork does not cover the order of its halves")
	}
	message[0] = 0x03
	message = append(message, "right-half"...)
	folder := must[node](t)(fork(folderTree, left, 4, right, 3, "right-half"))
	if folder.hash != sha256.Sum256(message) || folder.hash == file.hash {
		t.Fatal("a folder's fork is not the hash of the bytes the format names")
	}
	// The longest name there can be fits the room that a fork is hashed in.
	longest := string(make([]byte, MaxNameBytes))
	long := must[node](t)(fork(folderTree, left, 4, right, 3, longest))
	if long.hash != sha256.Sum256(append(message[:97:97], longest...)) {
		t.Fatal("a fork with the longest name is not the hash of its bytes")
	}
}

func TestTheTopOfAListFollowsTheRuleAtEveryLength(t *testing.T) {
	for _, of := range bothKinds {
		if top := must[node](t)(topOf(of.kind, nil)); top != (node{}) {
			t.Fatalf("%s with nothing in it has a tree", of.name)
		}
		empty := must[*listTree](t)(buildListTree(of.kind, nil))
		if top := must[node](t)(empty.top()); top != (node{}) || empty.count() != 0 {
			t.Fatalf("%s with nothing in it has a tree that is kept", of.name)
		}
		for count := 1; count <= 70; count++ {
			leaves := of.list(count)
			expected := byTheRule(t, of.kind, leaves, 0, count)
			if top := must[node](t)(topOf(of.kind, leaves)); top != expected {
				t.Fatalf("%s of %d is not hashed by the rule", of.name, count)
			}
			tree := must[*listTree](t)(buildListTree(of.kind, leaves))
			if top := must[node](t)(tree.top()); top != expected || tree.count() != uint64(count) {
				t.Fatalf("%s of %d that is kept is not hashed by the rule", of.name, count)
			}
		}
	}
}

func TestAListOfOneIsItsLeafAndALongerListIsNeverOneOfItsLeaves(t *testing.T) {
	leaves := fileLeaves(3)
	if top := must[node](t)(topOf(fileTree, leaves[:1])); top != leaves[0].node {
		t.Fatal("a list of one is not its leaf")
	}
	top := must[node](t)(topOf(fileTree, leaves))
	var size uint64
	for _, leaf := range leaves {
		if leaf.node.hash == top.hash {
			t.Fatal("a longer list is one of its leaves")
		}
		size += leaf.node.size
	}
	if top.size != size {
		t.Fatalf("the list holds %d bytes, and its leaves %d", top.size, size)
	}
}

func TestTheSameItemsInAnotherOrderOrNumberGiveAnotherTop(t *testing.T) {
	leaves := fileLeaves(6)
	top := must[node](t)(topOf(fileTree, leaves))
	// A last item that is listed twice is a longer list, not the same one.
	repeated := append(slices.Clone(leaves), leaves[5])
	if must[node](t)(topOf(fileTree, repeated)).hash == top.hash {
		t.Fatal("a list with its last item twice is the same list")
	}
	if must[node](t)(topOf(fileTree, leaves[:5])).hash == top.hash {
		t.Fatal("a list without its last item is the same list")
	}
	leaves[1], leaves[2] = leaves[2], leaves[1]
	if must[node](t)(topOf(fileTree, leaves)).hash == top.hash {
		t.Fatal("a list in another order is the same list")
	}
	// The same leaves as a folder's entries are another thing than as a
	// file's pieces.
	file := fileLeaves(4)
	folder := sortedFolder(4)
	if must[node](t)(topOf(fileTree, file)).hash == must[node](t)(topOf(folderTree, folder)).hash {
		t.Fatal("a folder's tree is a file's tree")
	}
}

func TestAFolderListOutOfOrderOrWithANameTwiceHasNoHash(t *testing.T) {
	for _, names := range [][]string{{"b", "a"}, {"a", "a"}, {"a", "c", "b"}, {"a", "b", "b"}} {
		leaves := folderLeaves(names...)
		if _, err := topOf(folderTree, leaves); err == nil {
			t.Fatalf("%v has a hash", names)
		}
		if _, err := buildListTree(folderTree, leaves); err == nil {
			t.Fatalf("%v has a tree", names)
		}
	}
}

func TestEveryRunOfEveryListOpensAndRebuildsToTheTop(t *testing.T) {
	for _, of := range bothKinds {
		for count := 1; count <= 33; count++ {
			leaves := of.list(count)
			tree := must[*listTree](t)(buildListTree(of.kind, leaves))
			top := must[node](t)(tree.top())
			for start := 0; start < count; start++ {
				for end := start + 1; end <= count; end++ {
					what := fmt.Sprintf("%s of %d, %d up to %d", of.name, count, start, end)
					nodes := must[[]carried](t)(tree.open(uint64(start), uint64(end)))
					result, err := checkOpening(of.kind, uint64(count), uint64(start), leaves[start:end], nodes)
					if err != nil {
						t.Fatalf("%s: %v", what, err)
					}
					if result.top != top || result.offset != sizeBefore(leaves, start) {
						t.Fatalf("%s rebuilds to another top, or begins at another byte", what)
					}
					// An opening carries a node for each run it leaves out,
					// and never more than two for a level.
					if len(nodes) > 2*6 || (start == 0 && end == count && len(nodes) != 0) {
						t.Fatalf("%s carries %d nodes", what, len(nodes))
					}
				}
			}
		}
	}
}

func TestAnOpeningThatIsChangedDoesNotRebuildToTheTop(t *testing.T) {
	leaves := fileLeaves(11)
	tree := must[*listTree](t)(buildListTree(fileTree, leaves))
	top := must[node](t)(tree.top())
	nodes := must[[]carried](t)(tree.open(4, 6))
	// rebuilds says whether an opening rebuilds to the top, and whether it
	// is refused before that.
	rebuilds := func(count, start uint64, opened []leaf, nodes []carried) (toTheTop, refused bool) {
		result, err := checkOpening(fileTree, count, start, opened, nodes)
		return err == nil && result.top == top, err != nil
	}
	if toTheTop, _ := rebuilds(11, 4, leaves[4:6], nodes); !toTheTop {
		t.Fatal("the opening as it was made does not rebuild to the top")
	}
	notTheTop := func(what string, count, start uint64, opened []leaf, nodes []carried) {
		t.Helper()
		if toTheTop, _ := rebuilds(count, start, opened, nodes); toTheTop {
			t.Fatalf("%s rebuilds to the top", what)
		}
	}
	refused := func(what string, count, start uint64, opened []leaf, nodes []carried) {
		t.Helper()
		if _, refused := rebuilds(count, start, opened, nodes); !refused {
			t.Fatalf("%s is not refused", what)
		}
	}
	// Another item in an opened place, and the same items one place on.
	notTheTop("another item in an opened place", 11, 4, []leaf{leaves[4], leaves[7]}, nodes)
	notTheTop("the same items one place on", 11, 5, leaves[4:6], nodes)
	notTheTop("the same items one place back", 11, 3, leaves[4:6], nodes)
	notTheTop("the same items the other way round", 11, 4, []leaf{leaves[5], leaves[4]}, nodes)
	// The same item with another size.
	resized := leaves[4]
	resized.node.size++
	notTheTop("an item with another size", 11, 4, []leaf{resized, leaves[5]}, nodes)
	// A carried node with another size, two carried nodes changed about,
	// one too few and one too many.
	for at := range nodes {
		wrongSize := slices.Clone(nodes)
		wrongSize[at].node.size++
		notTheTop(fmt.Sprintf("carried node %d with another size", at), 11, 4, leaves[4:6], wrongSize)
		wrongHash := slices.Clone(nodes)
		wrongHash[at].node.hash[31] ^= 1
		notTheTop(fmt.Sprintf("carried node %d with another hash", at), 11, 4, leaves[4:6], wrongHash)
	}
	if len(nodes) < 2 {
		t.Fatalf("the opening carries %d nodes", len(nodes))
	}
	swapped := slices.Clone(nodes)
	swapped[0], swapped[1] = swapped[1], swapped[0]
	notTheTop("two carried nodes changed about", 11, 4, leaves[4:6], swapped)
	refused("one carried node too few", 11, 4, leaves[4:6], nodes[1:])
	refused("one carried node too few at the end", 11, 4, leaves[4:6], nodes[:len(nodes)-1])
	refused("one carried node too many", 11, 4, leaves[4:6], append(slices.Clone(nodes), nodes[0]))
	// A list that is said to be longer or shorter.
	for _, count := range []uint64{6, 10, 12, 16, MaxListItems} {
		notTheTop(fmt.Sprintf("the list as %d items", count), count, 4, leaves[4:6], nodes)
	}
	refused("a list longer than any list", MaxListItems+1, 4, leaves[4:6], nodes)
	// Items outside the list, and none at all.
	refused("items beyond the end", 11, 10, leaves[4:6], nodes)
	refused("no items", 11, 4, nil, nodes)
	refused("items at a place no number reaches", 11, math.MaxUint64, leaves[4:6], nodes)
}

func TestAListsTreeHasNoPlaceBeyondItsCount(t *testing.T) {
	// Trees that fill a level by repeating their last item let that item be
	// shown again at the place after it. Here the place is not there.
	for _, count := range []uint64{1, 3, 5, 7} {
		leaves := fileLeaves(int(count))
		tree := must[*listTree](t)(buildListTree(fileTree, leaves))
		nodes := must[[]carried](t)(tree.open(count-1, count))
		if _, err := checkOpening(fileTree, count, count, leaves[count-1:], nodes); err == nil {
			t.Fatalf("the last of %d items is shown again at the place after it", count)
		}
		if _, err := tree.open(count, count+1); err == nil {
			t.Fatalf("a list of %d opens at the place after its last", count)
		}
		if _, err := tree.open(count-1, count-1); err == nil {
			t.Fatalf("a list of %d opens with nothing opened", count)
		}
	}
}

func TestSizesThatDoNotFitAreAnError(t *testing.T) {
	leaves := fileLeaves(2)
	leaves[0].node.size = math.MaxUint64
	if _, err := topOf(fileTree, leaves); err == nil {
		t.Fatal("sizes that do not fit were added")
	}
	if _, err := buildListTree(fileTree, leaves); err == nil {
		t.Fatal("sizes that do not fit were added in a tree that is kept")
	}
	// The same for the bytes that come before an opened item.
	four := fileLeaves(4)
	tree := must[*listTree](t)(buildListTree(fileTree, four))
	nodes := must[[]carried](t)(tree.open(3, 4))
	for at := range nodes {
		nodes[at].node.size = math.MaxUint64
	}
	if _, err := checkOpening(fileTree, 4, 3, four[3:], nodes); err == nil {
		t.Fatal("sizes that do not fit were added in an opening")
	}
}

func TestANameInASortedFolderIsFoundAtItsOnePlaceOrShownMissing(t *testing.T) {
	type verdict struct{ answered, present bool }
	for count := 1; count <= 20; count++ {
		leaves := sortedFolder(count)
		tree := must[*listTree](t)(buildListTree(folderTree, leaves))
		top := must[node](t)(tree.top())
		verdictAt := func(name string, index int) verdict {
			nodes := must[[]carried](t)(tree.open(uint64(index), uint64(index)+1))
			result, present, err := checkLookup(uint64(count), uint64(index), leaves[index], name, nodes)
			if err != nil || result.top != top {
				return verdict{}
			}
			return verdict{true, present}
		}
		for place, entry := range leaves {
			for index := range count {
				// The name itself: there, at its own place and nowhere else.
				if got := verdictAt(entry.name, index); got != (verdict{index == place, index == place}) {
					t.Fatalf("%d entries: the name at %d, asked at %d: %+v", count, place, index, got)
				}
				// A name just after it: missing, shown by the entry before
				// it, and by no other entry.
				if got := verdictAt(entry.name+"!", index); got != (verdict{index == place, false}) {
					t.Fatalf("%d entries: a name after %d, asked at %d: %+v", count, place, index, got)
				}
			}
		}
		// A name before every entry: missing, shown by the first entry.
		for index := range count {
			if got := verdictAt("a", index); got != (verdict{index == 0, false}) {
				t.Fatalf("%d entries: a name before all, asked at %d: %+v", count, index, got)
			}
		}
	}
}

func TestAFolderBuiltOutOfOrderCannotShowOneNameAtTwoPlaces(t *testing.T) {
	// An author is free to hash any list. Whatever it hashed, a name has
	// one place, and an entry anywhere else cannot be shown at all.
	generator := numbers(0x5c10_71c5_eed5_eed5)
	alphabet := []string{"a", "b", "c", "d", "e", "f"}
	type answer struct {
		index   int
		present bool
	}
	shownAnywhere := 0
	for range 400 {
		count := 2 + generator.next(16)
		names := make([]string, count)
		for at := range names {
			names[at] = alphabet[generator.next(len(alphabet))]
		}
		leaves := folderLeaves(names...)
		tree := must[*listTree](t)(listTreeOf(folderTree, leaves))
		top := must[node](t)(tree.top())
		for _, name := range alphabet {
			var answers []answer
			for index, entry := range leaves {
				nodes := must[[]carried](t)(tree.open(uint64(index), uint64(index)+1))
				result, present, err := checkLookup(uint64(count), uint64(index), entry, name, nodes)
				if err == nil && result.top == top {
					answers = append(answers, answer{index, present})
				}
				// An entry with this name that is shown as a plain opening
				// passes only at the place a search would reach.
				if entry.name == name {
					plain, err := checkOpening(folderTree, uint64(count), uint64(index), []leaf{entry}, nodes)
					opens := err == nil && plain.top == top
					foundHere := len(answers) > 0 && answers[len(answers)-1] == (answer{index, true})
					if opens != foundHere {
						t.Fatalf("%v: %s at %d opens: %v, and a search finds it there: %v", names, name, index, opens, foundHere)
					}
					if opens {
						shownAnywhere++
					}
				}
			}
			if len(answers) > 1 {
				t.Fatalf("%v: %s is answered at %+v", names, name, answers)
			}
		}
	}
	// The test would say nothing if no entry of these folders could ever be
	// shown.
	if shownAnywhere < 400 {
		t.Fatalf("only %d entries could be shown at all", shownAnywhere)
	}
}

func TestRunsOfAFolderBuiltOutOfOrderOpenOnlyWhereASearchAgrees(t *testing.T) {
	// "c" sits left of a fork named "b": it cannot be shown, alone or in a
	// run, and a search for it is sent to "b", which says it is missing.
	leaves := folderLeaves("a", "c", "b", "d")
	tree := must[*listTree](t)(listTreeOf(folderTree, leaves))
	top := must[node](t)(tree.top())
	opens := func(start, end int) bool {
		nodes := must[[]carried](t)(tree.open(uint64(start), uint64(end)))
		result, err := checkOpening(folderTree, 4, uint64(start), leaves[start:end], nodes)
		return err == nil && result.top == top
	}
	for _, run := range []struct {
		what       string
		start, end int
		opens      bool
	}{
		{"a", 0, 1, true},
		{"c, alone", 1, 2, false},
		{"a and c", 0, 2, false},
		{"c and b", 1, 3, false},
		{"b", 2, 3, true},
		{"d", 3, 4, true},
		{"b and d", 2, 4, true},
		{"c, b and d", 1, 4, false},
		{"the whole list", 0, 4, false},
	} {
		if opens(run.start, run.end) != run.opens {
			t.Fatalf("%s opens: %v", run.what, !run.opens)
		}
	}
	nodes := must[[]carried](t)(tree.open(2, 3))
	result, present, err := checkLookup(4, 2, leaves[2], "c", nodes)
	if err != nil || result.top != top || present {
		t.Fatalf("a search for c at the place of b: present: %v (%v)", present, err)
	}
	// And at the place where c is, a search for c is not answered.
	nodes = must[[]carried](t)(tree.open(1, 2))
	if result, _, err := checkLookup(4, 1, leaves[1], "c", nodes); err == nil && result.top == top {
		t.Fatal("a search for c is answered at a place it would not reach")
	}
}

func TestACarriedNameIsHeldToTheForkItEnters(t *testing.T) {
	// The first name under a carried right half is part of the fork above
	// it. Another name there gives another top.
	leaves := sortedFolder(8)
	tree := must[*listTree](t)(buildListTree(folderTree, leaves))
	top := must[node](t)(tree.top())
	nodes := must[[]carried](t)(tree.open(1, 2))
	if honest := must[shown](t)(checkOpening(folderTree, 8, 1, leaves[1:2], nodes)); honest.top != top {
		t.Fatal("the opening as it was made does not rebuild to the top")
	}
	last := len(nodes) - 1
	nodes[last].firstName = "name-9999"
	if forged, err := checkOpening(folderTree, 8, 1, leaves[1:2], nodes); err == nil && forged.top == top {
		t.Fatal("a carried node under another name rebuilds to the top")
	}
}

func TestAFilesTreeCarriesNoNames(t *testing.T) {
	leaves := fileLeaves(4)
	tree := must[*listTree](t)(buildListTree(fileTree, leaves))
	nodes := must[[]carried](t)(tree.open(0, 1))
	for at := range nodes {
		if nodes[at].firstName != "" {
			t.Fatal("a file's tree gave a name")
		}
	}
	nodes[0].firstName = "x"
	if _, err := checkOpening(fileTree, 4, 0, leaves[:1], nodes); err == nil {
		t.Fatal("a node of a file's tree was taken with a name")
	}
}

func TestTheCarriedNodeThatBeginsAFoldersListHasNoName(t *testing.T) {
	// A fork hashes the first name of its right half. The node that begins
	// the list is in no right half, so nothing would cover a name for it:
	// it has none, and one that comes with a name is refused. Every other
	// carried node has the name its fork hashes, and no other.
	for count := 2; count <= 20; count++ {
		leaves := sortedFolder(count)
		tree := must[*listTree](t)(buildListTree(folderTree, leaves))
		top := must[node](t)(tree.top())
		for start := 0; start < count; start++ {
			for end := start + 1; end <= count; end++ {
				nodes := must[[]carried](t)(tree.open(uint64(start), uint64(end)))
				rebuilds := func(nodes []carried) bool {
					result, err := checkOpening(folderTree, uint64(count), uint64(start), leaves[start:end], nodes)
					return err == nil && result.top == top
				}
				if !rebuilds(nodes) {
					t.Fatalf("%d entries, %d up to %d: the opening does not rebuild to the top", count, start, end)
				}
				for index := range nodes {
					what := fmt.Sprintf("%d entries, %d up to %d, carried node %d", count, start, end, index)
					beginsTheList := index == 0 && start > 0
					if (nodes[index].firstName == "") != beginsTheList {
						t.Fatalf("%s is carried under the name %q", what, nodes[index].firstName)
					}
					renamed := slices.Clone(nodes)
					renamed[index].firstName = "name-9999"
					if rebuilds(renamed) {
						t.Fatalf("%s rebuilds to the top under another name", what)
					}
					nameless := slices.Clone(nodes)
					nameless[index].firstName = ""
					if rebuilds(nameless) != beginsTheList {
						t.Fatalf("%s, with no name, rebuilds to the top: %v", what, !beginsTheList)
					}
				}
			}
		}
	}
}
