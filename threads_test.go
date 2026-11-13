package scionic

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"
)

// Several goroutines build and check what one builds and checks. And a
// build that fails, or whose store falls over, ends: it is told as it
// happened, nobody is left waiting, and no folder is left held open.

func TestOneFileIsTheSameWhateverNumberOfThreadsKeepItsPieces(t *testing.T) {
	// 6,250 pieces of 16 bytes: more than a builder hands on at a time.
	data := corpusBytes("threads-one-file", 100_000)
	var roots []Root
	var stores [][]storedThing
	for _, threads := range []int{1, 2, 8, 0} {
		store := NewMemoryStore()
		builder := NewBuilder(store, smallPieces(t)).Threads(threads)
		streamed := must[Target](t)(builder.File(bytes.NewReader(data)))
		inPlace := must[Target](t)(builder.FileFromBytes(data))
		if streamed != inPlace {
			t.Fatalf("%d threads: a file from a stream is another file than the same bytes in memory", threads)
		}
		roots = append(roots, must[Root](t)(builder.Finish(streamed, ExtraData{})))
		stores = append(stores, contentsOf(store))
	}
	for at := 1; at < len(roots); at++ {
		if roots[at] != roots[0] || !slices.Equal(stores[at], stores[0]) {
			t.Fatal("the number of threads changed the tree")
		}
	}
}

func TestAFileCutByContentIsTheSameWithHelp(t *testing.T) {
	data := corpusBytes("threads-by-content", 9<<20)
	var roots []Root
	for _, threads := range []int{1, 4} {
		store := NewMemoryStore()
		builder := NewBuilder(store, FastCDCV1).Threads(threads)
		streamed := must[Target](t)(builder.File(bytes.NewReader(data)))
		if streamed != must[Target](t)(builder.FileFromBytes(data)) {
			t.Fatalf("%d threads: a file from a stream is another file than the same bytes in memory", threads)
		}
		if store.Count(StoredPiece) <= 2 {
			t.Fatal("the file was not cut into several pieces")
		}
		root := must[Root](t)(builder.Finish(streamed, ExtraData{}))
		for _, workers := range []int{1, 4} {
			must[*Summary](t)(VerifyWith(store, root, VerifyOptions{Workers: workers, Recut: true}))
		}
		roots = append(roots, root)
	}
	if roots[0] != roots[1] {
		t.Fatal("help changed the tree")
	}
}

func TestAFolderIsTheSameWhenThreadsOutnumberItsFiles(t *testing.T) {
	folder := writeTree(t, sampleFiles())
	var roots []Root
	var stores [][]storedThing
	for _, workers := range []int{1, 2, 64} {
		store := NewMemoryStore()
		root := must[Root](t)(Build(store, folder, BuildOptions{Cutting: smallPieces(t), Workers: workers}))
		must[*Summary](t)(VerifyWith(store, root, VerifyOptions{Workers: workers}))
		roots = append(roots, root)
		stores = append(stores, contentsOf(store))
	}
	for at := 1; at < len(roots); at++ {
		if roots[at] != roots[0] || !slices.Equal(stores[at], stores[0]) {
			t.Fatal("the number of threads changed the tree")
		}
	}
}

// errWillNotKeep is what a store says that will not keep a piece.
var errWillNotKeep = errors.New("this store will not keep that piece")

// refusingStore will not keep one piece. It has the three methods that a
// store has to have, and no others.
type refusingStore struct {
	inner   *MemoryStore
	refused Hash
}

func (s refusingStore) Put(kind Stored, hash Hash, content []byte) error {
	if kind == StoredPiece && hash == s.refused {
		return errWillNotKeep
	}
	return s.inner.Put(kind, hash, content)
}

func (s refusingStore) Get(kind Stored, hash Hash) ([]byte, bool, error) {
	return s.inner.Get(kind, hash)
}

func (s refusingStore) Remove(kind Stored, hash Hash) error {
	return s.inner.Remove(kind, hash)
}

func TestAPieceThatCannotBeKeptStopsTheFileWhoeverWasKeepingIt(t *testing.T) {
	data := corpusBytes("threads-refused", 100_000)
	refused := HashOf(data[64_000:64_016])
	for _, threads := range []int{1, 8} {
		store := refusingStore{inner: NewMemoryStore(), refused: refused}
		builder := NewBuilder(store, smallPieces(t)).Threads(threads)
		if _, err := builder.File(bytes.NewReader(data)); !errors.Is(err, errWillNotKeep) {
			t.Fatalf("%d threads, from a stream: %v", threads, err)
		}
		if _, err := builder.FileFromBytes(data); !errors.Is(err, errWillNotKeep) {
			t.Fatalf("%d threads, from memory: %v", threads, err)
		}
		// A file whose pieces are not all kept has no list.
		if store.inner.Count(StoredFileList) != 0 {
			t.Fatalf("%d threads: a file whose pieces are not all kept has a list", threads)
		}
	}
}

// movable reports whether a folder can be given another name and its own
// again. Windows refuses that for a folder that anything holds open, so
// this says that a build has let go of everything.
func movable(folder string) error {
	if err := os.Rename(folder, folder+"-moved"); err != nil {
		return err
	}
	return os.Rename(folder+"-moved", folder)
}

func TestTheFailureThatIsToldIsTheOneThatHappened(t *testing.T) {
	// A folder of many files, one of which holds a piece the store refuses.
	// The goroutines that stop because of it have nothing of their own to
	// tell.
	files := map[string][]byte{}
	for number := range 64 {
		files[fmt.Sprintf("file-%02d", number)] = corpusBytes(fmt.Sprintf("failure-%d", number), 64)
	}
	folder := writeTree(t, files)
	refused := HashOf(corpusBytes("failure-40", 64)[:16])
	for _, workers := range []int{1, 2, 8} {
		store := refusingStore{inner: NewMemoryStore(), refused: refused}
		if _, err := Build(store, folder, BuildOptions{Cutting: smallPieces(t), Workers: workers}); !errors.Is(err, errWillNotKeep) {
			t.Fatalf("%d workers: %v", workers, err)
		}
		if err := movable(folder); err != nil {
			t.Fatalf("%d workers: the folder is still held after the build failed: %v", workers, err)
		}
	}
}

// fallingStore falls over whenever it is handed a piece, as one would whose
// own code has a fault.
type fallingStore struct{ *MemoryStore }

func (s fallingStore) Put(kind Stored, hash Hash, content []byte) error {
	if kind == StoredPiece {
		panic("this store falls over")
	}
	return s.MemoryStore.Put(kind, hash, content)
}

// fellWith gives what a panic that reached a caller was raised with: the
// value itself, or the value that a goroutine of the library fell with.
func fellWith(raised any) any {
	if onWorker, is := raised.(*WorkerPanic); is {
		return onWorker.Value
	}
	return raised
}

// endsByFalling runs work on a goroutine of its own, and gives what it fell
// over with. Work that ends in any other way, or does not end, fails the
// test.
func endsByFalling(t *testing.T, what string, work func()) any {
	t.Helper()
	type outcome struct {
		raised any
		fell   bool
	}
	done := make(chan outcome, 1)
	go func() {
		fell := true
		defer func() { done <- outcome{recover(), fell} }()
		work()
		fell = false
	}()
	select {
	case ended := <-done:
		if !ended.fell {
			t.Fatalf("%s: it ended, and it did not fall over as its store did", what)
		}
		return fellWith(ended.raised)
	case <-time.After(20 * time.Second):
		t.Fatalf("%s: it was left waiting", what)
	}
	return nil
}

func TestAStoreThatFallsOverDoesNotLeaveABuildWaiting(t *testing.T) {
	// More files than the goroutines take at once: if every one that cuts
	// is gone, the one that reads the folder must not wait for them.
	files := map[string][]byte{}
	for number := range 400 {
		files[fmt.Sprintf("dir-%d/file-%03d", number%40, number)] = corpusBytes(fmt.Sprintf("falling-%d", number), 16)
	}
	folder := writeTree(t, files)
	for _, workers := range []int{1, 2, 8} {
		what := fmt.Sprintf("a build with %d workers", workers)
		// The build ends, and it ends as the store did: by falling over.
		raised := endsByFalling(t, what, func() {
			Build(fallingStore{NewMemoryStore()}, folder, BuildOptions{Cutting: smallPieces(t), Workers: workers})
		})
		if raised != "this store falls over" {
			t.Fatalf("%s fell over with %v", what, raised)
		}
		// And it has let go of every folder it held.
		if err := movable(folder); err != nil {
			t.Fatalf("%s: the folder is still held after the build fell over: %v", what, err)
		}
	}
}

func TestAStoreThatFallsOverDoesNotLeaveAFileOrACheckWaiting(t *testing.T) {
	data := corpusBytes("threads-falling-file", 100_000)
	for _, threads := range []int{1, 2, 8} {
		what := fmt.Sprintf("a file with %d threads", threads)
		for _, fromStream := range []bool{true, false} {
			raised := endsByFalling(t, what, func() {
				builder := NewBuilder(fallingStore{NewMemoryStore()}, smallPieces(t)).Threads(threads)
				if fromStream {
					builder.File(bytes.NewReader(data))
				} else {
					builder.FileFromBytes(data)
				}
			})
			if raised != "this store falls over" {
				t.Fatalf("%s fell over with %v", what, raised)
			}
		}
	}
	// A check of a held tree, whose store falls over when a piece is asked
	// for.
	store := NewMemoryStore()
	builder := NewBuilder(store, smallPieces(t))
	root := must[Root](t)(builder.Finish(must[Target](t)(builder.FileFromBytes(data)), ExtraData{}))
	for _, workers := range []int{1, 2, 8} {
		what := fmt.Sprintf("a check with %d workers", workers)
		raised := endsByFalling(t, what, func() {
			VerifyWith(fallingOnGet{store}, root, VerifyOptions{Workers: workers})
		})
		if raised != "this store falls over" {
			t.Fatalf("%s fell over with %v", what, raised)
		}
	}
}

// fallingOnGet falls over whenever a piece is asked for.
type fallingOnGet struct{ *MemoryStore }

func (s fallingOnGet) Get(kind Stored, hash Hash) ([]byte, bool, error) {
	if kind == StoredPiece {
		panic("this store falls over")
	}
	return s.MemoryStore.Get(kind, hash)
}
