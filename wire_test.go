package scionic

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"runtime"
	"slices"
	"testing"
)

// What goes between two peers: requests, frames, and frames on a stream.

func everyRequest() []Request {
	hash := HashOf([]byte("x"))
	return []Request{
		RequestRecord(Root(hash)),
		RequestFolder(hash),
		RequestFolderRun(hash, 3, 9),
		RequestFolderName(hash, "model.safetensors"),
		RequestFile(hash),
		RequestFileRun(hash, 100, 100),
		RequestFileBytes(hash, 500<<30, 1<<30),
		RequestPieces(hash, 100, 101),
		RequestPiece(hash),
		RequestHave(hash),
	}
}

func TestEveryRequestReadsBackAndIsReadInOneWayOnly(t *testing.T) {
	var tags []byte
	for _, request := range everyRequest() {
		encoded := request.Encode()
		if read, err := DecodeRequest(encoded); err != nil || read != request || !bytes.Equal(read.Encode(), encoded) {
			t.Fatalf("%+v does not read back: %v", request, err)
		}
		for cut := range len(encoded) {
			_, err := DecodeRequest(encoded[:cut])
			refuses(t, fmt.Sprintf("%+v cut at %d", request, cut), err)
		}
		_, err := DecodeRequest(joined(encoded, []byte{0}))
		refuses(t, fmt.Sprintf("%+v with a byte after it", request), err)
		if len(encoded) > MaxRequestBytes {
			t.Fatalf("%+v takes %d bytes", request, len(encoded))
		}
		// Every byte of a request is part of what it asks: no two requests
		// read as one.
		for at := range encoded {
			changed := slices.Clone(encoded)
			changed[at] ^= 0x01
			if read, err := DecodeRequest(changed); err == nil && read == request {
				t.Fatalf("%+v with byte %d changed is read as the same request", request, at)
			}
		}
		tags = append(tags, encoded[0])
	}
	if !bytes.Equal(tags, []byte{0x30, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39}) {
		t.Fatalf("the requests start with % x", tags)
	}
	// "Pieces 100 to 200 of this file" is 45 bytes, whatever the file.
	if length := len(everyRequest()[7].Encode()); length != 45 {
		t.Fatal(length)
	}
	// The longest request there can be asks for the longest name.
	longest := RequestFolderName(HashOf([]byte("x")), string(bytes.Repeat([]byte{'n'}, MaxNameBytes)))
	if encoded := longest.Encode(); len(encoded) != MaxRequestBytes {
		t.Fatal(len(encoded))
	} else if read, err := DecodeRequest(encoded); err != nil || read != longest {
		t.Fatal(err)
	}
}

func TestARequestForNothingOrForANameNoFolderHoldsIsRefused(t *testing.T) {
	hash := HashOf([]byte("x"))
	run := func(tag byte, start uint64, count uint32) error {
		_, err := DecodeRequest(joined([]byte{tag}, hash[:], be64(start), be32(count)))
		return err
	}
	for _, tag := range []byte{0x32, 0x35, 0x37} {
		takes(t, fmt.Sprintf("request %#x for one item", tag), run(tag, 0, 1))
		takes(t, fmt.Sprintf("request %#x for the last place there can be", tag), run(tag, MaxListItems-1, 1))
		takes(t, fmt.Sprintf("request %#x for as many items as a number holds", tag), run(tag, 0, math.MaxUint32))
		refuses(t, fmt.Sprintf("request %#x for no items", tag), run(tag, 0, 0))
		refuses(t, fmt.Sprintf("request %#x beyond any list", tag), run(tag, MaxListItems, 1))
		refuses(t, fmt.Sprintf("request %#x at the widest place", tag), run(tag, math.MaxUint64, 1))
	}
	named := func(name string) error {
		_, err := DecodeRequest(joined([]byte{0x33}, hash[:], be16(len(name)), []byte(name)))
		return err
	}
	takes(t, "a request for a name", named("a"))
	for _, name := range []string{"", ".", "..", "a/b", "a\x00", "\xff", string(bytes.Repeat([]byte{'n'}, MaxNameBytes+1))} {
		refuses(t, fmt.Sprintf("a request for the name %q", name), named(name))
	}
	stretch := func(offset, length uint64) error {
		_, err := DecodeRequest(joined([]byte{0x36}, hash[:], be64(offset), be64(length)))
		return err
	}
	takes(t, "a request for one byte", stretch(0, 1))
	takes(t, "a request for the last byte a number reaches", stretch(math.MaxUint64-1, 1))
	refuses(t, "a request for no bytes", stretch(0, 0))
	refuses(t, "a request for bytes beyond any file", stretch(math.MaxUint64, 1))
	refuses(t, "a request for bytes that wrap around", stretch(2, math.MaxUint64))
	for _, tag := range []byte{0x00, 0x20, 0x2f, 0x3a, 0xff} {
		_, err := DecodeRequest(joined([]byte{tag}, hash[:]))
		refuses(t, fmt.Sprintf("a request of the unknown kind %#x", tag), err)
	}
}

func TestAMapOfHeldPiecesHasOneBitAPiece(t *testing.T) {
	held := must[*HaveMap](t)(NewHaveMap(11))
	if length := len(held.Encode()); length != 8+2 {
		t.Fatal(length)
	}
	for _, index := range []uint64{0, 3, 10} {
		takes(t, fmt.Sprintf("piece %d", index), held.Set(index))
	}
	refuses(t, "a piece after the last", held.Set(11))
	refuses(t, "a piece at the widest place", held.Set(math.MaxUint64))
	var set []uint64
	for index := range uint64(16) {
		if held.Get(index) {
			set = append(set, index)
		}
	}
	if held.Held() != 3 || held.Count() != 11 || !slices.Equal(set, []uint64{0, 3, 10}) || held.Get(math.MaxUint64) {
		t.Fatalf("%d pieces are held: %v", held.Held(), set)
	}
	encoded := held.Encode()
	if !bytes.Equal(encoded[8:], []byte{0b1001_0000, 0b0010_0000}) {
		t.Fatalf("the map is written % 08b", encoded[8:])
	}
	if read := must[*HaveMap](t)(DecodeHaveMap(encoded)); !bytes.Equal(read.Encode(), encoded) || read.Held() != 3 {
		t.Fatal("a map does not read back")
	}
	// A bit past the count, a byte too many and a byte too few.
	past := slices.Clone(encoded)
	past[9] |= 0b0001_0000
	_, err := DecodeHaveMap(past)
	refuses(t, "a bit past the count", err)
	_, err = DecodeHaveMap(joined(encoded, []byte{0}))
	refuses(t, "a byte too many", err)
	_, err = DecodeHaveMap(encoded[:9])
	refuses(t, "a byte too few", err)
	_, err = DecodeHaveMap(encoded[:7])
	refuses(t, "half a count", err)
	_, err = DecodeHaveMap(be64(MaxListItems + 1))
	refuses(t, "a map for more pieces than a file can have", err)
	_, err = NewHaveMap(MaxListItems + 1)
	refuses(t, "a new map for more pieces than a file can have", err)
	// A terabyte file's map is about 60 KB.
	if length := len(must[*HaveMap](t)(NewHaveMap(500_000)).Encode()); length != 8+62_500 {
		t.Fatal(length)
	}
	for _, count := range []uint64{0, 1, 7, 8, 9} {
		empty := must[*HaveMap](t)(NewHaveMap(count))
		if read := must[*HaveMap](t)(DecodeHaveMap(empty.Encode())); read.Count() != count || read.Held() != 0 {
			t.Fatalf("a map of %d pieces reads back as one of %d", count, read.Count())
		}
		// Every piece of it can be held, and then every bit is set.
		for index := range count {
			takes(t, "a piece", empty.Set(index))
		}
		if read := must[*HaveMap](t)(DecodeHaveMap(empty.Encode())); read.Held() != count {
			t.Fatalf("a full map of %d pieces holds %d", count, read.Held())
		}
	}
}

func TestFramesAreToldApartByTheirFirstByte(t *testing.T) {
	if frame := must[Frame](t)(DecodeFrame(PieceFrame([]byte("abc")))); frame.Kind != FramePiece || string(frame.Bytes) != "abc" {
		t.Fatalf("a piece: %+v", frame)
	}
	list := bytes.Repeat([]byte{7}, 36)
	if frame := must[Frame](t)(DecodeFrame(FileListFrame(list))); frame.Kind != FrameFileList || !bytes.Equal(frame.Bytes, list) {
		t.Fatalf("a file's list: %+v", frame)
	}
	if frame := must[Frame](t)(DecodeFrame(FolderListFrame(nil))); frame.Kind != FrameFolderList || len(frame.Bytes) != 0 {
		t.Fatalf("a folder's list: %+v", frame)
	}
	for _, why := range []Refusal{RefusalNotHeld, RefusalOutOfRange} {
		if frame := must[Frame](t)(DecodeFrame(RefusedFrame(why))); frame.Kind != FrameRefused || frame.Refusal != why {
			t.Fatalf("a refusal: %+v", frame)
		}
	}
	held := must[*HaveMap](t)(NewHaveMap(3))
	takes(t, "a piece", held.Set(1))
	if frame := must[Frame](t)(DecodeFrame(HaveFrame(held))); frame.Kind != FrameHave || !bytes.Equal(frame.Have.Encode(), held.Encode()) {
		t.Fatalf("a map of held pieces: %+v", frame)
	}
	if frame := must[Frame](t)(DecodeFrame([]byte{0x20, 1})); frame.Kind != FrameRecord || !bytes.Equal(frame.Bytes, []byte{0x20, 1}) {
		t.Fatalf("a record: %+v", frame)
	}
	fileRun := must[*FileRun](t)(must[*FileList](t)(NewFileList([]PieceRef{pieceOf(1, 4), pieceOf(2, 4), pieceOf(3, 4)})).Open(1, 2))
	if frame := must[Frame](t)(DecodeFrame(FileRunFrame(fileRun))); frame.Kind != FrameFileRun || !bytes.Equal(frame.FileRun.Encode(), fileRun.Encode()) {
		t.Fatalf("a run of a file's list: %+v", frame)
	}
	folderRun := must[*FolderRun](t)(folderNamed(t, "a", "b", "c").Open(1, 2))
	if frame := must[Frame](t)(DecodeFrame(FolderRunFrame(folderRun))); frame.Kind != FrameFolderRun || !bytes.Equal(frame.FolderRun.Encode(), folderRun.Encode()) {
		t.Fatalf("a run of a folder's list: %+v", frame)
	}
	// Every kind of frame has a first byte of its own.
	first := []byte{
		PieceFrame([]byte("abc"))[0], FileListFrame(list)[0], FolderListFrame(nil)[0], RefusedFrame(RefusalNotHeld)[0],
		HaveFrame(held)[0], FileRunFrame(fileRun)[0], FolderRunFrame(folderRun)[0], 0x20,
	}
	slices.Sort(first)
	if !bytes.Equal(first, []byte{0x20, 0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27}) {
		t.Fatalf("the frames start with % x", first)
	}
	for _, bad := range [][]byte{{}, {0x25}, {0x27}, {0x27, 0}, {0x27, 3}, {0x27, 1, 1}, {0x28}, {0x1f}, {0x23}, {0x24}, {0x26}, {0x30}, {0xff}} {
		_, err := DecodeFrame(bad)
		refuses(t, fmt.Sprintf("the frame % x", bad), err)
	}
	_, err := DecodeFrame(PieceFrame(make([]byte, MaxPieceBytes+1)))
	refuses(t, "a piece of more than 8 MiB", err)
	_, err = DecodeFrame(PieceFrame(make([]byte, MaxPieceBytes)))
	takes(t, "a piece of 8 MiB", err)
}

// failingReader gives an error of its own after its bytes.
type failingReader struct {
	content []byte
}

var errFailingReader = errors.New("this reader fails")

func (r *failingReader) Read(out []byte) (int, error) {
	if len(r.content) == 0 {
		return 0, errFailingReader
	}
	count := copy(out, r.content)
	r.content = r.content[count:]
	return count, nil
}

func TestFramesGoThroughAStreamWholeOrNotAtAll(t *testing.T) {
	var stream bytes.Buffer
	takes(t, "a frame", WriteFrame(&stream, []byte("one")))
	takes(t, "a frame", WriteFrame(&stream, []byte("three")))
	written := stream.Bytes()
	if !bytes.Equal(written, joined(be32(3), []byte("one"), be32(5), []byte("three"))) {
		t.Fatalf("the stream holds % x", written)
	}
	input := bytes.NewReader(written)
	for _, want := range []string{"one", "three"} {
		if frame := must[[]byte](t)(ReadFrame(input, 16)); string(frame) != want {
			t.Fatalf("%q was read as %q", want, frame)
		}
	}
	// Between two frames a stream may end, and that is told apart from
	// every failure.
	if _, err := ReadFrame(input, 16); err != io.EOF {
		t.Fatalf("the end of the stream: %v", err)
	}
	// A frame over the limit is refused before it is read.
	input = bytes.NewReader(written)
	if _, err := ReadFrame(input, 2); !limitExceededWith(err, 2, 3) || input.Len() != len(written)-4 {
		t.Fatalf("a frame over the limit: %v, with %d bytes left", err, input.Len())
	}
	if _, err := ReadFrame(bytes.NewReader(written), -1); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("a limit below nothing: %v", err)
	}
	if frame := must[[]byte](t)(ReadFrame(bytes.NewReader(written), 3)); string(frame) != "one" {
		t.Fatal("a frame of exactly the limit was refused")
	}
	// A stream that ends inside a length or inside a frame.
	for _, cut := range []int{1, 3, 5, 6} {
		_, err := ReadFrame(bytes.NewReader(written[:cut]), 16)
		if err == nil || err == io.EOF || !errors.Is(err, ErrInvalidData) {
			t.Fatalf("a stream cut at %d: %v", cut, err)
		}
	}
	// A frame of no bytes is no frame.
	if _, err := ReadFrame(bytes.NewReader([]byte{0, 0, 0, 0}), 16); err == nil || err == io.EOF {
		t.Fatalf("a frame of no bytes: %v", err)
	}
	// A stream that fails is told as it failed, wherever it fails.
	for _, cut := range []int{0, 2, 4, 6} {
		_, err := ReadFrame(&failingReader{content: written[:cut]}, 16)
		if !errors.Is(err, ErrStream) || !errors.Is(err, errFailingReader) {
			t.Fatalf("a stream that fails after %d bytes: %v", cut, err)
		}
	}
	for _, left := range []int{0, 2, 4, 6} {
		err := WriteFrame(&brokenWriter{left: left}, []byte("three"))
		if !errors.Is(err, ErrStream) || !errors.Is(err, errBrokenWriter) {
			t.Fatalf("a writer that fails after %d bytes: %v", left, err)
		}
	}
	// A stream that claims a long frame and sends none of it costs its
	// reader a step or two of memory, however long the frame is said to be.
	claimed := bytes.NewReader(joined(be32(1<<30), make([]byte, 10)))
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := ReadFrame(claimed, 1<<30)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrInvalidData) {
		t.Fatalf("a frame that never comes: %v", err)
	}
	if set := after.TotalAlloc - before.TotalAlloc; set > 8*readStepBytes {
		t.Fatalf("a frame of a gigabyte that never came had %d bytes set aside for it", set)
	}
}
