package scionic

import (
	"encoding/binary"
	"io"
	"math/bits"
	"unicode/utf8"
)

// What a receiver asks, and the frames that answer it.
//
// A receiver asks from the top down: for the record of a root, for a
// folder's entries, for a file's list, and for pieces. It asks for pieces by
// the file's hash and their numbers in it, or for one piece by its hash.
//
// An answer is one frame, or for pieces one frame for each piece. A frame
// never says which hash it belongs under: the receiver works the hash out
// from the frame's bytes, and holds it against a hash it already trusts.
//
// A request and a frame each start with one byte that says what they are.
// Numbers are big-endian.

// FrameKind is the first byte of a frame, which says what the frame is.
type FrameKind uint8

const (
	// FrameRecord is a tree's record. The frame is the record itself, whose
	// first byte this is.
	FrameRecord FrameKind = recordMark
	// FrameFileList is a file's whole list.
	FrameFileList FrameKind = 0x21
	// FrameFolderList is a folder's whole list.
	FrameFolderList FrameKind = 0x22
	// FrameFileRun is a run of a file's list, with the nodes that stand for
	// the rest.
	FrameFileRun FrameKind = 0x23
	// FrameFolderRun is a run of a folder's list, with the nodes that stand
	// for the rest.
	FrameFolderRun FrameKind = 0x24
	// FramePiece is a piece's bytes.
	FramePiece FrameKind = 0x25
	// FrameHave says which pieces of a file a peer holds.
	FrameHave FrameKind = 0x26
	// FrameRefused: the peer does not answer, and why.
	FrameRefused FrameKind = 0x27
)

// RequestKind is the first byte of a request, which says what is asked for.
type RequestKind uint8

const (
	// AskRecord asks for the record of a root.
	AskRecord RequestKind = 0x30
	// AskFolder asks for all of a folder's entries.
	AskFolder RequestKind = 0x31
	// AskFolderRun asks for Count of a folder's entries, from place Start.
	AskFolderRun RequestKind = 0x32
	// AskFolderName asks what is under one name in a folder, or that the
	// name is not there.
	AskFolderName RequestKind = 0x33
	// AskFile asks for all of a file's list.
	AskFile RequestKind = 0x34
	// AskFileRun asks for Count items of a file's list, from piece number
	// Start.
	AskFileRun RequestKind = 0x35
	// AskFileBytes asks for the items of a file's list that hold Length
	// bytes from Offset.
	AskFileBytes RequestKind = 0x36
	// AskPieces asks for Count pieces of a file, from piece number Start.
	AskPieces RequestKind = 0x37
	// AskPiece asks for one piece, by its hash.
	AskPiece RequestKind = 0x38
	// AskHave asks which pieces of a file the peer holds.
	AskHave RequestKind = 0x39
)

// BundleMagic is what starts a bundle: a tree written out as one stream of
// frames.
const BundleMagic = "SCIONIC\x01"

// MaxRequestBytes is the most bytes a request may take. The longest one asks
// for a name in a folder: its first byte, the folder's hash, and the longest
// name with its length.
const MaxRequestBytes = 35 + MaxNameBytes

// readStepBytes is the most bytes that are set aside for a frame before they
// have come. A stream that claims a long frame and then sends nothing costs
// its reader no more than this.
const readStepBytes = 1 << 20

// Request is what a receiver asks a peer for. Kind says what, and Hash says
// of which thing: the root, the folder, the file or the piece. The other
// fields are used by the kinds that their names are given with above.
type Request struct {
	Kind RequestKind
	Hash Hash
	// Start and Count: for AskFolderRun, AskFileRun and AskPieces.
	Start uint64
	Count uint32
	// Name: for AskFolderName.
	Name string
	// Offset and Length: for AskFileBytes.
	Offset uint64
	Length uint64
}

// RequestRecord asks for the record of a root.
func RequestRecord(root Root) Request {
	return Request{Kind: AskRecord, Hash: root.Hash()}
}

// RequestFolder asks for all of a folder's entries.
func RequestFolder(folder Hash) Request {
	return Request{Kind: AskFolder, Hash: folder}
}

// RequestFolderRun asks for count of a folder's entries, from place start.
func RequestFolderRun(folder Hash, start uint64, count uint32) Request {
	return Request{Kind: AskFolderRun, Hash: folder, Start: start, Count: count}
}

// RequestFolderName asks what is under one name in a folder, or that the
// name is not there.
func RequestFolderName(folder Hash, name string) Request {
	return Request{Kind: AskFolderName, Hash: folder, Name: name}
}

// RequestFile asks for all of a file's list.
func RequestFile(file Hash) Request {
	return Request{Kind: AskFile, Hash: file}
}

// RequestFileRun asks for count items of a file's list, from piece number
// start.
func RequestFileRun(file Hash, start uint64, count uint32) Request {
	return Request{Kind: AskFileRun, Hash: file, Start: start, Count: count}
}

// RequestFileBytes asks for the items of a file's list that hold length
// bytes from offset.
func RequestFileBytes(file Hash, offset, length uint64) Request {
	return Request{Kind: AskFileBytes, Hash: file, Offset: offset, Length: length}
}

// RequestPieces asks for count pieces of a file, from piece number start.
func RequestPieces(file Hash, start uint64, count uint32) Request {
	return Request{Kind: AskPieces, Hash: file, Start: start, Count: count}
}

// RequestPiece asks for one piece, by its hash.
func RequestPiece(piece Hash) Request {
	return Request{Kind: AskPiece, Hash: piece}
}

// RequestHave asks which pieces of a file the peer holds.
func RequestHave(file Hash) Request {
	return Request{Kind: AskHave, Hash: file}
}

// Encode gives the request's bytes.
func (r Request) Encode() []byte {
	out := make([]byte, 0, 49)
	out = append(out, byte(r.Kind))
	out = append(out, r.Hash[:]...)
	switch r.Kind {
	case AskFolderRun, AskFileRun, AskPieces:
		out = binary.BigEndian.AppendUint64(out, r.Start)
		out = binary.BigEndian.AppendUint32(out, r.Count)
	case AskFolderName:
		out = binary.BigEndian.AppendUint16(out, uint16(len(r.Name)))
		out = append(out, r.Name...)
	case AskFileBytes:
		out = binary.BigEndian.AppendUint64(out, r.Offset)
		out = binary.BigEndian.AppendUint64(out, r.Length)
	}
	return out
}

// DecodeRequest reads a request. One that asks for nothing, or for a name
// that no folder can hold, is refused here.
func DecodeRequest(bytes []byte) (Request, error) {
	r := reader{bytes: bytes, what: "a request"}
	request := Request{Kind: RequestKind(r.u8()), Hash: r.hash()}
	if r.err != nil {
		return Request{}, r.err
	}
	switch request.Kind {
	case AskRecord, AskFolder, AskFile, AskPiece, AskHave:
	case AskFolderRun, AskFileRun, AskPieces:
		request.Start = r.u64()
		request.Count = r.u32()
		if r.err != nil {
			return Request{}, r.err
		}
		if request.Count == 0 || request.Start >= MaxListItems {
			return Request{}, invalid("a request asks for nothing, or beyond any list")
		}
	case AskFolderName:
		length := int(r.u16())
		name := r.take(length)
		if r.err != nil {
			return Request{}, r.err
		}
		if !utf8.Valid(name) {
			return Request{}, invalid("a name is not UTF-8")
		}
		request.Name = string(name)
		if err := CheckName(request.Name); err != nil {
			return Request{}, err
		}
	case AskFileBytes:
		request.Offset = r.u64()
		request.Length = r.u64()
		if r.err != nil {
			return Request{}, r.err
		}
		if request.Length == 0 || request.Offset+request.Length < request.Offset {
			return Request{}, invalid("a request asks for no bytes, or beyond any file")
		}
	default:
		return Request{}, invalid("a request of an unknown kind")
	}
	if err := r.finish(); err != nil {
		return Request{}, err
	}
	return request, nil
}

// Refusal is why a peer does not answer.
type Refusal uint8

const (
	// RefusalNotHeld: the peer does not hold what was asked for.
	RefusalNotHeld Refusal = 1
	// RefusalOutOfRange: what was asked for lies outside the list or the
	// file.
	RefusalOutOfRange Refusal = 2
)

func (r Refusal) String() string {
	switch r {
	case RefusalNotHeld:
		return "not held"
	case RefusalOutOfRange:
		return "out of range"
	}
	return "a refusal of an unknown kind"
}

// HaveMap says which pieces of a file a peer holds: one bit a piece, in the
// file's order. A peer says this about itself, and nothing proves it.
type HaveMap struct {
	count uint64
	bits  []byte
}

// NewHaveMap makes a map for a file of count pieces, with none held.
func NewHaveMap(count uint64) (*HaveMap, error) {
	if count > MaxListItems {
		return nil, invalid("a map for more pieces than a file can have")
	}
	length := (count + 7) / 8
	if length > uint64(maxInt) {
		return nil, limitExceeded("a map of held pieces", uint64(maxInt), length)
	}
	return &HaveMap{count: count, bits: make([]byte, length)}, nil
}

// Count is how many pieces the file has.
func (m *HaveMap) Count() uint64 {
	return m.count
}

// Get reports whether piece index is held. A piece beyond the count is not.
func (m *HaveMap) Get(index uint64) bool {
	return index < m.count && m.bits[index/8]&(0x80>>(index%8)) != 0
}

// Set marks piece index as held.
func (m *HaveMap) Set(index uint64) error {
	if index >= m.count {
		return invalid("a piece beyond the end of the file")
	}
	m.bits[index/8] |= 0x80 >> (index % 8)
	return nil
}

// Held is how many pieces are held.
func (m *HaveMap) Held() uint64 {
	var held uint64
	for _, value := range m.bits {
		held += uint64(bits.OnesCount8(value))
	}
	return held
}

// Encode gives the map's bytes.
func (m *HaveMap) Encode() []byte {
	out := make([]byte, 0, 8+len(m.bits))
	out = binary.BigEndian.AppendUint64(out, m.count)
	return append(out, m.bits...)
}

// DecodeHaveMap reads a map. It has one bit for each piece and no more, and
// the bits left over in its last byte are zero.
func DecodeHaveMap(bytes []byte) (*HaveMap, error) {
	r := reader{bytes: bytes, what: "a map of held pieces"}
	count := r.u64()
	if r.err != nil {
		return nil, r.err
	}
	if count > MaxListItems || uint64(r.remaining()) != (count+7)/8 {
		return nil, invalid("a map of held pieces does not fit its count")
	}
	held := append([]byte(nil), r.take(r.remaining())...)
	spare := (8 - count%8) % 8
	if len(held) > 0 && held[len(held)-1]&(1<<spare-1) != 0 {
		return nil, invalid("a map of held pieces has bits past its count")
	}
	return &HaveMap{count: count, bits: held}, nil
}

// Frame is a frame, read. Kind says what it is, and one of the other fields
// holds what it carries.
type Frame struct {
	Kind FrameKind
	// Bytes is the whole frame for a record, the list for a file's or a
	// folder's whole list, and the piece's bytes for a piece. It is a part
	// of the bytes that were read, and not a copy.
	Bytes []byte
	// FileRun: for FrameFileRun.
	FileRun *FileRun
	// FolderRun: for FrameFolderRun.
	FolderRun *FolderRun
	// Have: for FrameHave.
	Have *HaveMap
	// Refusal: for FrameRefused.
	Refusal Refusal
}

// DecodeFrame reads a frame. This says what kind of frame it is and that it
// is well formed. It does not say that the frame is true: that is checked
// against a hash the receiver trusts.
func DecodeFrame(bytes []byte) (Frame, error) {
	if len(bytes) == 0 {
		return Frame{}, invalid("a frame is empty")
	}
	kind, body := FrameKind(bytes[0]), bytes[1:]
	switch kind {
	case FrameRecord:
		return Frame{Kind: kind, Bytes: bytes}, nil
	case FrameFileList, FrameFolderList:
		return Frame{Kind: kind, Bytes: body}, nil
	case FrameFileRun:
		run, err := DecodeFileRun(body)
		if err != nil {
			return Frame{}, err
		}
		return Frame{Kind: kind, FileRun: run}, nil
	case FrameFolderRun:
		run, err := DecodeFolderRun(body)
		if err != nil {
			return Frame{}, err
		}
		return Frame{Kind: kind, FolderRun: run}, nil
	case FramePiece:
		if len(body) == 0 || len(body) > MaxPieceBytes {
			return Frame{}, invalid("a piece does not hold 1 byte to 8 MiB")
		}
		return Frame{Kind: kind, Bytes: body}, nil
	case FrameHave:
		held, err := DecodeHaveMap(body)
		if err != nil {
			return Frame{}, err
		}
		return Frame{Kind: kind, Have: held}, nil
	case FrameRefused:
		if len(body) == 1 && (Refusal(body[0]) == RefusalNotHeld || Refusal(body[0]) == RefusalOutOfRange) {
			return Frame{Kind: kind, Refusal: Refusal(body[0])}, nil
		}
		return Frame{}, invalid("a refusal of an unknown kind")
	}
	return Frame{}, invalid("a frame of an unknown kind")
}

func framed(kind FrameKind, body []byte) []byte {
	frame := make([]byte, 0, 1+len(body))
	frame = append(frame, byte(kind))
	return append(frame, body...)
}

// FileListFrame is the frame for a file's whole list.
func FileListFrame(list []byte) []byte {
	return framed(FrameFileList, list)
}

// FolderListFrame is the frame for a folder's whole list.
func FolderListFrame(list []byte) []byte {
	return framed(FrameFolderList, list)
}

// FileRunFrame is the frame for a run of a file's list.
func FileRunFrame(run *FileRun) []byte {
	return framed(FrameFileRun, run.Encode())
}

// FolderRunFrame is the frame for a run of a folder's list.
func FolderRunFrame(run *FolderRun) []byte {
	return framed(FrameFolderRun, run.Encode())
}

// PieceFrame is the frame for a piece's bytes.
func PieceFrame(piece []byte) []byte {
	return framed(FramePiece, piece)
}

// HaveFrame is the frame for a map of held pieces.
func HaveFrame(held *HaveMap) []byte {
	return framed(FrameHave, held.Encode())
}

// RefusedFrame is the frame that says why a peer does not answer.
func RefusedFrame(why Refusal) []byte {
	return []byte{byte(FrameRefused), byte(why)}
}

// WriteFrame writes one frame to a stream: its length as four bytes, then
// the frame.
func WriteFrame(out io.Writer, frame []byte) error {
	if uint64(len(frame)) > 0xFFFF_FFFF {
		return limitExceeded("a frame", 0xFFFF_FFFF, uint64(len(frame)))
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(frame)))
	if err := writeAll(out, header[:]); err != nil {
		return streamError("write a frame", err)
	}
	if err := writeAll(out, frame); err != nil {
		return streamError("write a frame", err)
	}
	return nil
}

// writeEnd writes a length of zero to a stream: the end of an answer.
func writeEnd(out io.Writer) error {
	if err := writeAll(out, []byte{0, 0, 0, 0}); err != nil {
		return streamError("write a frame", err)
	}
	return nil
}

// nextOnStream is what comes next on a stream of frames.
type nextOnStream uint8

const (
	// nextFrame: a frame.
	nextFrame nextOnStream = iota
	// nextEnd: a length of zero. Where answers are sent, it ends one
	// answer.
	nextEnd
	// nextClosed: the stream has ended, between two frames.
	nextClosed
)

// readNext reads what comes next on a stream. A frame longer than limit is
// refused before any of it is read, and a stream that ends inside a frame is
// an error. A frame's bytes are set aside as they come, a megabyte at a
// time.
//
// The stream is read as a steady reader gives it. A stream that fails while
// it gives a frame's last bytes has failed: the frame is not handed on as if
// nothing had happened.
func readNext(input *steady, limit int) (nextOnStream, []byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(input, header[:]); err != nil {
		switch err {
		// io.EOF itself, and not an error that wraps it: a reader says that
		// it has ended with io.EOF, and anything else is a failure.
		case io.EOF:
			return nextClosed, nil, nil
		case io.ErrUnexpectedEOF:
			return 0, nil, invalid("a stream ends inside a frame")
		}
		return 0, nil, streamError("read a frame", err)
	}
	length := uint64(binary.BigEndian.Uint32(header[:]))
	if length == 0 {
		return nextEnd, nil, nil
	}
	if limit < 0 || length > uint64(limit) {
		return 0, nil, limitExceeded("a frame", uint64(max(limit, 0)), length)
	}
	var frame []byte
	for uint64(len(frame)) < length {
		held := len(frame)
		more := int(min(length-uint64(held), readStepBytes))
		frame = append(frame, make([]byte, more)...)
		if _, err := io.ReadFull(input, frame[held:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return 0, nil, invalid("a stream ends inside a frame")
			}
			return 0, nil, streamError("read a frame", err)
		}
	}
	if err := input.failed(); err != nil {
		return 0, nil, streamError("read a frame", err)
	}
	return nextFrame, frame, nil
}

// ReadFrame reads one frame from a stream. When the stream has ended
// between two frames, it gives io.EOF. A frame longer than limit is refused
// before any of it is read, and a stream that ends inside a frame is an
// error.
func ReadFrame(input io.Reader, limit int) ([]byte, error) {
	next, frame, err := readNext(steadied(input), limit)
	if err != nil {
		return nil, err
	}
	switch next {
	case nextEnd:
		return nil, invalid("a frame is empty")
	case nextClosed:
		return nil, io.EOF
	}
	return frame, nil
}
