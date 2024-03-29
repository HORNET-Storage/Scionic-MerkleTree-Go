package dag

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
)

func TestFull(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "test")
	if err != nil {
		t.Fatalf("Could not create temp directory: %s", err)
	}

	defer os.RemoveAll(tmpDir)

	GenerateDummyDirectory(filepath.Join(tmpDir, "input"), 6, 6)
	if err != nil {
		t.Fatalf("Could not generate dummy directory: %s", err)
	}

	input := filepath.Join(tmpDir, "input")
	output := filepath.Join(tmpDir, "output")

	SetChunkSize(4096)

	dag, err := CreateDag(input, true)
	if err != nil {
		t.Fatalf("Error: %s", err)
	}

	err = dag.Verify()
	if err != nil {
		t.Fatalf("Error: %s", err)
	}

	err = dag.CreateDirectory(output)
	if err != nil {
		t.Fatalf("Error: %s", err)
	}
}

func TestPartial(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "test")
	if err != nil {
		t.Fatalf("Could not create temp directory: %s", err)
	}

	defer os.RemoveAll(tmpDir)

	GenerateDummyDirectory(filepath.Join(tmpDir, "input"), 6, 6)
	if err != nil {
		t.Fatalf("Could not generate dummy directory: %s", err)
	}

	input := filepath.Join(tmpDir, "input")
	output := filepath.Join(tmpDir, "output")

	SetChunkSize(4096)

	dag, err := CreateDag(input, true)
	if err != nil {
		t.Fatal("Error: ", err)
	}

	// Retrieve the root leaf as this is what you will always start with
	parentLeaf := dag.Leafs[dag.Root].Clone()

	// Remove the links as the leaves probably wouldn't have them
	parentLeaf.Links = map[string]string{}

	// Verify the root leaf
	err = parentLeaf.VerifyRootLeaf()
	if err != nil {
		t.Logf("ERROR: %v", err)
		t.Fatal("Failed to verify branch for random leaf")
	}

	// Create a new dag builder and add the root leaf
	dagBuilder := CreateDagBuilder()
	dagBuilder.AddLeaf(parentLeaf, nil)

	for ok := true; ok; ok = parentLeaf.Type == DirectoryLeafType {
		originalParentLeaf := dag.Leafs[parentLeaf.Hash]

		if len(originalParentLeaf.Links) < 1 {
			break
		}

		// Now retrieve a random child of the parent leaf from the original dag to simulate branch verification
		randomLeaf := FindRandomChild(originalParentLeaf, dag.Leafs)
		randomLeaf = randomLeaf.Clone()

		// Remove the links as the leaf probably wouldn't have them
		randomLeaf.Links = map[string]string{}

		// Verify the random leaf
		err := randomLeaf.VerifyLeaf()
		if err != nil {
			t.Fatal("Failed to verify branch for random leaf")
		}

		// Retrieve the branch for random child
		branch, err := originalParentLeaf.GetBranch(GetLabel(randomLeaf.Hash)) //index
		if err != nil {
			t.Fatal("Failed to retrieve root leaf branch")
		}

		if branch != nil {
			// Verify the branch before adding the leaf to the dag
			err = parentLeaf.VerifyBranch(branch)
			if err != nil {
				t.Fatal("Failed to verify branch for random leaf")
			}
		}

		// Add the leaf to the dag builder
		dagBuilder.AddLeaf(randomLeaf, parentLeaf)

		// Set the parent leaf to the new leaf so we can find the next random child if it's a directory
		parentLeaf = randomLeaf
	}

	// Build the dag and verify it
	dag = dagBuilder.BuildDag(dag.Root)

	// Verify the dag
	err = dag.Verify()
	if err != nil {
		t.Fatal("Error: ", err)
	}

	// Re-create the directory from the dag
	err = dag.CreateDirectory(output)
	if err != nil {
		t.Fatal("Error: ", err)
	}
}
