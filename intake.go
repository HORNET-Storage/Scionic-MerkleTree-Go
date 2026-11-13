package scionic

import (
	"bytes"
	"errors"
	"slices"
	"sync/atomic"
)

// Taking a tree in, and checking a tree that is held.
//
// There is one set of checks, and it is here. A tree that arrives from a
// peer, a tree read from a bundle and a tree that already sits in a store
// all go through an Intake:
//
//   - the record must hash to the root that was asked for;
//   - a list is taken only when a list that is already checked names it, and
//     only when it has the hash, the size and the piece lengths it has to;
//   - a piece is taken only when a file's list that is already checked names
//     it, and only with the bytes and the length that list gives it;
//   - at the end nothing may be missing, folders may not sit too deep, and
//     the record's totals must be those of the tree.
//
// So parents come first, and nothing is kept before it is tied to the root.
// A frame never says which hash it belongs under. Its hash is worked out
// from its bytes.

// piecesPerRequest is the most pieces one request asks for.
const piecesPerRequest = 64

// ArrivedKind is the kind of thing a frame turned out to be.
type ArrivedKind uint8

const (
	// ArrivedRecord is the tree's record.
	ArrivedRecord ArrivedKind = iota + 1
	// ArrivedFolderList is a folder's whole list.
	ArrivedFolderList
	// ArrivedFileList is a file's whole list.
	ArrivedFileList
	// ArrivedPiece is a piece.
	ArrivedPiece
)

// Arrived is what a frame turned out to be, once its hash was worked out:
// the record, or a list or a piece with that hash.
type Arrived struct {
	Kind ArrivedKind
	// Hash is the folder's, the file's or the piece's. A record has none.
	Hash Hash
}

// Accepted is what became of a frame that an intake accepted.
type Accepted struct {
	What Arrived
	// Kept says whether it was kept now. It is not when it was held
	// already.
	Kept bool
}

// Summary is a tree that is whole and checked.
type Summary struct {
	Record *Record
	// Folders is how many different folders the tree has.
	Folders uint64
	// Files is how many different files the tree has.
	Files uint64
}

// VerifyOptions says how a held tree is checked, beyond what is always
// checked. The zero value checks what is always checked, on every core.
type VerifyOptions struct {
	// Recut also cuts every file's bytes again, and refuses a file whose
	// list is not the one the cutter gives. With this, the same bytes
	// always have the same file hash. It costs one more pass over the
	// bytes.
	Recut bool
	// DiscardDamaged removes from the store what is damaged there: a piece,
	// a list or a record whose bytes are not what its hash says. The check
	// then ends with one of them as missing, and a fetch of the tree brings
	// them all again. Without this the check stops at a damaged thing and
	// names it, and the store is left as it is.
	DiscardDamaged bool
	// Workers is how many pieces are hashed at once. Zero is one for each
	// core.
	Workers int
}

type intakeMode struct {
	// recut: cut again the files that are held whole.
	recut bool
	// discard: remove from the store what is damaged there, and want it
	// again.
	discard bool
}

type folderSlot struct {
	size  uint64
	order uint64
	held  bool
	// inside are the folders directly inside this one.
	inside []Hash
}

type fileSlot struct {
	size  uint64
	order uint64
	held  bool
}

type pieceSlot struct {
	length uint32
	// wanted says that the piece is not held, and wantedAt where it was
	// first listed: the file, by the order it was named in, and the
	// piece's number there.
	wanted   bool
	wantedAt place
}

// Intake takes one tree into a store, checking every part as it arrives.
//
// An intake is given frames in any order that puts parents first. What the
// store holds already is found when it is named, and is not asked for.
//
// A frame that is refused at the door leaves the intake as it was: one that
// nothing names, one that is malformed, one that was not asked for. A list
// that is named and then fails a check shows that the tree itself is wrong.
// The list is not kept, and the intake is done: every later call gives
// ErrIntakeFailed. A new intake picks up from what the store holds.
//
// What the store holds under a hash and is not what the hash says is
// damaged. An intake removes it from the store and wants it again, so a tree
// that was harmed where it is kept is made whole by fetching it once more.
// Nothing a peer sends can make an intake remove what is sound.
//
// An intake is used by one goroutine at a time.
type Intake struct {
	store  Store
	root   Root
	mode   intakeMode
	record *Record
	// measuring is how the files are cut, for a tree that has no record
	// yet.
	measuring *Cutting
	folders   map[Hash]*folderSlot
	files     map[Hash]*fileSlot
	pieces    map[Hash]pieceSlot
	// wantedLists are the lists that are named and not held, in the order
	// they were named.
	wantedLists ordered[Target]
	// wantedPieces are the pieces that are named and not held, by where
	// they were first listed.
	wantedPieces ordered[Hash]
	fileByOrder  map[uint64]Hash
	nextOrder    uint64
	counted      Totals
	pending      []Target
	// toRecut are the files to cut again, once their pieces are hashed.
	toRecut []Hash
	cutter  *streamCutter
	// failed: a step that changes what the intake knows failed half way.
	failed bool
}

// NewIntake starts taking in the tree with this root. If the store holds
// the tree's record, everything else it holds of the tree is found now.
func NewIntake(store Store, root Root) (*Intake, error) {
	return newIntake(store, root, intakeMode{discard: true})
}

func bareIntake(store Store, root Root, mode intakeMode) *Intake {
	return &Intake{
		store:       store,
		root:        root,
		mode:        mode,
		folders:     make(map[Hash]*folderSlot),
		files:       make(map[Hash]*fileSlot),
		pieces:      make(map[Hash]pieceSlot),
		fileByOrder: make(map[uint64]Hash),
	}
}

func newIntake(store Store, root Root, mode intakeMode) (*Intake, error) {
	intake := bareIntake(store, root, mode)
	record, err := loadRecord(store, root)
	switch {
	case err == nil && record != nil:
		if err := intake.begin(record); err != nil {
			return nil, err
		}
	case err == nil:
		// The record is not held, and is asked for.
	case errors.Is(err, ErrCorrupt) && mode.discard:
		// A record that is damaged where it is kept is fetched again.
		if err := store.Remove(StoredRecord, root.Hash()); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	return intake, nil
}

// measure checks every list under top as an intake checks it, for a tree
// that has no record yet, and counts what its record has to say: each
// different piece and each different list, once. The store must hold every
// list. This is how a builder comes by a record's totals, so a tree that is
// built has passed the checks of a tree that is taken in.
func measure(store Store, top Target, cutting Cutting) (Totals, error) {
	intake := bareIntake(store, Root{}, intakeMode{})
	intake.measuring = &cutting
	if err := intake.refer(top); err != nil {
		return Totals{}, err
	}
	if err := intake.drain(); err != nil {
		return Totals{}, err
	}
	if err := intake.listsHeld(); err != nil {
		return Totals{}, err
	}
	if err := intake.checkDepth(top); err != nil {
		return Totals{}, err
	}
	return intake.counted, nil
}

// Root is the root of the tree that is taken in.
func (i *Intake) Root() Root {
	return i.root
}

// Record is the tree's record, once it is in, and nil before. Its totals
// say how much the tree will take before any of it is taken, and an intake
// stops as soon as a tree turns out to hold more than its record says.
func (i *Intake) Record() *Record {
	return i.record
}

// Outstanding is how many lists and pieces are named and not held yet. The
// record counts as one while it is not in.
func (i *Intake) Outstanding() int {
	outstanding := i.wantedLists.len() + i.wantedPieces.len()
	if i.record == nil {
		outstanding++
	}
	return outstanding
}

// clampInt gives a number as an int, and the largest int for a number that
// is larger.
func clampInt(value uint64) int {
	if value > uint64(maxInt) {
		return maxInt
	}
	return int(value)
}

// addOne gives the number after value, and never wraps.
func addOne(value int) int {
	if value == maxInt {
		return maxInt
	}
	return value + 1
}

// listsLeft is how many bytes of lists the record still leaves room for.
func (i *Intake) listsLeft() int {
	if i.record == nil {
		return 0
	}
	recorded := i.record.Totals().ListBytes
	if recorded <= i.counted.ListBytes {
		return 0
	}
	return clampInt(recorded - i.counted.ListBytes)
}

// longestPiece is the longest piece the tree's way of cutting allows.
func (i *Intake) longestPiece() int {
	if i.record != nil {
		if size, fixed := i.record.Cutting().Fixed(); fixed {
			return int(size)
		}
	}
	return MaxPieceBytes
}

// FrameLimit is the longest frame that a stream of this tree can hold,
// whatever comes next: for a reader of a stream that was not asked frame by
// frame, as a bundle is. Before the record it is the longest record. After
// it, it is the longest of a record, a piece, and all the bytes of lists
// that the record gives the tree. What the store holds already may come
// too, and is passed over, so this does not shrink as lists come.
func (i *Intake) FrameLimit() int {
	if i.record == nil {
		return MaxRecordBytes
	}
	limit := max(clampInt(i.record.Totals().ListBytes), i.longestPiece())
	return max(addOne(limit), MaxRecordBytes)
}

// AnswerLimit is the longest frame that can answer one of this intake's
// requests. A peer that sends a longer one is not answering what was asked,
// and its frame need not be read: a file's list is no longer than the
// file's size allows, no list is longer than the record leaves room for,
// and a piece is no longer than the tree's way of cutting allows.
func (i *Intake) AnswerLimit(request Request) int {
	limit := 0
	switch request.Kind {
	case AskRecord:
		limit = MaxRecordBytes
	case AskFolder:
		limit = addOne(i.listsLeft())
	case AskFile:
		var most uint64
		if slot := i.files[request.Hash]; i.record != nil && slot != nil {
			_, most = i.record.Cutting().CountBounds(slot.size)
		}
		bytes := uint64(maxInt)
		if most <= uint64(maxInt)/ListItemBytes {
			bytes = most * ListItemBytes
		}
		limit = addOne(min(clampInt(bytes), i.listsLeft()))
	case AskPieces, AskPiece:
		limit = i.longestPiece() + 1
	}
	// A refusal takes two bytes, and is always let through.
	return max(limit, 2)
}

func (i *Intake) begin(record *Record) error {
	i.record = record
	if err := i.refer(record.Top()); err != nil {
		return err
	}
	return i.drain()
}

func (i *Intake) recorded() (*Record, error) {
	if i.record == nil {
		return nil, notWanted("a frame that comes before the record")
	}
	return i.record, nil
}

// cutting is how the tree's files are cut.
func (i *Intake) cutting() (Cutting, error) {
	if i.measuring != nil {
		return *i.measuring, nil
	}
	record, err := i.recorded()
	if err != nil {
		return Cutting{}, err
	}
	return record.Cutting(), nil
}

// listsHeld: every list that is named has to be held.
func (i *Intake) listsHeld() error {
	first := i.wantedLists.first()
	if first == nil {
		return nil
	}
	if first.value.Kind == KindFolder {
		return missing("the list of the folder", first.value.Hash)
	}
	return missing("the list of the file", first.value.Hash)
}

// damaged: the store holds, under hash, bytes that are not what the hash
// says. They are removed when this intake may do that, so that they are
// wanted again. Otherwise they are an error that names them.
func (i *Intake) damaged(kind Stored, hash Hash, what string) error {
	if i.mode.discard {
		return i.store.Remove(kind, hash)
	}
	return corrupt(what, hash)
}

// holds reports whether the store holds the piece that item names. The
// piece's bytes are counted here, not hashed.
func (i *Intake) holds(item PieceRef) (bool, error) {
	size, found, err := storeSize(i.store, StoredPiece, item.Hash)
	if err != nil || !found {
		return false, err
	}
	if size == uint64(item.Length) {
		return true, nil
	}
	// Either the store's bytes are damaged, or the list gives the piece a
	// length it does not have. Only damaged bytes are ever removed: a list
	// that names a sound piece wrongly must not cost the store it.
	kept, found, err := i.store.Get(StoredPiece, item.Hash)
	if err != nil || !found {
		return false, err
	}
	if HashOf(kept) == item.Hash {
		return false, sizeMismatch("a held piece", uint64(item.Length), size)
	}
	if err := i.damaged(StoredPiece, item.Hash, "a stored piece"); err != nil {
		return false, err
	}
	return false, nil
}

// refer notes that a checked parent names target.
func (i *Intake) refer(target Target) error {
	var known *uint64
	switch target.Kind {
	case KindFolder:
		if slot := i.folders[target.Hash]; slot != nil {
			known = &slot.size
		}
	case KindFile:
		if slot := i.files[target.Hash]; slot != nil {
			known = &slot.size
		}
	default:
		return invalid("an entry is neither a file nor a folder")
	}
	if known != nil {
		if *known != target.Size {
			return sizeMismatch("a file or folder that is named twice", *known, target.Size)
		}
		return nil
	}
	order := i.nextOrder
	i.nextOrder++
	if target.Kind == KindFolder {
		i.folders[target.Hash] = &folderSlot{size: target.Size, order: order}
	} else {
		i.files[target.Hash] = &fileSlot{size: target.Size, order: order}
		i.fileByOrder[order] = target.Hash
	}
	i.pending = append(i.pending, target)
	return nil
}

// drain looks in the store for every list that was newly named. A list
// that is there is checked like one that arrives, and the lists it names
// are looked for in turn. A list that is not there is wanted, and so is one
// that is damaged there.
func (i *Intake) drain() error {
	for len(i.pending) > 0 {
		target := i.pending[len(i.pending)-1]
		i.pending = i.pending[:len(i.pending)-1]
		if target.Kind == KindFolder {
			kept, found, err := i.store.Get(StoredFolderList, target.Hash)
			if err != nil {
				return err
			}
			if !found {
				i.want(target)
				continue
			}
			list, err := DecodeFolderList(kept)
			if err != nil || list.Hash() != target.Hash {
				if err := i.damaged(StoredFolderList, target.Hash, "a stored folder's list"); err != nil {
					return err
				}
				i.want(target)
				continue
			}
			if err := i.admitFolder(list); err != nil {
				return err
			}
			continue
		}
		kept, found, err := i.store.Get(StoredFileList, target.Hash)
		if err != nil {
			return err
		}
		if !found {
			i.want(target)
			continue
		}
		list, err := DecodeFileList(kept)
		if err != nil || list.Hash() != target.Hash {
			if err := i.damaged(StoredFileList, target.Hash, "a stored file's list"); err != nil {
				return err
			}
			i.want(target)
			continue
		}
		if err := i.admitFile(list); err != nil {
			return err
		}
	}
	return nil
}

// orderOf gives the order that a named file or folder was named in.
func (i *Intake) orderOf(target Target) (uint64, bool) {
	if target.Kind == KindFolder {
		if slot := i.folders[target.Hash]; slot != nil {
			return slot.order, true
		}
		return 0, false
	}
	if slot := i.files[target.Hash]; slot != nil {
		return slot.order, true
	}
	return 0, false
}

func (i *Intake) want(target Target) {
	if order, named := i.orderOf(target); named {
		i.wantedLists.put(place{major: order}, target)
	}
}

func (i *Intake) countListBytes(length uint64) error {
	total := i.counted.ListBytes + length
	if total < length {
		return sizeOverflow("list bytes")
	}
	i.counted.ListBytes = total
	// A tree that is being measured has no record to be held to yet.
	if i.record == nil {
		return nil
	}
	if recorded := i.record.Totals().ListBytes; total > recorded {
		return totalsMismatch("list bytes", recorded, total)
	}
	return nil
}

func totalsMismatch(field string, recorded, counted uint64) error {
	return &Error{kind: kindTotalsMismatch, What: field, Expected: recorded, Actual: counted}
}

func (i *Intake) countPiece(length uint32) error {
	pieces := i.counted.Pieces + 1
	if pieces == 0 {
		return sizeOverflow("pieces")
	}
	pieceBytes := i.counted.PieceBytes + uint64(length)
	if pieceBytes < uint64(length) {
		return sizeOverflow("piece bytes")
	}
	i.counted.Pieces = pieces
	i.counted.PieceBytes = pieceBytes
	if i.record == nil {
		return nil
	}
	recorded := i.record.Totals()
	if pieces > recorded.Pieces {
		return totalsMismatch("pieces", recorded.Pieces, pieces)
	}
	if pieceBytes > recorded.PieceBytes {
		return totalsMismatch("piece bytes", recorded.PieceBytes, pieceBytes)
	}
	return nil
}

// admitFolder takes a folder's list that is named, and notes what it names.
func (i *Intake) admitFolder(list *FolderList) error {
	slot := i.folders[list.Hash()]
	if slot == nil {
		return invariant("a folder is admitted before it is named")
	}
	if slot.size != list.Size() {
		return sizeMismatch("a folder", slot.size, list.Size())
	}
	slot.held = true
	slot.inside = slot.inside[:0]
	for _, entry := range list.Entries() {
		if entry.Kind() == KindFolder {
			slot.inside = append(slot.inside, entry.Hash())
		}
	}
	slices.SortFunc(slot.inside, func(left, right Hash) int { return bytes.Compare(left[:], right[:]) })
	slot.inside = slices.Compact(slot.inside)
	i.wantedLists.remove(place{major: slot.order})
	if err := i.countListBytes(list.EncodedLen()); err != nil {
		return err
	}
	for _, entry := range list.Entries() {
		if err := i.refer(entry.Target()); err != nil {
			return err
		}
	}
	return nil
}

// admitFile takes a file's list that is named, and notes the pieces it
// names.
func (i *Intake) admitFile(list *FileList) error {
	cutting, err := i.cutting()
	if err != nil {
		return err
	}
	if err := list.CheckCutting(cutting); err != nil {
		return err
	}
	slot := i.files[list.Hash()]
	if slot == nil {
		return invariant("a file is admitted before it is named")
	}
	if slot.size != list.Size() {
		return sizeMismatch("a file", slot.size, list.Size())
	}
	slot.held = true
	order := slot.order
	i.wantedLists.remove(place{major: order})
	if err := i.countListBytes(list.EncodedLen()); err != nil {
		return err
	}
	for number, item := range list.Items() {
		if known, listed := i.pieces[item.Hash]; listed {
			if known.length != item.Length {
				return sizeMismatch("a piece that is listed with two lengths", uint64(known.length), uint64(item.Length))
			}
			continue
		}
		if err := i.countPiece(item.Length); err != nil {
			return err
		}
		held, err := i.holds(item)
		if err != nil {
			return err
		}
		piece := pieceSlot{length: item.Length}
		if !held {
			piece.wanted = true
			piece.wantedAt = place{major: order, minor: uint64(number)}
			i.wantedPieces.put(piece.wantedAt, item.Hash)
		}
		i.pieces[item.Hash] = piece
	}
	if _, fixed := cutting.Fixed(); i.mode.recut && !fixed {
		i.toRecut = append(i.toRecut, list.Hash())
	}
	return nil
}

// rehashHeld hashes again every piece of this tree that the store holds,
// several at once. A piece whose bytes are not what its hash says is
// damaged.
func (i *Intake) rehashHeld(workers int) error {
	held := make([]PieceRef, 0, len(i.pieces))
	for hash, slot := range i.pieces {
		if !slot.wanted {
			held = append(held, PieceRef{Hash: hash, Length: slot.length})
		}
	}
	slices.SortFunc(held, func(left, right PieceRef) int { return bytes.Compare(left.Hash[:], right.Hash[:]) })
	store := i.store
	workers = max(min(workerCount(workers), len(held)), 1)
	var next atomic.Int64
	// The places, in held, of the pieces that are damaged, for each worker.
	spoiledBy := make([][]int, workers)
	results := runWorkers(workers, nil, func(worker int) error {
		for {
			index := next.Add(1) - 1
			if index >= int64(len(held)) {
				return nil
			}
			item := held[index]
			kept, found, err := store.Get(StoredPiece, item.Hash)
			if err == nil && !found {
				err = missing("the piece", item.Hash)
			}
			if err != nil {
				next.Store(int64(len(held)))
				return err
			}
			if !item.Matches(kept) {
				spoiledBy[worker] = append(spoiledBy[worker], int(index))
			}
		}
	})
	for _, err := range results {
		if err != nil {
			return err
		}
	}
	spoiled := slices.Concat(spoiledBy...)
	slices.Sort(spoiled)
	for _, index := range spoiled {
		if err := i.damaged(StoredPiece, held[index].Hash, "a stored piece"); err != nil {
			return err
		}
	}
	if len(spoiled) > 0 {
		return missing("the piece", held[spoiled[0]].Hash)
	}
	return nil
}

// recutHeld cuts again every file that is held whole, and holds the result
// against the file's list. The pieces were hashed before this.
func (i *Intake) recutHeld() error {
	toRecut := i.toRecut
	i.toRecut = nil
	for _, file := range toRecut {
		kept, found, err := i.store.Get(StoredFileList, file)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		list, err := DecodeFileList(kept)
		if err != nil {
			return err
		}
		whole := true
		for _, item := range list.Items() {
			if slot, listed := i.pieces[item.Hash]; !listed || slot.wanted {
				whole = false
				break
			}
		}
		if whole {
			if err := i.recut(list); err != nil {
				return err
			}
		}
	}
	return nil
}

// recut cuts a file's bytes again, and holds the result against its list.
func (i *Intake) recut(list *FileList) error {
	if i.cutter == nil {
		i.cutter = &streamCutter{}
	}
	items := list.Items()
	unread, uncut := 0, 0
	var current []byte
	miscut := func() error {
		return invalid("a file is not cut where its bytes say it is cut")
	}
	err := i.cutter.cut(FastCDCV1, 2*MaxPieceBytes,
		func(buffer []byte) (int, error) {
			for len(current) == 0 {
				if unread == len(items) {
					return 0, nil
				}
				item := items[unread]
				unread++
				kept, found, err := i.store.Get(StoredPiece, item.Hash)
				if err != nil {
					return 0, err
				}
				if !found || uint64(len(kept)) != uint64(item.Length) {
					return 0, missing("the piece", item.Hash)
				}
				current = kept
			}
			count := copy(buffer, current)
			current = current[count:]
			return count, nil
		},
		func(pieces [][]byte) error {
			for _, piece := range pieces {
				if uncut == len(items) || uint64(items[uncut].Length) != uint64(len(piece)) {
					return miscut()
				}
				uncut++
			}
			return nil
		})
	if err != nil {
		return err
	}
	if uncut != len(items) {
		return miscut()
	}
	return nil
}

// Accept takes one frame: a record, a whole list, or a piece.
//
// A frame is accepted when it is what a checked parent names. A frame that
// is named and held already is accepted and not kept again. Any other frame
// is an error: one that nothing names, one that is malformed, and one that
// does not have the size it was given.
func (i *Intake) Accept(frame []byte) (Accepted, error) {
	return i.accept(frame, nil)
}

// AcceptExpected takes one frame, which has to be exactly expected: the
// answer to what was asked. Anything else is refused, and nothing of it is
// kept.
func (i *Intake) AcceptExpected(frame []byte, expected Arrived) (Accepted, error) {
	return i.accept(frame, &expected)
}

// taking runs a step that changes what the intake knows of its tree. If the
// step fails, the intake knows half of something, and is done.
func (i *Intake) taking(step func() error) error {
	err := step()
	if err != nil {
		i.failed = true
	}
	return err
}

func (i *Intake) accept(frame []byte, expected *Arrived) (Accepted, error) {
	if i.failed {
		return Accepted{}, &Error{kind: kindIntakeFailed}
	}
	if len(frame) == 0 {
		return Accepted{}, invalid("a frame is empty")
	}
	body := frame[1:]
	asAsked := func(arrived Arrived, what string) error {
		if expected != nil && *expected != arrived {
			return notWanted(what)
		}
		return nil
	}
	switch FrameKind(frame[0]) {
	case FrameRecord:
		what := Arrived{Kind: ArrivedRecord}
		if err := asAsked(what, "a record"); err != nil {
			return Accepted{}, err
		}
		if RootOfRecord(frame) != i.root {
			return Accepted{}, hashMismatch("a record")
		}
		if i.record != nil {
			return Accepted{What: what}, nil
		}
		record, err := DecodeRecord(frame)
		if err != nil {
			return Accepted{}, err
		}
		if err := i.store.Put(StoredRecord, i.root.Hash(), frame); err != nil {
			return Accepted{}, err
		}
		if err := i.taking(func() error { return i.begin(record) }); err != nil {
			return Accepted{}, err
		}
		return Accepted{What: what, Kept: true}, nil
	case FrameFolderList:
		if _, err := i.recorded(); err != nil {
			return Accepted{}, err
		}
		list, err := DecodeFolderList(body)
		if err != nil {
			return Accepted{}, err
		}
		what := Arrived{Kind: ArrivedFolderList, Hash: list.Hash()}
		if err := asAsked(what, "a folder's list"); err != nil {
			return Accepted{}, err
		}
		slot := i.folders[list.Hash()]
		switch {
		case slot == nil:
			return Accepted{}, notWanted("a folder's list")
		case slot.held:
			return Accepted{What: what}, nil
		case slot.size != list.Size():
			return Accepted{}, sizeMismatch("a folder", slot.size, list.Size())
		}
		// The list is kept only once all it says has passed.
		err = i.taking(func() error {
			if err := i.admitFolder(list); err != nil {
				return err
			}
			if err := i.store.Put(StoredFolderList, list.Hash(), body); err != nil {
				return err
			}
			return i.drain()
		})
		if err != nil {
			return Accepted{}, err
		}
		return Accepted{What: what, Kept: true}, nil
	case FrameFileList:
		if _, err := i.recorded(); err != nil {
			return Accepted{}, err
		}
		list, err := DecodeFileList(body)
		if err != nil {
			return Accepted{}, err
		}
		what := Arrived{Kind: ArrivedFileList, Hash: list.Hash()}
		if err := asAsked(what, "a file's list"); err != nil {
			return Accepted{}, err
		}
		slot := i.files[list.Hash()]
		switch {
		case slot == nil:
			return Accepted{}, notWanted("a file's list")
		case slot.held:
			return Accepted{What: what}, nil
		case slot.size != list.Size():
			return Accepted{}, sizeMismatch("a file", slot.size, list.Size())
		}
		// The list is kept only once all it says has passed.
		err = i.taking(func() error {
			if err := i.admitFile(list); err != nil {
				return err
			}
			return i.store.Put(StoredFileList, list.Hash(), body)
		})
		if err != nil {
			return Accepted{}, err
		}
		return Accepted{What: what, Kept: true}, nil
	case FramePiece:
		if _, err := i.recorded(); err != nil {
			return Accepted{}, err
		}
		if len(body) == 0 || len(body) > MaxPieceBytes {
			return Accepted{}, invalid("a piece does not hold 1 byte to 8 MiB")
		}
		hash := HashOf(body)
		what := Arrived{Kind: ArrivedPiece, Hash: hash}
		if err := asAsked(what, "a piece"); err != nil {
			return Accepted{}, err
		}
		slot, listed := i.pieces[hash]
		if !listed {
			return Accepted{}, notWanted("a piece")
		}
		if !slot.wanted {
			return Accepted{What: what}, nil
		}
		if uint64(slot.length) != uint64(len(body)) {
			return Accepted{}, sizeMismatch("a piece", uint64(slot.length), uint64(len(body)))
		}
		if err := i.store.Put(StoredPiece, hash, body); err != nil {
			return Accepted{}, err
		}
		i.wantedPieces.remove(slot.wantedAt)
		i.pieces[hash] = pieceSlot{length: slot.length}
		return Accepted{What: what, Kept: true}, nil
	}
	return Accepted{}, invalid("a frame that no tree is taken in by")
}

// Requests gives what to ask a peer for next: at most limit requests. The
// record comes first, then lists, then pieces by their file and their
// numbers. Nothing is asked for twice while it is still wanted, so the
// caller asks again only after the answers have come, or have failed to.
func (i *Intake) Requests(limit int) []Request {
	if i.failed {
		return nil
	}
	if i.record == nil {
		return []Request{RequestRecord(i.root)}
	}
	var requests []Request
	for node := i.wantedLists.first(); node != nil && len(requests) < limit; node = node.after() {
		if node.value.Kind == KindFolder {
			requests = append(requests, RequestFolder(node.value.Hash))
		} else {
			requests = append(requests, RequestFile(node.value.Hash))
		}
	}
	// A run of pieces of one file whose numbers follow one another.
	var run struct {
		started bool
		order   uint64
		start   uint64
		count   uint32
	}
	push := func() {
		if file, named := i.fileByOrder[run.order]; named {
			requests = append(requests, RequestPieces(file, run.start, run.count))
		}
		run.started = false
	}
	for node := i.wantedPieces.first(); node != nil; node = node.after() {
		order, number := node.at.major, node.at.minor
		if run.started && run.order == order && run.start+uint64(run.count) == number && run.count < piecesPerRequest {
			run.count++
			continue
		}
		if run.started {
			push()
		}
		if len(requests) >= limit {
			break
		}
		run.started, run.order, run.start, run.count = true, order, number, 1
	}
	if run.started && len(requests) < limit {
		push()
	}
	return requests
}

// Expected gives what the answer to one of this intake's requests has to
// be: the frames, in order. A peer may send fewer pieces than were asked
// for, and never anything else.
func (i *Intake) Expected(request Request) []Arrived {
	switch request.Kind {
	case AskRecord:
		return []Arrived{{Kind: ArrivedRecord}}
	case AskFolder:
		return []Arrived{{Kind: ArrivedFolderList, Hash: request.Hash}}
	case AskFile:
		return []Arrived{{Kind: ArrivedFileList, Hash: request.Hash}}
	case AskPieces:
		slot := i.files[request.Hash]
		if slot == nil {
			return nil
		}
		var expected []Arrived
		for offset := uint64(0); offset < uint64(request.Count); offset++ {
			number := request.Start + offset
			if number < request.Start {
				break
			}
			hash, wanted := i.wantedPieces.get(place{major: slot.order, minor: number})
			if !wanted {
				break
			}
			expected = append(expected, Arrived{Kind: ArrivedPiece, Hash: hash})
		}
		return expected
	}
	return nil
}

// checkDepth: no path from the top may pass through more folders than the
// format allows. A folder that sits at several places is measured once.
func (i *Intake) checkDepth(top Target) error {
	if top.Kind != KindFolder {
		return nil
	}
	tooDeep := func() error {
		return &Error{kind: kindTooDeep, Limit: MaxFolderDepth}
	}
	// How many folders the longest path down from a folder passes through,
	// that folder counted.
	heights := make(map[Hash]int)
	type step struct {
		folder Hash
		next   int
	}
	path := []step{{folder: top.Hash}}
	for len(path) > 0 {
		last := &path[len(path)-1]
		var inside []Hash
		if slot := i.folders[last.folder]; slot != nil {
			inside = slot.inside
		}
		if last.next < len(inside) {
			child := inside[last.next]
			last.next++
			height, measured := heights[child]
			switch {
			case measured && len(path)+height > MaxFolderDepth:
				return tooDeep()
			case measured:
			case len(path) >= MaxFolderDepth:
				return tooDeep()
			default:
				path = append(path, step{folder: child})
			}
			continue
		}
		height := 0
		for _, child := range inside {
			if below, measured := heights[child]; measured && below > height {
				height = below
			}
		}
		heights[last.folder] = height + 1
		path = path[:len(path)-1]
	}
	return nil
}

// Finish ends the intake. The tree must be whole: the record in, every list
// and every piece held, no folder too deep, and the record's totals right.
func (i *Intake) Finish() (*Summary, error) {
	if i.failed {
		return nil, &Error{kind: kindIntakeFailed}
	}
	if first := i.wantedPieces.first(); first != nil && i.record != nil && i.wantedLists.len() == 0 {
		return nil, missing("the piece", first.value)
	}
	return i.finishLists()
}

// finishLists ends the intake as Finish does, without asking that the
// pieces are held: every list must be there and right, and the totals are
// counted from the lists.
func (i *Intake) finishLists() (*Summary, error) {
	if i.failed {
		return nil, &Error{kind: kindIntakeFailed}
	}
	if i.record == nil {
		return nil, missing("the record of", i.root.Hash())
	}
	if err := i.listsHeld(); err != nil {
		return nil, err
	}
	if err := i.checkDepth(i.record.Top()); err != nil {
		return nil, err
	}
	recorded := i.record.Totals()
	for _, total := range []struct {
		field             string
		recorded, counted uint64
	}{
		{"pieces", recorded.Pieces, i.counted.Pieces},
		{"piece bytes", recorded.PieceBytes, i.counted.PieceBytes},
		{"list bytes", recorded.ListBytes, i.counted.ListBytes},
	} {
		if total.recorded != total.counted {
			return nil, totalsMismatch(total.field, total.recorded, total.counted)
		}
	}
	return &Summary{
		Record:  i.record,
		Folders: uint64(len(i.folders)),
		Files:   uint64(len(i.files)),
	}, nil
}

// Verify checks a whole tree that a store holds: every list, every piece's
// bytes, the depth of its folders and the totals in its record. It is the
// same set of checks that a tree goes through when it is taken in.
func Verify(store Store, root Root) (*Summary, error) {
	return VerifyWith(store, root, VerifyOptions{})
}

// VerifyWith checks a whole tree that a store holds, with more checks
// switched on.
//
// The lists are checked first. Then every piece is hashed again, several at
// once, and last, when that is asked for, every file is cut again.
func VerifyWith(store Store, root Root, options VerifyOptions) (*Summary, error) {
	intake, err := newIntake(store, root, intakeMode{recut: options.Recut, discard: options.DiscardDamaged})
	if err != nil {
		return nil, err
	}
	if err := intake.rehashHeld(options.Workers); err != nil {
		return nil, err
	}
	if err := intake.recutHeld(); err != nil {
		return nil, err
	}
	return intake.Finish()
}
