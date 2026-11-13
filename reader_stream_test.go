package scionic

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"testing"
	"testing/iotest"
)

// The same for a stream of frames, and for what the library writes: a
// stream is read whole however it is handed over, a stream that fails has
// failed, and a writer that takes fewer bytes than it is given is told.

// greedyReader says that it gave more bytes than it was asked for.
type greedyReader struct{}

func (greedyReader) Read(out []byte) (int, error) {
	return len(out) + 1, nil
}

// shortWriter takes one byte fewer than it is given, and says nothing.
type shortWriter struct {
	taken int
}

func (w *shortWriter) Write(content []byte) (int, error) {
	if len(content) == 0 {
		return 0, nil
	}
	w.taken += len(content) - 1
	return len(content) - 1, nil
}

// manners gives a stream in each of the ways a reader may hand it over.
func manners(stream []byte) map[string]func() io.Reader {
	return map[string]func() io.Reader{
		"all at once":                 func() io.Reader { return bytes.NewReader(stream) },
		"a byte at a time":            func() io.Reader { return iotest.OneByteReader(bytes.NewReader(stream)) },
		"half of what is asked for":   func() io.Reader { return iotest.HalfReader(bytes.NewReader(stream)) },
		"its last bytes with its end": func() io.Reader { return iotest.DataErrReader(bytes.NewReader(stream)) },
		"its end with its only read":  func() io.Reader { return &scriptedReader{steps: []readStep{{give: stream, err: io.EOF}}} },
		"reads of nothing in between": func() io.Reader {
			var steps []readStep
			for start := 0; start < len(stream); start += 7 {
				steps = append(steps, readStep{}, readStep{}, readStep{give: stream[start:min(start+7, len(stream))]})
			}
			return &scriptedReader{steps: steps}
		},
	}
}

func TestFramesComeOffAStreamHoweverItHandsThemOver(t *testing.T) {
	frames := [][]byte{[]byte("one"), corpusBytes("stream-frame", 3000), []byte("three")}
	var stream bytes.Buffer
	for _, frame := range frames {
		takes(t, "a frame", WriteFrame(&stream, frame))
	}
	for what, open := range manners(stream.Bytes()) {
		// One reader for the whole stream, asked frame after frame.
		input := open()
		for at, want := range frames {
			got, err := ReadFrame(input, 4096)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("%s: frame %d was read as %d bytes (%v)", what, at, len(got), err)
			}
		}
		if _, err := ReadFrame(input, 4096); err != io.EOF {
			t.Fatalf("%s: the end of the stream: %v", what, err)
		}
		// And asked once more, it has still ended.
		if _, err := ReadFrame(input, 4096); err != io.EOF {
			t.Fatalf("%s: the end of the stream, asked again: %v", what, err)
		}
	}
}

func TestAStreamThatFailsHasFailedWhereverItFails(t *testing.T) {
	var stream bytes.Buffer
	takes(t, "a frame", WriteFrame(&stream, []byte("one")))
	takes(t, "a frame", WriteFrame(&stream, []byte("three")))
	written := stream.Bytes()
	failed := errors.New("the connection was lost")
	// The stream fails with the bytes up to each place in turn: before the
	// first byte, inside a length, inside a frame, with a frame's last
	// byte, and with the last byte of all.
	for cut := 0; cut <= len(written); cut++ {
		input := &scriptedReader{steps: []readStep{{give: written[:cut], err: failed}}}
		var read [][]byte
		var err error
		for err == nil {
			var frame []byte
			if frame, err = ReadFrame(input, 16); err == nil {
				read = append(read, frame)
			}
		}
		if !errors.Is(err, ErrStream) || !errors.Is(err, failed) {
			t.Fatalf("a stream that fails after %d bytes: %v", cut, err)
		}
		// A frame that came whole before the failure is a frame. One that
		// the stream failed on, were it with its last byte, is not.
		whole := 0
		if cut > 7 {
			whole = 1
		}
		if len(read) != whole {
			t.Fatalf("a stream that fails after %d bytes gave %d frames", cut, len(read))
		}
	}
	// A stream that never gives a byte, at the start and inside a frame.
	for _, before := range [][]byte{nil, written[:2], written[:5]} {
		idle := &idleReader{}
		_, err := ReadFrame(io.MultiReader(bytes.NewReader(before), idle), 16)
		if !errors.Is(err, ErrStream) || !errors.Is(err, io.ErrNoProgress) || idle.calls > 200 {
			t.Fatalf("a stream that stops after %d bytes: %v, asked %d times", len(before), err, idle.calls)
		}
	}
	// A reader that miscounts its own bytes is an error, and never a crash.
	if _, err := ReadFrame(greedyReader{}, 16); !errors.Is(err, ErrStream) || !errors.Is(err, ErrInvariant) {
		t.Fatalf("a reader that gives more than it was asked for: %v", err)
	}
	if _, err := NewBuilder(NewMemoryStore(), smallPieces(t)).File(greedyReader{}); !errors.Is(err, ErrInvariant) {
		t.Fatalf("a reader that gives more than it was asked for, as a file: %v", err)
	}
	// An error that only wraps the end of a stream is an error, and not the
	// end: a reader says that it has ended with io.EOF itself.
	wrapped := fmt.Errorf("cut off: %w", io.EOF)
	input := &scriptedReader{steps: []readStep{{give: written[:7]}, {err: wrapped}}}
	if frame, err := ReadFrame(input, 16); err != nil || string(frame) != "one" {
		t.Fatal(err)
	}
	if _, err := ReadFrame(input, 16); err == io.EOF || !errors.Is(err, ErrStream) {
		t.Fatalf("an error that wraps the end of a stream: %v", err)
	}
	store := NewMemoryStore()
	cutOff := &scriptedReader{steps: []readStep{{give: corpusBytes("cut-off", 100)}, {err: wrapped}}}
	if _, err := NewBuilder(store, smallPieces(t)).File(cutOff); !errors.Is(err, ErrStream) || store.Count(StoredFileList) != 0 {
		t.Fatalf("a file whose reader was cut off: %v", err)
	}
}

func TestABundleIsTakenInHoweverItIsHandedOverAndNeverWhenItsStreamFails(t *testing.T) {
	source, root := sampleTree(t)
	var bundle bytes.Buffer
	takes(t, "a bundle", Export(source, root, &bundle))
	written := bundle.Bytes()
	for what, open := range manners(written) {
		dest := NewMemoryStore()
		if _, err := Import(dest, root, open()); err != nil || !sameStores(dest, source) {
			t.Fatalf("%s: the bundle was not taken in whole (%v)", what, err)
		}
	}
	// The stream fails with the bytes up to each of many places, the last
	// byte of the bundle among them. A stream that failed is no bundle.
	failed := errors.New("the connection was lost")
	for cut := 0; cut <= len(written); cut += max(1, (len(written)-cut)/40) {
		dest := NewMemoryStore()
		_, err := Import(dest, root, &scriptedReader{steps: []readStep{{give: written[:cut], err: failed}}})
		if !errors.Is(err, failed) {
			t.Fatalf("a bundle whose stream fails after %d of %d bytes: %v", cut, len(written), err)
		}
		assertOnlyFrom(t, dest, source)
	}
	// A stream that stops giving bytes is given up.
	idle := &idleReader{}
	if _, err := Import(NewMemoryStore(), root, io.MultiReader(bytes.NewReader(written[:len(written)/2]), idle)); !errors.Is(err, io.ErrNoProgress) || idle.calls > 200 {
		t.Fatalf("a bundle whose stream stops: %v, asked %d times", err, idle.calls)
	}
}

func TestAWriterThatTakesFewerBytesThanItIsGivenIsTold(t *testing.T) {
	store, root := sampleTree(t)
	model := targetAt(t, store, root, "weights/model.bin")
	short := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, ErrStream) || !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("%s: %v", what, err)
		}
	}
	short("a file read out", ReadFile(store, model, &shortWriter{}))
	short("a part of a file read out", ReadRange(store, model, 10, 100, &shortWriter{}))
	short("a frame", WriteFrame(&shortWriter{}, []byte("frame")))
	short("a bundle", Export(store, root, &shortWriter{}))
	remote := must[*Remote](t)(OpenRemote(newTestPeer(store), root))
	short("a part of a file read from a peer", remote.Read(model, 0, 1000, &shortWriter{}))
	// Serving stops, too: what it sent is not what it meant to send.
	var requests bytes.Buffer
	takes(t, "a request", WriteFrame(&requests, RequestRecord(root).Encode()))
	short("an answer", Serve(store, &requests, &shortWriter{}))
	// A writer that takes everything is not told off.
	var whole bytes.Buffer
	takes(t, "a file read out", ReadFile(store, model, &whole))
	if whole.Len() != 1000 {
		t.Fatal(whole.Len())
	}
}
