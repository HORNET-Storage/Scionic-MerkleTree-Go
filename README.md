![example workflow](https://github.com/HORNET-Storage/scionic-merkletree/actions/workflows/go.yml/badge.svg)
[![codecov](https://codecov.io/gh/HORNET-Storage/scionic-merkletree/graph/badge.svg?token=1UBLJ1YYFI)](https://codecov.io/gh/HORNET-Storage/scionic-merkletree)


# Scionic Merkle Trees

## Combining Merkle DAGs and Merkle Trees

We've designed a [new type of Merkle DAG-Tree hybrid](https://www.hornetstorage.com/dag-trees). Scionic Merkle Trees have small branches like Classic Merkle Trees, the file/folder storage ability of Merkle DAGs, and numbered leaves so relays can request a range of missing leaf numbers to sync quickly. They are an innovative model that merge the advantages of Classic Merkle Trees and Merkle DAGs while addressing several of their limitations.

In plant grafting, the scion is the upper part of the plant, chosen for its desirable fruits or flowers. It's grafted onto another plant's base to grow together. In a similar vein, the Scionic Merkle DAG-Tree was born from grafting together Merkle DAGs and Classic Merkle Trees. This process emphasizes why we use the term "scion" for the Scionic Merkle DAG-Trees… it symbolizes the digital grafting of these two similar data structures, combining their strengths into one piece.

![Tree Comparison Diagram](https://static.wixstatic.com/media/e9326a_b761315944af43f993b01b00b2ac11b5~mv2.png/v1/fill/w_1718,h_431,al_c,q_90,usm_0.66_1.00_0.01,enc_auto/Comparsion_Diagram_Yellow.png)

## Classic Merkle Trees and Merkle DAGs: A Comparison

### ***Classic Merkle Trees***

 Merkle Trees are cryptographic structures used to manage and securely verify large amounts of data. However, they have a significant drawback: they cannot store folders or files.

The number of hashes required for a Merkle proof in a Classic Merkle Tree grows logarithmically with the number of files, meaning the growth rate slows as the input (tree) size increases. This pattern makes them very efficient for large datasets because the branches become exponentially smaller as the number of files in the folder rises.

### ***Merkle DAGs (Directed Acyclic Graphs)***

Merkle DAGs were developed as a solution to incorporate folders and files, addressing a key limitation of Classic Merkle Trees. However, this structure has its own challenge: to securely download a single file, you must download the hash of every other file inside the folder its stored in. This requirement can be slow and costly for users when dealing with folders that contain a large number of files.

## The Strengths of Scionic Merkle Trees

### ***Folders and Files:***

Like Merkle DAGs, Scionic Merkle Trees can accommodate folders and files. However, they also maintain the efficiency of Classic Merkle Trees.

### ***Internal Arrangement:***

The unique feature of Scionic Merkle Trees is their internal structure. Within each folder (parent leaf) across the tree, its list of files (children) are organized as a Classic Merkle tree rather than a plaintext list.

### ***Efficient File Download and Branch Verification:***

If a user wants a specific file from a folder on the tree, they no longer need to download every hash in the folder. Instead, they download a Classic Merkle branch linked to the folder (parent leaf) they're downloading the file from. This process allows the user to verify that the file is part of the tree without needing to download every hash of all other files in the folder.

### ***Improved Scalability for Users with Large Datasets:***

This streamlined process significantly improves efficiency, especially with large datasets. Scionic Merkle Trees are a powerful tool for handling folders with numerous files, combining the directory-friendly nature of Merkle DAGs and the compact efficiency of Classic Merkle Trees.

### ***Scionic Merkle DAG-Tree:***
![Scionic Merkle Tree Diagram](https://i.ibb.co/XJjbwmP/Scionic-Merkle-Tree.jpg)

### ***Scionic Merkle Branch:***
![Scionic Merkle Branch Diagram](https://i.ibb.co/nLcNLw1/Merkle-Branch.png)

## Scionic Merkle Branch Statistics

*Comparing the size of a Scionic Merkle Branch to bloated Merkle DAG Branches:*

* For a folder containing 10 files, a Scionic branch needs just 5 leaves, while a Merkle DAG branch requires all 10. This makes the Scionic branch about **2x smaller**.
* When the folder contains 1000 files, a Scionic branch uses only 11 leaves, compared to the full 1000 required by a Merkle DAG branch. This results in the Scionic branch being approximately **90x smaller**.
* In the case of a folder with 10,000 files, a Scionic branch requires 15 leaves, while a Merkle DAG branch needs all 10,000. This means the Scionic branch is roughly **710x smaller**.
* If the folder contains 1,000,000 files, a Scionic branch for any file in that folder would require around 21 leaves. This Scionic branch would be **50,000x smaller**.

These statistics underline the substantial efficiency improvements made by Scionic Merkle Trees.

## Understanding Growth Patterns: Logarithmic vs Linear

In the case of Scionic Merkle Trees, which incorporate Classic Merkle Trees within their structure, they exhibit logarithmic growth. This means that as the size of the input (the number of files in a folder) increases, the growth rate of the Classic Merkle Tree branches decreases. This makes Scionic Merkle Trees an efficient structure for managing large datasets, ***as the branches become exponentially smaller with the increasing number of files in the folder.***

In stark contrast, the number of hashes required to validate a single folder in a Merkle DAG exhibits linear growth. If there are more children (files) in the folder, you must download the hash of each one to retrieve any individual file from the folder. This constant requirement can lead to overly large Merkle branches. The amount of hashes needed to validate a single file increases in direct proportion to the number of files in the folder, making it less efficient for large datasets.

## Syncing Trees Across Relays by Requesting a Range of Leaves

To further enhance the functionality of Scionic Merkle Trees and support efficient data retrieval, each leaf in the tree is labeled with a sequenced number. This method facilitates the [request for a range of Merkle leaves](https://www.hornetstorage.com/forest), much like what GraphSync attempts to accomplish, but without the complexity of using complex graph selectors and large request sizes.

The total number of leaves is recorded at the root of the tree. By doing so, users can request a range of leaves from a given folder and receive it as a small Scionic Merkle branch, reducing the bandwidth overhead and computational workload required to access multiple files in the same folder.

This approach provides the structural advantages of Scionic Merkle Trees, such as logarithmic growth of branches and efficient file download and verification, and also provides enhanced support for ranged requests, contributing to their practicality in large-scale data management scenarios.

# Documentation

## Install
```
go get github.com/HORNET-Storage/scionic-merkletree/dag
```

## Example Usage
There are good examples inside the dag/dag_test.go file, but below is a basic example to get you started. This library is intended to be very simple while still allowing for powerful usage...

Turn a folder and its files into a Scionic Merkle DAG-Tree, verify, then convert the Scionic Merkle tree back to the original files in a new directory:
```go
input := filepath.Join(tmpDir, "input")
output := filepath.Join(tmpDir, "output")

SetChunkSize(4096)

dag, err := CreateDag(input, true)
if err != nil {
  fmt.Fatalf("Error: %s", err)
}

result, err := dag.Verify()
if err != nil {
  fmt.Fatalf("Error: %s", err)
}

fmt.Println("Dag verified successfully")

err = dag.CreateDirectory(output)
if err != nil {
  fmt.Fatalf("Error: %s", err)
}
```

## Types

The dag builder and dag leaf builder types are used to temporarily store data during the dag creation process as the dag is created from the root down but then built from the bottom back up to the root.
It is not required to understand how this works but if you plan to build the trees yourself without the built in creation process (for example you may wish to create trees from data already in memory) then these will be useful.

### Dag Leaf
```go
type DagLeaf struct {
	Hash              string
	ItemName          string
	Type              LeafType
	ContentHash       []byte
	Content           []byte
	ClassicMerkleRoot []byte
	CurrentLinkCount  int
	LatestLabel       string
	LeafCount         int
	Links             map[string]string
	ParentHash        string
	AdditionalData    map[string]string
}
```

Every leaf in the tree consists of the DagLeaf data type and these are what they are used for:

### Hash: string
The hash field is a cid, encoded as a string, of the following fields serialized in cbor with sha256 hasing:
- ItemName
- Type
- ContentHash
- ClassicMerkleRoot
- CurrentLinkCount
- AdditionalData

Only the root leaf has these fields included in the hash
- LatestLabel
- LeafCount

### ItemName: string
This can be anything but our usage is the file name including the type so that we can accurately re-create a directory / file with all the files and types intact

### Type: LeafType
This is a string but we use a custom type to enforce specific usage, there are currently only 3 types that a leaf can be:
```go
type LeafType string

const (
	FileLeafType      LeafType = "file"
	ChunkLeafType     LeafType = "chunk"
	DirectoryLeafType LeafType = "directory"
)
```

file is a file
chunk are the chunks that make up a file incase the file was larger than the max chunk size
directory is a directory

New types can be added without breaking existing data if needed

### ContentHash: []byte
### content: []byte
ContentHash and Content are important together as you can't have one without the other.
The content hash is a sha256 hash of the content, currently the content is from a file on disk but it could be anything as long as it's serialized in a byte array.
We have no need to encode any of this data as we are using cbor for serializing the leaf data which can safely handle byte arrays directly as it's not a plain text format like json.
The content hash is included in the leaf hash which means it's cryptographically verifiable, which also means the content can be verified as well to ensure there isn't tampering.
This is important because it means we can send and recieve the leaves with or without the content, while still being able to verify the content, which is important for de-duplicating data transmission over the network.

### ClassicMerkleRoot: []byte
We use classic merkle trees inside of our dag by creating a tree of the links inside of a leaf, if the leaf has more than 1 link. This allows us to verify the leaves without having all of the children present making our branches a lot smaller.
This also means we do not need to include the links in the leaf hash because this merkle root is included in their place, potentially removing a lot of data when sending individual leaves if there are a lot of child leaves present.

### CurrentLinkCount: int
This is the count of how many links a leaf has and it's included in the leaf hash to ensure that we always know and can verify how many links a leaf should have which prevents any lying about the number of children when verifying branches or partial trees.

### LatestLabel: string
We label every child leaf in the dag where the root starts at 0 and each leaf that gets built becomes the next integer. Because these are included in the classic merkle tree, and the classic merkle root is included in the leaf hash, we can now reference leaves by their root hash and number instead of their root hash and their leaf hash.
This is stored as a string as it is appended to the cid (Hash) of each leaf. It's important to remember that labelling is not per leaf but per dag.

### LeafCount: int
The overall number of leaves that the entire dag contains which is why this is only stored and hashed in the root leaf, it ensures you can always know if you have all of the children or not.

### Links: map[string]string
The links to all of the children of a leaf where the key is the label and the value is the label:cid of the child

### ParentHash: string
We add the parent hash (label:cid) to the child leaf to make traversal upwards possible but this is purely for speed and the parent it points to should still be verified as we can't include the parent hash inside of the leaf hash.
This is because the parent hash doesn't exist yet, the leaf hashes are created from bottom to top, despite dag creation starting at the top.

### AdditionalData: map[string]string
This map is included in the leaf hash allowing for developers to add additional data to the dag leaves if and when needed.
AdditionalData does get included in the leaf hash so any content stored here is cryptographically verifiable, the map is sorted by keys alphanumerically before it gets serialized and hashed to ensure consistency no matter what order they get added.
Currently we only use this to store the timestamp in the root leaf which is an optional parameter when creating a dag from a directory or file but advanced users that build the trees themselves can utilize this feature to store anything they want.

## Functions
```go
func CreateDagBuilder() *DagBuilder
func (b *DagBuilder) AddLeaf(leaf *DagLeaf, parentLeaf *DagLeaf) error
func (b *DagBuilder) BuildDag(root string) *Dag
func (b *DagBuilder) GetLatestLabel()
func (b *DagBuilder) GetNextAvailableLabel()

func CreateDag(path string, timestampRoot bool) (*Dag, error)
func (dag *Dag) Verify() error
func (dag *Dag) CreateDirectory(path string) error
func (dag *Dag) GetContentFromLeaf(leaf *DagLeaf) ([]byte, error)
func (dag *Dag) IterateDag(processLeaf func(leaf *DagLeaf, parent *DagLeaf) error) error

func CreateDagLeafBuilder(name string) *DagLeafBuilder
func (b *DagLeafBuilder) SetType(leafType LeafType) 
func (b *DagLeafBuilder) SetData(data []byte)
func (b *DagLeafBuilder) AddLink(label string, hash string) 
func (b *DagLeafBuilder) BuildLeaf(additionalData map[string]string) (*DagLeaf, error) 
func (b *DagLeafBuilder) BuildRootLeaf(dag *DagBuilder, additionalData map[string]string) (*DagLeaf, error)

func (leaf *DagLeaf) GetBranch(key string) (*ClassicTreeBranch, error)
func (leaf *DagLeaf) VerifyBranch(branch *ClassicTreeBranch) error
func (leaf *DagLeaf) VerifyLeaf() error
func (leaf *DagLeaf) VerifyRootLeaf() error
func (leaf *DagLeaf) CreateDirectoryLeaf(path string, dag *Dag) error
func (leaf *DagLeaf) HasLink(hash string) bool
func (leaf *DagLeaf) AddLink(hash string)
func (leaf *DagLeaf) Clone() *DagLeaf
func (leaf *DagLeaf) SetLabel(label string)
```

The trees are now in beta and the data structure of the trees will no longer change
#
