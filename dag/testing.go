package dag

import (
	"fmt"
	"io/ioutil"
	"math/rand"
	"os"
	"time"

	"github.com/multiformats/go-multibase"
)

func GenerateDummyDirectory(path string, maxItems int, maxDepth int) {
	rand.Seed(time.Now().UnixNano())

	err := createRandomDirsAndFiles(path, maxDepth, maxItems)
	if err != nil {
		fmt.Println("Error:", err)
	}
}

func createRandomDirsAndFiles(path string, depth int, maxItems int) error {
	if depth == 0 {
		return nil
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		err := os.Mkdir(path, 0755)
		if err != nil {
			return err
		}
	}

	numItems := rand.Intn(maxItems) + 1
	for i := 0; i < numItems; i++ {
		if rand.Intn(2) == 0 {
			subDir := fmt.Sprintf("%s/subdir%d", path, i)
			err := createRandomDirsAndFiles(subDir, depth-1, maxItems)
			if err != nil {
				return err
			}
		} else {
			filePath := fmt.Sprintf("%s/file%d.txt", path, i)
			randomData := make([]byte, rand.Intn(100))
			rand.Read(randomData)
			err := ioutil.WriteFile(filePath, randomData, 0644)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func FindRandomChild(leaf *DagLeaf, leafs map[string]*DagLeaf, encoder multibase.Encoder) *DagLeaf {
	if leaf.Type == DirectoryLeafType {
		rand.Seed(time.Now().UnixNano())
		index := rand.Intn(len(leaf.Links))

		var newLeaf *DagLeaf

		curIndex := 1
		for label, link := range leaf.Links {
			if curIndex >= index && label != "0" {
				newLeaf = leafs[link]
			}

			curIndex++
		}

		return newLeaf
	}

	return leaf
}

func CreateDummyLeaf(name string, encoder multibase.Encoder) (*DagLeaf, error) {
	rand.Seed(time.Now().UnixNano())

	builder := CreateDagLeafBuilder(name)

	builder.SetType(FileLeafType)

	data := make([]byte, rand.Intn(100)+10) // 10 to 100 bytes of random data
	rand.Read(data)

	chunkSize := 20
	var chunks [][]byte
	for i := 0; i < len(data); i += chunkSize {
		end := i + chunkSize
		if end > len(data) {
			end = len(data)
		}
		chunks = append(chunks, data[i:end])
	}

	if len(chunks) == 1 {
		builder.SetData(chunks[0])
	} else {
		for i, chunk := range chunks {
			chunkEntryName := fmt.Sprintf("%s_%d", name, i)
			chunkBuilder := CreateDagLeafBuilder(chunkEntryName)

			chunkBuilder.SetType(ChunkLeafType)
			chunkBuilder.SetData(chunk)

			chunkLeaf, err := chunkBuilder.BuildLeaf(encoder)
			if err != nil {
				return nil, err
			}

			label := fmt.Sprintf("%d", i)
			builder.AddLink(label, chunkLeaf.Hash)
		}
	}

	return builder.BuildLeaf(encoder)
}
