package dag

import (
	"crypto/sha256"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/HORNET-Storage/scionic-merkletree/tree"

	"github.com/HORNET-Storage/scionic-merkletree/merkletree"

	cbor "github.com/fxamacker/cbor/v2"
	"github.com/multiformats/go-multibase"

	merkle_tree "github.com/HORNET-Storage/scionic-merkletree/tree"
)

func CreateDagLeafBuilder(name string) *DagLeafBuilder {
	builder := &DagLeafBuilder{
		Name:  name,
		Links: map[string]string{},
	}

	return builder
}

func (b *DagLeafBuilder) SetType(leafType LeafType) {
	b.LeafType = leafType
}

func (b *DagLeafBuilder) SetData(data []byte) {
	b.Data = data
}

func (b *DagLeafBuilder) AddLink(label string, hash string) {
	b.Links[label] = label + ":" + hash
}

func (b *DagLeafBuilder) GetLatestLabel() string {
	var result string = "0"
	var latestLabel int64 = 0
	for _, hash := range b.Links {
		label := GetLabel(hash)

		if label == "" {
			fmt.Println("Failed to find label in hash")
		}

		parsed, err := strconv.ParseInt(label, 10, 64)
		if err != nil {
			fmt.Println("Failed to parse label")
		}

		if parsed > latestLabel {
			latestLabel = parsed
			result = label
		}
	}

	return result
}

func (b *DagLeafBuilder) GetNextAvailableLabel() string {
	latestLabel := b.GetLatestLabel()

	number, err := strconv.ParseInt(latestLabel, 10, 64)
	if err != nil {
		fmt.Println("Failed to parse label")
	}

	nextLabel := strconv.FormatInt(number+1, 10)

	return nextLabel
}

func (b *DagLeafBuilder) BuildLeaf(encoder multibase.Encoder) (*DagLeaf, error) {
	if b.LeafType == "" {
		err := fmt.Errorf("Leaf must have a type defined")
		return nil, err
	}

	merkleRoot := []byte{}

	if len(b.Links) > 1 {
		builder := tree.CreateTree()
		for _, link := range b.Links {
			builder.AddLeaf(GetLabel(link), link)
		}

		merkleTree, _, err := builder.Build()
		if err != nil {
			return nil, err
		}

		merkleRoot = merkleTree.Root
	}

	latestLabel := b.GetLatestLabel()

	leafData := struct {
		Name             string
		Type             LeafType
		MerkleRoot       []byte
		CurrentLinkCount int
		LatestLabel      string
		Data             []byte
	}{
		Name:             b.Name,
		Type:             b.LeafType,
		MerkleRoot:       merkleRoot,
		CurrentLinkCount: len(b.Links),
		LatestLabel:      latestLabel,
		Data:             b.Data,
	}

	serializedLeafData, err := cbor.Marshal(leafData)
	if err != nil {
		return nil, err
	}

	hash := sha256.Sum256(serializedLeafData)
	result := &DagLeaf{
		Hash:             encoder.Encode(hash[:]),
		Name:             b.Name,
		Type:             b.LeafType,
		MerkleRoot:       merkleRoot,
		CurrentLinkCount: len(b.Links),
		LatestLabel:      latestLabel,
		Data:             b.Data,
		Links:            b.Links,
	}

	return result, nil
}

type kv struct {
	Key   string
	Value string
	Order int
}

func (leaf *DagLeaf) GetBranch(key string) (*ClassicTreeBranch, error) {
	t := merkle_tree.CreateTree()

	for k, v := range leaf.Links {
		t.AddLeaf(k, v)
	}

	merkleTree, leafs, err := t.Build()
	if err != nil {
		log.Println("Failed to build merkle tree")
		return nil, err
	}

	index, result := merkleTree.GetIndexForKey(key)
	if !result {
		return nil, fmt.Errorf("Unable to find index for given key")
	}

	branchLeaf := leafs[key]

	branch := &ClassicTreeBranch{
		Leaf:  &branchLeaf,
		Proof: merkleTree.Proofs[index],
	}

	return branch, nil
}

func (leaf *DagLeaf) VerifyBranch(branch *ClassicTreeBranch) (bool, error) {
	result, err := merkletree.Verify(*branch.Leaf, branch.Proof, leaf.MerkleRoot, nil)
	if err != nil {
		return false, err
	}

	return result, nil
}

func (leaf *DagLeaf) VerifyLeaf(encoder multibase.Encoder) (bool, error) {
	leafData := struct {
		Name             string
		Type             LeafType
		MerkleRoot       []byte
		CurrentLinkCount int
		LatestLabel      string
		Data             []byte
	}{
		Name:             leaf.Name,
		Type:             leaf.Type,
		MerkleRoot:       leaf.MerkleRoot,
		CurrentLinkCount: leaf.CurrentLinkCount,
		LatestLabel:      leaf.LatestLabel,
		Data:             leaf.Data,
	}

	serializedLeafData, err := cbor.Marshal(leafData)
	if err != nil {
		return false, err
	}

	hash := sha256.Sum256(serializedLeafData)

	var result bool = false
	if HasLabel(leaf.Hash) {
		result = encoder.Encode(hash[:]) == GetHash(leaf.Hash)
	} else {
		result = encoder.Encode(hash[:]) == leaf.Hash
	}

	/*
		// Should this be here?
		if leaf.MerkleRoot != "" {
			for i := 0; i < len(leaf.Links); i++ {
				branch, err := leaf.GetBranch(i)
				if err != nil {
					log.Println("Failed to get leaf branch at index: ", i)
					return false, err
				}

				branchResult, err := leaf.VerifyBranch(branch)

				if !branchResult {
					result = false
				}
			}
		}
	*/

	return result, nil
}

func (leaf *DagLeaf) CreateDirectoryLeaf(path string, dag *Dag, encoder multibase.Encoder) error {
	switch leaf.Type {
	case DirectoryLeafType:
		_ = os.Mkdir(path, os.ModePerm)

		for _, link := range leaf.Links {
			childLeaf := dag.Leafs[link]
			if childLeaf == nil {
				return fmt.Errorf("invalid link: %s", link)
			}

			childPath := filepath.Join(path, childLeaf.Name)
			err := childLeaf.CreateDirectoryLeaf(childPath, dag, encoder)
			if err != nil {
				return err
			}
		}

	case FileLeafType:
		var content []byte

		if len(leaf.Links) > 0 {
			for _, link := range leaf.Links {
				childLeaf := dag.Leafs[link]
				if childLeaf == nil {
					return fmt.Errorf("invalid link: %s", link)
				}

				content = append(content, childLeaf.Data...)
			}
		} else {
			content = leaf.Data
		}

		err := ioutil.WriteFile(path, content, os.ModePerm)
		if err != nil {
			return err
		}
	}

	return nil
}

func (leaf *DagLeaf) HasLink(hash string) bool {
	for _, link := range leaf.Links {
		if HasLabel(hash) {
			if HasLabel(link) {
				if link == hash {
					return true
				}
			} else {
				if link == GetHash(hash) {
					return true
				}
			}
		} else {
			if HasLabel(link) {
				if GetHash(link) == hash {
					return true
				}
			} else {
				if GetHash(link) == GetHash(hash) {
					return true
				}
			}
		}
	}

	return false
}

func (leaf *DagLeaf) AddLink(hash string) {
	label := GetLabel(hash)

	if label == "" {
		fmt.Println("This hash does not have a label")
	}

	leaf.Links[label] = hash
}

func (leaf *DagLeaf) Clone() *DagLeaf {
	return &DagLeaf{
		Hash:             leaf.Hash,
		Name:             leaf.Name,
		Type:             leaf.Type,
		Data:             leaf.Data,
		MerkleRoot:       leaf.MerkleRoot,
		CurrentLinkCount: leaf.CurrentLinkCount,
		LatestLabel:      leaf.LatestLabel,
		Links:            leaf.Links,
	}
}

func (leaf *DagLeaf) GetLatestLabel() string {
	var result string = "0"
	var latestLabel int64 = 0
	for _, hash := range leaf.Links {
		label := GetLabel(hash)

		if label == "" {
			fmt.Println("Failed to find label in hash")
		}

		parsed, err := strconv.ParseInt(label, 10, 64)
		if err != nil {
			fmt.Println("Failed to parse label")
		}

		if parsed > latestLabel {
			latestLabel = parsed
			result = label
		}
	}

	return result
}

func (leaf *DagLeaf) GetNextAvailableLabel() string {
	latestLabel := leaf.GetLatestLabel()

	number, err := strconv.ParseInt(latestLabel, 10, 64)
	if err != nil {
		fmt.Println("Failed to parse label")
	}

	nextLabel := strconv.FormatInt(number+1, 10)

	return nextLabel
}

func (leaf *DagLeaf) SetLabel(label string) {
	leaf.Hash = label + ":" + leaf.Hash
}

func (leaf *DagLeaf) RebuildLeaf(encoder multibase.Encoder) (*DagLeaf, error) {
	log.Println("Rebuilding leaf: " + leaf.Hash)
	builder := CreateDagLeafBuilder(leaf.Name)

	builder.Data = leaf.Data
	builder.LeafType = leaf.Type

	for label, hash := range leaf.Links {
		builder.AddLink(label, GetHash(hash))
	}

	newLeaf, err := builder.BuildLeaf(encoder)
	if err != nil {
		return nil, err
	}

	return newLeaf, nil
}

func (leaf *DagLeaf) RemoveLink(label string) error {
	if _, ok := leaf.Links[label]; !ok {
		return fmt.Errorf("link with label %s does not exist", label)
	}

	delete(leaf.Links, label)

	return nil
}

func (leaf *DagLeaf) ReplaceLink(label string, hash string) error {
	if _, ok := leaf.Links[label]; !ok {
		return fmt.Errorf("link with label %s does not exist", label)
	}

	leaf.Links[label] = hash

	return nil
}

func HasLabel(hash string) bool {
	if GetLabel(hash) != "" {
		return true
	} else {
		return false
	}
}

func GetHash(hash string) string {
	parts := strings.Split(hash, ":")

	if len(parts) != 2 {
		return hash
	}

	return parts[1]
}

func GetLabel(hash string) string {
	parts := strings.Split(hash, ":")
	if len(parts) != 2 {
		return ""
	}

	return parts[0]
}
