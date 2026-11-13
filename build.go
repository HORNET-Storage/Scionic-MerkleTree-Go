package scionic

import (
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Building a tree: from a folder or a file on disk, or part by part.

const (
	// roomAlone is how much of a file a builder holds at a time when it
	// works alone: two pieces of the longest kind.
	roomAlone = 2 * FastCDCMaxSize
	// roomWithHelp is how much of a file a builder holds at a time when
	// other goroutines help it: enough pieces to give each of them some.
	roomWithHelp = 8 * FastCDCMaxSize
	// piecesAtATime is how many pieces of a file that lies in memory are
	// handed on at a time.
	piecesAtATime = 64
	// foldersAhead is how many folders' files may wait for the goroutines
	// that cut. A folder is kept open while it has files that are not cut
	// yet, so this bounds how many folders are open at a time.
	foldersAhead = 16
)

// helpers counts the goroutines that are free to help with a long file.
//
// A file is cut on the goroutine that asks for it, because where one piece
// ends decides where the next begins. Hashing and keeping the pieces is the
// greater part of the work and needs no order, so it is shared out among
// that goroutine and the helpers that are free.
type helpers struct {
	free atomic.Int64
}

func newHelpers(free int) *helpers {
	shared := &helpers{}
	shared.free.Store(int64(free))
	return shared
}

// helpersForThreads gives the helpers of a builder that may use threads
// goroutines, its own counted. Zero is one for each core.
func helpersForThreads(threads int) *helpers {
	return newHelpers(workerCount(threads) - 1)
}

func (h *helpers) any() bool {
	return h.free.Load() > 0
}

// take takes up to most helpers, and says how many it got.
func (h *helpers) take(most int) int {
	for {
		free := h.free.Load()
		taken := min(free, int64(most))
		if taken <= 0 {
			return 0
		}
		if h.free.CompareAndSwap(free, free-taken) {
			return int(taken)
		}
	}
}

func (h *helpers) give(count int) {
	h.free.Add(int64(count))
}

// Builder builds a tree part by part: files from their bytes, folders from
// their entries, and at the end the record.
//
// Everything goes into the store as it is made. A piece, a file or a folder
// that the store holds already is not written again.
//
// A builder is used by one goroutine at a time.
type Builder struct {
	store   Store
	cutting Cutting
	cutter  streamCutter
	helpers *helpers
}

// NewBuilder makes a builder that uses one goroutine for each core on a
// long file.
func NewBuilder(store Store, cutting Cutting) *Builder {
	return sharingBuilder(store, cutting, helpersForThreads(0))
}

func sharingBuilder(store Store, cutting Cutting, shared *helpers) *Builder {
	return &Builder{store: store, cutting: cutting, helpers: shared}
}

// Threads sets how many goroutines hash and keep the pieces of one file: 1
// is the calling goroutine alone, and zero is one for each core. A file is
// always cut on the calling goroutine. With help, a builder holds up to
// 64 MiB of a long file at a time; alone it holds up to 16 MiB. It gives
// the builder back.
func (b *Builder) Threads(threads int) *Builder {
	b.helpers = helpersForThreads(threads)
	return b
}

// Cutting is how this builder cuts files.
func (b *Builder) Cutting() Cutting {
	return b.cutting
}

// File cuts everything reader gives into pieces, keeps the pieces and the
// file's list, and says what the file is. The file is never held in memory
// whole: only a window of it, as Threads says.
func (b *Builder) File(reader io.Reader) (Target, error) {
	read := readOf(reader)
	return b.fileFrom(func(buffer []byte) (int, error) {
		count, err := read(buffer)
		if err != nil {
			return 0, streamError("read a file's bytes", err)
		}
		return count, nil
	})
}

// FileFromBytes is the file with these bytes. They are cut where they lie,
// and nothing is copied on the way to the store.
func (b *Builder) FileFromBytes(content []byte) (Target, error) {
	var items []PieceRef
	pieces := b.cutting.Cut(content)
	for len(pieces) > 0 {
		count := min(len(pieces), piecesAtATime)
		if err := keepPieces(b.store, b.helpers, pieces[:count], &items); err != nil {
			return Target{}, err
		}
		pieces = pieces[count:]
	}
	return b.keepList(items)
}

// FileAt is the file at path on disk.
func (b *Builder) FileAt(path string) (Target, error) {
	file, err := os.Open(path)
	if err != nil {
		return Target{}, ioError("open", path, err)
	}
	defer file.Close()
	read := readOf(file)
	return b.fileFrom(func(buffer []byte) (int, error) {
		count, err := read(buffer)
		if err != nil {
			return 0, ioError("read", path, err)
		}
		return count, nil
	})
}

func (b *Builder) fileFrom(read func(buffer []byte) (int, error)) (Target, error) {
	room := roomAlone
	if b.helpers.any() {
		room = roomWithHelp
	}
	var items []PieceRef
	err := b.cutter.cut(b.cutting, room, read, func(pieces [][]byte) error {
		return keepPieces(b.store, b.helpers, pieces, &items)
	})
	if err != nil {
		return Target{}, err
	}
	return b.keepList(items)
}

// keepList keeps the list of a file whose pieces are kept, and says what
// the file is.
func (b *Builder) keepList(items []PieceRef) (Target, error) {
	list, err := newFileList(items)
	if err != nil {
		return Target{}, err
	}
	held, err := storeHas(b.store, StoredFileList, list.Hash())
	if err != nil {
		return Target{}, err
	}
	if !held {
		if err := b.store.Put(StoredFileList, list.Hash(), list.Encode()); err != nil {
			return Target{}, err
		}
	}
	return list.Target(), nil
}

// Folder keeps the folder with these entries, and says what it is. The
// entries may come in any order. Two entries with one name are an error.
func (b *Builder) Folder(entries []Entry) (Target, error) {
	sorted := slices.Clone(entries)
	slices.SortStableFunc(sorted, func(left, right Entry) int { return strings.Compare(left.name, right.name) })
	list, err := newFolderList(sorted)
	if err != nil {
		return Target{}, err
	}
	held, err := storeHas(b.store, StoredFolderList, list.Hash())
	if err != nil {
		return Target{}, err
	}
	if !held {
		if err := b.store.Put(StoredFolderList, list.Hash(), list.Encode()); err != nil {
			return Target{}, err
		}
	}
	return list.Target(), nil
}

// Finish writes the record of the tree that has top on top, and gives the
// tree's root. The store must hold every list under top.
//
// The whole tree is checked before its record is kept: its sizes, how its
// files are cut, and how deep its folders sit. The checks are the ones a
// tree goes through when it is taken in, and the totals in the record are
// what those checks counted.
func (b *Builder) Finish(top Target, extra ExtraData) (Root, error) {
	totals, err := measure(b.store, top, b.cutting)
	if err != nil {
		return Root{}, err
	}
	record, err := NewRecord(top, totals, b.cutting, extra)
	if err != nil {
		return Root{}, err
	}
	encoded := record.Encode()
	root := RootOfRecord(encoded)
	if err := b.store.Put(StoredRecord, root.Hash(), encoded); err != nil {
		return Root{}, err
	}
	return root, nil
}

// keepPiece hashes and keeps one piece, and says what it is. A piece that
// the store holds already is not written again.
func keepPiece(store Store, piece []byte) (PieceRef, error) {
	item, err := PieceRefOf(piece)
	if err != nil {
		return PieceRef{}, err
	}
	held, err := storeHas(store, StoredPiece, item.Hash)
	if err != nil {
		return PieceRef{}, err
	}
	if !held {
		if err := store.Put(StoredPiece, item.Hash, piece); err != nil {
			return PieceRef{}, err
		}
	}
	return item, nil
}

// keepPieces hashes and keeps pieces that follow one another in a file, and
// adds what they are to items, in their order. Helpers that are free take a
// share. The pieces are used only until this returns, or panics.
func keepPieces(store Store, shared *helpers, pieces [][]byte, items *[]PieceRef) error {
	// A helper is worth starting only for a few pieces of its own.
	helping := shared.take(len(pieces) / 2)
	if helping == 0 {
		for _, piece := range pieces {
			item, err := keepPiece(store, piece)
			if err != nil {
				return err
			}
			*items = append(*items, item)
		}
		return nil
	}
	// What became of each piece, at the piece's place.
	type outcome struct {
		item PieceRef
		err  error
		done bool
	}
	kept := make([]outcome, len(pieces))
	var next atomic.Int64
	work := func() {
		for {
			index := next.Add(1) - 1
			if index >= int64(len(pieces)) {
				return
			}
			item, err := keepPiece(store, pieces[index])
			if err != nil {
				next.Store(int64(len(pieces)))
			}
			kept[index] = outcome{item: item, err: err, done: true}
		}
	}
	var panics caught
	var wait sync.WaitGroup
	for range helping {
		wait.Add(1)
		go func() {
			defer wait.Done()
			defer panics.note(func() { next.Store(int64(len(pieces))) })
			work()
		}()
	}
	func() {
		// The helpers read the caller's pieces, so they are waited for
		// whatever happens here, and are free again after that. If this
		// goroutine falls over, they take no more pieces.
		finished := false
		defer shared.give(helping)
		defer wait.Wait()
		defer func() {
			if !finished {
				next.Store(int64(len(pieces)))
			}
		}()
		work()
		finished = true
	}()
	panics.raise()
	for index := range kept {
		if kept[index].err != nil {
			return kept[index].err
		}
	}
	for index := range kept {
		if !kept[index].done {
			return invariant("a piece was left out of a file")
		}
		*items = append(*items, kept[index].item)
	}
	return nil
}

// Links is what a builder does with a link or a special file that it finds
// in a folder. Neither is stored in a tree, and a link is never followed:
// it could lead out of the folder that is being built.
type Links uint8

const (
	// LinksRefuse stops, and says which one it was.
	LinksRefuse Links = iota
	// LinksSkip leaves it out of the tree.
	LinksSkip
)

// BuildOptions says how a tree is built from disk. The zero value cuts by
// content, on every core, and refuses links.
type BuildOptions struct {
	// Cutting is how files are cut. The zero value cuts them by content,
	// as every tree outside a test should be.
	Cutting Cutting
	// Extra is extra data for the tree's record.
	Extra ExtraData
	// Timestamp writes the time of building into the record. Two builds of
	// the same folder then have different roots.
	Timestamp bool
	// Workers is how many goroutines build the tree. Zero is one for each
	// core. Files are cut several at once, and a goroutine that has no
	// file of its own helps to hash the pieces of a long one.
	Workers int
	// Links is what is done with a link or a special file.
	Links Links
}

// EntryExtra gives the extra data for one entry: its path on disk, its
// name, and whether it is a file or a folder.
type EntryExtra func(path, name string, kind Kind) (ExtraData, error)

// Build builds the tree of the folder or the file at path into store, and
// gives its root.
//
// A folder's own name is not part of its tree, so the same folder gives the
// same root wherever it is and whatever it is called. Entries have no extra
// data: BuildWith adds some.
func Build(store Store, path string, options BuildOptions) (Root, error) {
	return BuildWith(store, path, options, nil)
}

// BuildWith builds a tree as Build does, and asks entryExtra for the extra
// data of every entry.
func BuildWith(store Store, path string, options BuildOptions, entryExtra EntryExtra) (Root, error) {
	extra := options.Extra
	if options.Timestamp {
		stamp, err := timestamp(time.Now())
		if err != nil {
			return Root{}, err
		}
		if extra, err = extra.WithText(TimestampKey, stamp); err != nil {
			return Root{}, err
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return Root{}, ioError("look at", path, err)
	}
	builder := NewBuilder(store, options.Cutting).Threads(options.Workers)
	if !info.IsDir() {
		top, err := builder.FileAt(path)
		if err != nil {
			return Root{}, err
		}
		return builder.Finish(top, extra)
	}
	// From here on nothing is reached by its path: the folder is held open,
	// and so is every folder under it while it is read.
	top, err := openHeld(path)
	if err != nil {
		return Root{}, ioError("open", path, err)
	}
	folders, fileTargets, err := func() ([]builtFolder, []Target, error) {
		defer top.release()
		return walkAndCut(store, options.Cutting, top, options.Links, options.Workers)
	}()
	if err != nil {
		return Root{}, err
	}
	// A folder is found after the folder it is in, so going backwards every
	// folder is made after the folders inside it.
	folderTargets := make([]Target, len(folders))
	made := make([]bool, len(folders))
	for index := len(folders) - 1; index >= 0; index-- {
		entries := make([]Entry, 0, len(folders[index].children))
		for _, child := range folders[index].children {
			// A file's path is put together here, for the caller to see. No
			// file is opened by it.
			var target Target
			var childPath string
			if child.kind == KindFile {
				if child.index >= len(fileTargets) {
					return Root{}, invariant("a file was left out of the build")
				}
				target = fileTargets[child.index]
				childPath = pathWithin(folders[index].path, child.name)
			} else {
				if !made[child.index] {
					return Root{}, invariant("a folder was made before a folder inside it")
				}
				target = folderTargets[child.index]
				childPath = folders[child.index].path
			}
			var entryData ExtraData
			if entryExtra != nil {
				if entryData, err = entryExtra(childPath, child.name, child.kind); err != nil {
					return Root{}, err
				}
			}
			entry, err := NewEntry(child.name, target, entryData)
			if err != nil {
				return Root{}, err
			}
			entries = append(entries, entry)
		}
		if folderTargets[index], err = builder.Folder(entries); err != nil {
			return Root{}, err
		}
		made[index] = true
	}
	return builder.Finish(folderTargets[0], extra)
}

// builtFolder is a folder that a walk found.
type builtFolder struct {
	path     string
	children []builtChild
}

type builtChild struct {
	name string
	kind Kind
	// index is the child's place among the folders, or among the files.
	index int
}

// folderBelow is a folder inside the one that is read, which is still to be
// read: its name, and its place among the folders.
type folderBelow struct {
	name  string
	index int
}

// batchFile is a file's place among the files, and its name in its folder.
type batchFile struct {
	index int
	name  string
}

// fileBatch is the files that the walk found in one folder, for the
// goroutines to cut. The folder is held open for the batch, and each file
// is opened from it by its name.
type fileBatch struct {
	folder *held
	files  []batchFile
	// taken is how many of the files are taken.
	taken atomic.Int64
}

// take gives a file that no goroutine has taken yet. Each file is given
// once, to whichever goroutine asks first.
func (b *fileBatch) take() (batchFile, bool) {
	at := b.taken.Add(1) - 1
	if at >= int64(len(b.files)) {
		return batchFile{}, false
	}
	return b.files[at], true
}

func (b *fileBatch) allTaken() bool {
	return b.taken.Load() >= int64(len(b.files))
}

func unsupported(path, what string) error {
	return &Error{kind: kindUnsupportedEntry, Path: path, What: what}
}

// changed: what was there when a folder was read has to be there still when
// it is opened. If a link took its place meanwhile, the link is not
// followed.
func changed(path string, err error) error {
	switch {
	case errors.Is(err, errRefusedLink):
		return unsupported(path, "a link now, where a file or a folder was when the build began")
	case errors.Is(err, errRefusedOther):
		return unsupported(path, "something else now than it was when the build began")
	}
	return ioError("open", path, err)
}

// errStopped is what a goroutine gives when it stopped because the build
// had failed somewhere else. The error that the caller is given is that
// failure's.
var errStopped = invariant("the build was stopped")

// cutFileIn cuts and keeps the file name of a folder that is held open. The
// file is opened from the folder itself. Once stop is set, the file is
// given up, however much of it is left.
func cutFileIn(builder *Builder, folder *held, name string, stop *atomic.Bool) (Target, error) {
	file, err := folder.openFile(name)
	if err != nil {
		return Target{}, changed(pathWithin(folder.path, name), err)
	}
	defer file.Close()
	read := readOf(file)
	return builder.fileFrom(func(buffer []byte) (int, error) {
		if stop != nil && stop.Load() {
			return 0, errStopped
		}
		count, err := read(buffer)
		if err != nil {
			// The path is for messages, and is put together only for one.
			return 0, ioError("read", pathWithin(folder.path, name), err)
		}
		return count, nil
	})
}

// walker is what a walk through a folder has found so far.
type walker struct {
	links   Links
	folders []builtFolder
	// files is how many files were found.
	files int
	// onFiles takes the files of each folder, once the folder is read. It
	// takes over the batch's hold on the folder, whatever it gives back.
	onFiles func(batch *fileBatch) error
}

// read reads one folder, which is depth folders down from the top, the top
// counted. It notes what is in it, hands its files on, and gives the
// folders inside it, each with its place among the folders.
func (w *walker) read(folder *held, index, depth int) ([]folderBelow, error) {
	listed, err := folder.list()
	if err != nil {
		return nil, ioError("list", folder.path, err)
	}
	children := make([]builtChild, 0, len(listed))
	var inside []folderBelow
	var files []batchFile
	for _, entry := range listed {
		name := entry.name
		if !utf8.ValidString(name) {
			return nil, &Error{kind: kindNonUTF8FileName, Path: pathWithin(folder.path, name)}
		}
		if err := CheckName(name); err != nil {
			return nil, err
		}
		switch entry.found {
		case foundFolder:
			if depth >= MaxFolderDepth {
				return nil, &Error{kind: kindTooDeep, Limit: MaxFolderDepth}
			}
			below := len(w.folders)
			children = append(children, builtChild{name: name, kind: KindFolder, index: below})
			w.folders = append(w.folders, builtFolder{path: pathWithin(folder.path, name)})
			inside = append(inside, folderBelow{name: name, index: below})
		case foundFile:
			children = append(children, builtChild{name: name, kind: KindFile, index: w.files})
			files = append(files, batchFile{index: w.files, name: name})
			w.files++
		default:
			if w.links == LinksRefuse {
				what := "a special file"
				if entry.found == foundLink {
					what = "a link"
				}
				return nil, unsupported(pathWithin(folder.path, name), what)
			}
		}
	}
	slices.SortFunc(children, func(left, right builtChild) int { return strings.Compare(left.name, right.name) })
	w.folders[index].children = children
	if len(files) != 0 {
		if err := w.onFiles(&fileBatch{folder: folder.retain(), files: files}); err != nil {
			return nil, err
		}
	}
	return inside, nil
}

// walk finds every folder and every file under top, and hands the files of
// each folder to onFiles once the folder is read. A link is never followed.
// It gives the folders, and how many files there are.
//
// A folder is opened from the folder it is in, which is held open, and is
// held open itself while it is read. So no folder on the way to a file can
// be swapped for a link between the reading of a folder and the opening of
// what is in it.
func walk(top *held, links Links, onFiles func(batch *fileBatch) error) ([]builtFolder, int, error) {
	w := &walker{links: links, folders: []builtFolder{{path: top.path}}, onFiles: onFiles}
	// The folders on the way down to the one that is read, each held open,
	// with the folders inside it that are still to be read.
	type onTheWay struct {
		folder *held
		inside []folderBelow
	}
	var way []onTheWay
	// next is the folder that is read next, held for the reading.
	var next *held
	// However the walk ends, by its end, by an error or by a goroutine that
	// falls over, every folder it holds is let go of. Nothing does that for
	// a folder that is forgotten: it would stay held for as long as the
	// program runs.
	defer func() {
		if next != nil {
			next.release()
		}
		for _, step := range way {
			step.folder.release()
		}
	}()
	next = top.retain()
	nextIndex := 0
	for {
		if next != nil {
			inside, err := w.read(next, nextIndex, len(way)+1)
			if err != nil {
				return nil, 0, err
			}
			way = append(way, onTheWay{folder: next, inside: inside})
			next = nil
		}
		if len(way) == 0 {
			return w.folders, w.files, nil
		}
		last := &way[len(way)-1]
		if len(last.inside) == 0 {
			last.folder.release()
			way = way[:len(way)-1]
			continue
		}
		below := last.inside[len(last.inside)-1]
		last.inside = last.inside[:len(last.inside)-1]
		opened, err := last.folder.openFolder(below.name)
		if err != nil {
			return nil, 0, changed(w.folders[below.index].path, err)
		}
		next, nextIndex = opened, below.index
	}
}

// waiting holds the folders whose files wait for the goroutines that cut,
// between the goroutine that walks and those goroutines.
//
// Nobody waits here for a goroutine that is gone. A file that cannot be
// cut, a walk that fails and a goroutine that falls over each set failed,
// and wake whoever waits.
type waiting struct {
	lock sync.Mutex
	// more is told when there is a folder to take files from, or none will
	// come.
	more *sync.Cond
	// room is told when a folder's files are all taken, so that the walk
	// may go on.
	room    *sync.Cond
	batches []*fileBatch
	// over: the walk is over, and no more folders will come.
	over bool
	// failed: nothing more is to be cut.
	failed atomic.Bool
}

func newWaiting() *waiting {
	queue := &waiting{}
	queue.more = sync.NewCond(&queue.lock)
	queue.room = sync.NewCond(&queue.lock)
	return queue
}

// add, for the walk: adds a folder's files, once there is room for them.
// The batch's hold on its folder passes to the queue, or is given up.
func (w *waiting) add(batch *fileBatch) error {
	oneFile := len(batch.files) == 1
	w.lock.Lock()
	for {
		if w.failed.Load() {
			w.lock.Unlock()
			batch.folder.release()
			return errStopped
		}
		if len(w.batches) < foldersAhead {
			break
		}
		w.room.Wait()
	}
	w.batches = append(w.batches, batch)
	w.lock.Unlock()
	if oneFile {
		w.more.Signal()
	} else {
		w.more.Broadcast()
	}
	return nil
}

// next, for a goroutine that cuts: a folder that has files left to take,
// held once more for the caller, who releases it. It gives nil when the walk
// is over and every file is taken, or when the build has failed.
func (w *waiting) next() *fileBatch {
	w.lock.Lock()
	defer w.lock.Unlock()
	for {
		if w.failed.Load() {
			return nil
		}
		for len(w.batches) > 0 && w.batches[0].allTaken() {
			w.batches[0].folder.release()
			w.batches[0] = nil
			w.batches = w.batches[1:]
			w.room.Signal()
		}
		if len(w.batches) > 0 {
			batch := w.batches[0]
			batch.folder.retain()
			return batch
		}
		if w.over {
			return nil
		}
		w.more.Wait()
	}
}

// fail: nothing more is to be cut. It is set with the lock held, so that no
// goroutine can look, miss it, and then wait.
func (w *waiting) fail() {
	w.lock.Lock()
	w.failed.Store(true)
	w.lock.Unlock()
	w.more.Broadcast()
	w.room.Broadcast()
}

// end: the walk is over.
func (w *waiting) end() {
	w.lock.Lock()
	w.over = true
	w.lock.Unlock()
	w.more.Broadcast()
}

// drop gives up the folders whose files nobody will take any more.
func (w *waiting) drop() {
	w.lock.Lock()
	for _, batch := range w.batches {
		batch.folder.release()
	}
	w.batches = nil
	w.lock.Unlock()
}

// cutFile is a file that was cut: its place among the files, and what it
// is.
type cutFile struct {
	index  int
	target Target
}

// walkAndCut finds every folder and every file under top, and cuts and
// keeps the files while they are found, several at once. It gives the
// folders, and what each file is.
func walkAndCut(store Store, cutting Cutting, top *held, links Links, threads int) ([]builtFolder, []Target, error) {
	threads = workerCount(threads)
	// A goroutine that has no file left gives itself up to help with the
	// long files that are still being cut.
	shared := newHelpers(0)
	if threads == 1 {
		// One goroutine walks and cuts in turn.
		builder := sharingBuilder(store, cutting, shared)
		var targets []Target
		folders, _, err := walk(top, links, func(batch *fileBatch) error {
			defer batch.folder.release()
			for _, file := range batch.files {
				if file.index != len(targets) {
					return invariant("a file was cut out of its turn")
				}
				target, err := cutFileIn(builder, batch.folder, file.name, nil)
				if err != nil {
					return err
				}
				targets = append(targets, target)
			}
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
		return folders, targets, nil
	}
	queue := newWaiting()
	done := make([][]cutFile, threads)
	failures := make([]error, threads)
	work := func(worker int) error {
		// With no file left, or gone, it is one more that may help with the
		// long files that are still being cut.
		defer shared.give(1)
		builder := sharingBuilder(store, cutting, shared)
		var firstError error
		for {
			batch := queue.next()
			if batch == nil {
				break
			}
			func() {
				defer batch.folder.release()
				// Every goroutine that is free takes files from the same
				// folder, one each, until the folder has none left.
				for {
					file, left := batch.take()
					if !left || queue.failed.Load() {
						return
					}
					target, err := cutFileIn(builder, batch.folder, file.name, &queue.failed)
					if err != nil {
						if firstError == nil {
							firstError = err
						}
						queue.fail()
						continue
					}
					done[worker] = append(done[worker], cutFile{index: file.index, target: target})
				}
			}()
		}
		return firstError
	}
	var panics caught
	var wait sync.WaitGroup
	for worker := range threads {
		wait.Add(1)
		go func() {
			defer wait.Done()
			// A goroutine that falls over tells the others as it goes, so
			// that none of them waits for it.
			defer panics.note(queue.fail)
			failures[worker] = work(worker)
		}()
	}
	var folders []builtFolder
	var files int
	var walkErr error
	func() {
		// The walk is on this goroutine. Whatever happens to it, the others
		// are told that no more folders will come, and are waited for.
		walked := false
		// Last of all, with every goroutine gone: the folders whose files
		// nobody took are let go of.
		defer queue.drop()
		defer wait.Wait()
		defer func() {
			if !walked {
				queue.fail()
			}
			// With the walk over, the goroutines run out of files and end.
			queue.end()
		}()
		folders, files, walkErr = walk(top, links, queue.add)
		// A walk that failed stops the cutting too: a goroutine gives up
		// the file it is on, however long that file is.
		walked = walkErr == nil
	}()
	panics.raise()
	// The failure that came first on its goroutine is the one to tell. A
	// goroutine that only stopped for another's failure has nothing to
	// tell.
	stopped := false
	cut := 0
	for worker := range threads {
		switch err := failures[worker]; {
		case err == nil:
			cut += len(done[worker])
		case err == errStopped:
			stopped = true
		default:
			return nil, nil, err
		}
	}
	if walkErr != nil {
		return nil, nil, walkErr
	}
	if stopped || cut != files {
		if stopped {
			return nil, nil, errStopped
		}
		return nil, nil, invariant("a file was left out of the build")
	}
	targets := make([]Target, files)
	filled := make([]bool, files)
	for worker := range threads {
		for _, file := range done[worker] {
			if file.index >= files || filled[file.index] {
				return nil, nil, invariant("a file was cut twice, or out of its place")
			}
			targets[file.index] = file.target
			filled[file.index] = true
		}
	}
	return folders, targets, nil
}

// timestamp gives the time as text: 2026-11-12T08:30:00Z, in UTC, to the
// second.
func timestamp(now time.Time) (string, error) {
	if now.Unix() < 0 {
		return "", &Error{kind: kindSystemTimeBeforeUnixEpoch}
	}
	return formatEpochSeconds(now.Unix()), nil
}

func formatEpochSeconds(seconds int64) string {
	return time.Unix(seconds, 0).UTC().Format("2006-01-02T15:04:05Z")
}
