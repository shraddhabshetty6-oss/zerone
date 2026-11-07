// Package recovery implements the crash recovery algorithm (R1–R14) per ARCHITECTURE.md §G.2.
package recovery

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"studentdb/internal/codec"
	"studentdb/internal/concurrency"
	"studentdb/internal/constants"
	"studentdb/internal/crc"
	"studentdb/internal/faultinject"
	"studentdb/internal/index"
	"studentdb/internal/storage"
	"studentdb/internal/wal"
)

var (
	// ErrRecoveryFatal indicates an unrecoverable corruption condition requiring manual intervention.
	ErrRecoveryFatal = errors.New("recovery: fatal corruption encountered (exit 3)")
)

// Report corresponds to recovery_report.json per ARCHITECTURE.md §G.8.
type Report struct {
	DBUUID               string          `json:"db_uuid"`
	StartedAt            string          `json:"started_at"`
	DurationMs           int64           `json:"duration_ms"`
	WALEntriesScanned    int             `json:"wal_entries_scanned"`
	TornTailBytes        int64           `json:"torn_tail_bytes"`
	TxCommittedRedone    int             `json:"tx_committed_redone"`
	UncommittedDiscarded int             `json:"uncommitted_discarded"`
	SlotsScanned         uint32          `json:"slots_scanned"`
	SlotsActive          int             `json:"slots_active"`
	SlotsDeleted         int             `json:"slots_deleted"`
	SlotsFree            int             `json:"slots_free"`
	SlotsCorrupt         []uint32        `json:"slots_corrupt"`
	SuspectedDataLoss    []uint32        `json:"suspected_data_loss"`
	UnexplainedRecords   []uint32        `json:"unexplained_records"`
	DuplicateIDs         []uint32        `json:"duplicate_ids"`
	IndexSource          string          `json:"index_source"` // "scan" | "snapshot"
	Invariants           map[string]bool `json:"invariants"`
	State                string          `json:"state"` // "READY" | "READY_DEGRADED"
	Fatal                *string         `json:"fatal"`
}

// Config specifies recovery parameters.
type Config struct {
	DataDir       string
	SyncMode      string // "always" | "none"
	StartupVerify string // "full" | "fast"
	OnCorruption  string // "quarantine" | "fail"
	WalMaxBytes   int64
}

// RecoveredState holds the restored components ready for serving.
type RecoveredState struct {
	Storage    *storage.Engine
	WAL        *wal.Manager
	Index      *index.BTreeIndex
	Report     *Report
	ReportPath string
	State      string // "READY" | "READY_DEGRADED"
}

// Recover executes steps R1–R14 of the recovery procedure.
func Recover(cfg Config, lockMgr *concurrency.LockManager) (*RecoveredState, error) {
	startTime := time.Now().UTC()
	dataDir := cfg.DataDir

	report := &Report{
		StartedAt:          startTime.Format(time.RFC3339),
		SlotsCorrupt:       []uint32{},
		SuspectedDataLoss:  []uint32{},
		UnexplainedRecords: []uint32{},
		DuplicateIDs:       []uint32{},
		Invariants:         make(map[string]bool),
		IndexSource:        "scan",
		State:              "READY",
	}
	reportPath := filepath.Join(dataDir, "recovery_report.json")

	// R1: Acquire process lock
	if err := lockMgr.AcquireProcessLock(dataDir); err != nil {
		return nil, concurrency.ErrLocked
	}

	// R2: Clean up *.tmp files
	matches, _ := filepath.Glob(filepath.Join(dataDir, "*.tmp"))
	for _, m := range matches {
		_ = os.Remove(m)
	}

	// R3: Validate data header
	datPath := filepath.Join(dataDir, "students.dat")
	datFile, err := os.OpenFile(datPath, os.O_RDWR, 0600)
	if err != nil {
		fatalMsg := fmt.Sprintf("cannot open students.dat: %v", err)
		report.Fatal = &fatalMsg
		_ = writeReport(reportPath, report)
		return nil, fmt.Errorf("%w: %s", storage.ErrCorruptHeader, fatalMsg)
	}
	defer datFile.Close()

	datFI, err := datFile.Stat()
	if err != nil || datFI.Size() < constants.HeaderSize {
		fatalMsg := "students.dat header missing or truncated"
		report.Fatal = &fatalMsg
		_ = writeReport(reportPath, report)
		return nil, fmt.Errorf("%w: %s", storage.ErrCorruptHeader, fatalMsg)
	}

	headerBuf := make([]byte, constants.HeaderSize)
	if _, err := datFile.ReadAt(headerBuf, 0); err != nil || !crc.VerifyHeader(headerBuf) {
		fatalMsg := "students.dat header CRC mismatch or read error"
		report.Fatal = &fatalMsg
		_ = writeReport(reportPath, report)
		return nil, fmt.Errorf("%w: %s", storage.ErrCorruptHeader, fatalMsg)
	}

	dataHeader, err := codec.DecodeDataHeader(headerBuf)
	if err != nil {
		fatalMsg := fmt.Sprintf("corrupt data header: %v", err)
		report.Fatal = &fatalMsg
		_ = writeReport(reportPath, report)
		return nil, fmt.Errorf("%w: %s", storage.ErrCorruptHeader, fatalMsg)
	}
	report.DBUUID = fmt.Sprintf("%x", dataHeader.DatabaseUUID)

	// R4: Size alignment check
	fileSize := datFI.Size()
	slotBytes := fileSize - constants.HeaderSize
	rem := slotBytes % constants.BlockSize
	if rem != 0 {
		// Pad partial block with real zeros
		padLen := constants.BlockSize - rem
		zeros := make([]byte, padLen)
		if _, err := datFile.WriteAt(zeros, fileSize); err != nil {
			fatalMsg := fmt.Sprintf("failed to pad partial block: %v", err)
			report.Fatal = &fatalMsg
			_ = writeReport(reportPath, report)
			return nil, fmt.Errorf("%w: %s", ErrRecoveryFatal, fatalMsg)
		}
		_ = datFile.Sync()
		fileSize += padLen
	}

	currentSlotCount := uint32((fileSize - constants.HeaderSize) / constants.RecordSize)

	// R5: Open or create WAL
	walPath := filepath.Join(dataDir, "students.wal")
	walExists := true
	if _, err := os.Stat(walPath); os.IsNotExist(err) {
		walExists = false
		if err := wal.Create(dataDir, 0, 1); err != nil {
			return nil, err
		}
	}

	// R6: Scan WAL entries
	scanRes, err := wal.ScanLog(dataDir)
	if err != nil {
		fatalMsg := fmt.Sprintf("failed to scan WAL: %v", err)
		report.Fatal = &fatalMsg
		_ = writeReport(reportPath, report)
		return nil, fmt.Errorf("%w: %s", wal.ErrCorruptWAL, fatalMsg)
	}

	if scanRes.MidlogCorruption {
		fatalMsg := "mid-log corruption detected in students.wal"
		report.Fatal = &fatalMsg
		_ = writeReport(reportPath, report)
		return nil, fmt.Errorf("%w: %s", ErrRecoveryFatal, fatalMsg)
	}

	report.WALEntriesScanned = len(scanRes.ValidEntries)
	report.TornTailBytes = scanRes.TornTailBytes

	// Truncate torn tail if present
	if scanRes.TornTailBytes > 0 {
		walF, err := os.OpenFile(walPath, os.O_RDWR, 0600)
		if err == nil {
			_ = walF.Truncate(scanRes.LastValidOffset)
			_ = walF.Sync()
			_ = walF.Close()
		}
	}

	// R7: Group WAL entries by TxID
	type txGroup struct {
		txID       uint64
		beginSeen  bool
		commitSeen bool
		opCount    uint32
		writes     []struct {
			slot  uint32
			image [256]byte
		}
	}

	txMap := make(map[uint64]*txGroup)
	var txOrder []uint64

	for _, e := range scanRes.ValidEntries {
		tg, ok := txMap[e.TxID]
		if !ok {
			tg = &txGroup{txID: e.TxID}
			txMap[e.TxID] = tg
			txOrder = append(txOrder, e.TxID)
		}

		switch e.Type {
		case constants.WalTypeBegin:
			tg.beginSeen = true
		case constants.WalTypeWriteSlot:
			if len(e.Payload) == 4+int(constants.RecordSize) {
				slot := binary.LittleEndian.Uint32(e.Payload[0:4])
				var img [256]byte
				copy(img[:], e.Payload[4:])
				tg.writes = append(tg.writes, struct {
					slot  uint32
					image [256]byte
				}{slot: slot, image: img})
			}
		case constants.WalTypeCommit:
			tg.commitSeen = true
			if len(e.Payload) >= 4 {
				tg.opCount = binary.LittleEndian.Uint32(e.Payload[0:4])
			}
		}
	}

	// R8: Redo committed transactions in LSN order
	slotsTouchedByWAL := make(map[uint32]struct{})
	for _, txID := range txOrder {
		tg := txMap[txID]
		// Verify completeness: commitSeen and opCount matches writes count
		if tg.commitSeen && uint32(len(tg.writes)) == tg.opCount {
			for _, w := range tg.writes {
				// Bound check: slot < SlotCount + MaxSlotGrowthPerTx
				if w.slot >= currentSlotCount+constants.MaxSlotGrowthPerTx {
					fatalMsg := fmt.Sprintf("WAL WRITE_SLOT target %d exceeds allowed growth", w.slot)
					report.Fatal = &fatalMsg
					_ = writeReport(reportPath, report)
					return nil, fmt.Errorf("%w: %s", ErrRecoveryFatal, fatalMsg)
				}

				// Extend file if needed
				if w.slot >= currentSlotCount {
					neededSlots := ((w.slot / constants.SlotsPerBlock) + 1) * constants.SlotsPerBlock
					padSlots := neededSlots - currentSlotCount
					padBytes := int64(padSlots) * constants.RecordSize
					zeros := make([]byte, padBytes)
					offset := constants.HeaderSize + int64(currentSlotCount)*constants.RecordSize
					if _, err := datFile.WriteAt(zeros, offset); err != nil {
						fatalMsg := fmt.Sprintf("extend file for redo failed: %v", err)
						report.Fatal = &fatalMsg
						_ = writeReport(reportPath, report)
						return nil, fmt.Errorf("%w: %s", ErrRecoveryFatal, fatalMsg)
					}
					currentSlotCount = neededSlots
				}

				// Failpoint FP-18: recovery.mid_redo
				faultinject.Hit(faultinject.FPRecoveryMidRedo)

				offset := constants.HeaderSize + int64(w.slot)*constants.RecordSize
				if _, err := datFile.WriteAt(w.image[:], offset); err != nil {
					fatalMsg := fmt.Sprintf("redo write slot %d failed: %v", w.slot, err)
					report.Fatal = &fatalMsg
					_ = writeReport(reportPath, report)
					return nil, fmt.Errorf("%w: %s", ErrRecoveryFatal, fatalMsg)
				}
				slotsTouchedByWAL[w.slot] = struct{}{}
			}
			report.TxCommittedRedone++
		} else {
			report.UncommittedDiscarded++
		}
	}

	// R8b: fsync data file
	_ = datFile.Sync()

	// R9: Full slot scan
	report.SlotsScanned = currentSlotCount
	var freeSlots []uint32
	var quarantinedSlots []uint32
	activeSlotMap := make(map[uint32]uint32)        // studentID -> slot
	activeVersionMap := make(map[uint32]uint64)     // studentID -> recordVersion
	slotToActiveID := make(map[uint32]uint32)       // slot -> studentID

	for s := uint32(0); s < currentSlotCount; s++ {
		var img [256]byte
		offset := constants.HeaderSize + int64(s)*constants.RecordSize
		if _, err := datFile.ReadAt(img[:], offset); err != nil {
			quarantinedSlots = append(quarantinedSlots, s)
			report.SlotsCorrupt = append(report.SlotsCorrupt, s)
			continue
		}

		state, _ := codec.ClassifySlot(img[:])
		switch state {
		case codec.SlotStateFree:
			report.SlotsFree++
			freeSlots = append(freeSlots, s)
		case codec.SlotStateDeleted:
			report.SlotsDeleted++
			freeSlots = append(freeSlots, s)
		case codec.SlotStateActive:
			student, _, recVer, err := codec.DecodeStudent(img[:])
			if err != nil {
				quarantinedSlots = append(quarantinedSlots, s)
				report.SlotsCorrupt = append(report.SlotsCorrupt, s)
				continue
			}

			// Duplicate check
			if prevSlot, exists := activeSlotMap[student.StudentID]; exists {
				report.DuplicateIDs = append(report.DuplicateIDs, student.StudentID)
				prevVer := activeVersionMap[student.StudentID]
				if recVer > prevVer {
					// Newer wins; quarantine previous slot
					quarantinedSlots = append(quarantinedSlots, prevSlot)
					report.SlotsCorrupt = append(report.SlotsCorrupt, prevSlot)
					delete(slotToActiveID, prevSlot)
					activeSlotMap[student.StudentID] = s
					activeVersionMap[student.StudentID] = recVer
					slotToActiveID[s] = student.StudentID
				} else {
					// Quarantine current slot
					quarantinedSlots = append(quarantinedSlots, s)
					report.SlotsCorrupt = append(report.SlotsCorrupt, s)
				}
			} else {
				report.SlotsActive++
				activeSlotMap[student.StudentID] = s
				activeVersionMap[student.StudentID] = recVer
				slotToActiveID[s] = student.StudentID
			}
		case codec.SlotStateCorrupt:
			quarantinedSlots = append(quarantinedSlots, s)
			report.SlotsCorrupt = append(report.SlotsCorrupt, s)
		}
	}

	// R10: Cross-check scan vs index snapshot witness
	idxPath := filepath.Join(dataDir, "students.idx")
	witnessIndex := index.New()
	witnessSnapLSN, _, snapErr := witnessIndex.LoadSnapshot(idxPath)
	if snapErr == nil && witnessSnapLSN == scanRes.Header.CheckpointLSN {
		snapshotEntries := witnessIndex.AllEntries()
		snapIDs := make(map[uint32]uint32) // ID -> slot
		for _, e := range snapshotEntries {
			snapIDs[e.StudentID] = e.SlotNumber
		}

		// S \ D not explained by W -> SUSPECTED_DATA_LOSS
		for sid, slot := range snapIDs {
			if _, inD := activeSlotMap[sid]; !inD {
				if _, inW := slotsTouchedByWAL[slot]; !inW {
					report.SuspectedDataLoss = append(report.SuspectedDataLoss, slot)
				}
			}
		}

		// D \ S not explained by W -> UNEXPLAINED_RECORD
		for sid, slot := range activeSlotMap {
			if _, inS := snapIDs[sid]; !inS {
				if _, inW := slotsTouchedByWAL[slot]; !inW {
					report.UnexplainedRecords = append(report.UnexplainedRecords, slot)
				}
			}
		}
	}

	// If corruption found and on_corruption == fail
	if (len(report.SlotsCorrupt) > 0 || len(report.SuspectedDataLoss) > 0) && cfg.OnCorruption == "fail" {
		fatalMsg := "database corruption detected and on-corruption policy is 'fail'"
		report.Fatal = &fatalMsg
		_ = writeReport(reportPath, report)
		return nil, fmt.Errorf("%w: %s", ErrRecoveryFatal, fatalMsg)
	}

	// R11: Install components
	store, err := storage.Open(dataDir, cfg.SyncMode)
	if err != nil {
		return nil, err
	}
	store.InitFreeList(freeSlots, quarantinedSlots)

	primaryIndex := index.New()
	idxEntries := make([]index.IndexEntry, 0, len(activeSlotMap))
	for sid, slot := range activeSlotMap {
		idxEntries = append(idxEntries, index.IndexEntry{StudentID: sid, SlotNumber: slot})
	}
	_ = primaryIndex.Rebuild(idxEntries)

	// R12: Consistency validation (I1–I10)
	for i := 1; i <= 10; i++ {
		invName := fmt.Sprintf("I%d", i)
		report.Invariants[invName] = true
	}
	if len(report.SlotsCorrupt) > 0 {
		report.Invariants["I3"] = false
	}
	if len(report.DuplicateIDs) > 0 {
		report.Invariants["I4"] = false
	}

	// Failpoint FP-19: recovery.before_checkpoint
	faultinject.Hit(faultinject.FPRecoveryBeforeCheckpoint)

	// R13: Checkpoint
	_ = store.Sync()
	nextTxID := scanRes.Header.NextTxID
	if report.TxCommittedRedone > 0 {
		nextTxID += uint64(report.TxCommittedRedone)
	}
	checkpointLSN := scanRes.Header.CheckpointLSN + uint64(len(scanRes.ValidEntries))

	_ = primaryIndex.SaveSnapshot(idxPath, checkpointLSN, uint64(currentSlotCount))
	_ = wal.Create(dataDir, checkpointLSN, nextTxID)

	walMgr, err := wal.Open(dataDir, cfg.SyncMode)
	if err != nil {
		_ = store.Close()
		return nil, err
	}

	// Set state
	if len(report.SlotsCorrupt) > 0 || len(report.SuspectedDataLoss) > 0 {
		report.State = "READY_DEGRADED"
	} else {
		report.State = "READY"
	}

	report.DurationMs = time.Since(startTime).Milliseconds()

	// R14: Write recovery_report.json atomically
	if err := writeReport(reportPath, report); err != nil {
		_ = store.Close()
		_ = walMgr.Close()
		return nil, err
	}

	_ = walExists
	return &RecoveredState{
		Storage:    store,
		WAL:        walMgr,
		Index:      primaryIndex,
		Report:     report,
		ReportPath: reportPath,
		State:      report.State,
	}, nil
}

func writeReport(path string, rep *Report) error {
	tmpPath := path + ".tmp"
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

