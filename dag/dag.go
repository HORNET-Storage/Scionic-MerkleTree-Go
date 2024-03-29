package dag

import (
	"fmt"
	"io/fs"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	cbor "github.com/fxamacker/cbor/v2"
)

type fileInfoDirEntry struct {
	fileInfo os.FileInfo
}

func (e fileInfoDirEntry) Name() string {
	return e.fileInfo.Name()
}

func (e fileInfoDirEntry) IsDir() bool {
	return e.fileInfo.IsDir()
}

func (e fileInfoDirEntry) Type() fs.FileMode {
	return e.fileInfo.Mode().Type()
}

func (e fileInfoDirEntry) Info() (fs.FileInfo, error) {
	return e.fileInfo, nil
}

func newDirEntry(path string) (fs.DirEntry, error) {
	fileInfo, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	return fileInfoDirEntry{fileInfo: fileInfo}, nil
}

func CreateDag(path string, timestampRoot bool) (*Dag, error) {
	dag := CreateDagBuilder()

	fileInfo, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	dirEntry, err := newDirEntry(path)
	if err != nil {
		return nil, err
	}

	parentPath := filepath.Dir(path)

	var leaf *DagLeaf

	if fileInfo.IsDir() {
		leaf, err = processDirectory(dirEntry, &parentPath, dag, true, timestampRoot)
	} else {
		leaf, err = processFile(dirEntry, &parentPath, dag, true, timestampRoot)
	}

	if err != nil {
		return nil, err
	}

	dag.AddLeaf(leaf, nil)

	rootHash := leaf.Hash
	return dag.BuildDag(rootHash), nil
}

func processEntry(entry fs.DirEntry, path *string, dag *DagBuilder) (*DagLeaf, error) {
	var result *DagLeaf
	var err error

	if entry.IsDir() {
		result, err = processDirectory(entry, path, dag, false, false)
	} else {
		result, err = processFile(entry, path, dag, false, false)
	}

	if err != nil {
		return nil, err
	}

	return result, nil
}

func processDirectory(entry fs.DirEntry, path *string, dag *DagBuilder, isRoot bool, timestampRoot bool) (*DagLeaf, error) {
	entryPath := filepath.Join(*path, entry.Name())

	relPath, err := filepath.Rel(*path, entryPath)
	if err != nil {
		return nil, err
	}

	builder := CreateDagLeafBuilder(relPath)

	builder.SetType(DirectoryLeafType)

	entries, err := os.ReadDir(entryPath)
	if err != nil {
		return nil, err
	}

	var result *DagLeaf

	for _, entry := range entries {
		leaf, err := processEntry(entry, &entryPath, dag)
		if err != nil {
			return nil, err
		}

		label := dag.GetNextAvailableLabel()
		builder.AddLink(label, leaf.Hash)
		leaf.SetLabel(label)
		dag.AddLeaf(leaf, nil)
	}

	var additionalData map[string]string = nil

	if timestampRoot {
		currentTime := time.Now().UTC()

		timeString := currentTime.Format(time.RFC3339)

		additionalData = map[string]string{
			"timestamp": timeString,
		}
	}

	if isRoot {
		result, err = builder.BuildRootLeaf(dag, additionalData)
	} else {
		result, err = builder.BuildLeaf(nil)
	}

	if err != nil {
		return nil, err
	}

	return result, nil
}

func processFile(entry fs.DirEntry, path *string, dag *DagBuilder, isRoot bool, timestampRoot bool) (*DagLeaf, error) {
	entryPath := filepath.Join(*path, entry.Name())

	relPath, err := filepath.Rel(*path, entryPath)
	if err != nil {
		return nil, err
	}

	var result *DagLeaf

	builder := CreateDagLeafBuilder(relPath)

	builder.SetType(FileLeafType)

	fileData, err := os.ReadFile(entryPath)
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

			chunkLeaf, err := chunkBuilder.BuildLeaf(nil)
			if err != nil {
				return nil, err
			}

			label := dag.GetNextAvailableLabel()
			builder.AddLink(label, chunkLeaf.Hash)
			chunkLeaf.SetLabel(label)
			dag.AddLeaf(chunkLeaf, nil)
		}
	}

	var additionalData map[string]string = nil

	if timestampRoot {
		currentTime := time.Now().UTC()

		timeString := currentTime.Format(time.RFC3339)

		additionalData = map[string]string{
			"timestamp": timeString,
		}
	}

	if isRoot {
		result, err = builder.BuildRootLeaf(dag, additionalData)
	} else {
		result, err = builder.BuildLeaf(nil)
	}

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

func (b *DagBuilder) AddLeaf(leaf *DagLeaf, parentLeaf *DagLeaf) error {
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

func (dag *Dag) Verify() error {
	err := dag.IterateDag(func(leaf *DagLeaf, parent *DagLeaf) error {
		if leaf.Hash == dag.Root {
			err := leaf.VerifyRootLeaf()
			if err != nil {
				return err
			}
		} else {
			err := leaf.VerifyLeaf()
			if err != nil {
				return err
			}

			if !parent.HasLink(leaf.Hash) {
				return fmt.Errorf("parent %s does not contain link to child %s", parent.Hash, leaf.Hash)
			}
		}

		return nil
	})

	if err != nil {
		return err
	}

	return nil
}

func (dag *Dag) CreateDirectory(path string) error {
	rootHash := dag.Root
	rootLeaf := dag.Leafs[rootHash]

	err := rootLeaf.CreateDirectoryLeaf(path, dag)
	if err != nil {
		return err
	}

	/*
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
	*/

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

func (dag *Dag) GetContentFromLeaf(leaf *DagLeaf) ([]byte, error) {
	if len(leaf.Content) <= 0 {
		return []byte{}, nil
	}

	var content []byte

	if len(leaf.Links) > 0 {
		for _, link := range leaf.Links {
			childLeaf := dag.Leafs[link]
			if childLeaf == nil {
				return nil, fmt.Errorf("invalid link: %s", link)
			}

			content = append(content, childLeaf.Content...)
		}
	} else {
		content = leaf.Content
	}

	return content, nil
}

func (d *Dag) IterateDag(processLeaf func(leaf *DagLeaf, parent *DagLeaf) error) error {
	var iterate func(leafHash string, parentHash *string) error
	iterate = func(leafHash string, parentHash *string) error {
		leaf, exists := d.Leafs[leafHash]
		if !exists {
			return fmt.Errorf("child is missing when iterating dag")
		}

		var parent *DagLeaf
		if parentHash != nil {
			parent = d.Leafs[*parentHash]
		}

		err := processLeaf(leaf, parent)
		if err != nil {
			return err
		}

		childHashes := []string{}
		for _, childHash := range leaf.Links {
			childHashes = append(childHashes, childHash)
		}

		sort.Slice(childHashes, func(i, j int) bool {
			numI, _ := strconv.Atoi(strings.Split(childHashes[i], ":")[0])
			numJ, _ := strconv.Atoi(strings.Split(childHashes[j], ":")[0])

			return numI < numJ
		})

		for _, childHash := range childHashes {
			err := iterate(childHash, &leaf.Hash)
			if err != nil {
				return err
			}
		}

		return nil
	}

	return iterate(d.Root, nil)
}
