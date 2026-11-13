package scionic_test

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"os"

	scionic "github.com/HORNET-Storage/Scionic-Merkle-Tree/v2"
)

// A whole tree can travel as one stream, and two trees in one store can be
// compared.
func Example_bundlesAndChanges() {
	// The example works in a folder of its own, which holds two releases.
	defer workIn(map[string][]byte{
		"release-1/README.md":   []byte("# My model\n"),
		"release-1/weights.bin": bytes.Repeat([]byte{7}, 3<<20),
		"release-2/README.md":   []byte("# My model, improved\n"),
		"release-2/weights.bin": bytes.Repeat([]byte{7}, 3<<20),
		"release-2/LICENSE":     []byte("MIT\n"),
	})()

	store := scionic.NewMemoryStore()
	older, err := scionic.Build(store, "release-1", scionic.BuildOptions{})
	if err != nil {
		log.Fatal(err)
	}
	newer, err := scionic.Build(store, "release-2", scionic.BuildOptions{})
	if err != nil {
		log.Fatal(err)
	}

	// What changed, by path. The chunks that both releases share are held once.
	changes, err := scionic.Diff(store, older, newer)
	if err != nil {
		log.Fatal(err)
	}
	for _, change := range changes {
		fmt.Println(change.Kind, change.Path)
	}

	// The new release as one file: the record, then every list and every
	// chunk once.
	bundle, err := os.Create("release-2.bundle")
	if err != nil {
		log.Fatal(err)
	}
	if err := scionic.Export(store, newer, bufio.NewWriter(bundle)); err != nil {
		log.Fatal(err)
	}
	if err := bundle.Close(); err != nil {
		log.Fatal(err)
	}

	// Reading it back runs the same checks as a fetch from a peer.
	bundle, err = os.Open("release-2.bundle")
	if err != nil {
		log.Fatal(err)
	}
	defer bundle.Close()
	elsewhere := scionic.NewMemoryStore()
	summary, err := scionic.Import(elsewhere, newer, bufio.NewReader(bundle))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(summary.Files, "files arrived")
	// Output:
	// added LICENSE
	// changed README.md
	// 3 files arrived
}
