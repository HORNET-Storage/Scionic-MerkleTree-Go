package scionic_test

import (
	"bytes"
	"fmt"
	"log"

	scionic "github.com/HORNET-Storage/Scionic-MerkleTree-Go/v2"
)

// A folder becomes a Scionic Merkle Tree, the tree is verified, one file is
// read out of it, and the tree is written back to disk.
func Example() {
	// The example works in a folder of its own, which holds a small model.
	defer workIn(map[string][]byte{
		"models/my-model/config.json":       []byte("{\"layers\": 12}\n"),
		"models/my-model/weights/model.bin": bytes.Repeat([]byte{7}, 3<<20),
	})()

	// A store keeps chunks and lists by their hashes. This one is in memory.
	store := scionic.NewMemoryStore()

	// Build a tree from a folder. The root depends on the folder's contents
	// alone: not on where the folder is, what it is called, or how many
	// goroutines built it.
	root, err := scionic.Build(store, "models/my-model", scionic.BuildOptions{})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(root) // bafkrei...

	// Verify everything the store holds for that root: every list, every chunk.
	summary, err := scionic.Verify(store, root)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%d files, %d bytes\n", summary.Files, summary.Record.Top().Size)

	// Read one file by its path in the tree.
	entry, found, err := scionic.Lookup(store, root, "config.json")
	if err != nil {
		log.Fatal(err)
	}
	if found {
		content, err := scionic.ReadFileBytes(store, entry.Target())
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("config.json holds %d bytes\n", len(content))
	}

	// Write the whole tree back out, to a path that does not exist yet.
	if err := scionic.WriteToDisk(store, root, "restored"); err != nil {
		log.Fatal(err)
	}
	// Output:
	// bafkreifxao4std4w2b2cmzl6boesplar2mlntpqdwx43ujnvijef7hbbsy
	// 2 files, 3145743 bytes
	// config.json holds 15 bytes
}
