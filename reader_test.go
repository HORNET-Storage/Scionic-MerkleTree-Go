package scionic

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"testing/iotest"
)

// A file is the same however its reader hands it over. And a reader that
// fails, or that never gives a byte and never ends, is told: it is never
// taken for a shorter file, and nobody waits for it for ever.

// readStep is one thing a scripted reader does: it gives these bytes, over
// as many reads as they take, and with the last of them this error.
type readStep struct {
	give []byte
	err  error
}

// scriptedReader does its steps one after another, and then says that it
// has ended.
type scriptedReader struct {
	steps []readStep
	calls int
}

func (r *scriptedReader) Read(out []byte) (int, error) {
	r.calls++
	if len(r.steps) == 0 {
		return 0, io.EOF
	}
	step := &r.steps[0]
	count := copy(out, step.give)
	step.give = step.give[count:]
	if len(step.give) > 0 {
		return count, nil
	}
	err := step.err
	r.steps = r.steps[1:]
	return count, err
}

// idleReader never gives a byte, and never ends. It gives up of its own
// accord after a great many reads, so that a test of it ends.
type idleReader struct {
	calls int
}

var errAskedTooOften = errors.New("this reader was asked ten thousand times")

func (r *idleReader) Read([]byte) (int, error) {
	r.calls++
	if r.calls >= 10_000 {
		return 0, errAskedTooOften
	}
	return 0, nil
}

func TestAFileIsTheSameHoweverItsReaderHandsItOver(t *testing.T) {
	for _, cutting := range []Cutting{smallPieces(t), FastCDCV1} {
		data := corpusBytes("readers", 100_000)
		if cutting == FastCDCV1 {
			data = corpusBytes("readers-by-content", 5<<20)
		}
		want := must[Target](t)(NewBuilder(NewMemoryStore(), cutting).FileFromBytes(data))
		// Bytes with nothing between them, and the same with reads that give
		// nothing in between, as a reader may when it has nothing yet.
		var withPauses []readStep
		for start := 0; start < len(data); start += 30_011 {
			end := min(start+30_011, len(data))
			for range 99 {
				withPauses = append(withPauses, readStep{})
			}
			withPauses = append(withPauses, readStep{give: data[start:end]})
		}
		readers := map[string]io.Reader{
			"all at once":                     bytes.NewReader(data),
			"half of what is asked for":       iotest.HalfReader(bytes.NewReader(data)),
			"its last bytes with its end":     iotest.DataErrReader(bytes.NewReader(data)),
			"99 reads of nothing before each": &scriptedReader{steps: withPauses},
			"its end told with its only read": &scriptedReader{steps: []readStep{{give: data, err: io.EOF}}},
		}
		if cutting != FastCDCV1 {
			readers["a byte at a time"] = iotest.OneByteReader(bytes.NewReader(data))
		}
		for what, reader := range readers {
			store := NewMemoryStore()
			got, err := NewBuilder(store, cutting).File(reader)
			if err != nil || got != want {
				t.Fatalf("%s, %s: another file (%v)", cutting.Tag(), what, err)
			}
			if read := must[[]byte](t)(ReadFileBytes(store, got)); !bytes.Equal(read, data) {
				t.Fatalf("%s, %s: the file does not read back", cutting.Tag(), what)
			}
		}
	}
	// A reader that has ended is not asked again.
	once := &scriptedReader{steps: []readStep{{give: []byte("the whole file"), err: io.EOF}}}
	must[Target](t)(NewBuilder(NewMemoryStore(), smallPieces(t)).File(once))
	if once.calls != 1 {
		t.Fatalf("a reader that had ended was read %d times", once.calls)
	}
}

func TestAReaderThatFailsIsNeverTakenForAShorterFile(t *testing.T) {
	data := corpusBytes("readers-failing", 1000)
	failed := errors.New("the connection was lost")
	for what, steps := range map[string][]readStep{
		"with its first bytes":   {{give: data[:500], err: failed}},
		"with later bytes":       {{give: data[:300]}, {give: data[300:600], err: failed}},
		"with no bytes":          {{give: data[:500]}, {err: failed}},
		"before any byte":        {{err: failed}},
		"with one piece exactly": {{give: data[:16], err: failed}},
		"after reads of nothing": {{give: data[:100]}, {}, {}, {}, {err: failed}},
	} {
		store := NewMemoryStore()
		reader := &scriptedReader{steps: steps}
		_, err := NewBuilder(store, smallPieces(t)).File(reader)
		var told *Error
		if !errors.Is(err, ErrStream) || !errors.Is(err, failed) || !errors.As(err, &told) || told.Operation != "read a file's bytes" {
			t.Fatalf("a reader that fails %s: %v", what, err)
		}
		// A file that was not read to its end has no list.
		if store.Count(StoredFileList) != 0 {
			t.Fatalf("a reader that fails %s: a file was kept", what)
		}
	}
}

func TestAReaderThatNeverGivesAByteIsGivenUp(t *testing.T) {
	for _, cutting := range []Cutting{smallPieces(t), FastCDCV1} {
		store := NewMemoryStore()
		reader := &idleReader{}
		_, err := NewBuilder(store, cutting).File(reader)
		if !errors.Is(err, ErrStream) || !errors.Is(err, io.ErrNoProgress) {
			t.Fatalf("%s: a reader that never gives a byte: %v", cutting.Tag(), err)
		}
		if reader.calls > 200 || store.Count(StoredFileList) != 0 {
			t.Fatalf("%s: the reader was asked %d times", cutting.Tag(), reader.calls)
		}
	}
	// The same after some bytes: what came is not kept as a file.
	store := NewMemoryStore()
	thenIdle := io.MultiReader(bytes.NewReader(corpusBytes("readers-idle", 100)), &idleReader{})
	if _, err := NewBuilder(store, smallPieces(t)).File(thenIdle); !errors.Is(err, io.ErrNoProgress) || store.Count(StoredFileList) != 0 {
		t.Fatalf("a reader that stops giving bytes: %v", err)
	}
}
