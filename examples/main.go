package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/multiformats/go-multibase"

	merkle_dag "github.com/HORNET-Storage/scionic-merkletree/dag"
)

func main() {
	dag, err := merkle_dag.CreateDag("D:/organizations/akashic_record/unsorted/nostr2.0/testDirectory", multibase.Base64)
	if err != nil {
		fmt.Println("Error: ", err)
		os.Exit(1)
	}

	encoder := multibase.MustNewEncoder(multibase.Base64)
	result, err := dag.Verify(encoder)
	if err != nil {
		log.Fatalf("Error: %s", err)
	}

	if result {
		log.Println("Dag verified correctly")
	} else {
		log.Fatal("Dag failed to verify")
	}

	path := "D:/organizations/akashic_record/unsorted/nostr2.0/newDirectory8"

	err = dag.CreateDirectory(path, encoder)
	if err != nil {
		log.Fatalf("Error: %s", err)
	}

	jsonData, err := dag.ToJSON()
	if err != nil {
		log.Println("Failed to serialize dag into cbor")
		os.Exit(1)
	}

	fileName := filepath.Join(path, "dag.json")
	file, err := os.Create(fileName)
	if err != nil {
		os.Exit(1)
	}

	defer file.Close()

	_, err = file.Write(jsonData)
	if err != nil {
		os.Exit(1)
	}
}
