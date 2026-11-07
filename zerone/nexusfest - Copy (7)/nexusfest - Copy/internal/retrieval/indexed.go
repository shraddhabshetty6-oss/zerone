package retrieval

import (
	"fmt"

	"studentdb/internal/codec"
	"studentdb/internal/concurrency"
	"studentdb/internal/constants"
	"studentdb/internal/crc"
	"studentdb/internal/index"
	"studentdb/internal/record"
	"studentdb/internal/storage"
)

// IndexedEngine implements point and range queries using the primary index (GO-09).
type IndexedEngine struct {
	index   index.IndexStore
	storage storage.SlotStore
	lockMgr *concurrency.LockManager
}

// NewIndexedEngine creates a new IndexedEngine.
func NewIndexedEngine(idx index.IndexStore, store storage.SlotStore, lockMgr *concurrency.LockManager) *IndexedEngine {
	return &IndexedEngine{
		index:   idx,
		storage: store,
		lockMgr: lockMgr,
	}
}

// GetByID performs an indexed point lookup with end-to-end verification (§E.4).
func (ie *IndexedEngine) GetByID(id uint32) (*record.Student, error) {
	unlock := ie.lockMgr.AcquireStateRLock()
	defer unlock()

	slot, found := ie.index.Lookup(id)
	if !found {
		return nil, ErrNotFound
	}

	// Range check against slot count
	if slot >= ie.storage.SlotCount() {
		return nil, fmt.Errorf("%w: index slot %d >= slotCount %d", ErrIndexInconsistent, slot, ie.storage.SlotCount())
	}

	// Check if slot is quarantined
	if ie.storage.IsQuarantined(slot) {
		return nil, fmt.Errorf("%w: slot %d is quarantined", ErrIndexInconsistent, slot)
	}

	image, err := ie.storage.ReadSlot(slot)
	if err != nil {
		return nil, err
	}

	// Verify CRC
	if !crc.VerifySlot(image[:]) {
		return nil, codec.ErrChecksumMismatch
	}

	// Decode and verify
	student, state, _, err := codec.DecodeStudent(image[:])
	if err != nil {
		return nil, err
	}
	if state != codec.SlotStateActive {
		return nil, fmt.Errorf("%w: slot %d is not active (state=%d)", ErrIndexInconsistent, slot, state)
	}
	if student.StudentID != id {
		return nil, fmt.Errorf("%w: slot %d holds student_id %d != key %d", ErrIndexInconsistent, slot, student.StudentID, id)
	}

	return student, nil
}

// RangeByID retrieves records with StudentID in [from, to] up to limit, ordered ascending.
func (ie *IndexedEngine) RangeByID(from, to uint32, limit int) ([]*record.Student, *uint32, error) {
	if limit <= 0 || limit > constants.MaxPageLimit {
		limit = constants.MaxPageLimit
	}

	unlock := ie.lockMgr.AcquireStateRLock()
	defer unlock()

	entries := ie.index.Range(from, to, limit+1)
	hasMore := len(entries) > limit
	if hasMore {
		entries = entries[:limit]
	}

	results := make([]*record.Student, 0, len(entries))
	for _, entry := range entries {
		if entry.SlotNumber >= ie.storage.SlotCount() {
			return nil, nil, fmt.Errorf("%w: slot %d >= slotCount", ErrIndexInconsistent, entry.SlotNumber)
		}
		if ie.storage.IsQuarantined(entry.SlotNumber) {
			continue // skip quarantined or report
		}

		image, err := ie.storage.ReadSlot(entry.SlotNumber)
		if err != nil {
			return nil, nil, err
		}

		if !crc.VerifySlot(image[:]) {
			return nil, nil, codec.ErrChecksumMismatch
		}

		student, state, _, err := codec.DecodeStudent(image[:])
		if err != nil {
			return nil, nil, err
		}
		if state != codec.SlotStateActive || student.StudentID != entry.StudentID {
			return nil, nil, fmt.Errorf("%w: entry %d mismatch at slot %d", ErrIndexInconsistent, entry.StudentID, entry.SlotNumber)
		}

		results = append(results, student)
	}

	var nextFrom *uint32
	if hasMore && len(results) > 0 {
		next := results[len(results)-1].StudentID + 1
		nextFrom = &next
	}

	return results, nextFrom, nil
}

