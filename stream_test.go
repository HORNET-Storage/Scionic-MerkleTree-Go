package scionic

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"testing"
)

// Syncing over a connection: requests one way, answers the other.

// sameSummary reports whether two checks of a tree found the same.
func sameSummary(left, right *Summary) bool {
	return left != nil && right != nil && left.Folders == right.Folders && left.Files == right.Files &&
		bytes.Equal(left.Record.Encode(), right.Record.Encode())
}

// fetched is what a fetch gave.
type fetched struct {
	summary *Summary
	err     error
}

func TestATreeIsFetchedOverAConnection(t *testing.T) {
	source, root := sampleTree(t)
	// A connection that holds next to nothing on the way back, so that the
	// server waits on every answer until the client reads it, and one with
	// room to spare.
	for _, room := range [][2]int{{RequestsAheadBytes, 1}, {RequestsAheadBytes, 100}, {1 << 20, 1 << 20}} {
		dest := NewMemoryStore()
		got, _, served := connected(source, room[0], room[1], func(peer *StreamPeer) fetched {
			summary, err := Fetch(dest, root, peer)
			return fetched{summary, err}
		})
		if served != nil || got.err != nil {
			t.Fatalf("%v: the fetch gave %v, and the serving %v", room, got.err, served)
		}
		want, err := Verify(dest, root)
		if err != nil || !sameSummary(got.summary, want) {
			t.Fatalf("%v: the fetch says %+v, and a check of what it took says %+v (%v)", room, got.summary, want, err)
		}
		if !sameStores(dest, source) {
			t.Fatalf("%v: what was fetched is not what was held", room)
		}
	}
}

func TestPartsOfATreeAreReadOverAConnection(t *testing.T) {
	source, root := sampleTree(t)
	content := corpusBytes("model", 1000)
	_, _, served := connected(source, RequestsAheadBytes, 7, func(peer *StreamPeer) bool {
		remote, err := OpenRemote(peer, root)
		if err != nil {
			t.Fatal(err)
		}
		record, err := RecordOf(source, root)
		if err != nil || !bytes.Equal(remote.Record().Encode(), record.Encode()) || remote.Root() != root {
			t.Fatalf("the record that was read is not the tree's (%v)", err)
		}
		model, found, err := remote.Lookup("weights/model.bin")
		if err != nil || !found {
			t.Fatal(found, err)
		}
		local, found, err := Lookup(source, root, "weights/model.bin")
		if err != nil || !found || !local.Equal(model) {
			t.Fatalf("a peer shows another entry than the store holds (%v)", err)
		}
		if _, found, err := remote.Lookup("weights/absent"); err != nil || found {
			t.Fatal(found, err)
		}
		var out bytes.Buffer
		if err := remote.Read(model.Target(), 100, 333, &out); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(out.Bytes(), content[100:433]) {
			t.Fatal("a stretch that was read from a peer is not the file's")
		}
		held, err := remote.Have(model.Target())
		if err != nil || held.Held() != 63 {
			t.Fatal(held, err)
		}
		weights, found, err := remote.Lookup("weights")
		if err != nil || !found {
			t.Fatal(found, err)
		}
		entries, err := remote.Entries(weights.Target(), 0, 10)
		if err != nil || entries.Total != 3 || len(entries.Entries) != 3 {
			t.Fatal(entries, err)
		}
		return true
	})
	if served != nil {
		t.Fatal(served)
	}
}

func TestRequestsAreSentAheadOfTheirAnswersAndNeverTooFar(t *testing.T) {
	// A folder of 150 small files: more lists than one round asks for, and
	// more requests in a round than are sent ahead at once.
	source := NewMemoryStore()
	builder := NewBuilder(source, smallChunks(t))
	var entries []Entry
	for index := range 150 {
		file, err := builder.FileFromBytes(corpusBytes(fmt.Sprintf("file-%d", index), 20))
		if err != nil {
			t.Fatal(err)
		}
		entry, err := NewEntry(fmt.Sprintf("f%03d", index), file, ExtraData{})
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
	top, err := builder.Folder(entries)
	if err != nil {
		t.Fatal(err)
	}
	root, err := builder.Finish(top, ExtraData{})
	if err != nil {
		t.Fatal(err)
	}
	// The connection holds on the way to the server exactly what a client
	// may send ahead, and on the way back one byte. A client that sent
	// further ahead would wait for the server, which waits for the client.
	dest := NewMemoryStore()
	got, sent, served := connected(source, RequestsAheadBytes, 1, func(peer *StreamPeer) fetched {
		summary, err := Fetch(dest, root, peer)
		return fetched{summary, err}
	})
	if served != nil || got.err != nil {
		t.Fatalf("the fetch gave %v, and the serving %v", got.err, served)
	}
	if !sameStores(dest, source) {
		t.Fatal("what was fetched is not what was held")
	}
	// 1 record, 1 folder, 150 lists and 150 requests for chunks.
	total := 0
	for _, flight := range sent.sizes {
		total += flight
		if flight > RequestsAheadBytes {
			t.Fatalf("%d bytes of requests were sent ahead at once: %v", flight, sent.sizes)
		}
	}
	if total != (33+4)*152+(45+4)*150 || total != len(sent.all) {
		t.Fatalf("%d bytes of requests were sent: %v", total, sent.sizes)
	}
	// 64 requests for lists take 2,368 bytes, so a round goes out in two
	// flights, and the first holds as many requests as fit.
	if !slices.Contains(sent.sizes, 55*37) || len(sent.sizes) >= 30 {
		t.Fatalf("the requests went out as %v", sent.sizes)
	}
}

// withLength gives bytes as they go over a connection: their length in four
// bytes, and then the bytes.
func withLength(content []byte) []byte {
	return append(binary.BigEndian.AppendUint32(nil, uint32(len(content))), content...)
}

// serveAll serves requests that are all there already, and gives what was
// answered.
func serveAll(source Store, requests []byte) ([]byte, error) {
	var answers bytes.Buffer
	err := Serve(source, bytes.NewReader(requests), &answers)
	return answers.Bytes(), err
}

func TestAnAnswerIsItsFramesAndThenALengthOfZero(t *testing.T) {
	source, root := sampleTree(t)
	held, err := RecordOf(source, root)
	if err != nil {
		t.Fatal(err)
	}
	record := held.Encode()
	unknown := RootOfRecord([]byte("nobody holds this"))
	end := []byte{0, 0, 0, 0}
	// Nothing asked, nothing answered.
	if answers, err := serveAll(source, nil); err != nil || len(answers) != 0 {
		t.Fatal(answers, err)
	}
	// Two requests: one for a record that is held, and one for a record
	// that is not.
	requests := slices.Concat(withLength(RequestRecord(root).Encode()), withLength(RequestRecord(unknown).Encode()))
	answers, err := serveAll(source, requests)
	want := slices.Concat(withLength(record), end, withLength([]byte{0x27, 1}), end)
	if err != nil || !bytes.Equal(answers, want) {
		t.Fatalf("%x (%v)", answers, err)
	}
	// A request that is malformed ends the serving, and so does one that is
	// empty, one that is too long, and one that the stream ends inside.
	for _, bad := range [][]byte{
		withLength([]byte{0x3a, 1, 2}),
		withLength(bytes.Repeat([]byte{0x30}, 32)),
		withLength(nil),
		withLength(bytes.Repeat([]byte{0x33}, MaxRequestBytes+1)),
		withLength(RequestRecord(root).Encode())[:20],
	} {
		if answers, err := serveAll(source, bad); err == nil || len(answers) != 0 {
			t.Fatalf("%x was answered with %x (%v)", bad, answers, err)
		}
	}
	// What came before it is answered.
	requests = slices.Concat(withLength(RequestRecord(root).Encode()), withLength([]byte{0x3a}))
	answers, err = serveAll(source, requests)
	if !errors.Is(err, ErrInvalidData) || !bytes.Equal(answers, slices.Concat(withLength(record), end)) {
		t.Fatalf("%x (%v)", answers, err)
	}
}

func TestAConnectionThatAnAnswerLeftHalfReadIsNotUsedAgain(t *testing.T) {
	source, root := sampleTree(t)
	request := RequestRecord(root).Encode()
	take := func([]byte) error { return nil }
	// An answer that is longer than its asker takes is refused by its
	// length. Its bytes are still on the connection, unread.
	results, _, _ := connected(source, 4096, 4096, func(peer *StreamPeer) [2]error {
		return [2]error{peer.Ask(request, 10, take), peer.Ask(request, MaxRecordBytes, take)}
	})
	if !errors.Is(results[0], ErrLimitExceeded) || !errors.Is(results[1], ErrPeer) {
		t.Fatal(results)
	}
	// The same when the asker refuses a frame for what is in it.
	results, _, _ = connected(source, 4096, 4096, func(peer *StreamPeer) [2]error {
		refuse := func([]byte) error { return ErrIntakeFailed }
		return [2]error{peer.Ask(request, MaxRecordBytes, refuse), peer.Ask(request, MaxRecordBytes, take)}
	})
	if !errors.Is(results[0], ErrIntakeFailed) || !errors.Is(results[1], ErrPeer) {
		t.Fatal(results)
	}
	// A connection whose answers were all read to their end is used again.
	frames, _, served := connected(source, 4096, 4096, func(peer *StreamPeer) int {
		frames := 0
		for range 3 {
			err := peer.Ask(request, MaxRecordBytes, func([]byte) error {
				frames++
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		return frames
	})
	if served != nil || frames != 3 {
		t.Fatal(frames, served)
	}
}
