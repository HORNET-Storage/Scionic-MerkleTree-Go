package scionic_test

import (
	"bytes"
	"fmt"
	"log"

	scionic "github.com/HORNET-Storage/Scionic-MerkleTree-Go/v2"
)

// Anything that answers requests is a peer: a connection to another machine,
// or a store in the same program, as here.
func Example_syncBetweenTwoStores() {
	// A publisher builds a tree. Here it is built from bytes in memory.
	publisher := scionic.NewMemoryStore()
	builder := scionic.NewBuilder(publisher, scionic.FastCDCV1)
	readme, err := builder.FileFromBytes([]byte("# My model\n"))
	if err != nil {
		log.Fatal(err)
	}
	weights, err := builder.FileFromBytes(bytes.Repeat([]byte{7}, 3<<20))
	if err != nil {
		log.Fatal(err)
	}
	readmeEntry, err := scionic.NewEntry("README.md", readme, scionic.ExtraData{})
	if err != nil {
		log.Fatal(err)
	}
	weightsEntry, err := scionic.NewEntry("weights.bin", weights, scionic.ExtraData{})
	if err != nil {
		log.Fatal(err)
	}
	top, err := builder.Folder([]scionic.Entry{readmeEntry, weightsEntry})
	if err != nil {
		log.Fatal(err)
	}
	root, err := builder.Finish(top, scionic.ExtraData{})
	if err != nil {
		log.Fatal(err)
	}

	// A responder answers requests from the publisher's store.
	peer := scionic.NewResponder(publisher)

	// Read a part of the tree and keep nothing: find one file by its name,
	// then take 16 bytes from the middle of it. Only the chunk that holds
	// those bytes is sent, and it is verified against the root.
	remote, err := scionic.OpenRemote(peer, root)
	if err != nil {
		log.Fatal(err)
	}
	entry, found, err := remote.Lookup("weights.bin")
	if err != nil || !found {
		log.Fatal("the tree has the file: ", err)
	}
	var middle bytes.Buffer
	if err := remote.Read(entry.Target(), 1<<20, 16, &middle); err != nil {
		log.Fatal(err)
	}
	fmt.Println(middle.Bytes())

	// Or fetch the whole tree into another store. Each part is verified
	// before it is kept, and what the store already holds is skipped.
	mirror := scionic.NewMemoryStore()
	summary, err := scionic.Fetch(mirror, root, peer)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(summary.Files, "files")
	// Output:
	// [7 7 7 7 7 7 7 7 7 7 7 7 7 7 7 7]
	// 2 files
}
