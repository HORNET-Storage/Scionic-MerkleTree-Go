package dag

import (
	"fmt"
	"io/fs"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strconv"

	cbor "github.com/fxamacker/cbor/v2"
	"github.com/multiformats/go-multibase"
)

func CreateDag(path string, encoding ...multibase.Encoding) (*Dag, error) {
	var e multibase.Encoding
	if len(encoding) > 0 {
		e = encoding[0]
	} else {
		e = multibase.Base64
	}
	encoder := multibase.MustNewEncoder(e)

	dag := CreateDagBuilder()

	relPath := filepath.Base(path)
	builder := CreateDagLeafBuilder(relPath)
	builder.SetType(DirectoryLeafType)

	entries, err := ioutil.ReadDir(path)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if entry.Name() != ".meta" {
			leaf, err := processEntry(entry, &path, dag, encoder)
			if err != nil {
				return nil, err
			}

			label := dag.GetNextAvailableLabel()
			builder.AddLink(label, leaf.Hash)
			leaf.SetLabel(label)
			dag.AddLeaf(leaf, encoder, nil)
		}
	}

	leaf, err := builder.BuildRootLeaf(dag, encoder)

	if err != nil {
		return nil, err
	}

	dag.AddLeaf(leaf, encoder, nil)

	rootHash := leaf.Hash
	return dag.BuildDag(rootHash), nil
}

func processEntry(entry fs.FileInfo, path *string, dag *DagBuilder, encoder multibase.Encoder) (*DagLeaf, error) {
	entryPath := filepath.Join(*path, entry.Name())

	relPath, err := filepath.Rel(*path, entryPath)
	if err != nil {
		return nil, err
	}

	builder := CreateDagLeafBuilder(relPath)

	if entry.IsDir() {
		builder.SetType(DirectoryLeafType)

		entries, err := ioutil.ReadDir(entryPath)
		if err != nil {
			return nil, err
		}

		for _, entry := range entries {
			if entry.Name() != ".meta" {
				leaf, err := processEntry(entry, &entryPath, dag, encoder)
				if err != nil {
					return nil, err
				}

				label := dag.GetNextAvailableLabel()
				builder.AddLink(label, leaf.Hash)
				leaf.SetLabel(label)
				dag.AddLeaf(leaf, encoder, nil)
			}
		}
	} else {
		fileData, err := ioutil.ReadFile(entryPath)
		if err != nil {
			return nil, err
		}

		builder.SetType(FileLeafType)

		fileChunks := chunkFile(fileData, ChunkSize)

		if len(fileChunks) == 1 {
			builder.SetData(fileChunks[0])
		} else {
			for i, chunk := range fileChunks {
				chunkEntryPath := filepath.Join(relPath, strconv.Itoa(i))
				chunkBuilder := CreateDagLeafBuilder(chunkEntryPath)

				chunkBuilder.SetType(ChunkLeafType)
				chunkBuilder.SetData(chunk)

				chunkLeaf, err := chunkBuilder.BuildLeaf(encoder)
				if err != nil {
					return nil, err
				}

				label := dag.GetNextAvailableLabel()
				builder.AddLink(label, chunkLeaf.Hash)
				chunkLeaf.SetLabel(label)
				dag.AddLeaf(chunkLeaf, encoder, nil)
			}
		}
	}

	result, err := builder.BuildLeaf(encoder)
	if err != nil {
		return nil, err
	}

	return result, nil
}

func chunkFile(fileData []byte, chunkSize int) [][]byte {
	var chunks [][]byte
	fileSize := len(fileData)

	for i := 0; i < fileSize; i += chunkSize {
		end := i + chunkSize
		if end > fileSize {
			end = fileSize
		}
		chunks = append(chunks, fileData[i:end])
	}

	return chunks
}

func CreateDagBuilder() *DagBuilder {
	return &DagBuilder{
		Leafs: map[string]*DagLeaf{},
	}
}

func (b *DagBuilder) AddLeaf(leaf *DagLeaf, encoder multibase.Encoder, parentLeaf *DagLeaf) error {
	if parentLeaf != nil {
		label := GetLabel(leaf.Hash)
		_, exists := parentLeaf.Links[label]
		if !exists {
			parentLeaf.AddLink(leaf.Hash)
		}
	}

	b.Leafs[leaf.Hash] = leaf

	return nil
}

func (b *DagBuilder) BuildDag(root string) *Dag {
	return &Dag{
		Leafs: b.Leafs,
		Root:  root,
	}
}

func (dag *Dag) Verify(encoder multibase.Encoder) (bool, error) {
	result := true

	for _, leaf := range dag.Leafs {
		if leaf.Hash == dag.Root {
			leafResult, err := leaf.VerifyRootLeaf(encoder)
			if err != nil {
				return false, err
			}

			if !leafResult {
				result = false
			}
		} else {
			leafResult, err := leaf.VerifyLeaf(encoder)
			if err != nil {
				return false, err
			}

			if !leafResult {
				result = false
			}
		}
	}

	return result, nil
}

func (dag *Dag) CreateDirectory(path string, encoder multibase.Encoder) error {
	rootHash := dag.Root
	rootLeaf := dag.Leafs[rootHash]

	err := rootLeaf.CreateDirectoryLeaf(path, dag, encoder)
	if err != nil {
		return err
	}

	cborData, err := dag.ToCBOR()
	if err != nil {
		log.Println("Failed to serialize dag into cbor")
		os.Exit(1)
	}

	fileName := filepath.Join(path, ".dag")
	file, err := os.Create(fileName)
	if err != nil {
		return err
	}

	defer file.Close()

	_, err = file.Write(cborData)
	if err != nil {
		return fmt.Errorf("failed to write to file: %w", err)
	}

	/*
		if runtime.GOOS == "windows" {
			p, err := syscall.UTF16PtrFromString(fileName)
			if err != nil {
				log.Fatal(err)
			}

			attrs, err := syscall.GetFileAttributes(p)
			if err != nil {
				log.Fatal(err)
			}

			err = syscall.SetFileAttributes(p, attrs|syscall.FILE_ATTRIBUTE_HIDDEN)
			if err != nil {
				log.Fatal(err)
			}
		}
	*/

	return nil
}

func ReadDag(path string) (*Dag, error) {
	fileData, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not read file: %w", err)
	}

	var result Dag
	if err := cbor.Unmarshal(fileData, &result); err != nil {
		return nil, fmt.Errorf("could not decode Dag: %w", err)
	}

	return &result, nil
}
