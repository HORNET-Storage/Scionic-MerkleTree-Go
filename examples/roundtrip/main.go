// Roundtrip takes a tree its whole way round, with a store on disk.
//
// It builds a tree from a folder, checks it, fetches it into a second store
// as a receiver would, reads stretches of its largest file without the rest,
// writes the tree out again, and holds the copy to the same root. It prints
// how long each step took.
//
//	go run ./examples/roundtrip <folder> <work-dir>
//
// <work-dir> must not exist. The stores and the copy are made in it.
//
// The store here keeps each thing as a file named by its hash. It is the
// smallest store that does not hold a tree in memory, and it shows what a
// store has to do: keep bytes under a hash, and give them back.
package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	scionic "github.com/HORNET-Storage/Scionic-Merkle-Tree/v2"
)

// dirStore keeps each thing as a file named by its hash.
type dirStore struct {
	dir string
	// writing lets one writer at a time keep a thing, by the start of its
	// hash. Two writers of the same thing would otherwise each put a file in
	// its place, and the second would put it in the place of a file that
	// someone may be reading, which Windows does not allow.
	writing [1024]sync.Mutex
	// read keeps memory that things are read into for the next read: fresh
	// memory for each chunk, as Get takes, costs some systems more than the
	// reading does.
	read sync.Pool
}

func (s *dirStore) path(kind scionic.Stored, hash scionic.Hash) string {
	folder := "records"
	switch kind {
	case scionic.StoredChunk:
		folder = "chunks"
	case scionic.StoredFileList:
		folder = "files"
	case scionic.StoredFolderList:
		folder = "folders"
	}
	name := hash.Hex()
	return filepath.Join(s.dir, folder, name[:2], name)
}

// Put keeps bytes under hash. Keeping what is kept already changes nothing.
func (s *dirStore) Put(kind scionic.Stored, hash scionic.Hash, content []byte) error {
	lock := &s.writing[int(binary.BigEndian.Uint16(hash[:2]))%len(s.writing)]
	lock.Lock()
	defer lock.Unlock()
	path := s.path(kind, hash)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// Written under another name first, so that a file is never found under
	// a hash before all of its bytes are there.
	unfinished, err := os.CreateTemp(filepath.Dir(path), "writing-*")
	if err != nil {
		return err
	}
	_, err = unfinished.Write(content)
	if closed := unfinished.Close(); err == nil {
		err = closed
	}
	if err == nil {
		err = os.Rename(unfinished.Name(), path)
	}
	if err != nil {
		os.Remove(unfinished.Name())
	}
	return err
}

// Get gives what is kept under hash, and false if nothing is.
func (s *dirStore) Get(kind scionic.Stored, hash scionic.Hash) ([]byte, bool, error) {
	content, err := os.ReadFile(s.path(kind, hash))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return content, true, nil
}

// Lend hands what is kept under hash to with, read into memory that was
// read into before. The library reads a tree's chunks this way.
func (s *dirStore) Lend(kind scionic.Stored, hash scionic.Hash, with func(bytes []byte) error) (bool, error) {
	file, err := os.Open(s.path(kind, hash))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return false, err
	}
	buffer, _ := s.read.Get().(*[]byte)
	if buffer == nil {
		buffer = new([]byte)
	}
	defer s.read.Put(buffer)
	if int64(cap(*buffer)) < info.Size() {
		*buffer = make([]byte, info.Size())
	}
	content := (*buffer)[:info.Size()]
	if _, err := io.ReadFull(file, content); err != nil {
		return false, err
	}
	return true, with(content)
}

// Size gives how many bytes are kept under hash, without reading them. A
// store need not have this method: the library uses it when it is there.
func (s *dirStore) Size(kind scionic.Stored, hash scionic.Hash) (uint64, bool, error) {
	info, err := os.Stat(s.path(kind, hash))
	if errors.Is(err, fs.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return uint64(info.Size()), true, nil
}

// Remove forgets what is kept under hash.
func (s *dirStore) Remove(kind scionic.Stored, hash scionic.Hash) error {
	if err := os.Remove(s.path(kind, hash)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// largestFile gives the largest file of a tree, each folder looked into
// once.
func largestFile(store scionic.Store, top scionic.Target) (scionic.Target, bool, error) {
	var largest scionic.Target
	found := false
	seen := make(map[scionic.Hash]bool)
	pending := []scionic.Target{top}
	for len(pending) > 0 {
		target := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if target.Kind == scionic.KindFile {
			if !found || largest.Size < target.Size {
				largest, found = target, true
			}
			continue
		}
		if seen[target.Hash] {
			continue
		}
		seen[target.Hash] = true
		list, err := scionic.FolderOf(store, target)
		if err != nil {
			return largest, false, err
		}
		for _, entry := range list.Entries() {
			pending = append(pending, entry.Target())
		}
	}
	return largest, found, nil
}

func report(out io.Writer, step string, started time.Time, size uint64) {
	seconds := time.Since(started).Seconds()
	rate := float64(size) / (1 << 20) / max(seconds, 1e-9)
	fmt.Fprintf(out, "%-12s %8.2f s  %8.0f MiB/s\n", step, seconds, rate)
}

// roundtrip takes the folder its whole way round, with its stores and its
// copy in the new folder work, and gives the tree's root.
func roundtrip(folder, work string, out io.Writer) (scionic.Root, error) {
	var none scionic.Root
	if err := os.Mkdir(work, 0o755); err != nil {
		return none, err
	}

	source := &dirStore{dir: filepath.Join(work, "source-store")}
	started := time.Now()
	root, err := scionic.Build(source, folder, scionic.BuildOptions{})
	if err != nil {
		return none, err
	}
	record, err := scionic.RecordOf(source, root)
	if err != nil {
		return none, err
	}
	size, totals := record.Top().Size, record.Totals()
	report(out, "built", started, size)
	fmt.Fprintf(out, "root         %s\n", root)
	fmt.Fprintf(out, "the tree     %d bytes; kept once: %d chunks of %d bytes, and %d bytes of lists\n",
		size, totals.Chunks, totals.ChunkBytes, totals.ListBytes)

	started = time.Now()
	summary, err := scionic.Verify(source, root)
	if err != nil {
		return none, err
	}
	report(out, "checked", started, totals.ChunkBytes)
	fmt.Fprintf(out, "             %d different files in %d different folders\n", summary.Files, summary.Folders)

	fetched := &dirStore{dir: filepath.Join(work, "fetched-store")}
	started = time.Now()
	if _, err := scionic.Fetch(fetched, root, scionic.NewResponder(source)); err != nil {
		return none, err
	}
	report(out, "fetched", started, totals.ChunkBytes)

	file, found, err := largestFile(source, record.Top())
	if err != nil {
		return none, err
	}
	if found {
		// A stretch of 4 KiB is read from 64 places spread over the file, each
		// asked for on its own, and the time one takes on average is printed:
		// a single read is shorter than some systems' clocks can tell apart.
		length := min(file.Size, 4096)
		const places = 64
		remote, err := scionic.OpenRemote(scionic.NewResponder(source), root)
		if err != nil {
			return none, err
		}
		var stretch bytes.Buffer
		started = time.Now()
		for place := range uint64(places) {
			stretch.Reset()
			if err := remote.Read(file, (file.Size-length)*place/(places-1), length, &stretch); err != nil {
				return none, err
			}
		}
		fmt.Fprintf(out, "a stretch    %8.4f s  %d bytes at a time, from %d places in a file of %d bytes\n",
			time.Since(started).Seconds()/places, stretch.Len(), places, file.Size)
	}

	written := filepath.Join(work, "written")
	started = time.Now()
	if err := scionic.WriteToDisk(fetched, root, written); err != nil {
		return none, err
	}
	report(out, "written out", started, size)

	again := &dirStore{dir: filepath.Join(work, "again-store")}
	started = time.Now()
	rebuilt, err := scionic.Build(again, written, scionic.BuildOptions{})
	if err != nil {
		return none, err
	}
	report(out, "built again", started, size)
	if rebuilt != root {
		return none, fmt.Errorf("the copy has the root %s, and not %s", rebuilt, root)
	}
	fmt.Fprintln(out, "the copy has the same root")
	return root, nil
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: roundtrip <folder> <work-dir>")
		os.Exit(2)
	}
	if _, err := roundtrip(os.Args[1], os.Args[2], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
