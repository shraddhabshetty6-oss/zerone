package retrieval

import (
	"fmt"

	"studentdb/internal/codec"
	"studentdb/internal/concurrency"
	"studentdb/internal/constants"
	"studentdb/internal/crc"
	"studentdb/internal/record"
	"studentdb/internal/storage"
)

// SequentialEngine implements physical-order sequential scanning (GO-08).
type SequentialEngine struct {
	storage storage.SlotStore
	lockMgr *concurrency.LockManager
}

// NewSequentialEngine creates a new SequentialEngine.
func NewSequentialEngine(store storage.SlotStore, lockMgr *concurrency.LockManager) *SequentialEngine {
	return &SequentialEngine{
		storage: store,
		lockMgr: lockMgr,
	}
}

// Scan scans records starting from slot cursor up to limit matching filter.
func (se *SequentialEngine) Scan(cursor uint32, limit int, filter Filter, strict bool) (*ScanPage, error) {
	if limit <= 0 || limit > constants.MaxPageLimit {
		limit = constants.MaxPageLimit
	}

	unlock := se.lockMgr.AcquireStateRLock()
	defer unlock()

	totalSlots := se.storage.SlotCount()
	if cursor >= totalSlots {
		return &ScanPage{
			Items:      []*record.Student{},
			Errors:     []ScanError{},
			NextCursor: nil,
		}, nil
	}

	page := &ScanPage{
		Items:  make([]*record.Student, 0, limit),
		Errors: make([]ScanError, 0),
	}

	currentSlot := cursor
	for currentSlot < totalSlots && len(page.Items) < limit {
		chunkSlots := constants.ScanChunkSlots
		if currentSlot+chunkSlots > totalSlots {
			chunkSlots = totalSlots - currentSlot
		}

		images, err := se.storage.ReadSlots(currentSlot, chunkSlots)
		if err != nil {
			return nil, fmt.Errorf("%w: read slots error: %v", storage.ErrEngineFailed, err)
		}

		for i, img := range images {
			slotNum := currentSlot + uint32(i)
			offset := constants.HeaderSize + int64(slotNum)*constants.RecordSize

			state, reason := codec.ClassifySlot(img[:])
			if state == codec.SlotStateFree || state == codec.SlotStateDeleted {
				continue
			}

			if state == codec.SlotStateCorrupt {
				kind := "INVALID_RECORD"
				if !crc.VerifySlot(img[:]) {
					kind = "CHECKSUM_MISMATCH"
				}
				if strict {
					return nil, fmt.Errorf("%w at slot %d (offset %d): %s", codec.ErrChecksumMismatch, slotNum, offset, reason)
				}
				page.Errors = append(page.Errors, ScanError{
					Slot:   slotNum,
					Offset: offset,
					Kind:   kind,
					Detail: reason,
				})
				continue
			}

			// ACTIVE slot: decode
			s, _, _, err := codec.DecodeStudent(img[:])
			if err != nil {
				if strict {
					return nil, err
				}
				page.Errors = append(page.Errors, ScanError{
					Slot:   slotNum,
					Offset: offset,
					Kind:   "INVALID_RECORD",
					Detail: err.Error(),
				})
				continue
			}

			if filter.Matches(s) {
				page.Items = append(page.Items, s)
				if len(page.Items) >= limit {
					next := slotNum + 1
					if next < totalSlots {
						page.NextCursor = &next
					}
					return page, nil
				}
			}
		}

		currentSlot += uint32(len(images))
	}

	if currentSlot < totalSlots {
		page.NextCursor = &currentSlot
	}

	return page, nil
}
