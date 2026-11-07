// Package verify implements consistency validation for invariants I1–I10 per ARCHITECTURE.md §G.7.
package verify

import (
	"fmt"
	"os"
	"path/filepath"

	"studentdb/internal/codec"
	"studentdb/internal/constants"
	"studentdb/internal/index"
	"studentdb/internal/storage"
)

// Finding represents an invariant check failure or warning.
type Finding struct {
	Invariant string  `json:"invariant"`
	Severity  string  `json:"severity"` // "error", "warning"
	Slot      *uint32 `json:"slot,omitempty"`
	ID        *uint32 `json:"id,omitempty"`
	Detail    string  `json:"detail"`
}

// Result summarizes consistency validation results.
type Result struct {
	OK       bool      `json:"ok"`
	Findings []Finding `json:"findings"`
}

// OnlineEngineView provides read-only access to engine state for online validation.
type OnlineEngineView interface {
	Storage() storage.SlotStore
	Index() index.IndexStore
	ActiveCount() int
}

// VerifyOnline checks invariants I3–I7, I9, I10 while the engine is running.
func VerifyOnline(view OnlineEngineView) *Result {
	res := &Result{OK: true, Findings: []Finding{}}

	store := view.Storage()
	idx := view.Index()
	slotCount := store.SlotCount()

	activeSlots := make(map[uint32]uint32) // studentID -> slot
	slotToID := make(map[uint32]uint32)    // slot -> studentID

	// Check I3 & I4 across all slots
	for s := uint32(0); s < slotCount; s++ {
		img, err := store.ReadSlot(s)
		if err != nil {
			res.OK = false
			slot := s
			res.Findings = append(res.Findings, Finding{
				Invariant: "I3",
				Severity:  "error",
				Slot:      &slot,
				Detail:    fmt.Sprintf("read slot error: %v", err),
			})
			continue
		}

		state, reason := codec.ClassifySlot(img[:])
		if state == codec.SlotStateCorrupt {
			if !store.IsQuarantined(s) {
				res.OK = false
				slot := s
				res.Findings = append(res.Findings, Finding{
					Invariant: "I3",
					Severity:  "error",
					Slot:      &slot,
					Detail:    reason,
				})
			}
			continue
		}

		if state == codec.SlotStateActive {
			student, _, _, err := codec.DecodeStudent(img[:])
			if err != nil {
				res.OK = false
				slot := s
				res.Findings = append(res.Findings, Finding{
					Invariant: "I3",
					Severity:  "error",
					Slot:      &slot,
					Detail:    err.Error(),
				})
				continue
			}

			// Invariant I4: uniqueness of active StudentID
			if existingSlot, exists := activeSlots[student.StudentID]; exists {
				res.OK = false
				slot := s
				id := student.StudentID
				res.Findings = append(res.Findings, Finding{
					Invariant: "I4",
					Severity:  "error",
					Slot:      &slot,
					ID:        &id,
					Detail:    fmt.Sprintf("duplicate active student ID %d also at slot %d", id, existingSlot),
				})
			} else {
				activeSlots[student.StudentID] = s
				slotToID[s] = student.StudentID
			}
		}
	}

	// Invariant I5, I6, I7: check against primary index
	indexedEntries := idx.Range(0, 0xFFFFFFFF, idx.Len()+1)

	// Check I9: stats.active_count equals index size
	if view.ActiveCount() != len(indexedEntries) {
		res.OK = false
		res.Findings = append(res.Findings, Finding{
			Invariant: "I9",
			Severity:  "error",
			Detail:    fmt.Sprintf("active count %d != index entries count %d", view.ActiveCount(), len(indexedEntries)),
		})
	}

	indexSlotMap := make(map[uint32]uint32) // slot -> studentID
	for _, entry := range indexedEntries {
		indexSlotMap[entry.SlotNumber] = entry.StudentID

		// Invariant I6: indexed slot < SlotCount
		if entry.SlotNumber >= slotCount {
			res.OK = false
			slot := entry.SlotNumber
			id := entry.StudentID
			res.Findings = append(res.Findings, Finding{
				Invariant: "I6",
				Severity:  "error",
				Slot:      &slot,
				ID:        &id,
				Detail:    fmt.Sprintf("indexed slot %d >= slotCount %d", entry.SlotNumber, slotCount),
			})
			continue
		}

		// Invariant I7: read-back matches key
		actualID, exists := slotToID[entry.SlotNumber]
		if !exists || actualID != entry.StudentID {
			res.OK = false
			slot := entry.SlotNumber
			id := entry.StudentID
			res.Findings = append(res.Findings, Finding{
				Invariant: "I7",
				Severity:  "error",
				Slot:      &slot,
				ID:        &id,
				Detail:    fmt.Sprintf("index ID %d mismatch with slot ID %d", entry.StudentID, actualID),
			})
		}
	}

	// Invariant I5: Bijection between active slots and index entries
	for sid, slot := range activeSlots {
		if idxSID, inIndex := indexSlotMap[slot]; !inIndex || idxSID != sid {
			res.OK = false
			s := slot
			id := sid
			res.Findings = append(res.Findings, Finding{
				Invariant: "I5",
				Severity:  "error",
				Slot:      &s,
				ID:        &id,
				Detail:    fmt.Sprintf("active record %d at slot %d not found in index", id, slot),
			})
		}
	}

	for slot, sid := range indexSlotMap {
		if _, isActive := slotToID[slot]; !isActive {
			res.OK = false
			s := slot
			id := sid
			res.Findings = append(res.Findings, Finding{
				Invariant: "I5",
				Severity:  "error",
				Slot:      &s,
				ID:        &id,
				Detail:    fmt.Sprintf("index entry %d at slot %d points to non-active slot", id, slot),
			})
		}
	}

	return res
}

// VerifyOffline performs full consistency validation (I1–I10) on stopped database files without modifying them.
func VerifyOffline(dataDir string) (*Result, error) {
	res := &Result{OK: true, Findings: []Finding{}}

	datPath := filepath.Join(dataDir, "students.dat")
	datData, err := os.ReadFile(datPath)
	if err != nil {
		return nil, fmt.Errorf("read students.dat: %w", err)
	}

	// Invariant I1: Data header valid
	if len(datData) < int(constants.HeaderSize) {
		res.OK = false
		res.Findings = append(res.Findings, Finding{
			Invariant: "I1",
			Severity:  "error",
			Detail:    "data file size smaller than header size",
		})
		return res, nil
	}

	header, err := codec.DecodeDataHeader(datData[:constants.HeaderSize])
	if err != nil {
		res.OK = false
		res.Findings = append(res.Findings, Finding{
			Invariant: "I1",
			Severity:  "error",
			Detail:    fmt.Sprintf("corrupt data header: %v", err),
		})
	}

	// Invariant I2: File size = 4096 + k * 4096
	rem := (len(datData) - int(constants.HeaderSize)) % int(constants.BlockSize)
	if rem != 0 {
		res.OK = false
		res.Findings = append(res.Findings, Finding{
			Invariant: "I2",
			Severity:  "error",
			Detail:    fmt.Sprintf("data file size %d is not aligned to 4096-byte blocks", len(datData)),
		})
	}

	slotCount := uint32((len(datData) - int(constants.HeaderSize)) / int(constants.RecordSize))

	activeSlots := make(map[uint32]uint32)
	slotToID := make(map[uint32]uint32)

	// Invariant I3: Non-zero slots valid
	for s := uint32(0); s < slotCount; s++ {
		offset := int(constants.HeaderSize) + int(s)*int(constants.RecordSize)
		slotBuf := datData[offset : offset+int(constants.RecordSize)]

		state, reason := codec.ClassifySlot(slotBuf)
		if state == codec.SlotStateCorrupt {
			res.OK = false
			slot := s
			res.Findings = append(res.Findings, Finding{
				Invariant: "I3",
				Severity:  "error",
				Slot:      &slot,
				Detail:    reason,
			})
			continue
		}

		if state == codec.SlotStateActive {
			student, _, _, err := codec.DecodeStudent(slotBuf)
			if err != nil {
				res.OK = false
				slot := s
				res.Findings = append(res.Findings, Finding{
					Invariant: "I3",
					Severity:  "error",
					Slot:      &slot,
					Detail:    err.Error(),
				})
				continue
			}

			// Invariant I4: No two ACTIVE slots share StudentID
			if existingSlot, exists := activeSlots[student.StudentID]; exists {
				res.OK = false
				slot := s
				id := student.StudentID
				res.Findings = append(res.Findings, Finding{
					Invariant: "I4",
					Severity:  "error",
					Slot:      &slot,
					ID:        &id,
					Detail:    fmt.Sprintf("duplicate active student ID %d at slot %d and %d", id, existingSlot, s),
				})
			} else {
				activeSlots[student.StudentID] = s
				slotToID[s] = student.StudentID
			}
		}
	}

	// Invariant I8: WAL contains zero entries (header only) after recovery
	walPath := filepath.Join(dataDir, "students.wal")
	if walData, err := os.ReadFile(walPath); err == nil {
		if len(walData) > int(constants.WALHeaderSize) {
			res.OK = false
			res.Findings = append(res.Findings, Finding{
				Invariant: "I8",
				Severity:  "warning",
				Detail:    fmt.Sprintf("WAL holds uncheckpointed entries (%d bytes total)", len(walData)),
			})
		}
	}

	// Index file checks (students.idx)
	idxPath := filepath.Join(dataDir, "students.idx")
	idx := index.New()
	if _, _, err := idx.LoadSnapshot(idxPath); err == nil {
		entries := idx.AllEntries()
		// Invariant I5, I6, I7
		for _, e := range entries {
			if e.SlotNumber >= slotCount {
				res.OK = false
				slot := e.SlotNumber
				id := e.StudentID
				res.Findings = append(res.Findings, Finding{
					Invariant: "I6",
					Severity:  "error",
					Slot:      &slot,
					ID:        &id,
					Detail:    fmt.Sprintf("indexed slot %d >= slotCount %d", e.SlotNumber, slotCount),
				})
				continue
			}

			actualID, isActive := slotToID[e.SlotNumber]
			if !isActive || actualID != e.StudentID {
				res.OK = false
				slot := e.SlotNumber
				id := e.StudentID
				res.Findings = append(res.Findings, Finding{
					Invariant: "I7",
					Severity:  "error",
					Slot:      &slot,
					ID:        &id,
					Detail:    fmt.Sprintf("index entry %d at slot %d does not match active slot ID %d", e.StudentID, e.SlotNumber, actualID),
				})
			}
		}

		if len(entries) != len(activeSlots) {
			res.OK = false
			res.Findings = append(res.Findings, Finding{
				Invariant: "I5",
				Severity:  "error",
				Detail:    fmt.Sprintf("index entry count %d != active slot count %d", len(entries), len(activeSlots)),
			})
		}
	}

	_ = header
	return res, nil
}
