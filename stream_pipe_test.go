package scionic

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

// Connections as a program meets them: Go's own in-memory pipes, which hold
// nothing and hand a write over only as the other end reads it, and a TCP
// connection. The other tests of syncing use a pipe of their own, which
// holds as many bytes as a test says.

// manySmallFiles is a tree whose fetch asks for many lists in one round.
func manySmallFiles(t *testing.T, count int) (*MemoryStore, Root) {
	t.Helper()
	store := NewMemoryStore()
	builder := NewBuilder(store, smallChunks(t))
	var entries []Entry
	for index := range count {
		file := must[Target](t)(builder.FileFromBytes(corpusBytes(fmt.Sprintf("small-%d", index), 20)))
		entries = append(entries, must[Entry](t)(NewEntry(fmt.Sprintf("f%03d", index), file, ExtraData{})))
	}
	top := must[Target](t)(builder.Folder(entries))
	return store, must[Root](t)(builder.Finish(top, ExtraData{}))
}

// twoWay is one end of a connection that is made of two pipes.
type twoWay struct {
	io.Reader
	io.Writer
	closers []io.Closer
}

func (w twoWay) Close() error {
	for _, closer := range w.closers {
		closer.Close()
	}
	return nil
}

// ioPipes is a connection made of two of io's pipes.
func ioPipes() (client, server io.ReadWriteCloser) {
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	return twoWay{clientIn, clientOut, []io.Closer{clientIn, clientOut}},
		twoWay{serverIn, serverOut, []io.Closer{serverIn, serverOut}}
}

// bufferedEnd is an end of a connection that is read and written through
// buffers. It has the Flush method that a buffered writer has.
type bufferedEnd struct {
	*bufio.ReadWriter
	end io.Closer
}

func (b bufferedEnd) Close() error { return b.end.Close() }

// buffered is an end of a connection that is read and written through
// buffers, as a program that uses one would.
func buffered(end io.ReadWriteCloser) io.ReadWriteCloser {
	return bufferedEnd{bufio.NewReadWriter(bufio.NewReader(end), bufio.NewWriter(end)), end}
}

// answerOneAtATime answers requests as Serve does, but reads one request
// at a time and nothing ahead of it: an answerer of another make.
func answerOneAtATime(store Store, input io.Reader, output io.Writer) error {
	responder := NewResponder(store)
	for {
		request, err := ReadFrame(input, MaxRequestBytes)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		err = responder.Answer(request, func(frame []byte) error {
			return WriteFrame(output, frame)
		})
		if err != nil {
			return err
		}
		if err := writeEnd(output); err != nil {
			return err
		}
		if err := flush(output, "send an answer"); err != nil {
			return err
		}
	}
}

// overAConnection answers from source at one end of a connection and
// fetches root at the other, and gives what was fetched, and how the fetch
// and the answering ended. Two ends that wait for each other are cut off
// after a while, so that they fail a test and do not hang it.
func overAConnection(
	source Store, root Root, client, server io.ReadWriteCloser,
	answer func(Store, io.Reader, io.Writer) error, set func(*StreamPeer) *StreamPeer,
) (*MemoryStore, error, error) {
	stuck := time.AfterFunc(20*time.Second, func() {
		client.Close()
		server.Close()
	})
	defer stuck.Stop()
	answered := make(chan error, 1)
	go func() {
		err := answer(source, server, server)
		server.Close()
		answered <- err
	}()
	dest := NewMemoryStore()
	_, err := Fetch(dest, root, set(NewStreamPeer(client, client)))
	// The client hangs up, and the answering ends.
	client.Close()
	return dest, err, <-answered
}

// asItIs leaves a peer as it was made.
func asItIs(peer *StreamPeer) *StreamPeer { return peer }

func TestTwoEndsNeverWaitForEachOtherOnAConnectionThatHoldsNothing(t *testing.T) {
	// A round of this tree's fetch asks for 60 lists, and they are sent
	// ahead of their answers, 55 of them at once.
	source, root := manySmallFiles(t, 60)
	connections := map[string]func() (client, server io.ReadWriteCloser){
		"net.Pipe": func() (io.ReadWriteCloser, io.ReadWriteCloser) { return net.Pipe() },
		"io.Pipe":  ioPipes,
		"net.Pipe, read and written through buffers": func() (io.ReadWriteCloser, io.ReadWriteCloser) {
			client, server := net.Pipe()
			return buffered(client), buffered(server)
		},
		"io.Pipe, read and written through buffers": func() (io.ReadWriteCloser, io.ReadWriteCloser) {
			client, server := ioPipes()
			return buffered(client), buffered(server)
		},
	}
	for what, connect := range connections {
		client, server := connect()
		dest, err, served := overAConnection(source, root, client, server, Serve, asItIs)
		if err != nil || served != nil {
			t.Fatalf("%s: the fetch gave %v, and the serving %v", what, err, served)
		}
		if !sameStores(dest, source) {
			t.Fatalf("%s: what was fetched is not what was held", what)
		}
	}
}

func TestRequestsCanBeSentOneAtATime(t *testing.T) {
	source, root := manySmallFiles(t, 60)
	// An answerer that reads one request at a time, on a connection that
	// holds nothing: no request may be sent ahead of an answer.
	for what, connect := range map[string]func() (client, server io.ReadWriteCloser){
		"net.Pipe": func() (io.ReadWriteCloser, io.ReadWriteCloser) { return net.Pipe() },
		"io.Pipe":  ioPipes,
	} {
		client, server := connect()
		dest, err, served := overAConnection(source, root, client, server, answerOneAtATime, func(peer *StreamPeer) *StreamPeer {
			return peer.Ahead(0)
		})
		if err != nil || served != nil {
			t.Fatalf("%s: the fetch gave %v, and the answering %v", what, err, served)
		}
		if !sameStores(dest, source) {
			t.Fatalf("%s: what was fetched is not what was held", what)
		}
	}
	// What is set is held to what the format allows, at both ends.
	sent := func(ahead int) []int {
		dest := NewMemoryStore()
		got, flights, served := connected(source, RequestsAheadBytes, 1, func(peer *StreamPeer) error {
			_, err := Fetch(dest, root, peer.Ahead(ahead))
			return err
		})
		if got != nil || served != nil || !sameStores(dest, source) {
			t.Fatalf("%d bytes ahead: the fetch gave %v, and the serving %v", ahead, got, served)
		}
		return flights.sizes
	}
	longest := func(sizes []int) int {
		most := 0
		for _, size := range sizes {
			most = max(most, size)
		}
		return most
	}
	// A request for a list takes 37 bytes, and one for chunks 49.
	if most := longest(sent(0)); most != 49 {
		t.Fatalf("with nothing sent ahead, %d bytes went out at once", most)
	}
	if most := longest(sent(-1)); most != 49 {
		t.Fatalf("with less than nothing sent ahead, %d bytes went out at once", most)
	}
	if most := longest(sent(100)); most > 100 || most < 74 {
		t.Fatalf("with 100 bytes sent ahead, %d bytes went out at once", most)
	}
	if most := longest(sent(1 << 30)); most > RequestsAheadBytes || most < RequestsAheadBytes-49 {
		t.Fatalf("with everything sent ahead, %d bytes went out at once", most)
	}
}

func TestARequestThatNoAnswererTakesIsNotSent(t *testing.T) {
	source, root := sampleTree(t)
	good := RequestRecord(root).Encode()
	take := func([]byte) error { return nil }
	got, sent, served := connected(source, RequestsAheadBytes, 1<<20, func(peer *StreamPeer) [4]error {
		return [4]error{
			peer.Ask(nil, MaxRecordBytes, take),
			peer.Ask(make([]byte, MaxRequestBytes+1), MaxRecordBytes, take),
			// One that is refused among good ones: none of them is sent.
			peer.AskAll([]Asked{{good, MaxRecordBytes}, {nil, 1}, {good, MaxRecordBytes}}, func(int, []byte) error { return nil }),
			// And the connection is as good as it was.
			peer.Ask(good, MaxRecordBytes, take),
		}
	})
	if served != nil {
		t.Fatal(served)
	}
	if !errors.Is(got[0], ErrInvalidData) || !errors.Is(got[1], ErrLimitExceeded) || !errors.Is(got[2], ErrInvalidData) {
		t.Fatalf("requests that no answerer takes: %v", got)
	}
	if got[3] != nil || len(sent.all) != len(good)+4 {
		t.Fatalf("after them, a good request gave %v, and %d bytes were sent in all", got[3], len(sent.all))
	}
}

func TestATreeIsFetchedOverARealConnection(t *testing.T) {
	// A TCP connection of this machine to itself, on a port the system
	// picks. Requests are sent ahead as far as they ever are, and the
	// connection has to hold them.
	source, root := manySmallFiles(t, 300)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("this machine gives no connection to itself: %v", err)
	}
	defer listener.Close()
	for _, wrap := range []func(io.ReadWriteCloser) io.ReadWriteCloser{
		func(end io.ReadWriteCloser) io.ReadWriteCloser { return end },
		buffered,
	} {
		accepted := make(chan net.Conn, 1)
		go func() {
			conn, err := listener.Accept()
			if err != nil {
				close(accepted)
				return
			}
			accepted <- conn
		}()
		client, err := net.DialTimeout("tcp", listener.Addr().String(), 20*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		server, open := <-accepted
		if !open {
			client.Close()
			t.Fatal("the connection was not taken")
		}
		dest, err, served := overAConnection(source, root, wrap(client), wrap(server), Serve, asItIs)
		if err != nil || served != nil {
			t.Fatalf("the fetch gave %v, and the serving %v", err, served)
		}
		if !sameStores(dest, source) {
			t.Fatal("what was fetched is not what was held")
		}
	}
}

func TestABundleThatIsWrittenThroughABufferIsWrittenWhole(t *testing.T) {
	source, root := sampleTree(t)
	// A buffer that is longer than the bundle: nothing reaches what is
	// under it until the buffer is flushed.
	var written bytes.Buffer
	takes(t, "a bundle", Export(source, root, bufio.NewWriterSize(&written, 1<<20)))
	dest := NewMemoryStore()
	if _, err := Import(dest, root, &written); err != nil || !sameStores(dest, source) {
		t.Fatalf("the bundle was not written whole (%v)", err)
	}
	// And a buffer that cannot be flushed is told.
	err := Export(source, root, bufio.NewWriterSize(&brokenWriter{left: 10}, 1<<20))
	if !errors.Is(err, ErrStream) || !errors.Is(err, errBrokenWriter) {
		t.Fatalf("a bundle that could not be written whole: %v", err)
	}
}
