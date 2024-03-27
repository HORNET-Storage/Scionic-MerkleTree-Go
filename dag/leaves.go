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

func (b *DagBuilder) GetLatestLabel() string {
	var result string = "1"
	var latestLabel int64 = 1
	for hash, _ := range b.Leafs {
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

func (b *DagBuilder) GetNextAvailableLabel() string {
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

	contentHash := sha256.Sum256(b.Data)

	leafData := struct {
		Name             string
		Type             LeafType
		MerkleRoot       []byte
		CurrentLinkCount int
		ContentHash      []byte
	}{
		Name:             b.Name,
		Type:             b.LeafType,
		MerkleRoot:       merkleRoot,
		CurrentLinkCount: len(b.Links),
		ContentHash:      contentHash[:],
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
		Content:          b.Data,
		ContentHash:      contentHash[:],
		Links:            b.Links,
	}

	return result, nil
}

func (b *DagLeafBuilder) BuildRootLeaf(dag *DagBuilder, encoder multibase.Encoder) (*DagLeaf, error) {
	if b.LeafType == "" {
		err := fmt.Errorf("leaf must have a type defined")
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

	latestLabel := dag.GetLatestLabel()
	contentHash := sha256.Sum256(b.Data)

	leafData := struct {
		Name             string
		Type             LeafType
		MerkleRoot       []byte
		CurrentLinkCount int
		LatestLabel      string
		LeafCount        int
		ContentHash      []byte
	}{
		Name:             b.Name,
		Type:             b.LeafType,
		MerkleRoot:       merkleRoot,
		CurrentLinkCount: len(b.Links),
		LatestLabel:      latestLabel,
		LeafCount:        len(dag.Leafs),
		ContentHash:      contentHash[:],
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
		LeafCount:        len(dag.Leafs),
		Content:          b.Data,
		ContentHash:      contentHash[:],
		Links:            b.Links,
	}

	return result, nil
}

func (leaf *DagLeaf) GetBranch(key string) (*ClassicTreeBranch, error) {
	if len(leaf.Links) > 1 {
		t := merkle_tree.CreateTree()

		for k, v := range leaf.Links {
			t.AddLeaf(k, v)
		}

		merkleTree, _, err := t.Build()
		if err != nil {
			log.Println("Failed to build merkle tree")
			return nil, err
		}

		index, result := merkleTree.GetIndexForKey(key)
		if !result {
			return nil, fmt.Errorf("Unable to find index for given key")
		}

		branchLeaf := leaf.Links[key]

		branch := &ClassicTreeBranch{
			Leaf:  branchLeaf,
			Proof: merkleTree.Proofs[index],
		}

		return branch, nil
	} else {
		return nil, nil
	}
}

func (leaf *DagLeaf) VerifyBranch(branch *ClassicTreeBranch) (bool, error) {
	block := tree.CreateLeaf(branch.Leaf)

	result, err := merkletree.Verify(block, branch.Proof, leaf.MerkleRoot, nil)
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
		ContentHash      []byte
	}{
		Name:             leaf.Name,
		Type:             leaf.Type,
		MerkleRoot:       leaf.MerkleRoot,
		CurrentLinkCount: leaf.CurrentLinkCount,
		ContentHash:      leaf.ContentHash,
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

func (leaf *DagLeaf) VerifyRootLeaf(encoder multibase.Encoder) (bool, error) {
	leafData := struct {
		Name             string
		Type             LeafType
		MerkleRoot       []byte
		CurrentLinkCount int
		LatestLabel      string
		LeafCount        int
		ContentHash      []byte
	}{
		Name:             leaf.Name,
		Type:             leaf.Type,
		MerkleRoot:       leaf.MerkleRoot,
		CurrentLinkCount: leaf.CurrentLinkCount,
		LatestLabel:      leaf.LatestLabel,
		LeafCount:        leaf.LeafCount,
		ContentHash:      leaf.ContentHash,
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

				content = append(content, childLeaf.Content...)
			}
		} else {
			content = leaf.Content
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
		Content:          leaf.Content,
		ContentHash:      leaf.ContentHash,
		MerkleRoot:       leaf.MerkleRoot,
		CurrentLinkCount: leaf.CurrentLinkCount,
		LatestLabel:      leaf.LatestLabel,
		LeafCount:        leaf.LeafCount,
		Links:            leaf.Links,
	}
}

func (leaf *DagLeaf) SetLabel(label string) {
	leaf.Hash = label + ":" + leaf.Hash
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
