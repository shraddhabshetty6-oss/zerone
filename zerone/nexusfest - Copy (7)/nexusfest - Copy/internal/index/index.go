// Package index implements the in-memory primary index and snapshot persistence per ARCHITECTURE.md §E.5.
package index

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"studentdb/internal/constants"
	"studentdb/internal/crc"
	"studentdb/internal/faultinject"
)

var (
	// ErrDuplicateID is returned when an active student ID is already indexed.
	ErrDuplicateID = errors.New("index: duplicate student ID (DUPLICATE_ID)")

	// ErrSnapshotInvalid is returned when students.idx is corrupt or has invalid CRC/magic.
	ErrSnapshotInvalid = errors.New("index: snapshot invalid or corrupt")
)

// IndexEntry represents one entry in the primary index.
type IndexEntry struct {
	StudentID  uint32
	SlotNumber uint32
}

// IndexStore defines the interface for the primary index.
type IndexStore interface {
	Insert(studentID, slot uint32) error
	Delete(studentID uint32) bool
	Lookup(studentID uint32) (uint32, bool)
	Range(from, to uint32, limit int) []IndexEntry
	Len() int
	Rebuild(entries []IndexEntry) error
	SaveSnapshot(path string, checkpointLSN uint64, slotCount uint64) error
	LoadSnapshot(path string) (checkpointLSN uint64, slotCount uint64, err error)
}

// BTreeIndex implements IndexStore using a B+ tree of order 32.
// Note: Concurrency safety is provided by the caller (GO-13 LockManager).
type BTreeIndex struct {
	mu   sync.RWMutex
	root *bNode
	size int
}

const (
	minKeys = 15
	maxKeys = 31 // Order 32
)

type bNode struct {
	isLeaf   bool
	entries  []IndexEntry
	children []*bNode
}

// New creates an empty primary index.
func New() *BTreeIndex {
	return &BTreeIndex{
		root: &bNode{isLeaf: true},
	}
}

// Len returns the number of active indexed entries.
func (idx *BTreeIndex) Len() int {
	return idx.size
}

// Lookup finds the slot number for a given StudentID.
func (idx *BTreeIndex) Lookup(studentID uint32) (uint32, bool) {
	node := idx.root
	for node != nil {
		i := sort.Search(len(node.entries), func(i int) bool {
			return node.entries[i].StudentID >= studentID
		})
		if i < len(node.entries) && node.entries[i].StudentID == studentID {
			return node.entries[i].SlotNumber, true
		}
		if node.isLeaf {
			return 0, false
		}
		node = node.children[i]
	}
	return 0, false
}

// Insert adds a new mapping StudentID -> SlotNumber.
func (idx *BTreeIndex) Insert(studentID, slot uint32) error {
	if _, found := idx.Lookup(studentID); found {
		return ErrDuplicateID
	}

	root := idx.root
	if len(root.entries) == maxKeys {
		newRoot := &bNode{
			isLeaf:   false,
			children: []*bNode{root},
		}
		splitChild(newRoot, 0, root)
		idx.root = newRoot
	}
	insertNonFull(idx.root, IndexEntry{StudentID: studentID, SlotNumber: slot})
	idx.size++
	return nil
}

func insertNonFull(node *bNode, entry IndexEntry) {
	i := sort.Search(len(node.entries), func(i int) bool {
		return node.entries[i].StudentID >= entry.StudentID
	})

	if node.isLeaf {
		node.entries = append(node.entries, IndexEntry{})
		copy(node.entries[i+1:], node.entries[i:])
		node.entries[i] = entry
		return
	}

	if len(node.children[i].entries) == maxKeys {
		splitChild(node, i, node.children[i])
		if entry.StudentID > node.entries[i].StudentID {
			i++
		}
	}
	insertNonFull(node.children[i], entry)
}

func splitChild(parent *bNode, index int, child *bNode) {
	mid := minKeys
	midEntry := child.entries[mid]

	sibling := &bNode{
		isLeaf:  child.isLeaf,
		entries: make([]IndexEntry, len(child.entries[mid+1:])),
	}
	copy(sibling.entries, child.entries[mid+1:])

	if !child.isLeaf {
		sibling.children = make([]*bNode, len(child.children[mid+1:]))
		copy(sibling.children, child.children[mid+1:])
		child.children = child.children[:mid+1]
	}
	child.entries = child.entries[:mid]

	parent.children = append(parent.children, nil)
	copy(parent.children[index+2:], parent.children[index+1:])
	parent.children[index+1] = sibling

	parent.entries = append(parent.entries, IndexEntry{})
	copy(parent.entries[index+1:], parent.entries[index:])
	parent.entries[index] = midEntry
}

// Delete removes a mapping for studentID. Returns true if present.
func (idx *BTreeIndex) Delete(studentID uint32) bool {
	if _, found := idx.Lookup(studentID); !found {
		return false
	}
	deleteFromNode(idx.root, studentID)
	if len(idx.root.entries) == 0 && !idx.root.isLeaf && len(idx.root.children) > 0 {
		idx.root = idx.root.children[0]
	}
	idx.size--
	return true
}

func deleteFromNode(node *bNode, studentID uint32) {
	i := sort.Search(len(node.entries), func(i int) bool {
		return node.entries[i].StudentID >= studentID
	})

	if i < len(node.entries) && node.entries[i].StudentID == studentID {
		if node.isLeaf {
			node.entries = append(node.entries[:i], node.entries[i+1:]...)
			return
		}
		// Replace with predecessor
		pred := getPredecessor(node.children[i])
		node.entries[i] = pred
		deleteFromNode(node.children[i], pred.StudentID)
		return
	}

	if node.isLeaf {
		return
	}

	deleteFromNode(node.children[i], studentID)
}

func getPredecessor(node *bNode) IndexEntry {
	for !node.isLeaf {
		node = node.children[len(node.children)-1]
	}
	return node.entries[len(node.entries)-1]
}

// Range returns entries in [from, to] up to limit, ordered ascending.
func (idx *BTreeIndex) Range(from, to uint32, limit int) []IndexEntry {
	if limit <= 0 {
		limit = constants.MaxPageLimit
	}
	var res []IndexEntry
	idx.rangeTraverse(idx.root, from, to, limit, &res)
	return res
}

func (idx *BTreeIndex) rangeTraverse(node *bNode, from, to uint32, limit int, res *[]IndexEntry) {
	if node == nil || len(*res) >= limit {
		return
	}

	for i := 0; i < len(node.entries); i++ {
		if !node.isLeaf && node.entries[i].StudentID >= from {
			idx.rangeTraverse(node.children[i], from, to, limit, res)
			if len(*res) >= limit {
				return
			}
		}

		if node.entries[i].StudentID >= from && node.entries[i].StudentID <= to {
			*res = append(*res, node.entries[i])
			if len(*res) >= limit {
				return
			}
		}

		if node.entries[i].StudentID > to {
			return
		}
	}

	if !node.isLeaf {
		idx.rangeTraverse(node.children[len(node.entries)], from, to, limit, res)
	}
}

// AllEntries returns all entries sorted ascending.
func (idx *BTreeIndex) AllEntries() []IndexEntry {
	return idx.Range(0, 0xFFFFFFFF, idx.size)
}

// Rebuild resets the index and populates it with the given entries.
func (idx *BTreeIndex) Rebuild(entries []IndexEntry) error {
	idx.root = &bNode{isLeaf: true}
	idx.size = 0

	// Sort entries by StudentID ascending
	sorted := make([]IndexEntry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].StudentID < sorted[j].StudentID
	})

	for _, e := range sorted {
		if err := idx.Insert(e.StudentID, e.SlotNumber); err != nil {
			return err
		}
	}
	return nil
}

// SaveSnapshot saves the index entries to students.idx per ARCHITECTURE.md §E.5.
// Process: students.idx.tmp -> fsync -> rename -> fsync directory.
func (idx *BTreeIndex) SaveSnapshot(path string, checkpointLSN uint64, slotCount uint64) error {
	entries := idx.AllEntries()
	entryCount := uint64(len(entries))

	totalSize := 32 + int(entryCount)*8 + 4
	buf := make([]byte, totalSize)

	// Magic: BSIX
	copy(buf[0:4], []byte(constants.IndexHeaderMagic))
	// Version: 1
	binary.LittleEndian.PutUint16(buf[4:6], constants.IndexVersion)
	// Reserved
	binary.LittleEndian.PutUint16(buf[6:8], 0)
	// EntryCount
	binary.LittleEndian.PutUint64(buf[8:16], entryCount)
	// CheckpointLSN
	binary.LittleEndian.PutUint64(buf[16:24], checkpointLSN)
	// SlotCount
	binary.LittleEndian.PutUint64(buf[24:32], slotCount)

	offset := 32
	for _, e := range entries {
		binary.LittleEndian.PutUint32(buf[offset:offset+4], e.StudentID)
		binary.LittleEndian.PutUint32(buf[offset+4:offset+8], e.SlotNumber)
		offset += 8
	}

	// Trailer CRC32 over all preceding bytes [0, 32 + 8*N)
	checksum := crc.Sum(buf[:offset])
	binary.LittleEndian.PutUint32(buf[offset:offset+4], checksum)

	dir := filepath.Dir(path)
	tmpPath := path + ".tmp"

	// Write temp file
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("create index snapshot temp: %w", err)
	}

	if _, err := f.Write(buf); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write index snapshot: %w", err)
	}

	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("sync index snapshot: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close index snapshot temp: %w", err)
	}

	// Failpoint FP-14: checkpoint.after_index_tmp_write
	faultinject.Hit(faultinject.FPCheckpointAfterIndexTmpWrite)

	// Atomic rename
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename index snapshot: %w", err)
	}

	// Sync parent directory
	if dirF, err := os.Open(dir); err == nil {
		_ = dirF.Sync()
		_ = dirF.Close()
	}

	// Failpoint FP-15: checkpoint.after_index_rename
	faultinject.Hit(faultinject.FPCheckpointAfterIndexRename)

	return nil
}

// LoadSnapshot reads students.idx and restores the primary index.
func (idx *BTreeIndex) LoadSnapshot(path string) (checkpointLSN uint64, slotCount uint64, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, err
	}

	if len(data) < 36 { // 32 header + 0 entries + 4 crc
		return 0, 0, ErrSnapshotInvalid
	}

	// Verify trailer CRC32
	storedCRC := binary.LittleEndian.Uint32(data[len(data)-4:])
	computedCRC := crc.Sum(data[:len(data)-4])
	if storedCRC != computedCRC {
		return 0, 0, ErrSnapshotInvalid
	}

	// Header checks
	if string(data[0:4]) != constants.IndexHeaderMagic {
		return 0, 0, ErrSnapshotInvalid
	}
	version := binary.LittleEndian.Uint16(data[4:6])
	if version != constants.IndexVersion {
		return 0, 0, ErrSnapshotInvalid
	}

	entryCount := binary.LittleEndian.Uint64(data[8:16])
	checkpointLSN = binary.LittleEndian.Uint64(data[16:24])
	slotCount = binary.LittleEndian.Uint64(data[24:32])

	expectedLen := 32 + int(entryCount)*8 + 4
	if len(data) != expectedLen {
		return 0, 0, ErrSnapshotInvalid
	}

	entries := make([]IndexEntry, entryCount)
	offset := 32
	var prevID uint32
	for i := uint64(0); i < entryCount; i++ {
		sid := binary.LittleEndian.Uint32(data[offset : offset+4])
		slot := binary.LittleEndian.Uint32(data[offset+4 : offset+8])
		offset += 8

		// Must be sorted strictly ascending
		if i > 0 && sid <= prevID {
			return 0, 0, ErrSnapshotInvalid
		}
		prevID = sid
		entries[i] = IndexEntry{StudentID: sid, SlotNumber: slot}
	}

	if err := idx.Rebuild(entries); err != nil {
		return 0, 0, fmt.Errorf("rebuild from snapshot: %w", err)
	}

	return checkpointLSN, slotCount, nil
}

