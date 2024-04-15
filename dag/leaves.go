package dag

import (
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/HORNET-Storage/scionic-merkletree/merkletree"

	cbor "github.com/fxamacker/cbor/v2"

	merkle_tree "github.com/HORNET-Storage/scionic-merkletree/tree"

	"github.com/ipfs/go-cid"
	mc "github.com/multiformats/go-multicodec"
	mh "github.com/multiformats/go-multihash"
)

func CreateDagLeafBuilder(name string) *DagLeafBuilder {
	builder := &DagLeafBuilder{
		ItemName: name,
		Links:    map[string]string{},
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
	for hash := range b.Leafs {
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

func (b *DagLeafBuilder) BuildLeaf(additionalData map[string]string) (*DagLeaf, error) {
	if b.LeafType == "" {
		err := fmt.Errorf("leaf must have a type defined")
		return nil, err
	}

	merkleRoot := []byte{}

	if len(b.Links) > 1 {
		builder := merkle_tree.CreateTree()
		for _, link := range b.Links {
			builder.AddLeaf(GetLabel(link), link)
		}

		merkleTree, _, err := builder.Build()
		if err != nil {
			return nil, err
		}

		merkleRoot = merkleTree.Root
	}

	additionalData = sortMapByKeys(additionalData)

	leafData := struct {
		ItemName         string
		Type             LeafType
		MerkleRoot       []byte
		CurrentLinkCount int
		ContentHash      []byte
		AdditionalData   map[string]string
	}{
		ItemName:         b.ItemName,
		Type:             b.LeafType,
		MerkleRoot:       merkleRoot,
		CurrentLinkCount: len(b.Links),
		ContentHash:      nil,
		AdditionalData:   additionalData,
	}

	if b.Data != nil {
		hash := sha256.Sum256(b.Data)
		leafData.ContentHash = hash[:]
	}

	serializedLeafData, err := cbor.Marshal(leafData)
	if err != nil {
		return nil, err
	}

	pref := cid.Prefix{
		Version:  1,
		Codec:    uint64(mc.Cbor),
		MhType:   mh.SHA2_256,
		MhLength: -1,
	}

	c, err := pref.Sum(serializedLeafData)
	if err != nil {
		return nil, err
	}

	result := &DagLeaf{
		Hash:              c.String(),
		ItemName:          b.ItemName,
		Type:              b.LeafType,
		ClassicMerkleRoot: merkleRoot,
		CurrentLinkCount:  len(b.Links),
		Content:           b.Data,
		ContentHash:       leafData.ContentHash,
		Links:             b.Links,
		AdditionalData:    additionalData,
	}

	return result, nil
}

func (b *DagLeafBuilder) BuildRootLeaf(dag *DagBuilder, additionalData map[string]string) (*DagLeaf, error) {
	if b.LeafType == "" {
		err := fmt.Errorf("leaf must have a type defined")
		return nil, err
	}

	merkleRoot := []byte{}

	if len(b.Links) > 1 {
		builder := merkle_tree.CreateTree()
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

	additionalData = sortMapByKeys(additionalData)

	leafData := struct {
		ItemName         string
		Type             LeafType
		MerkleRoot       []byte
		CurrentLinkCount int
		LatestLabel      string
		LeafCount        int
		ContentHash      []byte
		AdditionalData   map[string]string
	}{
		ItemName:         b.ItemName,
		Type:             b.LeafType,
		MerkleRoot:       merkleRoot,
		CurrentLinkCount: len(b.Links),
		LatestLabel:      latestLabel,
		LeafCount:        len(dag.Leafs),
		ContentHash:      nil,
		AdditionalData:   additionalData,
	}

	if b.Data != nil {
		hash := sha256.Sum256(b.Data)
		leafData.ContentHash = hash[:]
	}

	serializedLeafData, err := cbor.Marshal(leafData)
	if err != nil {
		return nil, err
	}

	pref := cid.Prefix{
		Version:  1,
		Codec:    uint64(mc.Cbor),
		MhType:   mh.SHA2_256,
		MhLength: -1,
	}

	c, err := pref.Sum(serializedLeafData)
	if err != nil {
		return nil, err
	}

	result := &DagLeaf{
		Hash:              c.String(),
		ItemName:          b.ItemName,
		Type:              b.LeafType,
		ClassicMerkleRoot: merkleRoot,
		CurrentLinkCount:  len(b.Links),
		LatestLabel:       latestLabel,
		LeafCount:         len(dag.Leafs),
		Content:           b.Data,
		ContentHash:       leafData.ContentHash,
		Links:             b.Links,
		AdditionalData:    additionalData,
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
			return nil, fmt.Errorf("unable to find index for given key")
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

func (leaf *DagLeaf) VerifyBranch(branch *ClassicTreeBranch) error {
	block := merkle_tree.CreateLeaf(branch.Leaf)

	err := merkletree.Verify(block, branch.Proof, leaf.ClassicMerkleRoot, nil)
	if err != nil {
		return err
	}

	return nil
}

func (leaf *DagLeaf) VerifyLeaf() error {
	additionalData := sortMapByKeys(leaf.AdditionalData)

	leafData := struct {
		ItemName         string
		Type             LeafType
		MerkleRoot       []byte
		CurrentLinkCount int
		ContentHash      []byte
		AdditionalData   map[string]string
	}{
		ItemName:         leaf.ItemName,
		Type:             leaf.Type,
		MerkleRoot:       leaf.ClassicMerkleRoot,
		CurrentLinkCount: leaf.CurrentLinkCount,
		ContentHash:      leaf.ContentHash,
		AdditionalData:   additionalData,
	}

	serializedLeafData, err := cbor.Marshal(leafData)
	if err != nil {
		return err
	}

	pref := cid.Prefix{
		Version:  1,
		Codec:    uint64(mc.Cbor),
		MhType:   mh.SHA2_256,
		MhLength: -1,
	}

	c, err := pref.Sum(serializedLeafData)
	if err != nil {
		return err
	}

	currentCid, err := cid.Decode(GetHash(leaf.Hash))
	if err != nil {
		return err
	}

	success := c.Equals(currentCid)
	if !success {
		return fmt.Errorf("leaf failed to verify")
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

	return nil
}

func (leaf *DagLeaf) VerifyRootLeaf() error {
	additionalData := sortMapByKeys(leaf.AdditionalData)

	leafData := struct {
		ItemName         string
		Type             LeafType
		MerkleRoot       []byte
		CurrentLinkCount int
		LatestLabel      string
		LeafCount        int
		ContentHash      []byte
		AdditionalData   map[string]string
	}{
		ItemName:         leaf.ItemName,
		Type:             leaf.Type,
		MerkleRoot:       leaf.ClassicMerkleRoot,
		CurrentLinkCount: leaf.CurrentLinkCount,
		LatestLabel:      leaf.LatestLabel,
		LeafCount:        leaf.LeafCount,
		ContentHash:      leaf.ContentHash,
		AdditionalData:   additionalData,
	}

	serializedLeafData, err := cbor.Marshal(leafData)
	if err != nil {
		return err
	}

	pref := cid.Prefix{
		Version:  1,
		Codec:    uint64(mc.Cbor),
		MhType:   mh.SHA2_256,
		MhLength: -1,
	}

	c, err := pref.Sum(serializedLeafData)
	if err != nil {
		return err
	}

	currentCid, err := cid.Decode(GetHash(leaf.Hash))
	if err != nil {
		return err
	}

	success := c.Equals(currentCid)
	if !success {
		return fmt.Errorf("leaf failed to verify")
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

	return nil
}

func (leaf *DagLeaf) CreateDirectoryLeaf(path string, dag *Dag) error {
	switch leaf.Type {
	case DirectoryLeafType:
		_ = os.Mkdir(path, os.ModePerm)

		for _, link := range leaf.Links {
			childLeaf := dag.Leafs[link]
			if childLeaf == nil {
				return fmt.Errorf("invalid link: %s", link)
			}

			childPath := filepath.Join(path, childLeaf.ItemName)
			err := childLeaf.CreateDirectoryLeaf(childPath, dag)
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

		err := os.WriteFile(path, content, os.ModePerm)
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
		Hash:              leaf.Hash,
		ItemName:          leaf.ItemName,
		Type:              leaf.Type,
		Content:           leaf.Content,
		ContentHash:       leaf.ContentHash,
		ClassicMerkleRoot: leaf.ClassicMerkleRoot,
		CurrentLinkCount:  leaf.CurrentLinkCount,
		LatestLabel:       leaf.LatestLabel,
		LeafCount:         leaf.LeafCount,
		Links:             leaf.Links,
		AdditionalData:    leaf.AdditionalData,
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

func sortMapByKeys(inputMap map[string]string) map[string]string {
	if inputMap == nil {
		return inputMap
	}

	keys := make([]string, 0, len(inputMap))

	for key := range inputMap {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	sortedMap := make(map[string]string)
	for _, key := range keys {
		sortedMap[key] = inputMap[key]
	}

	return sortedMap
}
