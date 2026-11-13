package scionic

import (
	"crypto/sha256"
	"strings"
)

const (
	base32Alphabet = "abcdefghijklmnopqrstuvwxyz234567"
	hexAlphabet    = "0123456789abcdef"
)

// base32Lower writes bytes as lowercase base32 with no padding.
func base32Lower(input []byte) string {
	var output strings.Builder
	output.Grow((len(input)*8 + 4) / 5)
	var accumulator uint32
	var bits uint
	for _, value := range input {
		accumulator = accumulator<<8 | uint32(value)
		bits += 8
		for bits >= 5 {
			bits -= 5
			output.WriteByte(base32Alphabet[(accumulator>>bits)&0x1f])
		}
	}
	if bits != 0 {
		output.WriteByte(base32Alphabet[(accumulator<<(5-bits))&0x1f])
	}
	return output.String()
}

// base32LowerDecode reads lowercase base32 with no padding. A letter outside
// the alphabet is an error, and so are bits left over that are not zero.
func base32LowerDecode(input string) ([]byte, error) {
	output := make([]byte, 0, len(input)*5/8)
	var accumulator uint32
	var bits uint
	for at := 0; at < len(input); at++ {
		letter := input[at]
		var value byte
		switch {
		case letter >= 'a' && letter <= 'z':
			value = letter - 'a'
		case letter >= '2' && letter <= '7':
			value = letter - '2' + 26
		default:
			return nil, &Error{kind: kindInvalidBase32}
		}
		accumulator = accumulator<<5 | uint32(value)
		bits += 5
		if bits >= 8 {
			bits -= 8
			output = append(output, byte(accumulator>>bits))
			accumulator &= 1<<bits - 1
		}
	}
	if bits != 0 && accumulator != 0 {
		return nil, &Error{kind: kindInvalidBase32}
	}
	return output, nil
}

func hexLower(input []byte) string {
	output := make([]byte, 0, len(input)*2)
	for _, value := range input {
		output = append(output, hexAlphabet[value>>4], hexAlphabet[value&0x0f])
	}
	return string(output)
}

// hexLowerDecode reads lowercase hex into output, which it must fill exactly.
func hexLowerDecode(input string, output []byte) error {
	if len(input) != len(output)*2 {
		return &Error{kind: kindInvalidHex}
	}
	for at := range output {
		high, ok := hexValue(input[2*at])
		low, alsoOK := hexValue(input[2*at+1])
		if !ok || !alsoOK {
			return &Error{kind: kindInvalidHex}
		}
		output[at] = high<<4 | low
	}
	return nil
}

func hexValue(letter byte) (byte, bool) {
	switch {
	case letter >= '0' && letter <= '9':
		return letter - '0', true
	case letter >= 'a' && letter <= 'f':
		return letter - 'a' + 10, true
	}
	return 0, false
}

// Hash is a SHA-256 hash: of a chunk's bytes, or of the bytes that describe
// a file, a folder or a tree.
type Hash [32]byte

// HashOf is the SHA-256 of bytes.
func HashOf(bytes []byte) Hash {
	return sha256.Sum256(bytes)
}

// HashFromSlice reads a hash from exactly 32 bytes.
func HashFromSlice(bytes []byte) (Hash, error) {
	var hash Hash
	if len(bytes) != len(hash) {
		return hash, invalid("a hash is not 32 bytes")
	}
	copy(hash[:], bytes)
	return hash, nil
}

// HashFromHex reads 64 lowercase hex digits.
func HashFromHex(text string) (Hash, error) {
	var hash Hash
	err := hexLowerDecode(text, hash[:])
	return hash, err
}

// Hex is the hash as 64 lowercase hex digits.
func (h Hash) Hex() string {
	return hexLower(h[:])
}

// String is the hash as 64 lowercase hex digits.
func (h Hash) String() string {
	return h.Hex()
}

// IsZero reports whether the hash is all zero, which is what an empty list
// has in place of a hash.
func (h Hash) IsZero() bool {
	return h == Hash{}
}

// rootPrefix is what a root's text form starts with, before the hash: CID
// version 1, raw content, SHA-256, 32 bytes.
var rootPrefix = [4]byte{0x01, 0x55, 0x12, 0x20}

// Root is the root of a tree: the hash of its record. It is what a tree is
// known by.
//
// Written out, a root is a CID that begins "bafkrei".
type Root Hash

// RootOfRecord is the root of the tree whose record is record.
func RootOfRecord(record []byte) Root {
	return Root(HashOf(record))
}

// Hash is the hash that the root is.
func (r Root) Hash() Hash {
	return Hash(r)
}

// String is the root as text.
func (r Root) String() string {
	var bytes [36]byte
	copy(bytes[:4], rootPrefix[:])
	copy(bytes[4:], r[:])
	return "b" + base32Lower(bytes[:])
}

// ParseRoot reads a root from its text. Only the one way of writing a root
// is read: any other spelling of the same bytes is refused.
func ParseRoot(text string) (Root, error) {
	var root Root
	notARoot := &Error{kind: kindInvalidRoot, Detail: first80(text)}
	if !strings.HasPrefix(text, "b") {
		return root, notARoot
	}
	bytes, err := base32LowerDecode(text[1:])
	if err != nil || len(bytes) != 36 || [4]byte(bytes[:4]) != rootPrefix {
		return root, notARoot
	}
	copy(root[:], bytes[4:])
	if root.String() != text {
		return Root{}, notARoot
	}
	return root, nil
}
