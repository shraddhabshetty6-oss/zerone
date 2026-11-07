// Package wal implements write-ahead logging and recovery scanning per ARCHITECTURE.md §F.2.
package wal

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"studentdb/internal/codec"
	"studentdb/internal/constants"
	"studentdb/internal/faultinject"
)

var (
	// ErrCorruptWAL is returned when the WAL header or mid-log entry is corrupted.
	ErrCorruptWAL = errors.New("wal: corrupt log (CORRUPT_WAL)")

	// ErrDiskFull is returned on ENOSPC during WAL write.
	ErrDiskFull = errors.New("wal: disk full (DISK_FULL)")

	// ErrEngineFailed is returned on unrecoverable I/O errors.
	ErrEngineFailed = errors.New("wal: engine failed (ENGINE_FAILED_READONLY)")
)

// TxSlotOp represents one WRITE_SLOT operation staged for WAL append.
type TxSlotOp struct {
	SlotNumber uint32
	SlotImage  [256]byte
}

// LogWriter defines the interface for appending transactions to the WAL.
type LogWriter interface {
	AppendTx(txID uint64, ops []TxSlotOp) (commitLSN uint64, err error)
	Sync() error
	Rotate(checkpointLSN uint64, nextTxID uint64) error
	Close() error
	Size() int64
}

// Manager manages students.wal file append and rotation.
type Manager struct {
	mu            sync.Mutex
	file          *os.File
	path          string
	dir           string
	syncMode      string
	checkpointLSN uint64
	nextLSN       uint64
	nextTxID      uint64
	size          int64
	failed        bool
}

// Create writes an empty students.wal with header {checkpointLSN, nextTxID} via temp file.
func Create(dataDir string, checkpointLSN, nextTxID uint64) error {
	walPath := filepath.Join(dataDir, "students.wal")
	tmpPath := walPath + ".tmp"

	h := &codec.WALHeader{
		Version:       constants.FormatVersion,
		CheckpointLSN: checkpointLSN,
		NextTxID:      nextTxID,
	}
	buf := codec.EncodeWALHeader(h)

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

	if err := os.Rename(tmpPath, walPath); err != nil {
		return err
	}

	if df, err := os.Open(dataDir); err == nil {
		_ = df.Sync()
		_ = df.Close()
	}

	return nil
}

// Open opens an existing students.wal and initializes manager state.
func Open(dataDir string, syncMode string) (*Manager, error) {
	walPath := filepath.Join(dataDir, "students.wal")
	f, err := os.OpenFile(walPath, os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}

	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if fi.Size() < constants.WALHeaderSize {
		_ = f.Close()
		return nil, ErrCorruptWAL
	}

	headerBuf := make([]byte, constants.WALHeaderSize)
	if _, err := f.ReadAt(headerBuf, 0); err != nil {
		_ = f.Close()
		return nil, ErrCorruptWAL
	}

	header, err := codec.DecodeWALHeader(headerBuf)
	if err != nil {
		_ = f.Close()
		return nil, ErrCorruptWAL
	}

	m := &Manager{
		file:          f,
		path:          walPath,
		dir:           dataDir,
		syncMode:      syncMode,
		checkpointLSN: header.CheckpointLSN,
		nextLSN:       header.CheckpointLSN + 1,
		nextTxID:      header.NextTxID,
		size:          fi.Size(),
	}

	// Seek to end of file for subsequent appends
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("%w: seek to wal end: %v", ErrEngineFailed, err)
	}

	return m, nil
}

// Close closes the WAL file handle.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.file != nil {
		err := m.file.Close()
		m.file = nil
		return err
	}
	return nil
}

// Size returns the current size of the WAL file.
func (m *Manager) Size() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.size
}

// SetNextLSN updates the next LSN to assign (used after recovery scan).
func (m *Manager) SetNextLSN(lsn uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextLSN = lsn
}

// SetNextTxID updates the next transaction ID to assign (used after recovery scan).
func (m *Manager) SetNextTxID(txID uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextTxID = txID
}

// NextTxID returns the next transaction ID.
func (m *Manager) NextTxID() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.nextTxID
}

// AppendTx appends a complete transaction (BEGIN, WRITE_SLOT x n, COMMIT) in a single write call.
func (m *Manager) AppendTx(txID uint64, ops []TxSlotOp) (commitLSN uint64, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.failed {
		return 0, ErrEngineFailed
	}

	// Failpoint FP-10: disk.enospc.wal
	if act := faultinject.Hit(faultinject.FPDiskEnospcWal); act.Kind == "error" || act.Err != nil {
		return 0, ErrDiskFull
	}

	// Failpoint FP-01: wal.append.before_write
	faultinject.Hit(faultinject.FPWalAppendBeforeWrite)

	// Build the transaction buffer
	var buf bytes.Buffer

	// 1. BEGIN entry
	beginLSN := m.nextLSN
	m.nextLSN++
	beginEntry := &codec.WALEntry{
		LSN:     beginLSN,
		TxID:    txID,
		Type:    constants.WalTypeBegin,
		Payload: nil,
	}
	buf.Write(codec.EncodeWALEntry(beginEntry))

	// 2. WRITE_SLOT entries
	for _, op := range ops {
		opLSN := m.nextLSN
		m.nextLSN++
		payload := make([]byte, 4+constants.RecordSize)
		binary.LittleEndian.PutUint32(payload[0:4], op.SlotNumber)
		copy(payload[4:], op.SlotImage[:])

		slotEntry := &codec.WALEntry{
			LSN:     opLSN,
			TxID:    txID,
			Type:    constants.WalTypeWriteSlot,
			Payload: payload,
		}
		buf.Write(codec.EncodeWALEntry(slotEntry))
	}

	// 3. COMMIT entry
	commitLSN = m.nextLSN
	m.nextLSN++
	commitPayload := make([]byte, 4)
	binary.LittleEndian.PutUint32(commitPayload, uint32(len(ops)))
	commitEntry := &codec.WALEntry{
		LSN:     commitLSN,
		TxID:    txID,
		Type:    constants.WalTypeCommit,
		Payload: commitPayload,
	}
	buf.Write(codec.EncodeWALEntry(commitEntry))

	dataBytes := buf.Bytes()

	// Failpoint FP-02: wal.append.partial_write (via MaybePartialWrite)
	n, writeErr := faultinject.MaybePartialWrite(faultinject.FPWalAppendPartialWrite, dataBytes, func(b []byte) (int, error) {
		return m.file.Write(b)
	})

	if writeErr != nil {
		m.failed = true
		if errors.Is(writeErr, syscall.ENOSPC) {
			return 0, ErrDiskFull
		}
		return 0, fmt.Errorf("%w: append tx to wal: %v", ErrEngineFailed, writeErr)
	}

	m.size += int64(n)

	// Failpoint FP-03: wal.append.after_write_before_sync
	faultinject.Hit(faultinject.FPWalAppendAfterWriteBeforeSync)

	// Fsync WAL (COMMIT POINT) per rule W3
	if m.syncMode != "none" {
		if err := m.file.Sync(); err != nil {
			m.failed = true
			return 0, fmt.Errorf("%w: wal sync: %v", ErrEngineFailed, err)
		}
		faultinject.RecordSyncedLength(m.path, m.size)
	}

	if txID >= m.nextTxID {
		m.nextTxID = txID + 1
	}

	return commitLSN, nil
}

// Sync flushes the WAL file.
func (m *Manager) Sync() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.syncMode == "none" {
		return nil
	}
	if err := m.file.Sync(); err != nil {
		m.failed = true
		return fmt.Errorf("%w: wal sync: %v", ErrEngineFailed, err)
	}
	faultinject.RecordSyncedLength(m.path, m.size)
	return nil
}

// Rotate switches to a fresh WAL file with new checkpointLSN and nextTxID per step K4.
func (m *Manager) Rotate(checkpointLSN uint64, nextTxID uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	tmpPath := m.path + ".tmp"

	// Failpoint FP-12: disk.enospc.checkpoint
	if act := faultinject.Hit(faultinject.FPDiskEnospcCheckpoint); act.Kind == "error" || act.Err != nil {
		return ErrDiskFull
	}

	h := &codec.WALHeader{
		Version:       constants.FormatVersion,
		CheckpointLSN: checkpointLSN,
		NextTxID:      nextTxID,
	}
	buf := codec.EncodeWALHeader(h)

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

	// Failpoint FP-16: checkpoint.before_wal_rename
	faultinject.Hit(faultinject.FPCheckpointBeforeWalRename)

	// Close existing WAL
	if m.file != nil {
		_ = m.file.Close()
		m.file = nil
	}

	// Atomic rename
	if err := os.Rename(tmpPath, m.path); err != nil {
		return err
	}

	// Sync parent directory
	if df, err := os.Open(m.dir); err == nil {
		_ = df.Sync()
		_ = df.Close()
	}

	// Failpoint FP-17: checkpoint.after_wal_rename
	faultinject.Hit(faultinject.FPCheckpointAfterWalRename)

	// Reopen new WAL
	newF, err := os.OpenFile(m.path, os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if _, err := newF.Seek(0, io.SeekEnd); err != nil {
		_ = newF.Close()
		return err
	}

	m.file = newF
	m.checkpointLSN = checkpointLSN
	m.nextLSN = checkpointLSN + 1
	m.nextTxID = nextTxID
	m.size = constants.WALHeaderSize

	return nil
}

// TruncateTo truncates the WAL file to offset bytes and fsyncs.
func (m *Manager) TruncateTo(offset int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.file.Truncate(offset); err != nil {
		return err
	}
	if _, err := m.file.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	if err := m.file.Sync(); err != nil {
		return err
	}
	m.size = offset
	faultinject.RecordSyncedLength(m.path, offset)
	return nil
}

// ScanResult holds the results of scanning the WAL during recovery.
type ScanResult struct {
	Header           *codec.WALHeader
	ValidEntries     []*codec.WALEntry
	TornTailBytes    int64
	MidlogCorruption bool
	LastValidOffset  int64
}

// ScanLog reads the entire WAL file from dataDir, verifying CRC and LSN continuity.
func ScanLog(dataDir string) (*ScanResult, error) {
	walPath := filepath.Join(dataDir, "students.wal")
	data, err := os.ReadFile(walPath)
	if err != nil {
		return nil, err
	}

	if len(data) < int(constants.WALHeaderSize) {
		return nil, ErrCorruptWAL
	}

	header, err := codec.DecodeWALHeader(data[:constants.WALHeaderSize])
	if err != nil {
		return nil, ErrCorruptWAL
	}

	res := &ScanResult{
		Header:          header,
		LastValidOffset: constants.WALHeaderSize,
	}

	offset := int(constants.WALHeaderSize)
	expectedLSN := header.CheckpointLSN + 1

	for offset < len(data) {
		entry, n, err := codec.DecodeWALEntry(data[offset:])
		if err == nil && entry.LSN == expectedLSN {
			res.ValidEntries = append(res.ValidEntries, entry)
			offset += n
			res.LastValidOffset = int64(offset)
			expectedLSN++
			continue
		}

		// Invalid entry or LSN mismatch encountered:
		// Check whether there are any subsequent valid entries (mid-log corruption)
		remBytes := data[offset:]
		foundLaterValid := false

		// Search for subsequent WALE magic markers
		for searchIdx := 1; searchIdx+36 <= len(remBytes); searchIdx++ {
			if string(remBytes[searchIdx:searchIdx+4]) == constants.WALEntryMagic {
				laterEntry, _, laterErr := codec.DecodeWALEntry(remBytes[searchIdx:])
				if laterErr == nil && laterEntry.LSN >= expectedLSN {
					foundLaterValid = true
					break
				}
			}
		}

		if foundLaterValid {
			res.MidlogCorruption = true
			return res, nil
		}

		// No later valid entries: this is a torn tail
		res.TornTailBytes = int64(len(data) - offset)
		break
	}

	return res, nil
}

