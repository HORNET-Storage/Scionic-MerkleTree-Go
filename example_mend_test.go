package scionic_test

import (
	"bytes"
	"errors"
	"fmt"
	"log"

	scionic "github.com/HORNET-Storage/Scionic-MerkleTree-Go/v2"
)

// Bytes that were harmed where a store keeps them are found when the tree is
// verified, named by their hash, and fetched again on their own.
func Example_damageIsFoundAndMended() {
	// A tree in two stores: a publisher's, and a mirror of it.
	publisher := scionic.NewMemoryStore()
	builder := scionic.NewBuilder(publisher, scionic.FastCDCV1)
	weights, err := builder.FileFromBytes(bytes.Repeat([]byte{7}, 3<<20))
	if err != nil {
		log.Fatal(err)
	}
	root, err := builder.Finish(weights, scionic.ExtraData{})
	if err != nil {
		log.Fatal(err)
	}
	mirror := scionic.NewMemoryStore()
	if _, err := scionic.Fetch(mirror, root, scionic.NewResponder(publisher)); err != nil {
		log.Fatal(err)
	}

	// A chunk is harmed where the mirror keeps it.
	chunk := mirror.Hashes(scionic.StoredChunk)[0]
	mirror.Remove(scionic.StoredChunk, chunk)
	mirror.Put(scionic.StoredChunk, chunk, []byte("not what the hash says"))

	// Verifying finds the damage, and the error names what was harmed.
	_, err = scionic.Verify(mirror, root)
	var damaged *scionic.Error
	if errors.Is(err, scionic.ErrCorrupt) && errors.As(err, &damaged) {
		fmt.Println("the damaged chunk is named:", damaged.Hash == chunk)
	}

	// With DiscardDamaged the damaged parts are removed, and the next fetch
	// brings back exactly those.
	_, err = scionic.VerifyWith(mirror, root, scionic.VerifyOptions{DiscardDamaged: true})
	fmt.Println("it is missing now:", errors.Is(err, scionic.ErrMissing))
	if _, err := scionic.Fetch(mirror, root, scionic.NewResponder(publisher)); err != nil {
		log.Fatal(err)
	}
	if _, err := scionic.Verify(mirror, root); err != nil {
		log.Fatal(err)
	}
	fmt.Println("the tree is whole again")
	// Output:
	// the damaged chunk is named: true
	// it is missing now: true
	// the tree is whole again
}
