package scionic

import (
	"bytes"
	"io"
	"strings"
	"sync"
)

// Syncing by file hash and piece number.
//
//   - A Responder answers a receiver's requests from a store.
//   - Fetch takes a whole tree from a peer into a store, from the top down,
//     and skips whatever the store holds already.
//   - A Remote reads parts of a tree straight from a peer: one name in a
//     folder, a run of entries, or a stretch of a file's bytes. It needs no
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
	// maxRunItems is the most items of a list that one answer shows.
	maxRunItems = 16_384
	// maxPiecesPerAnswer is the most pieces that one answer sends.
	maxPiecesPerAnswer = 256
	// runFixedBytes is the bytes of a run's frame that are there whatever it
	// shows: its first byte, the list's count, the run's start, how many
	// items it shows, and how many nodes it carries.
	runFixedBytes = 23
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
// of their answers. It is small on purpose. The other end answers one
// request before it reads the next, so requests that are sent ahead wait in
// the connection, and a connection that could not hold them would leave
// both ends waiting for each other. Every connection holds this much.
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

// folderRunLimit is the longest frame that shows entries entries of a
// folder.
func folderRunLimit(entries uint64) int {
	return timesPlus(entries, MaxEntryBytes, runFixedBytes+MaxCarriedNodes*(42+MaxNameBytes))
}

// fileRunLimit is the longest frame that shows items items of a file's
// list.
func fileRunLimit(items uint64) int {
	return timesPlus(items, ListItemBytes, runFixedBytes+MaxCarriedNodes*40)
}

// mostPiecesIn is the most pieces that length bytes of a file can lie in.
// All of them but the first and the last lie in the stretch whole, and no
// piece but a file's last is shorter than the tree's way of cutting allows.
func mostPiecesIn(cutting Cutting, length uint64) uint64 {
	shortest := uint64(FastCDCMinSize + 1)
	if size, fixed := cutting.Fixed(); fixed {
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

// Have sets whether a peer is told which pieces of a file are held here.
// Telling takes a look into the store for every piece of the file, so a
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
// onFrame. A request that is malformed is an error. A request for something
// the store does not hold is answered with a refusal.
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
	case AskPiece:
		return whole(StoredPiece, PieceFrame)
	case AskFolderRun:
		open, err := r.openFolder(asked.Hash)
		if err != nil {
			return err
		}
		if open == nil {
			return notHeld()
		}
		total := open.List().Count()
		if total == 0 {
			return onFrame(FolderRunFrame(&FolderRun{}))
		}
		if asked.Start >= total {
			return outOfRange()
		}
		end := min(total, asked.Start+min(uint64(asked.Count), maxRunItems))
		run, err := open.Open(asked.Start, end)
		if err != nil {
			return err
		}
		return onFrame(FolderRunFrame(run))
	case AskFolderName:
		open, err := r.openFolder(asked.Hash)
		if err != nil {
			return err
		}
		if open == nil {
			return notHeld()
		}
		run, err := open.OpenName(asked.Name)
		if err != nil {
			return err
		}
		return onFrame(FolderRunFrame(run))
	case AskFileRun:
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
		end := min(total, asked.Start+min(uint64(asked.Count), maxRunItems))
		run, err := open.Open(asked.Start, end)
		if err != nil {
			return err
		}
		return onFrame(FileRunFrame(run))
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
		first, after, err := open.PiecesFor(asked.Offset, min(asked.Length, size-asked.Offset))
		if err != nil {
			return err
		}
		run, err := open.Open(first, min(after, first+maxRunItems))
		if err != nil {
			return err
		}
		return onFrame(FileRunFrame(run))
	case AskPieces:
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
		items = items[:min(uint64(len(items)), uint64(asked.Count), maxPiecesPerAnswer)]
		for _, item := range items {
			kept, held, err := r.store.Get(StoredPiece, item.Hash)
			if err != nil {
				return err
			}
			if !held {
				return notHeld()
			}
			if err := onFrame(PieceFrame(kept)); err != nil {
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
			held, err := storeHas(r.store, StoredPiece, item.Hash)
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
// each file's list, then the pieces, by their file and their numbers in it.
// Whatever the store holds already is skipped by its hash, be it a piece, a
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

// Entries is a run of a folder's entries, as a peer showed it and as it was
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
	frame, err := askOne(r.peer, RequestFolderName(folder.Hash, name), folderRunLimit(1))
	if err != nil {
		return Entry{}, false, err
	}
	shown, err := DecodeFrame(frame)
	if err != nil {
		return Entry{}, false, err
	}
	if shown.Kind != FrameFolderRun {
		return Entry{}, false, otherKind()
	}
	return shown.FolderRun.CheckName(folder, name)
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
	frame, err := askOne(r.peer, RequestFolderRun(folder.Hash, start, count), folderRunLimit(uint64(count)))
	if err != nil {
		return nil, err
	}
	shown, err := DecodeFrame(frame)
	if err != nil {
		return nil, err
	}
	if shown.Kind != FrameFolderRun {
		return nil, otherKind()
	}
	run := shown.FolderRun
	if err := run.Check(folder); err != nil {
		return nil, err
	}
	if run.Count() != 0 && (run.Start() != start || uint64(len(run.Entries())) > uint64(count)) {
		return nil, notWanted("a run that was not asked for")
	}
	return &Entries{Total: run.Count(), Start: run.Start(), Entries: run.Entries()}, nil
}

// Read writes length bytes of a file, from byte offset, to out.
//
// The peer shows the run of the file's list that holds the stretch, and
// that proves each piece's number and the byte it starts at. Then the
// pieces are asked for by their numbers, and each is checked against the
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
	cutting := r.record.Cutting()
	at := offset
	for at < end {
		frame, err := askOne(r.peer, RequestFileBytes(file.Hash, at, end-at), fileRunLimit(mostPiecesIn(cutting, end-at)))
		if err != nil {
			return err
		}
		shown, err := DecodeFrame(frame)
		if err != nil {
			return err
		}
		if shown.Kind != FrameFileRun {
			return otherKind()
		}
		run := shown.FileRun
		runStart, err := run.CheckWithCutting(file, cutting)
		if err != nil {
			return err
		}
		items := run.Items()
		// The run has to begin with the piece that holds the first byte
		// still due, and may not reach past the piece that holds the last.
		var beforeLast uint64
		longest := 0
		for index, item := range items {
			if index < len(items)-1 {
				beforeLast += uint64(item.Length)
			}
			// No piece that is due is longer than the longest in the run.
			longest = max(longest, int(item.Length))
		}
		holdsFirst := runStart <= at && at < runStart+uint64(items[0].Length)
		if !holdsFirst || runStart+beforeLast >= end {
			return notWanted("a run that does not hold the bytes asked for")
		}
		taken := 0
		pieceStart := runStart
		refused := false
		pieces := RequestPieces(file.Hash, run.Start(), uint32(len(items)))
		err = r.peer.Ask(pieces.Encode(), longest+1, func(frame []byte) error {
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
			case FramePiece:
				if taken >= len(items) {
					return tooMuch()
				}
				item := items[taken]
				if err := checkPiece(item, sent.Bytes, "a piece"); err != nil {
					return err
				}
				pieceEnd := pieceStart + uint64(item.Length)
				from := at - pieceStart
				to := min(end, pieceEnd) - pieceStart
				if err := writeAll(out, sent.Bytes[from:to]); err != nil {
					return streamError("write a file's bytes", err)
				}
				at = min(end, pieceEnd)
				pieceStart = pieceEnd
				taken++
				return nil
			}
			return otherKind()
		})
		if err != nil {
			return err
		}
		if taken == 0 {
			return peerError("does not hold the pieces asked for")
		}
	}
	return nil
}

// Have gives which pieces of a file the peer says it holds, one bit a
// piece. This is what the peer says about itself: nothing proves it, and a
// piece it claims is still checked when it comes. Only the number of pieces
// is held to what the file's size allows.
func (r *Remote) Have(file Target) (*HaveMap, error) {
	if err := file.require(KindFile); err != nil {
		return nil, err
	}
	cutting := r.record.Cutting()
	_, most := cutting.CountBounds(file.Size)
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
	if err := cutting.CheckCount(file.Size, shown.Have.Count()); err != nil {
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
// bytes, the record, and then every list and every piece once, each after
// what names it. Import reads it back.
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
	pieces := make(map[Hash]bool)
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
			if err := WriteFrame(out, FolderListFrame(list.Encode())); err != nil {
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
		if err := WriteFrame(out, FileListFrame(list.Encode())); err != nil {
			return err
		}
		for _, item := range list.Items() {
			if pieces[item.Hash] {
				continue
			}
			pieces[item.Hash] = true
			kept, err := heldPiece(store, item)
			if err != nil {
				return err
			}
			if err := WriteFrame(out, PieceFrame(kept)); err != nil {
				return err
			}
		}
	}
	return nil
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
func Serve(store Store, from io.Reader, output io.Writer) error {
	input := steadied(from)
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
	broken bool
}

// NewStreamPeer makes a peer of a connection: input is what the other end
// sends, and output is what it reads. If output has a Flush method, as a
// buffered writer has, it is called once requests are written.
func NewStreamPeer(input io.Reader, output io.Writer) *StreamPeer {
	return &StreamPeer{input: steadied(input), output: output}
}

// Ask sends one request, and reads its answer.
func (p *StreamPeer) Ask(request []byte, limit int, onFrame func(frame []byte) error) error {
	return p.AskAll([]Asked{{Request: request, Limit: limit}}, func(_ int, frame []byte) error {
		return onFrame(frame)
	})
}

// AskAll sends requests ahead of their answers, as far as every connection
// is sure to hold them, and reads the answers in the order of the requests.
func (p *StreamPeer) AskAll(requests []Asked, onFrame func(place int, frame []byte) error) error {
	if p.broken {
		return peerError("is on a connection that an earlier answer left half read")
	}
	p.broken = true
	answered := 0
	for answered < len(requests) {
		// Send ahead what the connection is sure to hold, and one request
		// at the least.
		sent := answered
		ahead := 0
		for sent < len(requests) {
			length := len(requests[sent].Request) + 4
			if sent > answered && ahead+length > RequestsAheadBytes {
				break
			}
			if err := WriteFrame(p.output, requests[sent].Request); err != nil {
				return err
			}
			ahead += length
			sent++
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
