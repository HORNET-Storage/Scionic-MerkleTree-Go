package scionic

import (
	"crypto/sha256"
	"encoding/binary"
)

// fastcdc-v1: cutting a file into pieces by its content (FastCDC, Xia et al.
// 2016). It is the way a tree's files are cut. The Rust library's code and
// the vectors under spec/fastcdc-vectors define its exact bytes, and this
// file is held to them.
//
// Every constant here is frozen as fastcdc-v1. A change to a size, a mask, a
// gear entry or the boundary loop would be another way of cutting under
// another name, never a change to this one: two builders give the same file
// the same hash only when both cut at the same bytes.
const (
	// FastCDCMinSize is the floor below which no boundary is looked for: the
	// first FastCDCMinSize bytes of a piece are not hashed at all. Every piece
	// but a file's last is therefore at least FastCDCMinSize + 1 bytes.
	FastCDCMinSize = 512 * 1024
	// FastCDCTargetSize is where the strict mask gives way to the loose one,
	// which squeezes the sizes of pieces toward this size.
	FastCDCTargetSize = 2048 * 1024
	// FastCDCMaxSize is where a piece is cut whatever its bytes are.
	FastCDCMaxSize = 8192 * 1024
	// FastCDCStrictMask selects the top 23 bits of the rolling hash. It
	// judges positions before FastCDCTargetSize.
	FastCDCStrictMask uint64 = 0xFFFF_FE00_0000_0000
	// FastCDCLooseMask selects the top 19 bits. It judges positions at or
	// after FastCDCTargetSize.
	FastCDCLooseMask uint64 = 0xFFFF_E000_0000_0000
	// FastCDCGearSeedPrefix is what the gear table is made from: entry i is
	// the first 8 bytes, big-endian, of SHA-256(prefix + the byte i).
	FastCDCGearSeedPrefix = "scionic-fastcdc-v1:gear:"
)

// fastcdcGear is the frozen fastcdc-v1 gear table, made from
// FastCDCGearSeedPrefix when the package starts.
var fastcdcGear = func() (gear [256]uint64) {
	message := append([]byte(FastCDCGearSeedPrefix), 0)
	for value := range gear {
		message[len(message)-1] = byte(value)
		digest := sha256.Sum256(message)
		gear[value] = binary.BigEndian.Uint64(digest[:8])
	}
	return gear
}()

// fastcdcBoundary gives the length of the next piece cut from data. The
// caller makes sure that data is at least FastCDCMaxSize bytes long, or ends
// where the file ends. Then the answer is final: where a piece ends never
// depends on bytes past the forced maximum.
//
// The rule, one byte a step: the rolling hash starts at zero for every
// piece, bytes [0, FastCDCMinSize) are passed over without hashing, each
// later byte b turns the hash h into (h << 1) + G[b] in wrapping 64-bit
// arithmetic, and the first byte index i whose hash has no bit of the mask
// set cuts after that byte, at length i + 1. The strict mask judges indexes
// below FastCDCTargetSize, and the loose mask the ones from it on. This
// function gives exactly that, with four hashes at work side by side. The
// tests hold it against the one-byte loop.
func fastcdcBoundary(data []byte) int {
	length := len(data)
	if length <= FastCDCMinSize {
		return length
	}
	limit := min(length, FastCDCMaxSize)
	normal := min(FastCDCTargetSize, limit)
	var hash uint64
	if cut, found := fastcdcScan(data, FastCDCMinSize, normal, FastCDCStrictMask, &hash); found {
		return cut
	}
	if cut, found := fastcdcScan(data, normal, limit, FastCDCLooseMask, &hash); found {
		return cut
	}
	return limit
}

const (
	// gearWindow is how many bytes a hash remembers. Every step shifts it
	// one bit to the left, so a byte has left it for good 64 steps later.
	gearWindow = 64
	// gearStride is how many bytes each of the four hashes rolls over in a
	// round.
	gearStride = 4096
)

// fastcdcScan rolls the hash over data[from:to], and gives the length that
// the first matching byte cuts at. Without a match, the hash is left as it
// is after byte to-1.
//
// One hash can take a byte only when it is done with the byte before, and
// that wait is what bounds a plain loop. So four stretches that follow one
// another are rolled over side by side, a byte of each in turn. The first
// hash carries on from where it was. Each of the others starts 64 bytes
// before its own stretch, which is all a hash remembers, so it holds what a
// single hash would hold there. When any of the four matches, the first one
// goes on alone to the first match in the order of the bytes.
//
// from is at least FastCDCMinSize, where hashing begins: a stretch that is
// not the first one always has 64 hashed bytes before it.
func fastcdcScan(data []byte, from, to int, mask uint64, state *uint64) (int, bool) {
	gear := &fastcdcGear

	hash := *state
	at := from
	for to-at >= 4*gearStride {
		first := (*[gearStride]byte)(data[at:])
		second := (*[gearStride]byte)(data[at+gearStride:])
		third := (*[gearStride]byte)(data[at+2*gearStride:])
		fourth := (*[gearStride]byte)(data[at+3*gearStride:])
		h0 := hash
		var h1, h2, h3 uint64
		for _, value := range first[gearStride-gearWindow:] {
			h1 = h1<<1 + gear[value]
		}
		for _, value := range second[gearStride-gearWindow:] {
			h2 = h2<<1 + gear[value]
		}
		for _, value := range third[gearStride-gearWindow:] {
			h3 = h3<<1 + gear[value]
		}
		steps := 0
		for step := 0; step < gearStride; step++ {
			h0 = h0<<1 + gear[first[step]]
			h1 = h1<<1 + gear[second[step]]
			h2 = h2<<1 + gear[third[step]]
			h3 = h3<<1 + gear[fourth[step]]
			if h0&mask == 0 || h1&mask == 0 || h2&mask == 0 || h3&mask == 0 {
				steps = step + 1
				break
			}
		}
		if steps != 0 {
			if h0&mask == 0 {
				return at + steps, true
			}
			// A later stretch matched. An earlier byte may match too, so
			// the first hash goes on alone, and stops at the first match
			// there is.
			hash = h0
			at += steps
			break
		}
		hash = h3
		at += 4 * gearStride
	}
	for _, value := range data[at:to] {
		hash = hash<<1 + gear[value]
		at++
		if hash&mask == 0 {
			return at, true
		}
	}
	*state = hash
	return 0, false
}

// fastcdcBoundaryPlain is the rule as it is written down, one byte a step.
// The tests hold fastcdcBoundary to it.
func fastcdcBoundaryPlain(data []byte) int {
	length := len(data)
	if length <= FastCDCMinSize {
		return length
	}
	limit := min(length, FastCDCMaxSize)
	normal := min(FastCDCTargetSize, limit)
	var hash uint64
	for at := FastCDCMinSize; at < normal; at++ {
		hash = hash<<1 + fastcdcGear[data[at]]
		if hash&FastCDCStrictMask == 0 {
			return at + 1
		}
	}
	for at := normal; at < limit; at++ {
		hash = hash<<1 + fastcdcGear[data[at]]
		if hash&FastCDCLooseMask == 0 {
			return at + 1
		}
	}
	return limit
}

// Cut cuts data into pieces. The pieces are parts of data, in order, and
// nothing is copied. No bytes give no pieces.
//
// A caller that holds a file in memory cuts it with this, so that every
// builder cuts the same bytes at the same places.
func (c Cutting) Cut(data []byte) [][]byte {
	if size, fixed := c.Fixed(); fixed {
		pieces := make([][]byte, 0, len(data)/int(size)+1)
		for len(data) > 0 {
			cut := min(len(data), int(size))
			pieces = append(pieces, data[:cut:cut])
			data = data[cut:]
		}
		return pieces
	}
	pieces := make([][]byte, 0, len(data)/FastCDCTargetSize+1)
	for len(data) > 0 {
		cut := fastcdcBoundary(data)
		pieces = append(pieces, data[:cut:cut])
		data = data[cut:]
	}
	return pieces
}

const (
	// windowStart is what a cutter's window holds when it starts on its
	// first stream. The window grows only as a stream turns out to be
	// longer, so a small file never costs a large window.
	windowStart = 64 * 1024
	// fixedPiecesAtATime is the most pieces of a fixed size that are handed
	// on at a time.
	fixedPiecesAtATime = 1024
)

// streamCutter cuts a stream into pieces, holding a window of it and no
// more. One cutter serves many files, one after another, with the same
// window.
type streamCutter struct {
	window []byte
}

// grow makes the window longer: twice as long, and never longer than room.
func (c *streamCutter) grow(room int) {
	length := min(max(len(c.window)*2, windowStart), room)
	if length > len(c.window) {
		longer := make([]byte, length)
		copy(longer, c.window)
		c.window = longer
	}
}

// cut reads with read until it gives no more bytes, and hands the pieces to
// onPieces in order: each time, all the pieces that the window holds whole.
// The pieces are lent only for the call.
//
// room is the most bytes the window may hold. It always holds one piece of
// the longest kind; with more room, more pieces are handed on at a time, and
// fewer bytes are moved to make room for the next ones.
//
// read gives how many bytes it put into the buffer, and zero when the
// stream has ended.
//
// The windowing rule: where a piece ends is settled only once the window
// holds FastCDCMaxSize bytes from the piece's start, or the input has
// ended. That is what makes cutting a stream the same as cutting the whole
// of it at once.
func (c *streamCutter) cut(
	cutting Cutting,
	room int,
	read func(buffer []byte) (int, error),
	onPieces func(pieces [][]byte) error,
) error {
	longest := FastCDCMaxSize
	size, fixed := cutting.Fixed()
	if fixed {
		longest = int(size)
		most := longest * fixedPiecesAtATime
		room = max(min(room, most), longest)
	} else {
		room = max(room, FastCDCMaxSize)
	}
	filled := 0
	ended := false
	var pieces [][]byte
	for {
		for !ended && filled < room {
			if filled == len(c.window) {
				c.grow(room)
			}
			end := min(len(c.window), room)
			count, err := read(c.window[filled:end])
			if err != nil {
				return err
			}
			if count < 0 || count > end-filled {
				return invariant("a reader gave more bytes than it was asked for")
			}
			if count == 0 {
				ended = true
			} else {
				filled += count
			}
		}
		window := c.window[:filled]
		pieces = pieces[:0]
		start := 0
		for start < filled {
			rest := window[start:]
			if !ended && len(rest) < longest {
				break // not settled: fill the window again first
			}
			cut := min(len(rest), longest)
			if !fixed {
				cut = fastcdcBoundary(rest)
			}
			pieces = append(pieces, rest[:cut:cut])
			start += cut
		}
		if len(pieces) != 0 {
			if err := onPieces(pieces); err != nil {
				return err
			}
		}
		copy(c.window, window[start:])
		filled -= start
		if ended && filled == 0 {
			return nil
		}
	}
}
