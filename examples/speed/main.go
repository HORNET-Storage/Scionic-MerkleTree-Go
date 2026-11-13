// Speed measures how fast the parts of building and checking a tree are, on
// one file held in memory, so that no disk is in the way.
//
//	go run ./examples/speed [MiB [folder]]
//
// It prints, for a file of that many MiB of bytes that look random: cutting
// alone, hashing alone, building the file into a store in memory, and
// checking every piece of it again. Without a number, the file is 1 GiB.
//
// With a folder, it also builds that folder from disk into a store in
// memory, and prints the best of five runs: by then the system holds the
// files in memory, so it is the library that is measured and not the disk.
package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"time"

	scionic "github.com/HORNET-Storage/Scionic-Merkle-Tree/v2"
)

// filler gives bytes that look random and cost little to make: a xorshift
// generator. The Rust library's example makes the same bytes, so the two
// print the same root.
func filler(length int) []byte {
	state := uint64(0x9E37_79B9_7F4A_7C15)
	content := make([]byte, 0, length+8)
	for len(content) < length {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		content = binary.LittleEndian.AppendUint64(content, state)
	}
	return content[:length]
}

func report(out io.Writer, step string, started time.Time, size int) {
	seconds := time.Since(started).Seconds()
	rate := float64(size) / (1 << 20) / max(seconds, 1e-9)
	fmt.Fprintf(out, "%-22s %7.3f s  %8.0f MiB/s\n", step, seconds, rate)
}

// speed measures the parts on a file of so many MiB, and the building of
// folder if one is named. It gives the root of the file's tree.
func speed(megabytes int, folder string, out io.Writer) (scionic.Root, error) {
	var none scionic.Root
	data := filler(megabytes << 20)

	started := time.Now()
	pieces := scionic.FastCDCV1.Cut(data)
	report(out, "cut", started, len(data))
	fmt.Fprintf(out, "%-22s %d pieces\n", "", len(pieces))

	started = time.Now()
	var mixed byte
	for _, piece := range pieces {
		hash := scionic.HashOf(piece)
		mixed ^= hash[0]
	}
	report(out, "hashed", started, len(data))

	store := scionic.NewMemoryStore()
	started = time.Now()
	builder := scionic.NewBuilder(store, scionic.FastCDCV1)
	file, err := builder.FileFromBytes(data)
	if err != nil {
		return none, err
	}
	report(out, "built", started, len(data))

	root, err := builder.Finish(file, scionic.ExtraData{})
	if err != nil {
		return none, err
	}
	started = time.Now()
	if _, err := scionic.Verify(store, root); err != nil {
		return none, err
	}
	report(out, "checked", started, len(data))
	fmt.Fprintf(out, "root %s (%02x)\n", root, mixed)

	if folder != "" {
		best := math.MaxFloat64
		var size uint64
		var files int
		for range 5 {
			store := scionic.NewMemoryStore()
			started := time.Now()
			built, err := scionic.Build(store, folder, scionic.BuildOptions{})
			if err != nil {
				return none, err
			}
			best = min(best, time.Since(started).Seconds())
			record, err := scionic.RecordOf(store, built)
			if err != nil {
				return none, err
			}
			size = record.Top().Size
			files = store.Count(scionic.StoredFileList)
		}
		rate := float64(size) / (1 << 20) / max(best, 1e-9)
		fmt.Fprintf(out, "%-22s %7.3f s  %8.0f MiB/s\n", "built from disk", best, rate)
		fmt.Fprintf(out, "%-22s %d different files, %d bytes\n", "", files, size)
	}
	return root, nil
}

func main() {
	megabytes, folder := 1024, ""
	if len(os.Args) > 1 {
		given, err := strconv.Atoi(os.Args[1])
		if err != nil || given < 0 || given > 1<<20 {
			fmt.Fprintln(os.Stderr, "usage: speed [MiB [folder]]")
			os.Exit(2)
		}
		megabytes = given
	}
	if len(os.Args) > 2 {
		folder = os.Args[2]
	}
	if _, err := speed(megabytes, folder, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
