package scionic

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// What changed between two trees.
//
// A tree keeps a folder once, however many places the folder has in it. So
// a small tree can stand for a very large one, and whatever goes through a
// tree place by place has to know when to stop. Checking a tree, taking it
// in and sending it go through each folder once, and need no such care.
// Writing a tree out is in read_test.go. Listing what changed between two
// trees is here.

func sameChange(left, right Change) bool {
	return left.Kind == right.Kind && left.Path == right.Path && left.Old.Equal(right.Old) && left.New.Equal(right.New)
}

// writeFiles writes files under a folder that is there: each name may hold
// slashes.
func writeFiles(t *testing.T, folder string, files map[string][]byte) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(folder, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o666); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEachChangeIsListedOnceByItsPath(t *testing.T) {
	folder := writeTree(t, sampleFiles())
	options := BuildOptions{Cutting: smallPieces(t)}
	store := NewMemoryStore()
	old := must[Root](t)(Build(store, folder, options))
	if changes := must[[]Change](t)(Diff(store, old, old)); len(changes) != 0 {
		t.Fatalf("a tree differs from itself: %+v", changes)
	}
	// One piece of a file changes.
	model := corpusBytes("model", 1000)
	copy(model[320:336], corpusBytes("changed", 16))
	// A file gets another name.
	if err := os.Rename(filepath.Join(folder, "readme.txt"), filepath.Join(folder, "notes.txt")); err != nil {
		t.Fatal(err)
	}
	// A folder comes, with two files in it, and a folder goes.
	if err := os.RemoveAll(filepath.Join(folder, "data")); err != nil {
		t.Fatal(err)
	}
	// A file becomes a folder of the same name.
	if err := os.Remove(filepath.Join(folder, "empty.bin")); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, folder, map[string][]byte{
		"weights/model.bin": model,
		"more/one.bin":      corpusBytes("one", 40),
		"more/two.bin":      corpusBytes("two", 40),
		"empty.bin/inside":  []byte("no longer empty"),
	})
	new := must[Root](t)(Build(store, folder, options))
	changes := must[[]Change](t)(Diff(store, old, new))
	type listed struct {
		path string
		what ChangeKind
	}
	var got []listed
	for _, change := range changes {
		got = append(got, listed{change.Path, change.Kind})
	}
	want := []listed{
		{"data", ChangeRemoved},
		{"empty.bin", ChangeRemoved},
		{"empty.bin", ChangeAdded},
		{"more", ChangeAdded},
		{"notes.txt", ChangeAdded},
		{"readme.txt", ChangeRemoved},
		{"weights/model.bin", ChangeChanged},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%+v", got)
	}
	at := func(root Root, path string) Entry {
		t.Helper()
		entry, found, err := Lookup(store, root, path)
		if err != nil || !found {
			t.Fatalf("%q: %v %v", path, found, err)
		}
		return entry
	}
	for _, change := range changes {
		switch change.Kind {
		case ChangeAdded:
			if !change.New.Equal(at(new, change.Path)) || !change.Old.Equal(Entry{}) {
				t.Fatalf("%s: what was added is not what the new tree has there", change.Path)
			}
		case ChangeRemoved:
			if !change.Old.Equal(at(old, change.Path)) || !change.New.Equal(Entry{}) {
				t.Fatalf("%s: what was removed is not what the old tree had there", change.Path)
			}
		case ChangeChanged:
			if !change.Old.Equal(at(old, change.Path)) || !change.New.Equal(at(new, change.Path)) ||
				change.Old.Hash() == change.New.Hash() || change.Old.Kind() != KindFile ||
				change.Old.Size() != 1000 || change.New.Size() != 1000 {
				t.Fatalf("%s: what changed is not what the two trees have there", change.Path)
			}
		}
	}
	// The file that got another name is the same file.
	if changes[4].New.Target() != changes[5].Old.Target() {
		t.Fatal("the file that got another name is another file")
	}
	// The other way round, what was added is removed.
	back := must[[]Change](t)(Diff(store, new, old))
	if len(back) != len(changes) || back[3].Kind != ChangeRemoved || back[3].Path != "more" {
		t.Fatalf("%+v", back)
	}
	// What is the same in both trees is not read: the comparison works
	// without the folder that did not change, and without any file.
	store.Remove(StoredFolderList, at(new, "weights/shards").Hash())
	for _, kind := range []Stored{StoredFileList, StoredPiece} {
		for _, hash := range store.Hashes(kind) {
			store.Remove(kind, hash)
		}
	}
	if again := must[[]Change](t)(Diff(store, old, new)); !slices.EqualFunc(again, changes, sameChange) {
		t.Fatal("without what did not change, the changes are others")
	}
	// A folder that did change is needed.
	store.Remove(StoredFolderList, at(new, "weights").Hash())
	if _, err := Diff(store, old, new); !errors.Is(err, ErrMissing) {
		t.Fatal(err)
	}
}

func TestExtraDataThatChangedIsAChange(t *testing.T) {
	folder := writeTree(t, sampleFiles())
	store := NewMemoryStore()
	tagged := func(tag string) Root {
		t.Helper()
		return must[Root](t)(BuildWith(store, folder, BuildOptions{Cutting: smallPieces(t)}, func(_, name string, _ Kind) (ExtraData, error) {
			if name == "weights" || name == "readme.txt" {
				return ExtraData{}.WithText("tag", tag)
			}
			return ExtraData{}, nil
		}))
	}
	old, new := tagged("one"), tagged("two")
	if old == new {
		t.Fatal("two trees with other extra data have one root")
	}
	changes := must[[]Change](t)(Diff(store, old, new))
	if len(changes) != 2 || changes[0].Path != "readme.txt" || changes[1].Path != "weights" {
		t.Fatalf("%+v", changes)
	}
	for _, change := range changes {
		// The thing itself is the same. What is said about it differs.
		was, _ := change.Old.Extra().Text("tag")
		is, _ := change.New.Extra().Text("tag")
		if change.Kind != ChangeChanged || change.Old.Target() != change.New.Target() || was != "one" || is != "two" {
			t.Fatalf("%+v", change)
		}
	}
}

func TestTreesWithAFileOnTopHaveNoEntriesToCompare(t *testing.T) {
	store := NewMemoryStore()
	builder := NewBuilder(store, smallPieces(t))
	one := must[Target](t)(builder.FileFromBytes([]byte("one")))
	two := must[Target](t)(builder.FileFromBytes([]byte("two")))
	folder := must[Target](t)(builder.Folder(nil))
	finish := func(top Target, extra ExtraData) Root {
		t.Helper()
		return must[Root](t)(NewBuilder(store, smallPieces(t)).Finish(top, extra))
	}
	first, second, empty := finish(one, ExtraData{}), finish(two, ExtraData{}), finish(folder, ExtraData{})
	// The same file under two records is the same tree to compare.
	firstAgain := finish(one, must[ExtraData](t)(ExtraData{}.WithText("name", "again")))
	if first == firstAgain {
		t.Fatal("two records have one root")
	}
	if changes := must[[]Change](t)(Diff(store, first, firstAgain)); len(changes) != 0 {
		t.Fatalf("%+v", changes)
	}
	for _, pair := range [][2]Root{{first, second}, {first, empty}, {empty, second}} {
		if _, err := Diff(store, pair[0], pair[1]); !errors.Is(err, ErrInvalidData) {
			t.Fatal(err)
		}
	}
	if changes := must[[]Change](t)(Diff(store, empty, empty)); len(changes) != 0 {
		t.Fatalf("%+v", changes)
	}
}

// doubled gives a folder with one file, under so many folders that each
// hold the one below under two names.
func doubled(t *testing.T, builder *Builder, content string, levels int) Target {
	t.Helper()
	file := must[Target](t)(builder.FileFromBytes([]byte(content)))
	folder := must[Target](t)(builder.Folder([]Entry{plainEntry(t, "x", file)}))
	for range levels {
		folder = must[Target](t)(builder.Folder([]Entry{plainEntry(t, "a", folder), plainEntry(t, "b", folder)}))
	}
	return folder
}

func TestTwoSmallTreesThatDifferAtMorePlacesThanAListHoldsAreRefused(t *testing.T) {
	store := NewMemoryStore()
	builder := NewBuilder(store, smallPieces(t))
	finish := func(top Target) Root {
		t.Helper()
		return must[Root](t)(NewBuilder(store, smallPieces(t)).Finish(top, ExtraData{}))
	}
	// Two trees of 41 small folders each. They differ in one file, and that
	// file has 2^40 places.
	old, new := finish(doubled(t, builder, "old", 40)), finish(doubled(t, builder, "new", 40))
	if store.Count(StoredFolderList) != 82 {
		t.Fatalf("the two trees are %d folders", store.Count(StoredFolderList))
	}
	// The search stops soon after it has found more than it may list.
	_, err := DiffWithin(store, old, new, 10_000)
	var told *Error
	if !errors.Is(err, ErrLimitExceeded) || !errors.As(err, &told) || told.Limit != 10_000 || told.Requested <= 10_000 || told.Requested >= 11_000 {
		t.Fatal(err)
	}
	// Left to itself, it lists a million changes and no more.
	if DefaultMaxChanges != 1_000_000 {
		t.Fatal(DefaultMaxChanges)
	}
	if changes := must[[]Change](t)(Diff(store, old, old)); len(changes) != 0 {
		t.Fatalf("a tree differs from itself at %d places", len(changes))
	}
	// Ten levels of it are 1,024 places, and are listed.
	builder = NewBuilder(store, smallPieces(t))
	old, new = finish(doubled(t, builder, "old", 10)), finish(doubled(t, builder, "new", 10))
	changes := must[[]Change](t)(Diff(store, old, new))
	if len(changes) != 1024 || changes[0].Path != "a/a/a/a/a/a/a/a/a/a/x" || changes[1023].Path != "b/b/b/b/b/b/b/b/b/b/x" {
		t.Fatalf("%d changes", len(changes))
	}
	if within := must[[]Change](t)(DiffWithin(store, old, new, 1024)); !slices.EqualFunc(within, changes, sameChange) {
		t.Fatal("within a limit that is just enough, the changes are others")
	}
	if _, err := DiffWithin(store, old, new, 1023); !errors.Is(err, ErrLimitExceeded) {
		t.Fatal(err)
	}
}

func TestALimitOnChangesIsHeldExactly(t *testing.T) {
	folder := writeTree(t, sampleFiles())
	options := BuildOptions{Cutting: smallPieces(t)}
	store := NewMemoryStore()
	old := must[Root](t)(Build(store, folder, options))
	if err := os.Remove(filepath.Join(folder, "readme.txt")); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, folder, map[string][]byte{"weights/new.bin": []byte("new"), "data/new.bin": []byte("new")})
	new := must[Root](t)(Build(store, folder, options))
	if changes := must[[]Change](t)(Diff(store, old, new)); len(changes) != 3 {
		t.Fatalf("%+v", changes)
	}
	if changes := must[[]Change](t)(DiffWithin(store, old, new, 3)); len(changes) != 3 {
		t.Fatalf("%+v", changes)
	}
	for most := -1; most < 3; most++ {
		if _, err := DiffWithin(store, old, new, most); !errors.Is(err, ErrLimitExceeded) {
			t.Fatalf("%d: %v", most, err)
		}
	}
}
