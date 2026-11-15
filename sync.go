package scionic

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"strings"
	"sync"
)

// Syncing by file hash and chunk number.
//
//   - A Responder answers a receiver's requests from a store.
//   - Fetch takes a whole tree from a peer into a store, from the top down,
//     and skips whatever the store holds already.
//   - A Remote reads parts of a tree straight from a peer: one name in a
//     folder, a branch of entries, or a stretch of a file's bytes. It needs no
//     store, and takes nothing but the parts it is asked for.
//   - Export and Import carry a whole tree as one stream of frames.
//   - Serve answers requests on a connection, and a StreamPeer asks them
//     there.
//
// Nothing is compared between the two sides. The receiver walks down from
// the root it trusts, and every answer is checked as it arrives against a
// hash that is already checked. An answer that is forged, that belongs
// somewhere else, or that was not asked for is refused. With each request
// goes the longest frame that can answer it, so a peer cannot make a
// receiver set aside more memory than the tree it trusts calls for.

const (
	// requestsPerRound is the most requests Fetch sends before it looks at
	// what is still wanted.
	requestsPerRound = 64
	// maxBranchItems is the most items of a list that one answer shows.
	maxBranchItems = 16_384
	// maxChunksPerAnswer is the most chunks that one answer sends.
	maxChunksPerAnswer = 256
	// branchFixedBytes is the bytes of a branch's frame that are there whatever it
	// shows: its first byte, the list's count, the branch's start, how many
	// items it shows, and how many nodes it carries.
	branchFixedBytes = 23
	// recentItems is how many items of lists a responder keeps between
	// requests, for each of the three kinds it keeps, unless it is told
	// otherwise. This many items of files' lists stand for about half a
	// terabyte of files.
	recentItems = 1 << 18
	// recentLists is the most lists of one kind that a responder keeps,
	// however short.
	recentLists = 64
)

// RequestsAheadBytes is how many bytes of requests a StreamPeer sends ahead
// of their answers, and the most that the format lets anyone send ahead. It
// is small on purpose. The other end answers one request before it reads the
// next, so requests that are sent ahead wait in the connection, and a
// connection that could not hold them would leave both ends waiting for each
// other. A connection over a network holds this much. One that holds
// nothing, as Go's net.Pipe and io.Pipe do, needs [StreamPeer.Ahead] set to
// zero.
const RequestsAheadBytes = 2048

func otherKind() error {
	return notWanted("an answer of another kind than was asked for")
}

func tooMuch() error {
	return notWanted("more than was asked for")
}

// within refuses a frame that is longer than its asker takes.
func within(limit int, frame []byte) error {
	if len(frame) > limit {
		return limitExceeded("a frame", uint64(max(limit, 0)), uint64(len(frame)))
	}
	return nil
}

// timesPlus gives count times each, and more on top, and the largest int
// where that does not fit.
func timesPlus(count uint64, each, more int) int {
	if count > uint64(maxInt-more)/uint64(each) {
		return maxInt
	}
	return int(count)*each + more
}

// folderBranchLimit is the longest frame that shows entries entries of a
// folder.
func folderBranchLimit(entries uint64) int {
	return timesPlus(entries, MaxEntryBytes, branchFixedBytes+MaxCarriedNodes*(42+MaxNameBytes))
}

// fileBranchLimit is the longest frame that shows items items of a file's
// list.
func fileBranchLimit(items uint64) int {
	return timesPlus(items, ListItemBytes, branchFixedBytes+MaxCarriedNodes*40)
}

// mostChunksIn is the most chunks that length bytes of a file can lie in.
// All of them but the first and the last lie in the stretch whole, and no
// chunk but a file's last is shorter than the tree's way of chunking allows.
func mostChunksIn(chunking Chunking, length uint64) uint64 {
	shortest := uint64(FastCDCMinSize + 1)
	if size, fixed := chunking.Fixed(); fixed {
		shortest = uint64(size)
	}
	return length/shortest + 2
}

// Peer is something that answers requests: a connection to another machine,
// or a Responder in this one. A peer is used by one goroutine at a time.
//
// A peer that can send requests ahead of their answers may also have the
// method of PeerAskAll. It is used when it is there.
type Peer interface {
	// Ask sends one request, and hands each frame of the answer to onFrame
	// in the order the frames come. A frame is lent for the call only. An
	// error from onFrame ends the answer, and is what Ask returns.
	//
	// limit is the longest frame the asker takes in answer. A frame that
	// is longer is an error, and a peer on a connection refuses it by its
	// length, before any of it is read.
	Ask(request []byte, limit int, onFrame func(frame []byte) error) error
}

// Asked is a request as it is sent, with the longest frame that its asker
// takes in answer.
type Asked struct {
	Request []byte
	Limit   int
}

// PeerAskAll is a peer that takes several requests at once.
type PeerAskAll interface {
	// AskAll sends several requests, each with its limit, and hands each
	// frame to onFrame with the place of the request that it answers.
	// Answers come in the order of the requests.
	AskAll(requests []Asked, onFrame func(place int, frame []byte) error) error
}

// askAll asks several requests. A peer that takes them at once is given
// them at once. Any other is asked one at a time.
func askAll(peer Peer, requests []Asked, onFrame func(place int, frame []byte) error) error {
	if atOnce, can := peer.(PeerAskAll); can {
		return atOnce.AskAll(requests, onFrame)
	}
	for place, asked := range requests {
		err := peer.Ask(asked.Request, asked.Limit, func(frame []byte) error {
			return onFrame(place, frame)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

type recentList[T any] struct {
	hash  Hash
	value *T
	items uint64
}

// recent keeps the lists that were used last. Reading a list from the store
// and hashing it takes a pass over the whole of it, so a list that was
// asked about is kept for the next request. Lists are weighed by their
// items, and the ones used longest ago go first.
type recent[T any] struct {
	lock sync.Mutex
	kept []recentList[T]
}

func (r *recent[T]) get(hash Hash) *T {
	r.lock.Lock()
	defer r.lock.Unlock()
	for at := range r.kept {
		if r.kept[at].hash == hash {
			found := r.kept[at]
			r.kept = append(append(r.kept[:at], r.kept[at+1:]...), found)
			return found.value
		}
	}
	return nil
}

// keep keeps value, and lets go of older ones while there are more than
// most items or too many lists. The newest one always stays.
func (r *recent[T]) keep(hash Hash, value *T, items, most uint64) {
	r.lock.Lock()
	defer r.lock.Unlock()
	for at := range r.kept {
		if r.kept[at].hash == hash {
			r.kept = append(r.kept[:at], r.kept[at+1:]...)
			break
		}
	}
	r.kept = append(r.kept, recentList[T]{hash: hash, value: value, items: max(items, 1)})
	var total uint64
	for at := range r.kept {
		total += r.kept[at].items
	}
	for len(r.kept) > 1 && (total > most || len(r.kept) > recentLists) {
		total -= r.kept[0].items
		r.kept[0] = recentList[T]{}
		r.kept = r.kept[1:]
	}
}

// Responder answers requests from a store.
//
// It answers for whatever the store holds, by hash. It gives what it has
// and says so when it has nothing: a receiver never has to trust it, because
// a receiver checks every answer.
//
// One responder serves many goroutines at once.
type Responder struct {
	store Store
	// lists are the files whose lists were read last.
	lists recent[FileList]
	// files and folders are the files and the folders whose trees were
	// built last. Showing a part of a long list needs its tree.
	files   recent[OpenFile]
	folders recent[OpenFolder]
	recent  uint64
	have    bool
}

// NewResponder makes a responder that answers from store.
func NewResponder(store Store) *Responder {
	return &Responder{store: store, recent: recentItems, have: true}
}

// Recent sets how many items of lists are kept between requests, for each
// of the three kinds: files' lists, files' trees and folders' trees. A list
// that is not kept is read from the store and hashed again when it is next
// asked about, which takes a pass over the whole of it. An item kept costs
// some tens of bytes for a file and some hundreds for a folder. The list
// that was used last is kept whatever its length. It is set before the
// responder is used, and gives the responder back.
func (r *Responder) Recent(items uint64) *Responder {
	r.recent = items
	return r
}

// Have sets whether a peer is told which chunks of a file are held here.
// Telling takes a look into the store for every chunk of the file, so a
// responder that serves strangers may leave it off. It then answers such a
// request as it does for a file it does not hold. It is set before the
// responder is used, and gives the responder back.
func (r *Responder) Have(on bool) *Responder {
	r.have = on
	return r
}

func (r *Responder) fileList(file Hash) (*FileList, error) {
	if list := r.lists.get(file); list != nil {
		return list, nil
	}
	if open := r.files.get(file); open != nil {
		return open.List(), nil
	}
	kept, held, err := r.store.Get(StoredFileList, file)
	if err != nil || !held {
		return nil, err
	}
	list, err := DecodeFileList(kept)
	if err != nil || list.Hash() != file {
		return nil, corrupt("a stored file's list", file)
	}
	r.lists.keep(file, list, list.Count(), r.recent)
	return list, nil
}

func (r *Responder) openFile(file Hash) (*OpenFile, error) {
	if open := r.files.get(file); open != nil {
		return open, nil
	}
	var open *OpenFile
	if list := r.lists.get(file); list != nil {
		// The lock is let go before the list is hashed, so that other
		// goroutines are not kept waiting.
		built, err := NewOpenFile(list)
		if err != nil {
			return nil, err
		}
		open = built
	} else {
		kept, held, err := r.store.Get(StoredFileList, file)
		if err != nil || !held {
			return nil, err
		}
		// Read and hashed once, for the list and for its tree.
		decoded, err := DecodeOpenFile(kept)
		if err != nil || decoded.List().Hash() != file {
			return nil, corrupt("a stored file's list", file)
		}
		open = decoded
	}
	r.files.keep(file, open, open.List().Count(), r.recent)
	return open, nil
}

func (r *Responder) openFolder(folder Hash) (*OpenFolder, error) {
	if open := r.folders.get(folder); open != nil {
		return open, nil
	}
	kept, held, err := r.store.Get(StoredFolderList, folder)
	if err != nil || !held {
		return nil, err
	}
	open, err := DecodeOpenFolder(kept)
	if err != nil || open.List().Hash() != folder {
		return nil, corrupt("a stored folder's list", folder)
	}
	r.folders.keep(folder, open, open.List().Count(), r.recent)
	return open, nil
}

// Answer answers one request. Each frame of the answer is handed to
// onFrame, and is lent for the call only. A request that is malformed is
// an error. A request for something the store does not hold is answered
// with a refusal.
func (r *Responder) Answer(request []byte, onFrame func(frame []byte) error) error {
	asked, err := DecodeRequest(request)
	if err != nil {
		return err
	}
	notHeld := func() error { return onFrame(RefusedFrame(RefusalNotHeld)) }
	outOfRange := func() error { return onFrame(RefusedFrame(RefusalOutOfRange)) }
	// whole answers with one thing as the store holds it.
	whole := func(kind Stored, frame func(kept []byte) []byte) error {
		kept, held, err := r.store.Get(kind, asked.Hash)
		if err != nil {
			return err
		}
		if !held {
			return notHeld()
		}
		return onFrame(frame(kept))
	}
	switch asked.Kind {
	case AskRecord:
		return whole(StoredRecord, func(kept []byte) []byte { return kept })
	case AskFolder:
		return whole(StoredFolderList, FolderListFrame)
	case AskFile:
		return whole(StoredFileList, FileListFrame)
	case AskChunk:
		return whole(StoredChunk, ChunkFrame)
	case AskFolderBranch:
		open, err := r.openFolder(asked.Hash)
		if err != nil {
			return err
		}
		if open == nil {
			return notHeld()
		}
		total := open.List().Count()
		if total == 0 {
			return onFrame(FolderBranchFrame(&FolderBranch{}))
		}
		if asked.Start >= total {
			return outOfRange()
		}
		end := min(total, asked.Start+min(uint64(asked.Count), maxBranchItems))
		branch, err := open.Open(asked.Start, end)
		if err != nil {
			return err
		}
		return onFrame(FolderBranchFrame(branch))
	case AskFolderName:
		open, err := r.openFolder(asked.Hash)
		if err != nil {
			return err
		}
		if open == nil {
			return notHeld()
		}
		branch, err := open.OpenName(asked.Name)
		if err != nil {
			return err
		}
		return onFrame(FolderBranchFrame(branch))
	case AskFileBranch:
		open, err := r.openFile(asked.Hash)
		if err != nil {
			return err
		}
		if open == nil {
			return notHeld()
		}
		total := open.List().Count()
		if asked.Start >= total {
			return outOfRange()
		}
		end := min(total, asked.Start+min(uint64(asked.Count), maxBranchItems))
		branch, err := open.Open(asked.Start, end)
		if err != nil {
			return err
		}
		return onFrame(FileBranchFrame(branch))
	case AskFileBytes:
		open, err := r.openFile(asked.Hash)
		if err != nil {
			return err
		}
		if open == nil {
			return notHeld()
		}
		size := open.List().Size()
		if asked.Offset >= size {
			return outOfRange()
		}
		first, after, err := open.ChunksFor(asked.Offset, min(asked.Length, size-asked.Offset))
		if err != nil {
			return err
		}
		branch, err := open.Open(first, min(after, first+maxBranchItems))
		if err != nil {
			return err
		}
		return onFrame(FileBranchFrame(branch))
	case AskChunks:
		list, err := r.fileList(asked.Hash)
		if err != nil {
			return err
		}
		if list == nil {
			return notHeld()
		}
		if asked.Start >= list.Count() {
			return outOfRange()
		}
		items := list.Items()[asked.Start:]
		items = items[:min(uint64(len(items)), uint64(asked.Count), maxChunksPerAnswer)]
		// One frame is filled again for each chunk. Memory that is used
		// again costs much less than fresh memory for every chunk, which a
		// system has to find and clear first. A frame is lent to onFrame
		// for the call only.
		var frame []byte
		for _, item := range items {
			kept, held, err := r.store.Get(StoredChunk, item.Hash)
			if err != nil {
				return err
			}
			if !held {
				return notHeld()
			}
			frame = append(append(frame[:0], byte(FrameChunk)), kept...)
			if err := onFrame(frame); err != nil {
				return err
			}
		}
		return nil
	case AskHave:
		if !r.have {
			return notHeld()
		}
		list, err := r.fileList(asked.Hash)
		if err != nil {
			return err
		}
		if list == nil {
			return notHeld()
		}
		holds, err := NewHaveMap(list.Count())
		if err != nil {
			return err
		}
		for number, item := range list.Items() {
			held, err := storeHas(r.store, StoredChunk, item.Hash)
			if err != nil {
				return err
			}
			if held {
				if err := holds.Set(uint64(number)); err != nil {
					return err
				}
			}
		}
		return onFrame(HaveFrame(holds))
	}
	return invalid("a request of an unknown kind")
}

// Ask answers one request as a peer does: a frame that is longer than the
// asker takes is an error.
func (r *Responder) Ask(request []byte, limit int, onFrame func(frame []byte) error) error {
	return r.Answer(request, func(frame []byte) error {
		if err := within(limit, frame); err != nil {
			return err
		}
		return onFrame(frame)
	})
}

// askOne asks for one frame. A refusal, no frame and more than one frame
// are errors.
func askOne(peer Peer, request Request, limit int) ([]byte, error) {
	var answer []byte
	answered := false
	err := peer.Ask(request.Encode(), limit, func(frame []byte) error {
		if answered {
			return tooMuch()
		}
		answer, answered = bytes.Clone(frame), true
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !answered {
		return nil, peerError("gave no answer")
	}
	if len(answer) > 0 && FrameKind(answer[0]) == FrameRefused {
		refused, err := DecodeFrame(answer)
		if err != nil {
			return nil, err
		}
		if refused.Refusal == RefusalOutOfRange {
			return nil, peerError("says that what was asked for lies outside the list or the file")
		}
		return nil, peerError("does not hold what was asked for")
	}
	return answer, nil
}

// Fetch takes the whole tree with this root from a peer into a store.
//
// It goes from the top down: the record, then each folder's entries and
// each file's list, then the chunks, by their file and their numbers in it.
// Whatever the store holds already is skipped by its hash, be it a chunk, a
// file or a whole folder, and wherever it came from. Every answer has to be
// exactly what was asked for, and is checked before it is kept.
//
// A fetch that fails can be started again: it goes on from what the store
// holds.
func Fetch(store Store, root Root, peer Peer) (*Summary, error) {
	intake, err := NewIntake(store, root)
	if err != nil {
		return nil, err
	}
	for {
		requests := intake.Requests(requestsPerRound)
		if len(requests) == 0 {
			break
		}
		// What each answer has to be is settled before anything is sent.
		// Nothing that comes in one round changes it: a round asks for each
		// thing once.
		expected := make([][]Arrived, len(requests))
		asks := make([]Asked, len(requests))
		for place, request := range requests {
			expected[place] = intake.Expected(request)
			asks[place] = Asked{Request: request.Encode(), Limit: intake.AnswerLimit(request)}
		}
		taken := make([]int, len(requests))
		refused := make([]bool, len(requests))
		err := askAll(peer, asks, func(place int, frame []byte) error {
			if place < 0 || place >= len(requests) {
				return peerError("answered a request that was not sent")
			}
			if refused[place] {
				return peerError("sent more after it refused")
			}
			if len(frame) > 0 && FrameKind(frame[0]) == FrameRefused {
				if _, err := DecodeFrame(frame); err != nil {
					return err
				}
				refused[place] = true
				return nil
			}
			if taken[place] >= len(expected[place]) {
				return tooMuch()
			}
			if _, err := intake.AcceptExpected(frame, expected[place][taken[place]]); err != nil {
				return err
			}
			taken[place]++
			return nil
		})
		if err != nil {
			return nil, err
		}
		for _, count := range taken {
			if count == 0 {
				return nil, peerError("does not hold what the tree needs")
			}
		}
	}
	return intake.Finish()
}

// Entries is a branch of a folder's entries, as a peer showed it and as it was
// checked.
type Entries struct {
	// Total is how many entries the folder has in all.
	Total uint64
	// Start is the place of the first entry here.
	Start   uint64
	Entries []Entry
}

// Remote reads parts of one tree straight from a peer, with nothing kept.
//
// It starts from a root that the caller trusts. Each thing it hands back
// was shown by the peer and checked against that root: an entry by its
// place in its folder, and bytes by their place in their file.
type Remote struct {
	peer   Peer
	root   Root
	record *Record
}

// OpenRemote asks the peer for the record of root, and checks it.
func OpenRemote(peer Peer, root Root) (*Remote, error) {
	frame, err := askOne(peer, RequestRecord(root), MaxRecordBytes)
	if err != nil {
		return nil, err
	}
	if RootOfRecord(frame) != root {
		return nil, hashMismatch("a record")
	}
	record, err := DecodeRecord(frame)
	if err != nil {
		return nil, err
	}
	return &Remote{peer: peer, root: root, record: record}, nil
}

// Root is the root of the tree that is read.
func (r *Remote) Root() Root {
	return r.root
}

// Record is the tree's record.
func (r *Remote) Record() *Record {
	return r.record
}

// Entry gives what is under name in folder, or false when the folder has no
// such name. Either answer is proven by the one entry the peer shows.
func (r *Remote) Entry(folder Target, name string) (Entry, bool, error) {
	if err := CheckName(name); err != nil {
		return Entry{}, false, err
	}
	if err := folder.require(KindFolder); err != nil {
		return Entry{}, false, err
	}
	frame, err := askOne(r.peer, RequestFolderName(folder.Hash, name), folderBranchLimit(1))
	if err != nil {
		return Entry{}, false, err
	}
	shown, err := DecodeFrame(frame)
	if err != nil {
		return Entry{}, false, err
	}
	if shown.Kind != FrameFolderBranch {
		return Entry{}, false, otherKind()
	}
	return shown.FolderBranch.CheckName(folder, name)
}

// Lookup gives what is at path in the tree: names with "/" between them.
// One question is asked for each name on the way.
func (r *Remote) Lookup(path string) (Entry, bool, error) {
	current := r.record.Top()
	var found Entry
	for foldersPassed, name := range strings.Split(path, "/") {
		if err := CheckName(name); err != nil {
			return Entry{}, false, err
		}
		if current.Kind != KindFolder {
			return Entry{}, false, nil
		}
		if foldersPassed >= MaxFolderDepth {
			return Entry{}, false, &Error{kind: kindTooDeep, Limit: MaxFolderDepth}
		}
		entry, held, err := r.Entry(current, name)
		if err != nil || !held {
			return Entry{}, false, err
		}
		current = entry.Target()
		found = entry
	}
	return found, true, nil
}

// Entries gives up to count entries of a folder, from place start. Fewer
// come back when the folder ends first, or when the peer gives fewer at a
// time. An empty folder gives none.
func (r *Remote) Entries(folder Target, start uint64, count uint32) (*Entries, error) {
	if err := folder.require(KindFolder); err != nil {
		return nil, err
	}
	frame, err := askOne(r.peer, RequestFolderBranch(folder.Hash, start, count), folderBranchLimit(uint64(count)))
	if err != nil {
		return nil, err
	}
	shown, err := DecodeFrame(frame)
	if err != nil {
		return nil, err
	}
	if shown.Kind != FrameFolderBranch {
		return nil, otherKind()
	}
	branch := shown.FolderBranch
	if err := branch.Check(folder); err != nil {
		return nil, err
	}
	if branch.Count() != 0 && (branch.Start() != start || uint64(len(branch.Entries())) > uint64(count)) {
		return nil, notWanted("a branch that was not asked for")
	}
	return &Entries{Total: branch.Count(), Start: branch.Start(), Entries: branch.Entries()}, nil
}

// Read writes length bytes of a file, from byte offset, to out.
//
// The peer shows the branch of the file's list that holds the stretch, and
// that proves each chunk's number and the byte it starts at. Then the
// chunks are asked for by their numbers, and each is checked against the
// list before any of its bytes is written. Nothing else of the file is
// taken.
func (r *Remote) Read(file Target, offset, length uint64, out io.Writer) error {
	if err := file.require(KindFile); err != nil {
		return err
	}
	end := offset + length
	if end < offset || end > file.Size {
		return invalid("a stretch of bytes is outside the file")
	}
	chunking := r.record.Chunking()
	at := offset
	for at < end {
		frame, err := askOne(r.peer, RequestFileBytes(file.Hash, at, end-at), fileBranchLimit(mostChunksIn(chunking, end-at)))
		if err != nil {
			return err
		}
		shown, err := DecodeFrame(frame)
		if err != nil {
			return err
		}
		if shown.Kind != FrameFileBranch {
			return otherKind()
		}
		branch := shown.FileBranch
		branchStart, err := branch.CheckWithChunking(file, chunking)
		if err != nil {
			return err
		}
		items := branch.Items()
		// The branch has to begin with the chunk that holds the first byte
		// still due, and may not reach past the chunk that holds the last.
		var beforeLast uint64
		longest := 0
		for index, item := range items {
			if index < len(items)-1 {
				beforeLast += uint64(item.Length)
			}
			// No chunk that is due is longer than the longest in the branch.
			longest = max(longest, int(item.Length))
		}
		holdsFirst := branchStart <= at && at < branchStart+uint64(items[0].Length)
		if !holdsFirst || branchStart+beforeLast >= end {
			return notWanted("a branch that does not hold the bytes asked for")
		}
		taken := 0
		chunkStart := branchStart
		refused := false
		chunks := RequestChunks(file.Hash, branch.Start(), uint32(len(items)))
		err = r.peer.Ask(chunks.Encode(), longest+1, func(frame []byte) error {
			if refused {
				return peerError("sent more after it refused")
			}
			sent, err := DecodeFrame(frame)
			if err != nil {
				return err
			}
			switch sent.Kind {
			case FrameRefused:
				refused = true
				return nil
			case FrameChunk:
				if taken >= len(items) {
					return tooMuch()
				}
				item := items[taken]
				if err := checkChunk(item, sent.Bytes, "a chunk"); err != nil {
					return err
				}
				chunkEnd := chunkStart + uint64(item.Length)
				from := at - chunkStart
				to := min(end, chunkEnd) - chunkStart
				if err := writeAll(out, sent.Bytes[from:to]); err != nil {
					return streamError("write a file's bytes", err)
				}
				at = min(end, chunkEnd)
				chunkStart = chunkEnd
				taken++
				return nil
			}
			return otherKind()
		})
		if err != nil {
			return err
		}
		if taken == 0 {
			return peerError("does not hold the chunks asked for")
		}
	}
	return nil
}

// Have gives which chunks of a file the peer says it holds, one bit a
// chunk. This is what the peer says about itself: nothing proves it, and a
// chunk it claims is still checked when it comes. Only the number of chunks
// is held to what the file's size allows.
func (r *Remote) Have(file Target) (*HaveMap, error) {
	if err := file.require(KindFile); err != nil {
		return nil, err
	}
	chunking := r.record.Chunking()
	_, most := chunking.CountBounds(file.Size)
	frame, err := askOne(r.peer, RequestHave(file.Hash), timesPlus((most+7)/8, 1, 9))
	if err != nil {
		return nil, err
	}
	shown, err := DecodeFrame(frame)
	if err != nil {
		return nil, err
	}
	if shown.Kind != FrameHave {
		return nil, otherKind()
	}
	if err := chunking.CheckCount(file.Size, shown.Have.Count()); err != nil {
		return nil, err
	}
	return shown.Have, nil
}

// writtenList is a list that a bundle has written already.
type writtenList struct {
	kind Kind
	hash Hash
}

// Export writes a whole tree to out as one stream: the bundle's first
// bytes, the record, and then every list and every chunk once, each after
// what names it. Import reads it back. If out has a Flush method, as a
// buffered writer has, it is called once the bundle is written.
func Export(store Store, root Root, out io.Writer) error {
	record, err := RecordOf(store, root)
	if err != nil {
		return err
	}
	if err := writeAll(out, []byte(BundleMagic)); err != nil {
		return streamError("write a bundle", err)
	}
	if err := WriteFrame(out, record.Encode()); err != nil {
		return err
	}
	lists := make(map[writtenList]bool)
	chunks := make(map[Hash]bool)
	pending := []Target{record.Top()}
	for len(pending) > 0 {
		target := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if lists[writtenList{target.Kind, target.Hash}] {
			continue
		}
		lists[writtenList{target.Kind, target.Hash}] = true
		if target.Kind == KindFolder {
			list, err := FolderOf(store, target)
			if err != nil {
				return err
			}
			if err := writeFramed(out, FrameFolderList, list.Encode()); err != nil {
				return err
			}
			entries := list.Entries()
			for at := len(entries) - 1; at >= 0; at-- {
				pending = append(pending, entries[at].Target())
			}
			continue
		}
		list, err := FileOf(store, target)
		if err != nil {
			return err
		}
		if err := writeFramed(out, FrameFileList, list.Encode()); err != nil {
			return err
		}
		for _, item := range list.Items() {
			if chunks[item.Hash] {
				continue
			}
			chunks[item.Hash] = true
			kept, err := heldChunk(store, item)
			if err != nil {
				return err
			}
			if err := writeFramed(out, FrameChunk, kept); err != nil {
				return err
			}
		}
	}
	return flush(out, "write a bundle")
}

// Import reads a bundle that Export wrote, and takes the tree with this
// root from it into a store. Every frame goes through the same checks as a
// frame from a peer, and the tree must be whole when the stream ends. A
// frame for something the store holds already is passed over. A frame that
// nothing in the tree names is an error.
func Import(store Store, root Root, from io.Reader) (*Summary, error) {
	input := steadied(from)
	var magic [len(BundleMagic)]byte
	if _, err := io.ReadFull(input, magic[:]); err != nil {
		return nil, streamError("read a bundle", err)
	}
	if string(magic[:]) != BundleMagic {
		return nil, invalid("a stream does not start as a bundle does")
	}
	intake, err := NewIntake(store, root)
	if err != nil {
		return nil, err
	}
	for {
		next, frame, err := readNext(input, intake.FrameLimit())
		if err != nil {
			return nil, err
		}
		if next == nextClosed {
			break
		}
		if next == nextEnd {
			return nil, invalid("a frame is empty")
		}
		if _, err := intake.Accept(frame); err != nil {
			return nil, err
		}
	}
	return intake.Finish()
}

// flush sends on what a writer has kept back, if it keeps anything back.
func flush(output io.Writer, operation string) error {
	if buffered, keeps := output.(interface{ Flush() error }); keeps {
		if err := buffered.Flush(); err != nil {
			return streamError(operation, err)
		}
	}
	return nil
}

// serveBufferBytes is how many bytes of requests Serve takes off a
// connection at once: more than an asker may send ahead.
const serveBufferBytes = 2 * RequestsAheadBytes

// Serve answers the requests that come in on a connection, until it ends.
//
// A request comes as a frame does: its length in four bytes, and then its
// bytes. The answer goes out as its frames, each with its length before it,
// and then a length of zero. Requests are answered one at a time, in the
// order they come. If output has a Flush method, as a buffered writer has,
// it is called after each answer.
//
// A request that is malformed or too long ends the serving with an error.
// What is not held is answered with a refusal, and the serving goes on.
//
// Serve reads from the connection until it ends, and may read requests
// before it answers the ones before them.
func Serve(store Store, from io.Reader, output io.Writer) error {
	// Requests are read through a buffer that holds more than an asker may
	// send ahead. What was sent ahead is taken off the connection as soon as
	// the connection hands it over, and waits here while its answers are
	// written. So a connection that holds nothing of its own, as net.Pipe,
	// leaves no two ends waiting for each other.
	input := steadied(bufio.NewReaderSize(steadied(from), serveBufferBytes))
	responder := NewResponder(store)
	for {
		next, request, err := readNext(input, MaxRequestBytes)
		if err != nil {
			return err
		}
		switch next {
		case nextEnd:
			return invalid("a request is empty")
		case nextClosed:
			return nil
		}
		err = responder.Answer(request, func(frame []byte) error {
			return WriteFrame(output, frame)
		})
		if err != nil {
			return err
		}
		if err := writeEnd(output); err != nil {
			return err
		}
		if err := flush(output, "send an answer"); err != nil {
			return err
		}
	}
}

// StreamPeer is a peer at the other end of a connection, where Serve
// answers.
//
// An answer that is left half read, because a frame of it was refused,
// leaves the connection where nobody knows what comes next. Nothing more is
// asked on it then: open a new one.
type StreamPeer struct {
	input  *steady
	output io.Writer
	// ahead is how many bytes of requests are sent ahead of their answers.
	ahead  int
	broken bool
}

// NewStreamPeer makes a peer of a connection: input is what the other end
// sends, and output is what it reads. If output has a Flush method, as a
// buffered writer has, it is called once requests are written.
//
// Requests are sent ahead of their answers, up to RequestsAheadBytes of
// them, and the connection has to hold that much on the way: see Ahead.
func NewStreamPeer(input io.Reader, output io.Writer) *StreamPeer {
	return &StreamPeer{input: steadied(input), output: output, ahead: RequestsAheadBytes}
}

// Ahead sets how many bytes of requests are sent ahead of their answers:
// RequestsAheadBytes unless it is set, and never more than that. It is set
// before the peer is used, and gives the peer back.
//
// Requests that are sent ahead save a wait for each answer, which is most of
// the time a fetch of many small files takes on a slow connection. While the
// other end answers the first of them, the rest have to wait somewhere. A
// connection over a network holds them. Go's net.Pipe and io.Pipe hold
// nothing, and hand a write over only as the other end reads it: there Serve
// holds them, which reads requests through a buffer of its own. So a
// StreamPeer and a Serve do not wait for each other on any of these.
//
// Zero sends one request at a time, and waits for its answer. It is for an
// answerer of another make, which reads one request at a time from a
// connection that holds nothing. With requests sent ahead there, this end
// would wait to send its next request while the other end waits to send its
// first answer, for ever.
func (p *StreamPeer) Ahead(bytes int) *StreamPeer {
	p.ahead = min(max(bytes, 0), RequestsAheadBytes)
	return p
}

// Ask sends one request, and reads its answer.
func (p *StreamPeer) Ask(request []byte, limit int, onFrame func(frame []byte) error) error {
	return p.AskAll([]Asked{{Request: request, Limit: limit}}, func(_ int, frame []byte) error {
		return onFrame(frame)
	})
}

// AskAll sends requests ahead of their answers, as far as Ahead lets it,
// and reads the answers in the order of the requests.
//
// A request that no answerer takes, because it is empty or longer than a
// request may be, is an error before anything is sent.
func (p *StreamPeer) AskAll(requests []Asked, onFrame func(place int, frame []byte) error) error {
	if p.broken {
		return peerError("is on a connection that an earlier answer left half read")
	}
	for _, asked := range requests {
		switch length := len(asked.Request); {
		case length == 0:
			return invalid("a request is empty")
		case length > MaxRequestBytes:
			return limitExceeded("a request", MaxRequestBytes, uint64(length))
		}
	}
	p.broken = true
	answered := 0
	var flight []byte
	for answered < len(requests) {
		// Send ahead what the connection is sure to hold, and one request
		// at the least. They go out in one write, so that a connection
		// which hands a write over whole hands them over together.
		sent := answered
		flight = flight[:0]
		for sent < len(requests) {
			request := requests[sent].Request
			if sent > answered && len(flight)+len(request)+4 > p.ahead {
				break
			}
			flight = binary.BigEndian.AppendUint32(flight, uint32(len(request)))
			flight = append(flight, request...)
			sent++
		}
		if err := writeAll(p.output, flight); err != nil {
			return streamError("send a request", err)
		}
		if err := flush(p.output, "send a request"); err != nil {
			return err
		}
		for place := answered; place < sent; place++ {
			for {
				next, frame, err := readNext(p.input, requests[place].Limit)
				if err != nil {
					return err
				}
				if next == nextEnd {
					break
				}
				if next == nextClosed {
					return peerError("closed the connection inside an answer")
				}
				if err := onFrame(place, frame); err != nil {
					return err
				}
			}
		}
		answered = sent
	}
	p.broken = false
	return nil
}
