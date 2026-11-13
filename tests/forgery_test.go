package tests

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/HORNET-Storage/Scionic-Merkle-Tree/v2/dag"
)

// Each test in this file is a forgery: it hands a verifier a tree, a packet or a
// batch that no honest sender could have produced for the root in question, and
// requires the verifier to refuse it. Every test first checks that the honest
// form of the same input is accepted, so a refusal cannot come from an unrelated
// fault in the setup.

func forgeryFlatDag(t *testing.T, n int) *dag.Dag {
	t.Helper()
	dir := t.TempDir()
	for i := 0; i < n; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d.txt", i)), []byte{byte('a' + i)}, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d, err := dag.CreateDag(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// forgedLeafNamed returns a self-consistent leaf that belongs to no DAG. It
// passes VerifyLeaf, because a leaf's hash covers only the leaf's own fields.
// Different names give different hashes.
func forgedLeafNamed(t *testing.T, name string) *dag.DagLeaf {
	t.Helper()
	b := dag.CreateDagLeafBuilder(name)
	b.SetType(dag.FileLeafType)
	b.SetData([]byte("echo forged"))
	leaf, err := b.BuildLeaf(nil)
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}

func forgedFileLeaf(t *testing.T) *dag.DagLeaf {
	t.Helper()
	return forgedLeafNamed(t, "install.sh")
}

// An extra child appended to a parent of a full DAG has no proof at all.
func TestForgeryFullDagRejectsExtraChild(t *testing.T) {
	d := forgeryFlatDag(t, 3)
	if err := d.Verify(); err != nil {
		t.Fatalf("legit DAG must verify: %v", err)
	}
	f := forgedFileLeaf(t)
	d.Leafs[f.Hash] = f
	root := d.Leafs[d.Root]
	root.Links = append(root.Links, f.Hash)
	if err := d.Verify(); err == nil {
		t.Fatal("full verification accepted an extra child")
	}
}

// A forged child that carries a real sibling's proof under its own hash.
func TestForgeryPartialRejectsSiblingProof(t *testing.T) {
	d := forgeryFlatDag(t, 3)
	realHash := d.Leafs[d.Root].Links[0]
	partial, err := d.GetPartial([]string{realHash}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := partial.Verify(); err != nil {
		t.Fatalf("legit partial must verify: %v", err)
	}
	p := partial.Leafs[partial.Root]
	if p.Proofs[realHash] == nil {
		t.Fatal("expected the partial to carry a proof for the real child")
	}
	f := forgedFileLeaf(t)
	partial.Leafs[f.Hash] = f
	p.Links = append(p.Links, f.Hash)
	p.Proofs[f.Hash] = p.Proofs[realHash]
	if err := partial.Verify(); err == nil {
		t.Fatal("partial verification accepted a sibling's proof")
	}
}

// A child of a pruned parent, sent leaf by leaf with no proof.
func TestForgeryTransmissionRejectsUnprovenChild(t *testing.T) {
	d := forgeryFlatDag(t, 3)
	realHash := d.Leafs[d.Root].Links[0]
	partial, err := d.GetPartial([]string{realHash}, true)
	if err != nil {
		t.Fatal(err)
	}
	packets := partial.GetLeafSequence()
	recv := &dag.Dag{Root: partial.Root, Leafs: map[string]*dag.DagLeaf{}}
	if err := recv.ApplyAndVerifyTransmissionPacket(packets[0]); err != nil {
		t.Fatalf("root packet must verify: %v", err)
	}
	if err := recv.VerifyTransmissionPacket(packets[1]); err != nil {
		t.Fatalf("real child packet must verify: %v", err)
	}
	bad := &dag.TransmissionPacket{
		Leaf:       forgedFileLeaf(t),
		ParentHash: partial.Root,
		Proofs:     map[string]*dag.ClassicTreeBranch{},
	}
	if err := recv.VerifyTransmissionPacket(bad); err == nil {
		t.Fatal("accepted a child of a pruned parent with no proof")
	}
}

// An extra child added to a batch whose parent has all of its children present.
func TestForgeryBatchedRejectsExtraChild(t *testing.T) {
	d := forgeryFlatDag(t, 3)
	batch := d.GetBatchedLeafSequence()[0]
	clean := &dag.Dag{Root: d.Root, Leafs: map[string]*dag.DagLeaf{}}
	if err := clean.VerifyBatchedTransmissionPacket(batch); err != nil {
		t.Fatalf("legit batch must verify: %v", err)
	}
	f := forgedFileLeaf(t)
	batch.Leaves = append(batch.Leaves, f)
	batch.Relationships[f.Hash] = d.Root
	recv := &dag.Dag{Root: d.Root, Leafs: map[string]*dag.DagLeaf{}}
	if err := recv.VerifyBatchedTransmissionPacket(batch); err == nil {
		t.Fatal("batched verification accepted an extra child")
	}
}

// A forged leaf under a file that commits to no children, after a CBOR round trip.
func TestForgeryDecodedDagRejectsChildUnderFile(t *testing.T) {
	d := forgeryFlatDag(t, 3)
	file := d.Leafs[d.Leafs[d.Root].Links[0]]
	f := forgedFileLeaf(t)
	d.Leafs[f.Hash] = f
	file.Links = []string{f.Hash}
	data, err := d.ToCBOR()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := dag.FromCBOR(data)
	if err != nil {
		return // refused while decoding: fine
	}
	if err := decoded.Verify(); err == nil {
		got, _ := decoded.GetContentFromLeaf(decoded.Leafs[file.Hash])
		t.Fatalf("decoded DAG verified, and %s returns forged bytes %q", file.ItemName, got)
	}
}

// No forged leaf, and the leaf count is unchanged: a real leaf that commits to
// no children is given a link to another real leaf. root.Links is visited in
// order, so Links[0] is reached before Links[1]; both directions are tried.
func TestForgeryFullDagRejectsLinkUnderChildlessLeaf(t *testing.T) {
	for _, tc := range []struct{ from, to int }{{1, 0}, {0, 1}} {
		d := forgeryFlatDag(t, 3)
		root := d.Leafs[d.Root]
		d.Leafs[root.Links[2]].Content = nil // a leaf without its content is legal, and turns the size checks off
		if err := d.Verify(); err != nil {
			t.Fatalf("legit DAG with one content-less leaf must verify: %v", err)
		}
		from, to := d.Leafs[root.Links[tc.from]], d.Leafs[root.Links[tc.to]]
		from.Links = []string{to.Hash} // from commits to zero links
		if err := d.Verify(); err == nil {
			got, _ := d.GetContentFromLeaf(from)
			t.Errorf("case %d to %d: verified a DAG in which %s returns the bytes of %s: %q",
				tc.from, tc.to, from.ItemName, to.ItemName, got)
		}
	}
}

// The same edit in a partial DAG. Eight leaves are edited in turn, so the edit
// lands on leaves whose hash is above and below the root's.
func TestForgeryPartialRejectsLinkUnderChildlessLeaf(t *testing.T) {
	d := forgeryFlatDag(t, 9)
	kept := append([]string(nil), d.Leafs[d.Root].Links[:8]...)
	for i, h := range kept {
		partial, err := d.GetPartial(kept, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := partial.Verify(); err != nil {
			t.Fatalf("legit partial must verify: %v", err)
		}
		partial.Leafs[h].Links = []string{kept[(i+1)%len(kept)]}
		if err := partial.Verify(); err == nil {
			t.Errorf("partial verification accepted a link from %s, which commits to no children", h)
		}
	}
}

// A forged leaf filed under a real child's key, with its Hash field set to the root's hash.
func TestForgeryPartialRejectsLeafFiledUnderAnotherHash(t *testing.T) {
	d := forgeryFlatDag(t, 3)
	realHash := d.Leafs[d.Root].Links[0]
	build := func() *dag.Dag {
		partial, err := d.GetPartial([]string{realHash}, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := partial.Verify(); err != nil {
			t.Fatalf("legit partial must verify: %v", err)
		}
		partial.Leafs[realHash] = &dag.DagLeaf{
			Hash:     partial.Root, // the field says "root"; the key says realHash
			ItemName: partial.Leafs[realHash].ItemName,
			Type:     dag.FileLeafType,
			Content:  []byte("forged"),
		}
		return partial
	}
	if err := build().Verify(); err == nil {
		t.Error("accepted a leaf whose Hash differs from the key it is filed under")
	}
	data, err := build().ToCBOR()
	if err != nil {
		t.Fatal(err)
	}
	if decoded, err := dag.FromCBOR(data); err == nil {
		if err := decoded.Verify(); err == nil {
			t.Error("accepted the same DAG after a CBOR round trip")
		}
	}
}

// A later packet that claims to be the root.
func TestForgeryTransmissionRejectsForeignRoot(t *testing.T) {
	d := forgeryFlatDag(t, 3)
	foreign := forgeryFlatDag(t, 2).GetLeafSequence()[0] // the root packet of a different, valid DAG
	packets := d.GetLeafSequence()

	recv := &dag.Dag{Root: d.Root, Leafs: map[string]*dag.DagLeaf{}}
	if err := recv.ApplyAndVerifyTransmissionPacket(packets[0]); err != nil {
		t.Fatalf("root packet must verify: %v", err)
	}
	if err := recv.VerifyTransmissionPacket(foreign); err == nil {
		t.Error("accepted a root packet for a root other than d.Root")
	}

	store := dag.NewEmptyDagStore(nil, nil)
	if err := store.AddTransmissionPacket(packets[0]); err != nil {
		t.Fatalf("root packet must be stored: %v", err)
	}
	_ = store.AddTransmissionPacket(foreign)
	if store.Root != d.Root {
		t.Errorf("a later root packet replaced the store's root with %s", store.Root)
	}
}

// The store's streaming verifier, given an extra child.
func TestForgeryStoreVerifyStreamingRejectsExtraChild(t *testing.T) {
	d := forgeryFlatDag(t, 3)
	if err := dag.NewDagStore(d).VerifyStreaming(); err != nil {
		t.Fatalf("legit store must verify: %v", err)
	}
	f := forgedFileLeaf(t)
	d.Leafs[f.Hash] = f
	root := d.Leafs[d.Root]
	root.Links = append(root.Links, f.Hash)
	if err := dag.NewDagStore(d).VerifyStreaming(); err == nil {
		t.Error("streaming verification accepted an extra child")
	}
}

// The store's batched intake, given a leaf that is not a child of its parent.
func TestForgeryStoreBatchedRejectsUnboundLeaf(t *testing.T) {
	d := forgeryFlatDag(t, 3)
	batch := d.GetBatchedLeafSequence()[0]
	clean := dag.NewEmptyDagStore(nil, nil)
	if err := clean.AddBatchedTransmissionPacket(batch); err != nil {
		t.Fatalf("legit batch must be stored: %v", err)
	}
	f := forgedFileLeaf(t)
	batch.Leaves = append(batch.Leaves, f)
	batch.Relationships[f.Hash] = d.Root
	store := dag.NewEmptyDagStore(nil, nil)
	_ = store.AddBatchedTransmissionPacket(batch)
	if has, _ := store.HasLeaf(f.Hash); has {
		t.Error("the store kept a leaf that is not a child of its parent")
	}
}

// A file must not come back from a shortened list of chunks.
func TestForgeryContentRefusesIncompleteFile(t *testing.T) {
	dag.SetChunkSize(16)
	defer dag.SetDefaultChunkSize()
	dir := t.TempDir()
	var body []byte // three 16-byte chunks
	for _, c := range []string{"A", "B", "C"} {
		body = append(body, bytes.Repeat([]byte(c), 16)...)
	}
	if err := os.WriteFile(filepath.Join(dir, "big.bin"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "other.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := dag.CreateDag(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	var fileHash string
	for hash, leaf := range d.Leafs {
		if leaf.Type == dag.FileLeafType && len(leaf.Links) == 3 {
			fileHash = hash
		}
	}
	if fileHash == "" {
		t.Fatal("expected a file leaf with three chunks")
	}
	chunks := append([]string(nil), d.Leafs[fileHash].Links...)
	partial, err := d.GetPartial(chunks, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := partial.Verify(); err != nil {
		t.Fatalf("legit partial must verify: %v", err)
	}
	file := partial.Leafs[fileHash]
	if got, err := partial.GetContentFromLeaf(file); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("a complete file must come back whole: %v", err)
	}

	// Drop the middle chunk the way a sender could: the leaf, its link and its proof.
	delete(partial.Leafs, chunks[1])
	delete(file.Proofs, chunks[1])
	file.Links = []string{chunks[0], chunks[2]}
	if err := partial.Verify(); err != nil {
		t.Fatalf("a partial with two of three chunks is still a valid partial: %v", err)
	}
	if got, err := partial.GetContentFromLeaf(file); err == nil {
		t.Errorf("returned %d bytes for a %d-byte file with no error", len(got), len(body))
	}
}

// forgeryMustReturnError requires run to come back with an error: no hang and
// no panic. A run that never returns leaves its goroutine behind until the
// test binary exits.
func forgeryMustReturnError(t *testing.T, name string, run func() error) {
	t.Helper()
	type result struct {
		err      error
		panicked any
	}
	done := make(chan result, 1)
	go func() {
		var res result
		defer func() {
			res.panicked = recover()
			done <- res
		}()
		res.err = run()
	}()
	select {
	case res := <-done:
		if res.panicked != nil {
			t.Errorf("%s: panicked: %v", name, res.panicked)
		} else if res.err == nil {
			t.Errorf("%s: accepted", name)
		}
	case <-time.After(5 * time.Second):
		t.Errorf("%s: did not return", name)
	}
}

// Malformed input must come back as an error.
func TestForgeryMalformedInputReturnsError(t *testing.T) {
	d := forgeryFlatDag(t, 5)
	realHash := d.Leafs[d.Root].Links[0]
	newPartial := func() *dag.Dag {
		partial, err := d.GetPartial([]string{realHash}, true)
		if err != nil {
			t.Fatal(err)
		}
		return partial
	}

	noRoot := &dag.Dag{Root: d.Root, Leafs: map[string]*dag.DagLeaf{}}
	forgeryMustReturnError(t, "no root leaf", noRoot.Verify)

	nullProof := newPartial()
	nullProof.Leafs[nullProof.Root].Proofs[realHash] = nil
	forgeryMustReturnError(t, "null proof", nullProof.Verify)

	cycle := newPartial()
	x, y := forgedLeafNamed(t, "x"), forgedLeafNamed(t, "y")
	x.Links, y.Links = []string{y.Hash}, []string{x.Hash}
	cycle.Leafs[x.Hash], cycle.Leafs[y.Hash] = x, y
	forgeryMustReturnError(t, "two forged leaves that list each other", cycle.Verify)

	forgeryMustReturnError(t, "null leaf in JSON", func() error {
		decoded, err := dag.FromJSON([]byte(`{"Root":"x","Leafs":{"x":null}}`))
		if err != nil {
			return err
		}
		return decoded.Verify()
	})
}
