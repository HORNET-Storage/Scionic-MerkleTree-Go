package scionic_test

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"net"

	scionic "github.com/HORNET-Storage/Scionic-Merkle-Tree/v2"
)

// serveOnePeer is the side that holds the tree. It answers one peer's
// requests until the peer hangs up.
func serveOnePeer(store scionic.Store, listener net.Listener) error {
	conn, err := listener.Accept()
	if err != nil {
		return err
	}
	defer conn.Close()
	return scionic.Serve(store, conn, bufio.NewWriter(conn))
}

// fetchTree is the side that wants the tree. A fetch that was cut off goes
// on from what the store holds when it is run again.
func fetchTree(address string, root scionic.Root, store scionic.Store) error {
	conn, err := net.Dial("tcp", address)
	if err != nil {
		return err
	}
	defer conn.Close()
	peer := scionic.NewStreamPeer(bufio.NewReader(conn), bufio.NewWriter(conn))
	_, err = scionic.Fetch(store, root, peer)
	return err
}

// Syncing runs over anything that reads and writes bytes. Here it is a TCP
// connection of this machine to itself.
func Example_syncOverAConnection() {
	// The side that holds a tree. A single file can be a tree on its own.
	publisher := scionic.NewMemoryStore()
	builder := scionic.NewBuilder(publisher, scionic.FastCDCV1)
	weights, err := builder.FileFromBytes(bytes.Repeat([]byte{7}, 3<<20))
	if err != nil {
		log.Fatal(err)
	}
	root, err := builder.Finish(weights, scionic.ExtraData{})
	if err != nil {
		log.Fatal(err)
	}

	// It listens on this machine alone, on a port that the system picks.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()
	served := make(chan error, 1)
	go func() { served <- serveOnePeer(publisher, listener) }()

	// The side that wants the tree.
	mirror := scionic.NewMemoryStore()
	if err := fetchTree(listener.Addr().String(), root, mirror); err != nil {
		log.Fatal(err)
	}
	if err := <-served; err != nil {
		log.Fatal(err)
	}
	fmt.Println(mirror.Bytes(scionic.StoredPiece), "bytes of chunks arrived")
	// Output:
	// 3145728 bytes of chunks arrived
}
