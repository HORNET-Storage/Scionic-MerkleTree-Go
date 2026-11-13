package scionic

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// corpusBytes is the generator that the shared vectors are made with: block
// k of the stream is SHA-256(seed + k), where k is 8 bytes, big-endian,
// counted from 0. The blocks are joined, and cut to the length.
func corpusBytes(seed string, length int) []byte {
	out := make([]byte, 0, length+sha256.Size)
	message := append([]byte(seed), make([]byte, 8)...)
	for block := uint64(0); len(out) < length; block++ {
		binary.BigEndian.PutUint64(message[len(seed):], block)
		digest := sha256.Sum256(message)
		out = append(out, digest[:]...)
	}
	return out[:length]
}

// readManifest reads one of the manifests under spec/, after holding its
// bytes to the SHA-256 that this library was written against. A manifest
// that changes has to be changed here too, on purpose.
//
// The manifest is read with the standard library's JSON reader, and not
// with code of the library that is under test. A field that the tests do
// not know is an error, so that nothing in a manifest goes unchecked.
func readManifest(t *testing.T, into any, wantSHA256 string, path ...string) {
	t.Helper()
	name := filepath.Join(append([]string{"spec"}, path...)...)
	content, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	if got := hex.EncodeToString(digest[:]); got != wantSHA256 {
		t.Fatalf("%s has the SHA-256 %s, and these tests were written against %s", name, got, wantSHA256)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

func mustHex(t *testing.T, text string) []byte {
	t.Helper()
	out, err := hex.DecodeString(text)
	if err != nil {
		t.Fatalf("%q is not hex: %v", text, err)
	}
	return out
}

func mustHash(t *testing.T, text string) Hash {
	t.Helper()
	hash, err := HashFromHex(text)
	if err != nil {
		t.Fatalf("%q is not a hash: %v", text, err)
	}
	return hash
}

// smallPieces cuts into pieces of 16 bytes, so that small files have many
// pieces.
func smallPieces(t *testing.T) Cutting {
	t.Helper()
	cutting, err := FixedCutting(16)
	if err != nil {
		t.Fatal(err)
	}
	return cutting
}

func piecesEqual(left, right [][]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for at := range left {
		if !bytes.Equal(left[at], right[at]) {
			return false
		}
	}
	return true
}
