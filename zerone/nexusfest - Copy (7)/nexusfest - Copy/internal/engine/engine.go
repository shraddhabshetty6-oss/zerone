// Package engine implements the Record Manager (GO-06) CRUD facade per ARCHITECTURE.md §E.1, §E.2.
package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"studentdb/internal/codec"
	"studentdb/internal/concurrency"
	"studentdb/internal/constants"
	"studentdb/internal/index"
	"studentdb/internal/record"
	"studentdb/internal/recovery"
	"studentdb/internal/retrieval"
	"studentdb/internal/storage"
	"studentdb/internal/tx"
	"studentdb/internal/verify"
	"studentdb/internal/wal"
)

var (
	// ErrDuplicateID is returned on attempt to insert an already-existing active StudentID.
	ErrDuplicateID = errors.New("record already exists with student_id (DUPLICATE_ID)")

	// ErrNotFound is returned when operating on a non-existent student record.
	ErrNotFound = errors.New("record not found (NOT_FOUND)")
)

// TxInfo contains transaction metadata returned to clients upon commit.
type TxInfo struct {
	TxID      uint64 `json:"tx_id"`
	CommitLSN uint64 `json:"commit_lsn"`
}

// BatchOp represents one operation within an atomic batch.
type BatchOp struct {
	Op        string          `json:"op"` // "insert", "update", "delete"
	Student   *record.Student `json:"student,omitempty"`
	StudentID uint32          `json:"student_id,omitempty"`
}

// BatchResult represents the result of a single operation in a batch.
type BatchResult struct {
	Op        string          `json:"op"`
	StudentID uint32          `json:"student_id"`
	Student   *record.Student `json:"student,omitempty"`
}

// Stats holds runtime engine metrics.
type Stats struct {
	ActiveCount       int    `json:"active_count"`
	SlotCount         uint32 `json:"slot_count"`
	WalSizeBytes      int64  `json:"wal_size_bytes"`
	LastCheckpointLSN uint64 `json:"last_checkpoint_lsn"`
}

// DB represents a live BinStudentDB instance.
type DB struct {
	mu         sync.RWMutex
	cfg        recovery.Config
	lockMgr    *concurrency.LockManager
	storage    *storage.Engine
	wal        *wal.Manager
	index      *index.BTreeIndex
	txMgr      *tx.Manager
	seqEngine  *retrieval.SequentialEngine
	idxEngine  *retrieval.IndexedEngine
	recovery   *recovery.Report
	reportPath string
	state      string
	closed     bool
}

// Open initializes the database engine, executing crash recovery first.
func Open(cfg recovery.Config) (*DB, error) {
	lockMgr := concurrency.NewLockManager(constants.DefaultTxLockTimeout)

	recState, err := recovery.Recover(cfg, lockMgr)
	if err != nil {
		_ = lockMgr.ReleaseProcessLock()
		return nil, err
	}

	txMgr := tx.NewManager(
		recState.WAL,
		recState.Storage,
		recState.Index,
		lockMgr,
		cfg.WalMaxBytes,
		cfg.DataDir,
		recState.WAL.NextTxID(),
		uint64(recState.Report.TxCommittedRedone),
	)

	seqEng := retrieval.NewSequentialEngine(recState.Storage, lockMgr)
	idxEng := retrieval.NewIndexedEngine(recState.Index, recState.Storage, lockMgr)

	db := &DB{
		cfg:        cfg,
		lockMgr:    lockMgr,
		storage:    recState.Storage,
		wal:        recState.WAL,
		index:      recState.Index,
		txMgr:      txMgr,
		seqEngine:  seqEng,
		idxEngine:  idxEng,
		recovery:   recState.Report,
		reportPath: recState.ReportPath,
		state:      recState.State,
	}

	return db, nil
}

// Close gracefully closes the engine after performing a checkpoint.
func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if db.closed {
		return nil
	}
	db.closed = true

	// Graceful checkpoint
	_ = db.txMgr.Checkpoint()

	var errs []error
	if err := db.storage.Close(); err != nil {
		errs = append(errs, err)
	}
	if err := db.wal.Close(); err != nil {
		errs = append(errs, err)
	}
	if err := db.lockMgr.ReleaseProcessLock(); err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return fmt.Errorf("close errors: %v", errs)
	}
	return nil
}

// State returns the current engine state ("READY", "READY_DEGRADED", "FAILED_READONLY").
func (db *DB) State() string {
	if db.txMgr.IsReadOnly() {
		return "FAILED_READONLY"
	}
	return db.state
}

// RecoveryReport returns the report produced during startup recovery.
func (db *DB) RecoveryReport() *recovery.Report {
	return db.recovery
}

// ReportPath returns the path to recovery_report.json.
func (db *DB) ReportPath() string {
	return db.reportPath
}

// Stats returns engine stats.
func (db *DB) Stats() Stats {
	return Stats{
		ActiveCount:       db.index.Len(),
		SlotCount:         db.storage.SlotCount(),
		WalSizeBytes:      db.wal.Size(),
		LastCheckpointLSN: db.txMgr.LastCheckpointLSN(),
	}
}

// ForceCheckpoint initiates an explicit checkpoint.
func (db *DB) ForceCheckpoint() (uint64, error) {
	err := db.txMgr.Checkpoint()
	return db.txMgr.LastCheckpointLSN(), err
}

// Create inserts a new student record (FR-01).
func (db *DB) Create(ctx context.Context, s *record.Student) (*record.Student, TxInfo, error) {
	if err := s.Validate(); err != nil {
		return nil, TxInfo{}, err
	}

	// Uniqueness check
	if _, found := db.index.Lookup(s.StudentID); found {
		return nil, TxInfo{}, ErrDuplicateID
	}

	txn, err := db.txMgr.Begin(ctx)
	if err != nil {
		return nil, TxInfo{}, err
	}
	defer txn.Rollback()

	slot, err := db.storage.AllocateSlot()
	if err != nil {
		return nil, TxInfo{}, err
	}

	now := time.Now().UTC()
	s.CreatedAt = now
	s.UpdatedAt = now
	s.RecordVersion = txn.TxID()

	image, err := codec.EncodeStudent(s, txn.TxID())
	if err != nil {
		return nil, TxInfo{}, err
	}

	if err := txn.Stage(tx.StagedOp{
		Type:       "insert",
		StudentID:  s.StudentID,
		SlotNumber: slot,
		SlotImage:  image,
	}); err != nil {
		return nil, TxInfo{}, err
	}

	commitLSN, err := txn.Commit()
	if err != nil {
		return nil, TxInfo{}, err
	}

	return s, TxInfo{TxID: txn.TxID(), CommitLSN: commitLSN}, nil
}

// Get reads a student record by ID (FR-02).
func (db *DB) Get(ctx context.Context, id uint32) (*record.Student, error) {
	return db.idxEngine.GetByID(id)
}

// Update replaces an existing student record (FR-03).
func (db *DB) Update(ctx context.Context, id uint32, s *record.Student) (*record.Student, TxInfo, error) {
	s.StudentID = id
	if err := s.Validate(); err != nil {
		return nil, TxInfo{}, err
	}

	// Read existing to preserve CreatedAt
	existing, err := db.idxEngine.GetByID(id)
	if err != nil {
		return nil, TxInfo{}, ErrNotFound
	}

	slot, found := db.index.Lookup(id)
	if !found {
		return nil, TxInfo{}, ErrNotFound
	}

	txn, err := db.txMgr.Begin(ctx)
	if err != nil {
		return nil, TxInfo{}, err
	}
	defer txn.Rollback()

	now := time.Now().UTC()
	s.CreatedAt = existing.CreatedAt
	s.UpdatedAt = now
	s.RecordVersion = txn.TxID()

	image, err := codec.EncodeStudent(s, txn.TxID())
	if err != nil {
		return nil, TxInfo{}, err
	}

	db.storage.ReserveSlot(slot)
	if err := txn.Stage(tx.StagedOp{
		Type:       "update",
		StudentID:  id,
		SlotNumber: slot,
		SlotImage:  image,
	}); err != nil {
		return nil, TxInfo{}, err
	}

	commitLSN, err := txn.Commit()
	if err != nil {
		return nil, TxInfo{}, err
	}

	return s, TxInfo{TxID: txn.TxID(), CommitLSN: commitLSN}, nil
}

// Delete removes a student record by ID (FR-04).
func (db *DB) Delete(ctx context.Context, id uint32) (TxInfo, error) {
	slot, found := db.index.Lookup(id)
	if !found {
		return TxInfo{}, ErrNotFound
	}

	txn, err := db.txMgr.Begin(ctx)
	if err != nil {
		return TxInfo{}, err
	}
	defer txn.Rollback()

	tombstone := codec.EncodeTombstone(id, txn.TxID())
	db.storage.ReserveSlot(slot)

	if err := txn.Stage(tx.StagedOp{
		Type:       "delete",
		StudentID:  id,
		SlotNumber: slot,
		SlotImage:  tombstone,
	}); err != nil {
		return TxInfo{}, err
	}

	commitLSN, err := txn.Commit()
	if err != nil {
		return TxInfo{}, err
	}

	return TxInfo{TxID: txn.TxID(), CommitLSN: commitLSN}, nil
}

// Batch executes multi-operation atomic transactions (FR-05).
func (db *DB) Batch(ctx context.Context, ops []BatchOp) (TxInfo, []BatchResult, error) {
	if len(ops) == 0 {
		return TxInfo{}, nil, nil
	}
	if len(ops) > constants.MaxOpsPerTx {
		return TxInfo{}, nil, tx.ErrTooManyOps
	}

	txn, err := db.txMgr.Begin(ctx)
	if err != nil {
		return TxInfo{}, nil, err
	}
	defer txn.Rollback()

	// Track staged changes in memory for validation
	stagedState := make(map[uint32]*record.Student)
	stagedDeleted := make(map[uint32]bool)
	stagedSlots := make(map[uint32]uint32)

	results := make([]BatchResult, 0, len(ops))

	for _, op := range ops {
		switch op.Op {
		case "insert":
			if op.Student == nil {
				return TxInfo{}, nil, errors.New("batch insert: student body required")
			}
			if err := op.Student.Validate(); err != nil {
				return TxInfo{}, nil, err
			}
			sid := op.Student.StudentID

			// Check staged conflict or live index conflict
			if _, inStaged := stagedState[sid]; inStaged {
				return TxInfo{}, nil, ErrDuplicateID
			}
			if !stagedDeleted[sid] {
				if _, inIdx := db.index.Lookup(sid); inIdx {
					return TxInfo{}, nil, ErrDuplicateID
				}
			}

			slot, err := db.storage.AllocateSlot()
			if err != nil {
				return TxInfo{}, nil, err
			}

			now := time.Now().UTC()
			op.Student.CreatedAt = now
			op.Student.UpdatedAt = now
			op.Student.RecordVersion = txn.TxID()

			img, err := codec.EncodeStudent(op.Student, txn.TxID())
			if err != nil {
				return TxInfo{}, nil, err
			}

			if err := txn.Stage(tx.StagedOp{
				Type:       "insert",
				StudentID:  sid,
				SlotNumber: slot,
				SlotImage:  img,
			}); err != nil {
				return TxInfo{}, nil, err
			}

			stagedState[sid] = op.Student
			delete(stagedDeleted, sid)
			stagedSlots[sid] = slot
			results = append(results, BatchResult{Op: "insert", StudentID: sid, Student: op.Student})

		case "update":
			if op.Student == nil {
				return TxInfo{}, nil, errors.New("batch update: student body required")
			}
			sid := op.StudentID
			if sid == 0 {
				sid = op.Student.StudentID
			}
			op.Student.StudentID = sid
			if err := op.Student.Validate(); err != nil {
				return TxInfo{}, nil, err
			}

			var slot uint32
			var origCreatedAt time.Time

			if existing, inStaged := stagedState[sid]; inStaged {
				slot = stagedSlots[sid]
				origCreatedAt = existing.CreatedAt
			} else if !stagedDeleted[sid] {
				s, found := db.index.Lookup(sid)
				if !found {
					return TxInfo{}, nil, ErrNotFound
				}
				slot = s
				ex, err := db.idxEngine.GetByID(sid)
				if err != nil {
					return TxInfo{}, nil, ErrNotFound
				}
				origCreatedAt = ex.CreatedAt
			} else {
				return TxInfo{}, nil, ErrNotFound
			}

			now := time.Now().UTC()
			op.Student.CreatedAt = origCreatedAt
			op.Student.UpdatedAt = now
			op.Student.RecordVersion = txn.TxID()

			img, err := codec.EncodeStudent(op.Student, txn.TxID())
			if err != nil {
				return TxInfo{}, nil, err
			}

			db.storage.ReserveSlot(slot)
			if err := txn.Stage(tx.StagedOp{
				Type:       "update",
				StudentID:  sid,
				SlotNumber: slot,
				SlotImage:  img,
			}); err != nil {
				return TxInfo{}, nil, err
			}

			stagedState[sid] = op.Student
			results = append(results, BatchResult{Op: "update", StudentID: sid, Student: op.Student})

		case "delete":
			sid := op.StudentID
			if sid == 0 && op.Student != nil {
				sid = op.Student.StudentID
			}

			var slot uint32
			if _, inStaged := stagedState[sid]; inStaged {
				slot = stagedSlots[sid]
				delete(stagedState, sid)
			} else if !stagedDeleted[sid] {
				s, found := db.index.Lookup(sid)
				if !found {
					return TxInfo{}, nil, ErrNotFound
				}
				slot = s
			} else {
				return TxInfo{}, nil, ErrNotFound
			}

			tombstone := codec.EncodeTombstone(sid, txn.TxID())
			db.storage.ReserveSlot(slot)
			if err := txn.Stage(tx.StagedOp{
				Type:       "delete",
				StudentID:  sid,
				SlotNumber: slot,
				SlotImage:  tombstone,
			}); err != nil {
				return TxInfo{}, nil, err
			}

			stagedDeleted[sid] = true
			results = append(results, BatchResult{Op: "delete", StudentID: sid})

		default:
			return TxInfo{}, nil, fmt.Errorf("unknown batch operation: %q", op.Op)
		}
	}

	commitLSN, err := txn.Commit()
	if err != nil {
		return TxInfo{}, nil, err
	}

	return TxInfo{TxID: txn.TxID(), CommitLSN: commitLSN}, results, nil
}

// SequentialScan executes a physical slot scan (FR-06).
func (db *DB) SequentialScan(cursor uint32, limit int, filter retrieval.Filter, strict bool) (*retrieval.ScanPage, error) {
	return db.seqEngine.Scan(cursor, limit, filter, strict)
}

// RangeScan executes an indexed range query (FR-07).
func (db *DB) RangeScan(from, to uint32, limit int) ([]*record.Student, *uint32, error) {
	return db.idxEngine.RangeByID(from, to, limit)
}

// VerifyOnline runs online invariant checks (I3–I7, I9, I10).
func (db *DB) VerifyOnline() *verify.Result {
	unlock := db.lockMgr.AcquireStateRLock()
	defer unlock()

	return verify.VerifyOnline(db)
}

// Storage returns the underlying SlotStore (implements OnlineEngineView).
func (db *DB) Storage() storage.SlotStore {
	return db.storage
}

// Index returns the underlying IndexStore (implements OnlineEngineView).
func (db *DB) Index() index.IndexStore {
	return db.index
}

// ActiveCount returns the number of active records.
func (db *DB) ActiveCount() int {
	return db.index.Len()
}

