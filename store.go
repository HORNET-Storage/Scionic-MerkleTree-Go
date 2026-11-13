package scionic

import (
	"bytes"
	"slices"
	"sync"
)

// Where a tree's parts are kept.
//
// A store keeps four kinds of thing, each found by its hash: chunks, the
// lists of files, the lists of folders, and records. Because a thing's hash
// covers only what the thing is, a chunk, a file or a folder that occurs in
// many trees is kept once.
//
// A store checks nothing: it keeps what it is given under the hash it is
// given. Everything that this library puts into a store is checked first,
// and everything it takes out is checked again before it is used.

// Stored is one of the kinds of thing a store keeps.
type Stored uint8

const (
	// StoredChunk is a chunk's bytes, under the SHA-256 of them.
	StoredChunk Stored = iota + 1
	// StoredFileList is a file's whole list, under the file's hash.
	StoredFileList
	// StoredFolderList is a folder's whole list, under the folder's hash.
	StoredFolderList
	// StoredRecord is a tree's record, under the tree's root.
	StoredRecord
)

func (s Stored) String() string {
	switch s {
	case StoredChunk:
		return "chunk"
	case StoredFileList:
		return "file list"
	case StoredFolderList:
		return "folder list"
	case StoredRecord:
		return "record"
	}
	return "nothing a store keeps"
}

// Store is a place that keeps bytes under hashes. Its methods are called
// from many goroutines at once.
//
// A store that can answer without reading the bytes may also have the
// methods of [StoreHas] and [StoreSize]. They are used when they are there.
// A store on disk should have Size at the least: without it, seeing whether
// a chunk is there takes a read of the whole chunk.
type Store interface {
	// Put keeps bytes under hash. Keeping what is kept already changes
	// nothing. The bytes are lent for the call only: a store that keeps
	// them keeps a copy.
	Put(kind Stored, hash Hash, bytes []byte) error
	// Get gives what is kept under hash, and false if nothing is. The
	// bytes it gives are never changed afterwards, by the store or by the
	// caller.
	Get(kind Stored, hash Hash) ([]byte, bool, error)
	// Remove forgets what is kept under hash.
	Remove(kind Stored, hash Hash) error
}

// StoreHas is a store that says whether it keeps a thing without reading
// it. A store that has Size and not Has is asked for the size instead.
type StoreHas interface {
	// Has reports whether anything is kept under hash.
	Has(kind Stored, hash Hash) (bool, error)
}

// StoreSize is a store that says how long a thing is without reading it.
type StoreSize interface {
	// Size gives how many bytes are kept under hash, and false if nothing
	// is.
	Size(kind Stored, hash Hash) (uint64, bool, error)
}

// storeHas reports whether anything is kept under hash, without reading it
// where the store can say so.
func storeHas(store Store, kind Stored, hash Hash) (bool, error) {
	if direct, ok := store.(StoreHas); ok {
		return direct.Has(kind, hash)
	}
	if direct, ok := store.(StoreSize); ok {
		_, found, err := direct.Size(kind, hash)
		return found, err
	}
	_, found, err := store.Get(kind, hash)
	return found, err
}

// storeSize gives how many bytes are kept under hash.
func storeSize(store Store, kind Stored, hash Hash) (uint64, bool, error) {
	if direct, ok := store.(StoreSize); ok {
		return direct.Size(kind, hash)
	}
	kept, found, err := store.Get(kind, hash)
	return uint64(len(kept)), found, err
}

type storedKey struct {
	kind Stored
	hash Hash
}

// memoryShards is how many parts a store in memory is kept in, each with a
// lock of its own, so that many goroutines put and get at once.
const memoryShards = 64

type memoryShard struct {
	lock   sync.RWMutex
	things map[storedKey][]byte
}

// MemoryStore is a store in memory. The zero value is an empty store.
type MemoryStore struct {
	shards [memoryShards]memoryShard
}

// NewMemoryStore makes an empty store in memory.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{}
}

func (s *MemoryStore) shard(hash Hash) *memoryShard {
	return &s.shards[hash[0]%memoryShards]
}

// Put keeps a copy of bytes under hash.
func (s *MemoryStore) Put(kind Stored, hash Hash, content []byte) error {
	shard := s.shard(hash)
	key := storedKey{kind, hash}
	shard.lock.RLock()
	_, held := shard.things[key]
	shard.lock.RUnlock()
	if held {
		return nil
	}
	// Copied before the lock is taken, so that many goroutines put at once.
	kept := bytes.Clone(content)
	if kept == nil {
		kept = []byte{}
	}
	shard.lock.Lock()
	if shard.things == nil {
		shard.things = make(map[storedKey][]byte)
	}
	if _, held := shard.things[key]; !held {
		shard.things[key] = kept
	}
	shard.lock.Unlock()
	return nil
}

// Get gives what is kept under hash.
func (s *MemoryStore) Get(kind Stored, hash Hash) ([]byte, bool, error) {
	shard := s.shard(hash)
	shard.lock.RLock()
	kept, held := shard.things[storedKey{kind, hash}]
	shard.lock.RUnlock()
	return kept, held, nil
}

// Has reports whether anything is kept under hash.
func (s *MemoryStore) Has(kind Stored, hash Hash) (bool, error) {
	_, held, err := s.Get(kind, hash)
	return held, err
}

// Size gives how many bytes are kept under hash.
func (s *MemoryStore) Size(kind Stored, hash Hash) (uint64, bool, error) {
	kept, held, err := s.Get(kind, hash)
	return uint64(len(kept)), held, err
}

// Remove forgets what is kept under hash.
func (s *MemoryStore) Remove(kind Stored, hash Hash) error {
	shard := s.shard(hash)
	shard.lock.Lock()
	delete(shard.things, storedKey{kind, hash})
	shard.lock.Unlock()
	return nil
}

// each calls visit for every thing of kind.
func (s *MemoryStore) each(kind Stored, visit func(hash Hash, kept []byte)) {
	for at := range s.shards {
		shard := &s.shards[at]
		shard.lock.RLock()
		for key, kept := range shard.things {
			if key.kind == kind {
				visit(key.hash, kept)
			}
		}
		shard.lock.RUnlock()
	}
}

// Count is how many things of kind are kept.
func (s *MemoryStore) Count(kind Stored) int {
	count := 0
	s.each(kind, func(Hash, []byte) { count++ })
	return count
}

// Bytes is how many bytes the things of kind hold together.
func (s *MemoryStore) Bytes(kind Stored) uint64 {
	var total uint64
	s.each(kind, func(_ Hash, kept []byte) { total += uint64(len(kept)) })
	return total
}

// Hashes gives the hashes of the things of kind, in order.
func (s *MemoryStore) Hashes(kind Stored) []Hash {
	var hashes []Hash
	s.each(kind, func(hash Hash, _ []byte) { hashes = append(hashes, hash) })
	slices.SortFunc(hashes, func(left, right Hash) int { return bytes.Compare(left[:], right[:]) })
	return hashes
}

// loadRecord gives the record of root, read from the store and checked
// against the root. It gives nil when the store does not hold it.
func loadRecord(store Store, root Root) (*Record, error) {
	kept, found, err := store.Get(StoredRecord, root.Hash())
	if err != nil || !found {
		return nil, err
	}
	if RootOfRecord(kept) != root {
		return nil, corrupt("a stored record", root.Hash())
	}
	return DecodeRecord(kept)
}

// confirmSize: a list read from a store has to hold the bytes its parent
// gave it.
func confirmSize(read uint64, wanted Target, what string) error {
	if read != wanted.Size {
		return sizeMismatch(what, wanted.Size, read)
	}
	return nil
}

// loadFolder gives the list of the folder, read from the store and checked.
// Bytes that are not a list with the folder's hash are damaged. It gives nil
// when the store does not hold the list.
func loadFolder(store Store, folder Target) (*FolderList, error) {
	if err := folder.require(KindFolder); err != nil {
		return nil, err
	}
	kept, found, err := store.Get(StoredFolderList, folder.Hash)
	if err != nil || !found {
		return nil, err
	}
	const what = "a stored folder's list"
	list, err := DecodeFolderList(kept)
	if err != nil || list.Hash() != folder.Hash {
		return nil, corrupt(what, folder.Hash)
	}
	if err := confirmSize(list.Size(), folder, what); err != nil {
		return nil, err
	}
	return list, nil
}

// loadFile gives the list of the file, read from the store and checked.
// Bytes that are not a list with the file's hash are damaged. It gives nil
// when the store does not hold the list.
func loadFile(store Store, file Target) (*FileList, error) {
	if err := file.require(KindFile); err != nil {
		return nil, err
	}
	kept, found, err := store.Get(StoredFileList, file.Hash)
	if err != nil || !found {
		return nil, err
	}
	const what = "a stored file's list"
	list, err := DecodeFileList(kept)
	if err != nil || list.Hash() != file.Hash {
		return nil, corrupt(what, file.Hash)
	}
	if err := confirmSize(list.Size(), file, what); err != nil {
		return nil, err
	}
	return list, nil
}

// checkChunk reports whether bytes are the chunk that item names.
func checkChunk(item ChunkRef, content []byte, what string) error {
	if uint64(len(content)) != uint64(item.Length) {
		return sizeMismatch(what, uint64(item.Length), uint64(len(content)))
	}
	if HashOf(content) != item.Hash {
		return hashMismatch(what)
	}
	return nil
}

// loadChunk gives the bytes of the chunk that item names, read from the
// store and checked. Bytes that do not hash to the chunk's hash are damaged.
// Bytes that do, and are not as many as the item says, show that the item is
// wrong. It gives false when the store does not hold the chunk.
func loadChunk(store Store, item ChunkRef) ([]byte, bool, error) {
	kept, found, err := store.Get(StoredChunk, item.Hash)
	if err != nil || !found {
		return nil, false, err
	}
	if HashOf(kept) != item.Hash {
		return nil, false, corrupt("a stored chunk", item.Hash)
	}
	if uint64(len(kept)) != uint64(item.Length) {
		return nil, false, sizeMismatch("a stored chunk", uint64(item.Length), uint64(len(kept)))
	}
	return kept, true, nil
}
