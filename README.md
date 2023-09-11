![example workflow](https://github.com/HORNET-Storage/scionic-merkletree/actions/workflows/go.yml/badge.svg)
[![codecov](https://codecov.io/gh/HORNET-Storage/scionic-merkletree/graph/badge.svg?token=1UBLJ1YYFI)](https://codecov.io/gh/HORNET-Storage/scionic-merkletree)


# Scionic Merkle DAG-Trees

## An Evolution of Merkle DAGs and Merkle Trees

We've designed a [new type of Merkle DAG-Tree hybrid](https://www.hornetstorage.com/). Scionic Merkle DAG-Trees have small branches like Classic Merkle Trees, the file storage ability of Merkle DAGs, and numbered leaves so relays can request a range of missing leaf numbers to sync quickly. They are an innovative model that merge the advantages of Classic Merkle Trees and Merkle DAGs while addressing several of their limitations.

![Tree Comparison Diagram](https://static.wixstatic.com/media/e9326a_d4a555d0ed9e46da8e751967961710a3~mv2.png/v1/fill/w_1679,h_421,al_c,q_90,usm_0.66_1.00_0.01,enc_auto/Merkle%20DAG%20Tree%20Comparison%20Diagram.png)

## Classic Merkle Trees and Merkle DAGs: A Comparison

### ***Classic Merkle Trees***

 Merkle Trees are cryptographic structures used to manage and securely verify large amounts of data. However, they have a significant drawback: they cannot store folders or files.

The number of hashes required for a Merkle proof in a Classic Merkle Tree grows logarithmically with the number of files, meaning the growth rate slows as the input (tree) size increases. This pattern makes them very efficient for large datasets because the branches become exponentially smaller as the number of files in the folder rises.

### ***Merkle DAGs (Directed Acyclic Graphs)***

Merkle DAGs were developed as a solution to incorporate folders and files, addressing a key limitation of Classic Merkle Trees. However, this structure has its own challenge: to securely download a single file, you must download the hash of every other file inside the folder its stored in. This requirement can be slow and costly for users when dealing with folders that contain a large number of files.

## The Strengths of Scionic Merkle DAG-Trees

### ***Folders and Files:***

Like Merkle DAGs, Scionic Merkle DAG-Trees can accommodate folders and files. However, they also maintain the efficiency of Classic Merkle Trees.

### ***Internal Arrangement:***

The unique feature of Scionic Merkle DAG-Trees is their internal structure. Within each folder (parent leaf) across the tree, its list of files (children) are organized as a Classic Merkle tree rather than a plaintext list.

### ***Efficient File Download and Branch Verification:***

If a user wants a specific file from a folder on the tree, they no longer need to download every hash in the folder. Instead, they download a Classic Merkle branch linked to the folder (parent leaf) they're downloading the file from. This process allows the user to verify that the file is part of the tree without needing to download every hash of all other files in the folder.

### ***Improved Scalability for Users with Large Datasets:***

This streamlined process significantly improves efficiency, especially with large datasets. Scionic Merkle DAG-Trees are a powerful tool for handling folders with numerous files, combining the directory-friendly nature of Merkle DAGs and the compact efficiency of Classic Merkle Trees.

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

These statistics underline the substantial efficiency improvements made by Scionic Merkle DAG-Trees.

## Understanding Growth Patterns: Logarithmic vs Linear

In the case of Scionic Merkle DAG-Trees, which incorporate Classic Merkle Trees within their structure, they exhibit logarithmic growth. This means that as the size of the input (the number of files in a folder) increases, the growth rate of the Classic Merkle Tree branches decreases. This makes Scionic Merkle DAG-Trees an efficient structure for managing large datasets, ***as the branches become exponentially smaller with the increasing number of files in the folder.***

In stark contrast, the number of hashes required to validate a single folder in a Merkle DAG exhibits linear growth. If there are more children (files) in the folder, you must download the hash of each one to retrieve any individual file from the folder. This constant requirement can lead to overly large Merkle branches. The amount of hashes needed to validate a single file increases in direct proportion to the number of files in the folder, making it less efficient for large datasets.

## Syncing Trees Across Relays by Requesting a Range of Leaves

To further enhance the functionality of Scionic Merkle DAG-Trees and support efficient data retrieval, each leaf in the tree is labeled with a sequenced number. This method facilitates the [request for a range of Merkle leaves](https://www.hornetstorage.com/forest), much like what GraphSync attempts to accomplish, but without the complexity of using complex graph selectors and large request sizes.

The total number of leaves is recorded at the root of the tree. By doing so, users can request a range of leaves from a given folder and receive it as a small Scionic Merkle branch, reducing the bandwidth overhead and computational workload required to access multiple files in the same folder.

This approach provides the structural advantages of Scionic Merkle DAG-Trees, such as logarithmic growth of branches and efficient file download and verification, and also provides enhanced support for ranged requests, contributing to their practicality in large-scale data management scenarios.

##

#### Documentation 

See the docs [here](https://godoc.org/github.com/HORNET-Storage/scionic-merkletree).

#### Install
```
go get github.com/HORNET-Storage/scionic-merkletree/dag
```

#### Example Usage
There are good examples inside the dag/dag_test.go file, but below is a basic example to get you started.   This library is intended to be very simple while still allowing for powerful usage...

Turn a folder and its files into a Scionic Merkle DAG-Tree, then convert the Scionic Merkle tree back to the original files in a new directory:
```go
input := filepath.Join(tmpDir, "input")
output := filepath.Join(tmpDir, "output")

SetChunkSize(4096)

dag, err := CreateDag(input, multibase.Base64)
if err != nil {
  t.Fatalf("Error: %s", err)
}

encoder := multibase.MustNewEncoder(multibase.Base64)
result, err := dag.Verify(encoder)
if err != nil {
  t.Fatalf("Error: %s", err)
}

if !result {
  t.Fatal("Dag failed to verify")
}

err = dag.CreateDirectory(output, encoder)
if err != nil {
  t.Fatalf("Error: %s", err)
}
```

This repository is a work in progress. We plan to make quite a few improvements as we move forward with integrating the Scionic Merkle DAG-Trees into H.O.R.N.E.T. Storage. Although, what we have is currently working -- we do not recommend using the trees in anything production-related yet.
#
