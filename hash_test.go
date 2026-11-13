package scionic

import (
	"bytes"
	"strings"
	"testing"
)

func TestBase32ReadsBackWhatItWritesAtEveryLength(t *testing.T) {
	data := make([]byte, 64)
	for at := range data {
		data[at] = byte(at * 37 % 251)
	}
	for length := range data {
		text := base32Lower(data[:length])
		back, err := base32LowerDecode(text)
		if err != nil || !bytes.Equal(back, data[:length]) {
			t.Fatalf("length %d: %q read back as %x (%v)", length, text, back, err)
		}
	}
	if got := base32Lower([]byte("foobar")); got != "mzxw6ytboi" {
		t.Fatalf("foobar is %q", got)
	}
}

func TestBase32RefusesOtherLettersAndLeftoverBits(t *testing.T) {
	for _, text := range []string{"MZXW6", "mzxw1", "mzxw=", "mz xw", "mzxw6ytbo1"} {
		if _, err := base32LowerDecode(text); err == nil {
			t.Errorf("%q was read", text)
		}
	}
	// "my" is one byte and two bits that are zero; "mz" leaves a bit set.
	if back, err := base32LowerDecode("my"); err != nil || !bytes.Equal(back, []byte{0x66}) {
		t.Fatalf("my: %x %v", back, err)
	}
	if _, err := base32LowerDecode("mz"); err == nil {
		t.Fatal("mz was read")
	}
}

func TestHexReadsBackWhatItWritesAndRefusesTheRest(t *testing.T) {
	data := []byte{0x00, 0x01, 0x7f, 0x80, 0xab, 0xff}
	text := hexLower(data)
	if text != "00017f80abff" {
		t.Fatal(text)
	}
	back := make([]byte, 6)
	if err := hexLowerDecode(text, back); err != nil || !bytes.Equal(back, data) {
		t.Fatalf("%x %v", back, err)
	}
	for _, other := range []string{"00017F80ABFF", "00017f80abf", "00017f80abfff", "00017f80abfg"} {
		if err := hexLowerDecode(other, back); err == nil {
			t.Errorf("%q was read", other)
		}
	}
}

func TestAHashIsTheSHA256OfTheBytes(t *testing.T) {
	if got := HashOf([]byte("abc")).Hex(); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatal(got)
	}
	if got := HashOf(nil).Hex(); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatal(got)
	}
	hash := HashOf([]byte("abc"))
	if back, err := HashFromHex(hash.Hex()); err != nil || back != hash {
		t.Fatal(back, err)
	}
	if _, err := HashFromHex(strings.ToUpper(hash.Hex())); err == nil {
		t.Fatal("upper case was read")
	}
	if _, err := HashFromHex(hash.Hex()[:63]); err == nil {
		t.Fatal("63 digits were read")
	}
	if _, err := HashFromSlice(make([]byte, 31)); err == nil {
		t.Fatal("31 bytes were read")
	}
}

func TestARootIsWrittenAsACIDThatBeginsBafkrei(t *testing.T) {
	root := RootOfRecord([]byte("abc"))
	text := root.String()
	// The CID of the raw bytes "abc", as other tools write it.
	if text != "bafkreif2pall7dybz7vecqka3zo24irdwabwdi4wc55jznaq75q7eaavvu" {
		t.Fatal(text)
	}
	if back, err := ParseRoot(text); err != nil || back != root {
		t.Fatal(back, err)
	}
}

func TestOnlyTheOneSpellingOfARootIsRead(t *testing.T) {
	text := RootOfRecord([]byte("abc")).String()
	others := []string{
		strings.ToUpper(text),
		text[1:],
		text[:58],
		text + "a",
		strings.Replace(text, "bafkrei", "bafirei", 1),
		text + "=",
		"",
	}
	// The last letter carries two bits that must be zero.
	last := byte('b')
	if text[len(text)-1] == 'b' {
		last = 'c'
	}
	others = append(others, text[:len(text)-1]+string(last))
	for _, other := range others {
		if _, err := ParseRoot(other); err == nil {
			t.Errorf("%q was read as a root", other)
		}
	}
}
