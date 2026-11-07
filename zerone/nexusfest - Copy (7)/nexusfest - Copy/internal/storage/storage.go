// Package storage implements students.dat management, slot I/O, and allocation per ARCHITECTURE.md §C.
package storage

import (
	"container/heap"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"studentdb/internal/codec"
	"studentdb/internal/constants"
	"studentdb/internal/faultinject"
)

var (
	// ErrCorruptHeader is returned when the data file header is corrupted or fails CRC.
	ErrCorruptHeader = errors.New("storage: corrupt header (CORRUPT_HEADER)")

	// ErrDiskFull is returned when disk space is exhausted (ENOSPC).
	ErrDiskFull = errors.New("storage: disk full (DISK_FULL)")

	// ErrEngineFailed is returned on unrecoverable I/O errors, marking the engine read-only.
	ErrEngineFailed = errors.New("storage: engine failed read-only (ENGINE_FAILED_READONLY)")

	// ErrInvalidArgument is returned when slot index or parameters are out of range.
	ErrInvalidArgument = errors.New("storage: invalid argument")
)

// SlotStore defines the interface for data file operations.
type SlotStore interface {
	SlotCount() uint32
	ReadSlot(slot uint32) ([256]byte, error)
	WriteSlot(slot uint32, image [256]byte) error
	Extend(toSlots uint32) error
	ReadSlots(startSlot, count uint32) ([][256]byte, error)
	Sync() error
	Close() error

	// Allocation
	AllocateSlot() (uint32, error)
	FreeSlot(slot uint32)
	ReserveSlot(slot uint32)
	ReleaseReserved(slot uint32)
	QuarantineSlot(slot uint32)
	IsQuarantined(slot uint32) bool
	QuarantinedSlots() []uint32
	InitFreeList(freeSlots []uint32, quarantined []uint32)
}

// Min-heap for free list
type slotHeap []uint32

func (h slotHeap) Len() int           { return len(h) }
func (h slotHeap) Less(i, j int) bool { return h[i] < h[j] }
func (h slotHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *slotHeap) Push(x any)        { *h = append(*h, x.(uint32)) }
func (h *slotHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}

// Engine implements SlotStore for students.dat.
type Engine struct {
	mu           sync.RWMutex
	file         *os.File
	path         string
	slotCount    uint32
	syncMode     string
	failed       bool
	header       *codec.DataHeader
	freeSlots    slotHeap
	reservedSet  map[uint32]struct{}
	quarantine   map[uint32]struct{}
}

// Create initializes a new students.dat with a 4096-byte header via temp file + rename + fsync.
func Create(dataDir string) error {
	filePath := filepath.Join(dataDir, "students.dat")
	tmpPath := filePath + ".tmp"

	var uuid [16]byte
	_, _ = rand.Read(uuid[:])

	header := &codec.DataHeader{
		FormatVersion: constants.FormatVersion,
		HeaderSize:    uint32(constants.HeaderSize),
		BlockSize:     uint32(constants.BlockSize),
		RecordSize:    uint32(constants.RecordSize),
		SlotsPerBlock: constants.SlotsPerBlock,
		CreatedAtUnix: time.Now().UTC().Unix(),
		DatabaseUUID:  uuid,
	}

	buf := codec.EncodeDataHeader(header)

	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}

	if _, err := f.Write(buf[:]); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	if err := os.Rename(tmpPath, filePath); err != nil {
		return err
	}

	// fsync directory
	if df, err := os.Open(dataDir); err == nil {
		_ = df.Sync()
		_ = df.Close()
	}

	return nil
}

// Open opens an existing students.dat and validates the header.
func Open(dataDir string, syncMode string) (*Engine, error) {
	filePath := filepath.Join(dataDir, "students.dat")
	f, err := os.OpenFile(filePath, os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}

	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}

	fileSize := fi.Size()
	if fileSize < constants.HeaderSize {
		_ = f.Close()
		return nil, ErrCorruptHeader
	}

	headerBuf := make([]byte, constants.HeaderSize)
	if _, err := f.ReadAt(headerBuf, 0); err != nil {
		_ = f.Close()
		return nil, ErrCorruptHeader
	}

	header, err := codec.DecodeDataHeader(headerBuf)
	if err != nil {
		_ = f.Close()
		return nil, ErrCorruptHeader
	}

	slotCount := uint32((fileSize - constants.HeaderSize) / constants.RecordSize)

	eng := &Engine{
		file:        f,
		path:        filePath,
		slotCount:   slotCount,
		syncMode:    syncMode,
		header:      header,
		reservedSet: make(map[uint32]struct{}),
		quarantine:  make(map[uint32]struct{}),
	}
	heap.Init(&eng.freeSlots)

	return eng, nil
}

// Close closes the underlying data file.
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.file != nil {
		err := e.file.Close()
		e.file = nil
		return err
	}
	return nil
}

// SlotCount returns the number of slots in the file.
func (e *Engine) SlotCount() uint32 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.slotCount
}

// Header returns the data file header.
func (e *Engine) Header() *codec.DataHeader {
	return e.header
}

// ReadSlot reads a 256-byte slot at index slot.
func (e *Engine) ReadSlot(slot uint32) ([256]byte, error) {
	var image [256]byte
	e.mu.RLock()
	defer e.mu.RUnlock()

	if slot >= e.slotCount {
		return image, fmt.Errorf("%w: slot %d >= slotCount %d", ErrInvalidArgument, slot, e.slotCount)
	}

	offset := constants.HeaderSize + int64(slot)*constants.RecordSize
	n, err := e.file.ReadAt(image[:], offset)
	if err != nil && err != io.EOF {
		return image, fmt.Errorf("%w: read slot %d: %v", ErrEngineFailed, slot, err)
	}
	if n != int(constants.RecordSize) {
		return image, fmt.Errorf("%w: partial read of slot %d", ErrEngineFailed, slot)
	}

	return image, nil
}

// WriteSlot writes a 256-byte slot at index slot.
func (e *Engine) WriteSlot(slot uint32, image [256]byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.failed {
		return ErrEngineFailed
	}

	// Failpoint FP-11: disk.enospc.data
	if act := faultinject.Hit(faultinject.FPDiskEnospcData); act.Kind == "error" || act.Err != nil {
		return ErrDiskFull
	}

	// Extend file if writing past the current slotCount
	if slot >= e.slotCount {
		neededSlots := ((slot / constants.SlotsPerBlock) + 1) * constants.SlotsPerBlock
		if err := e.extendLocked(neededSlots); err != nil {
			return err
		}
	}

	// Failpoint FP-05: data.apply.before_slot_write
	faultinject.Hit(faultinject.FPDataApplyBeforeSlotWrite)

	offset := constants.HeaderSize + int64(slot)*constants.RecordSize

	// Failpoint FP-06: data.apply.partial_slot_write (via MaybePartialWrite)
	_, err := faultinject.MaybePartialWrite(faultinject.FPDataApplyPartialSlotWrite, image[:], func(b []byte) (int, error) {
		return e.file.WriteAt(b, offset)
	})

	if err != nil {
		e.failed = true
		if errors.Is(err, syscall.ENOSPC) {
			return ErrDiskFull
		}
		return fmt.Errorf("%w: write slot %d: %v", ErrEngineFailed, slot, err)
	}

	// Failpoint FP-07: data.apply.after_slot_write
	faultinject.Hit(faultinject.FPDataApplyAfterSlotWrite)

	return nil
}

// Extend expands the file to targetSlots in block-granular chunks (16 slots = 4096 B).
func (e *Engine) Extend(targetSlots uint32) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.extendLocked(targetSlots)
}

func (e *Engine) extendLocked(targetSlots uint32) error {
	if targetSlots <= e.slotCount {
		return nil
	}

	// Block-granular extension
	remainder := targetSlots % constants.SlotsPerBlock
	if remainder != 0 {
		targetSlots += (constants.SlotsPerBlock - remainder)
	}

	additionalSlots := targetSlots - e.slotCount
	additionalBytes := int64(additionalSlots) * constants.RecordSize
	startOffset := constants.HeaderSize + int64(e.slotCount)*constants.RecordSize

	// Write real zero bytes (not ftruncate)
	zeros := make([]byte, constants.BlockSize) // 4096 zero bytes per block
	numBlocks := additionalBytes / constants.BlockSize

	// Failpoint FP-08: data.extend.partial
	act := faultinject.Hit(faultinject.FPDataExtendPartial)
	if act.Kind == "partial" {
		part := act.PartialBytes
		if part > len(zeros) {
			part = len(zeros)
		}
		_, _ = e.file.WriteAt(zeros[:part], startOffset)
		faultinject.SelfCrash()
		return nil
	}

	for b := int64(0); b < numBlocks; b++ {
		offset := startOffset + b*constants.BlockSize
		if _, err := e.file.WriteAt(zeros, offset); err != nil {
			e.failed = true
			if errors.Is(err, syscall.ENOSPC) {
				return ErrDiskFull
			}
			return fmt.Errorf("%w: extend file: %v", ErrEngineFailed, err)
		}
	}

	e.slotCount = targetSlots
	return nil
}

// ReadSlots reads a batch of count slots starting from startSlot.
func (e *Engine) ReadSlots(startSlot, count uint32) ([][256]byte, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if startSlot >= e.slotCount {
		return nil, nil
	}

	endSlot := startSlot + count
	if endSlot > e.slotCount {
		endSlot = e.slotCount
	}
	actualCount := endSlot - startSlot
	if actualCount == 0 {
		return nil, nil
	}

	byteLen := int(actualCount) * int(constants.RecordSize)
	buf := make([]byte, byteLen)
	offset := constants.HeaderSize + int64(startSlot)*constants.RecordSize

	n, err := e.file.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("%w: chunk read slots %d..%d: %v", ErrEngineFailed, startSlot, endSlot, err)
	}

	numRead := n / int(constants.RecordSize)
	res := make([][256]byte, numRead)
	for i := 0; i < numRead; i++ {
		copy(res[i][:], buf[i*256:(i+1)*256])
	}

	return res, nil
}

// Sync flushes data file changes to disk.
func (e *Engine) Sync() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.syncMode == "none" {
		return nil
	}

	// Failpoint FP-09: data.sync.error
	if act := faultinject.Hit(faultinject.FPDataSyncError); act.Kind == "error" || act.Err != nil {
		e.failed = true
		return ErrEngineFailed
	}

	if err := e.file.Sync(); err != nil {
		e.failed = true
		return fmt.Errorf("%w: data file sync: %v", ErrEngineFailed, err)
	}

	faultinject.RecordSyncedLength(e.path, constants.HeaderSize+int64(e.slotCount)*constants.RecordSize)
	return nil
}

// Free List and Quarantine Management

// InitFreeList initializes the free list min-heap and quarantine set.
func (e *Engine) InitFreeList(freeSlots []uint32, quarantined []uint32) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.freeSlots = make(slotHeap, 0, len(freeSlots))
	e.reservedSet = make(map[uint32]struct{})
	e.quarantine = make(map[uint32]struct{}, len(quarantined))

	for _, s := range quarantined {
		e.quarantine[s] = struct{}{}
	}

	for _, s := range freeSlots {
		if _, isQ := e.quarantine[s]; !isQ {
			heap.Push(&e.freeSlots, s)
		}
	}
}

// AllocateSlot allocates the lowest free slot, extending the file by 1 block if necessary.
func (e *Engine) AllocateSlot() (uint32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	for e.freeSlots.Len() > 0 {
		slot := heap.Pop(&e.freeSlots).(uint32)
		// Check if quarantined or reserved
		if _, isQ := e.quarantine[slot]; isQ {
			continue
		}
		if _, isRes := e.reservedSet[slot]; isRes {
			continue
		}
		e.reservedSet[slot] = struct{}{}
		return slot, nil
	}

	// Allocate a new block
	newSlot := e.slotCount
	newTotal := e.slotCount + constants.SlotsPerBlock
	if err := e.extendLocked(newTotal); err != nil {
		return 0, err
	}

	// Add remaining new slots to free list
	for s := newSlot + 1; s < newTotal; s++ {
		heap.Push(&e.freeSlots, s)
	}

	e.reservedSet[newSlot] = struct{}{}
	return newSlot, nil
}

// ReserveSlot marks a slot as reserved during transaction staging.
func (e *Engine) ReserveSlot(slot uint32) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.reservedSet[slot] = struct{}{}
}

// ReleaseReserved removes reservation for a slot.
func (e *Engine) ReleaseReserved(slot uint32) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.reservedSet, slot)
}

// FreeSlot returns a slot to the free list.
func (e *Engine) FreeSlot(slot uint32) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.reservedSet, slot)
	if _, isQ := e.quarantine[slot]; !isQ {
		heap.Push(&e.freeSlots, slot)
	}
}

// QuarantineSlot marks a corrupted slot as quarantined.
func (e *Engine) QuarantineSlot(slot uint32) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.reservedSet, slot)
	e.quarantine[slot] = struct{}{}
}

// IsQuarantined checks if a slot is quarantined.
func (e *Engine) IsQuarantined(slot uint32) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	_, ok := e.quarantine[slot]
	return ok
}

// QuarantinedSlots returns a slice of all quarantined slot numbers.
func (e *Engine) QuarantinedSlots() []uint32 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	res := make([]uint32, 0, len(e.quarantine))
	for s := range e.quarantine {
		res = append(res, s)
	}
	return res
}

