package scionic

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"sync"
	"testing"
	"time"
)

// What the tests of syncing share: a sample tree, peers that a test can
// watch and change, and a connection inside one process that holds no more
// bytes than it is told to.

// sampleFiles is a folder with files of several sizes, a folder inside a
// folder, an empty file and an empty folder. Two of its files have the same
// bytes.
func sampleFiles() map[string][]byte {
	return map[string][]byte{
		"readme.txt":                  []byte("a small file"),
		"empty.bin":                   nil,
		"weights/model.bin":           corpusBytes("model", 1000),
		"weights/copy-of-model.bin":   corpusBytes("model", 1000),
		"weights/shards/a.bin":        corpusBytes("shard-a", 100),
		"weights/shards/b.bin":        corpusBytes("shard-b", 33),
		"data/exactly-two-pieces.bin": corpusBytes("two", 32),
		"nothing-here/":               nil,
	}
}

// sampleTree builds the sample folder into a new store with small pieces.
func sampleTree(t *testing.T) (*MemoryStore, Root) {
	t.Helper()
	store := NewMemoryStore()
	root, err := Build(store, writeTree(t, sampleFiles()), BuildOptions{Cutting: smallPieces(t)})
	if err != nil {
		t.Fatal(err)
	}
	return store, root
}

// sameStores reports whether two stores hold the same things.
func sameStores(left, right *MemoryStore) bool {
	for _, kind := range []Stored{StoredPiece, StoredFileList, StoredFolderList, StoredRecord} {
		hashes := left.Hashes(kind)
		if !slices.Equal(hashes, right.Hashes(kind)) {
			return false
		}
		for _, hash := range hashes {
			ours, _, _ := left.Get(kind, hash)
			theirs, _, _ := right.Get(kind, hash)
			if !bytes.Equal(ours, theirs) {
				return false
			}
		}
	}
	return true
}

// wire is what went over the wire to one peer: every request, the longest
// frame its asker would take in answer, and every frame of every answer.
type wire struct {
	requests [][]byte
	limits   []int
	frames   [][]byte
}

// framesOf gives how many frames of one kind were sent.
func (w *wire) framesOf(kind FrameKind) int {
	count := 0
	for _, frame := range w.frames {
		if len(frame) > 0 && FrameKind(frame[0]) == kind {
			count++
		}
	}
	return count
}

// requestsOf gives how many requests of one kind were asked.
func (w *wire) requestsOf(kind RequestKind) int {
	count := 0
	for _, request := range w.requests {
		if len(request) > 0 && RequestKind(request[0]) == kind {
			count++
		}
	}
	return count
}

// testPeer answers from a store, as an honest peer does, and lets a test
// see and change what goes over the wire.
type testPeer struct {
	responder *Responder
	wire      wire
	// tamper changes the frames of an answer before the receiver sees
	// them. It is given the request, and the frames the honest answer has.
	tamper func(request []byte, frames [][]byte) [][]byte
}

func newTestPeer(store Store) *testPeer {
	return &testPeer{responder: NewResponder(store)}
}

func tamperingPeer(store Store, tamper func(request []byte, frames [][]byte) [][]byte) *testPeer {
	return &testPeer{responder: NewResponder(store), tamper: tamper}
}

func (p *testPeer) Ask(request []byte, limit int, onFrame func(frame []byte) error) error {
	p.wire.requests = append(p.wire.requests, bytes.Clone(request))
	p.wire.limits = append(p.wire.limits, limit)
	var frames [][]byte
	err := p.responder.Answer(request, func(frame []byte) error {
		frames = append(frames, bytes.Clone(frame))
		return nil
	})
	if err != nil {
		return err
	}
	if p.tamper != nil {
		frames = p.tamper(request, frames)
	}
	for _, frame := range frames {
		p.wire.frames = append(p.wire.frames, bytes.Clone(frame))
		// A frame that is longer than its asker takes is refused, as a
		// connection refuses it before it reads the frame.
		if err := within(limit, frame); err != nil {
			return err
		}
		if err := onFrame(frame); err != nil {
			return err
		}
	}
	return nil
}

// scriptedPeer says whatever a test makes it say.
type scriptedPeer func(request []byte) [][]byte

func (script scriptedPeer) Ask(request []byte, limit int, onFrame func(frame []byte) error) error {
	for _, frame := range script(request) {
		if err := within(limit, frame); err != nil {
			return err
		}
		if err := onFrame(frame); err != nil {
			return err
		}
	}
	return nil
}

// pipeWait is how long one end of a pipe waits for the other before it
// gives up. Two ends that wait for each other would wait for ever, and a
// test that hangs says nothing.
const pipeWait = 20 * time.Second

var errPipeStuck = errors.New("both ends of a pipe wait for each other")

// pipeShared is one direction of a connection, inside one process. It holds
// capacity bytes and no more: a writer waits while it is full, as a writer
// to a real connection does, and a reader waits while it is empty.
type pipeShared struct {
	lock     sync.Mutex
	held     []byte
	capacity int
	// closed: one of the two ends is gone.
	closed bool
	// changed is closed, and a new one is put in its place, whenever bytes
	// come or go or an end is closed.
	changed chan struct{}
}

// pipeWriter is the end of a pipe that is written to. Closing it ends the
// stream, once what is in the pipe is read.
type pipeWriter struct{ shared *pipeShared }

// pipeReader is the end of a pipe that is read from. Once it is closed, a
// write to the pipe fails.
type pipeReader struct{ shared *pipeShared }

func testPipe(capacity int) (pipeReader, pipeWriter) {
	shared := &pipeShared{capacity: capacity, changed: make(chan struct{})}
	return pipeReader{shared}, pipeWriter{shared}
}

// tell wakes whoever waits. The lock is held.
func (p *pipeShared) tell() {
	close(p.changed)
	p.changed = make(chan struct{})
}

// wait waits for something to change. The lock is held when it is called,
// and when it comes back.
func (p *pipeShared) wait(deadline time.Time) error {
	changed := p.changed
	p.lock.Unlock()
	defer p.lock.Lock()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-changed:
		return nil
	case <-timer.C:
		return errPipeStuck
	}
}

func (p *pipeShared) close() error {
	p.lock.Lock()
	defer p.lock.Unlock()
	if !p.closed {
		p.closed = true
		p.tell()
	}
	return nil
}

func (w pipeWriter) Close() error { return w.shared.close() }
func (r pipeReader) Close() error { return r.shared.close() }

func (w pipeWriter) Write(content []byte) (int, error) {
	p := w.shared
	deadline := time.Now().Add(pipeWait)
	written := 0
	p.lock.Lock()
	defer p.lock.Unlock()
	for len(content) > 0 {
		for len(p.held) >= p.capacity && !p.closed {
			if err := p.wait(deadline); err != nil {
				return written, err
			}
		}
		if p.closed {
			return written, io.ErrClosedPipe
		}
		count := min(len(content), p.capacity-len(p.held))
		p.held = append(p.held, content[:count]...)
		content = content[count:]
		written += count
		p.tell()
	}
	return written, nil
}

func (r pipeReader) Read(out []byte) (int, error) {
	if len(out) == 0 {
		return 0, nil
	}
	p := r.shared
	deadline := time.Now().Add(pipeWait)
	p.lock.Lock()
	defer p.lock.Unlock()
	for len(p.held) == 0 {
		if p.closed {
			return 0, io.EOF
		}
		if err := p.wait(deadline); err != nil {
			return 0, err
		}
	}
	count := copy(out, p.held)
	p.held = p.held[count:]
	p.tell()
	return count, nil
}

// flights counts what a client sends between one flush and the next, and
// keeps all of it.
type flights struct {
	inner   io.Writer
	current int
	sizes   []int
	all     []byte
}

func (f *flights) Write(content []byte) (int, error) {
	count, err := f.inner.Write(content)
	f.current += count
	f.all = append(f.all, content[:count]...)
	return count, err
}

func (f *flights) Flush() error {
	f.sizes = append(f.sizes, f.current)
	f.current = 0
	return nil
}

// connected serves source on one goroutine, and runs client on this one,
// with a connection between them that holds no more than it is given: so
// many bytes on the way to the server, and so many on the way back.
//
// It gives what the client gave, what the client sent, flight by flight,
// and how the serving ended.
func connected[T any](source Store, toServer, toClient int, client func(peer *StreamPeer) T) (T, *flights, error) {
	serverIn, clientOut := testPipe(toServer)
	clientIn, serverOut := testPipe(toClient)
	sent := &flights{inner: clientOut}
	served := make(chan error, 1)
	go func() {
		err := Serve(source, serverIn, serverOut)
		// The serving is over: nothing more comes, and nothing more is
		// read.
		serverOut.Close()
		serverIn.Close()
		served <- err
	}()
	result := func() T {
		// The client hangs up, and the serving ends.
		defer clientIn.Close()
		defer clientOut.Close()
		return client(NewStreamPeer(clientIn, sent))
	}()
	return result, sent, <-served
}
