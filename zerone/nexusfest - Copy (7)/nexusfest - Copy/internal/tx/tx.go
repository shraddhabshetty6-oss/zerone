// Package tx implements transaction management and write ordering per ARCHITECTURE.md §F.3, §F.4, §F.6.
package tx

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"

	"studentdb/internal/concurrency"
	"studentdb/internal/constants"
	"studentdb/internal/faultinject"
	"studentdb/internal/index"
	"studentdb/internal/storage"
	"studentdb/internal/wal"
)

var (
	// ErrTooManyOps is returned when a transaction exceeds MaxOpsPerTx (1,000 ops).
	ErrTooManyOps = errors.New("tx: transaction exceeds maximum allowed operations (1000)")

	// ErrFailedReadOnly is returned for writes when the engine has encountered an I/O error.
	ErrFailedReadOnly = errors.New("tx: engine is in failed read-only state (ENGINE_FAILED_READONLY)")
)

// StagedOp represents one operation staged within an active transaction.
type StagedOp struct {
	Type       string // "insert", "update", "delete"
	StudentID  uint32
	SlotNumber uint32
	SlotImage  [256]byte
}

// Transaction represents an active in-flight transaction.
type Transaction struct {
	txID       uint64
	tm         *Manager
	unlockW    func()
	stagedOps  []StagedOp
	committed  bool
	rolledBack bool
}

// Manager manages transactions, write ordering, and checkpoints.
type Manager struct {
	mu           sync.Mutex
	wal          *wal.Manager
	storage      storage.SlotStore
	index        index.IndexStore
	lockMgr      *concurrency.LockManager
	walMaxBytes  int64
	dataDir      string
	nextTxID     uint64
	failedRO     atomic.Bool
	lastCheckLSN uint64
}

// NewManager creates a new transaction manager.
func NewManager(
	walMgr *wal.Manager,
	store storage.SlotStore,
	idx index.IndexStore,
	lockMgr *concurrency.LockManager,
	walMaxBytes int64,
	dataDir string,
	initialTxID uint64,
	initialLSN uint64,
) *Manager {
	if walMaxBytes <= 0 {
		walMaxBytes = constants.DefaultWALMaxBytes
	}
	m := &Manager{
		wal:          walMgr,
		storage:      store,
		index:        idx,
		lockMgr:      lockMgr,
		walMaxBytes:  walMaxBytes,
		dataDir:      dataDir,
		nextTxID:     initialTxID,
		lastCheckLSN: initialLSN,
	}
	return m
}

// IsReadOnly returns true if the engine has entered sticky FAILED_READONLY.
func (m *Manager) IsReadOnly() bool {
	return m.failedRO.Load()
}

// MarkReadOnly sets the sticky FAILED_READONLY flag.
func (m *Manager) MarkReadOnly() {
	m.failedRO.Store(true)
}

// Begin begins a new transaction by acquiring the writer lock.
func (m *Manager) Begin(ctx context.Context) (*Transaction, error) {
	if m.IsReadOnly() {
		return nil, ErrFailedReadOnly
	}

	unlockW, err := m.lockMgr.AcquireWriter(ctx)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	txID := m.nextTxID
	m.nextTxID++
	m.mu.Unlock()

	return &Transaction{
		txID:      txID,
		tm:        m,
		unlockW:   unlockW,
		stagedOps: make([]StagedOp, 0, 4),
	}, nil
}

// TxID returns the transaction ID assigned to this transaction.
func (tx *Transaction) TxID() uint64 {
	return tx.txID
}

// Stage stages an operation in memory (Rule W1: no-steal).
func (tx *Transaction) Stage(op StagedOp) error {
	if len(tx.stagedOps) >= constants.MaxOpsPerTx {
		return ErrTooManyOps
	}
	tx.stagedOps = append(tx.stagedOps, op)
	return nil
}

// Commit executes write ordering W2–W5 per ARCHITECTURE.md §F.4.
func (tx *Transaction) Commit() (commitLSN uint64, err error) {
	defer tx.unlockW()

	if tx.committed || tx.rolledBack {
		return 0, errors.New("tx: transaction already completed")
	}

	if len(tx.stagedOps) == 0 {
		tx.committed = true
		return 0, nil
	}

	if tx.tm.IsReadOnly() {
		tx.rollbackStaging()
		return 0, ErrFailedReadOnly
	}

	// Prepare WAL slot operations
	walOps := make([]wal.TxSlotOp, len(tx.stagedOps))
	for i, op := range tx.stagedOps {
		walOps[i] = wal.TxSlotOp{
			SlotNumber: op.SlotNumber,
			SlotImage:  op.SlotImage,
		}
	}

	// Rule W2 + W3: Append to WAL and fsync (COMMIT POINT)
	commitLSN, err = tx.tm.wal.AppendTx(tx.txID, walOps)
	if err != nil {
		tx.rollbackStaging()
		// Any WAL write or sync failure marks the engine failed
		tx.tm.MarkReadOnly()
		return 0, err
	}

	// Failpoint FP-04: wal.commit.after_sync (commit is durable, nothing applied)
	faultinject.Hit(faultinject.FPWalCommitAfterSync)

	// Rule W4: Take exclusive state lock, pwrite slot images to students.dat
	unlockState := tx.tm.lockMgr.AcquireStateLock()

	for _, op := range tx.stagedOps {
		if writeErr := tx.tm.storage.WriteSlot(op.SlotNumber, op.SlotImage); writeErr != nil {
			unlockState()
			tx.tm.MarkReadOnly()
			// Outcome in doubt to client, but durable in WAL
			return commitLSN, fmt.Errorf("%w: failed to apply slot %d: %v", storage.ErrEngineFailed, op.SlotNumber, writeErr)
		}
	}

	// Rule W5: Update in-memory index
	for _, op := range tx.stagedOps {
		if op.Type == "delete" {
			tx.tm.index.Delete(op.StudentID)
			tx.tm.storage.FreeSlot(op.SlotNumber)
		} else {
			// insert or update
			_ = tx.tm.index.Insert(op.StudentID, op.SlotNumber)
			// Release reservation
			tx.tm.storage.ReleaseReserved(op.SlotNumber)
		}
	}

	unlockState()
	tx.committed = true

	// Check if WAL size exceeds wal_max_bytes for background checkpoint trigger
	if tx.tm.wal.Size() >= tx.tm.walMaxBytes {
		go func() {
			_ = tx.tm.Checkpoint()
		}()
	}

	return commitLSN, nil
}

// Rollback discards staged operations and releases reservations.
func (tx *Transaction) Rollback() {
	if tx.committed || tx.rolledBack {
		return
	}
	defer tx.unlockW()
	tx.rollbackStaging()
	tx.rolledBack = true
}

func (tx *Transaction) rollbackStaging() {
	for _, op := range tx.stagedOps {
		if op.Type == "insert" {
			tx.tm.storage.FreeSlot(op.SlotNumber)
		} else {
			tx.tm.storage.ReleaseReserved(op.SlotNumber)
		}
	}
}

// Checkpoint executes steps K1–K5 per ARCHITECTURE.md §F.6.
func (txMgr *Manager) Checkpoint() error {
	if txMgr.IsReadOnly() {
		return ErrFailedReadOnly
	}

	// Step K1: Take writer lock, then state lock
	ctx := context.Background()
	unlockW, err := txMgr.lockMgr.AcquireWriter(ctx)
	if err != nil {
		return err
	}
	defer unlockW()

	unlockState := txMgr.lockMgr.AcquireStateLock()
	defer unlockState()

	// Failpoint FP-13: checkpoint.before_data_sync
	faultinject.Hit(faultinject.FPCheckpointBeforeDataSync)

	// Step K2: fsync(students.dat)
	if err := txMgr.storage.Sync(); err != nil {
		txMgr.MarkReadOnly()
		return fmt.Errorf("checkpoint K2 data sync failed: %w", err)
	}

	checkpointLSN := txMgr.wal.NextTxID() // current high-water mark
	slotCount := uint64(txMgr.storage.SlotCount())

	// Step K3: Write students.idx.tmp -> fsync -> rename -> fsync directory
	idxPath := filepath.Join(txMgr.dataDir, "students.idx")
	if err := txMgr.index.SaveSnapshot(idxPath, checkpointLSN, slotCount); err != nil {
		txMgr.MarkReadOnly()
		return fmt.Errorf("checkpoint K3 save index failed: %w", err)
	}

	// Step K4 & K5: Write students.wal.tmp -> fsync -> rename -> fsync dir -> reopen
	txMgr.mu.Lock()
	nextTxID := txMgr.nextTxID
	txMgr.mu.Unlock()

	if err := txMgr.wal.Rotate(checkpointLSN, nextTxID); err != nil {
		txMgr.MarkReadOnly()
		return fmt.Errorf("checkpoint K4 rotate wal failed: %w", err)
	}

	txMgr.lastCheckLSN = checkpointLSN
	return nil
}

// LastCheckpointLSN returns the LSN of the last completed checkpoint.
func (txMgr *Manager) LastCheckpointLSN() uint64 {
	txMgr.mu.Lock()
	defer txMgr.mu.Unlock()
	return txMgr.lastCheckLSN
}
