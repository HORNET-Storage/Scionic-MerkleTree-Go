// Proofsizes shows how small a Merkle branch is beside the list it stands in
// for.
//
//	go run ./examples/proofsizes
//
// In a Merkle DAG, checking one child of a parent takes the parent's whole
// list of hashes. Here a parent's list is a Merkle tree of its own, so one
// child is checked with a branch of it. This prints both sizes, in bytes on
// the wire, for folders of ten to a million entries and for files of ten to
// half a million chunks. A file of half a million chunks is about a
// terabyte.
package main

import (
	"fmt"
	"io"
	"os"

	scionic "github.com/HORNET-Storage/Scionic-Merkle-Tree/v2"
)

// folderCounts and fileCounts are the sizes that are measured.
var (
	folderCounts = []int{10, 1_000, 10_000, 1_000_000}
	fileCounts   = []int{10, 1_000, 10_000, 500_000}
)

// answerBytes is how many bytes a store's holder answers a request with.
func answerBytes(store scionic.Store, request scionic.Request) (int, error) {
	total := 0
	err := scionic.NewResponder(store).Answer(request.Encode(), func(frame []byte) error {
		total += len(frame)
		return nil
	})
	return total, err
}

// nameOf is the name of an entry: 16 bytes long.
func nameOf(number int) string {
	return fmt.Sprintf("file-%07d.bin", number)
}

// folderRow gives, for a folder of so many entries, how many bytes its whole
// list takes, and how many a branch that shows one entry by its name.
func folderRow(count int) (list, branch int, err error) {
	store := scionic.NewMemoryStore()
	cutting, err := scionic.FixedCutting(16)
	if err != nil {
		return 0, 0, err
	}
	builder := scionic.NewBuilder(store, cutting)
	file, err := builder.FileFromBytes([]byte("the same small file"))
	if err != nil {
		return 0, 0, err
	}
	entries := make([]scionic.Entry, 0, count)
	for number := range count {
		entry, err := scionic.NewEntry(nameOf(number), file, scionic.ExtraData{})
		if err != nil {
			return 0, 0, err
		}
		entries = append(entries, entry)
	}
	folder, err := builder.Folder(entries)
	if err != nil {
		return 0, 0, err
	}
	kept, _, err := store.Get(scionic.StoredFolderList, folder.Hash)
	if err != nil {
		return 0, 0, err
	}
	branch, err = answerBytes(store, scionic.RequestFolderName(folder.Hash, nameOf(count/2)))
	return len(kept), branch, err
}

// fileRow gives, for a file of so many chunks, how many bytes its whole
// list takes, and how many a branch that shows one chunk by its number.
func fileRow(count int) (list, branch int, err error) {
	// Chunks of one byte, so that a file of many chunks is small.
	store := scionic.NewMemoryStore()
	cutting, err := scionic.FixedCutting(1)
	if err != nil {
		return 0, 0, err
	}
	content := make([]byte, count)
	for at := range content {
		content[at] = byte(at % 251)
	}
	file, err := scionic.NewBuilder(store, cutting).FileFromBytes(content)
	if err != nil {
		return 0, 0, err
	}
	kept, _, err := store.Get(scionic.StoredFileList, file.Hash)
	if err != nil {
		return 0, 0, err
	}
	branch, err = answerBytes(store, scionic.RequestFileRun(file.Hash, uint64(count/2), 1))
	return len(kept), branch, err
}

func row(out io.Writer, count, list, branch int) {
	fmt.Fprintf(out, "%10d  %12d  %8d  %9.0fx\n", count, list, branch, float64(list)/float64(branch))
}

// sizes prints both tables.
func sizes(out io.Writer) error {
	fmt.Fprintln(out, "One entry of a folder, by its name. Names are 16 bytes long.")
	fmt.Fprintf(out, "%10s  %12s  %8s  %10s\n", "entries", "whole list", "branch", "smaller by")
	for _, count := range folderCounts {
		list, branch, err := folderRow(count)
		if err != nil {
			return err
		}
		row(out, count, list, branch)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "One chunk of a file, by its number.")
	fmt.Fprintf(out, "%10s  %12s  %8s  %10s\n", "chunks", "whole list", "branch", "smaller by")
	for _, count := range fileCounts {
		list, branch, err := fileRow(count)
		if err != nil {
			return err
		}
		row(out, count, list, branch)
	}
	return nil
}

func main() {
	if err := sizes(os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
