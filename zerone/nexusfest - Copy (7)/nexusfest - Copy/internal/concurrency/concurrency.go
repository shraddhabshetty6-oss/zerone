// Package concurrency implements concurrency control and file locking per ARCHITECTURE.md §E.7.
package concurrency

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"studentdb/internal/constants"
)

var (
	// ErrBusy is returned when a lock cannot be acquired within the timeout.
	ErrBusy = errors.New("concurrency: lock timeout (BUSY)")

	// ErrLocked is returned when students.lock is already held by another process.
	ErrLocked = errors.New("concurrency: database directory is locked by another process")
)

// LockManager coordinates concurrency between readers and writers, and owns the process lock.
type LockManager struct {
	writerMu    sync.Mutex
	stateMu     sync.RWMutex
	lockFile    *os.File
	lockTimeout time.Duration
}

// NewLockManager creates a new LockManager with the specified lock timeout.
func NewLockManager(timeout time.Duration) *LockManager {
	if timeout <= 0 {
		timeout = constants.DefaultTxLockTimeout
	}
	return &LockManager{
		lockTimeout: timeout,
	}
}

// AcquireWriter acquires the writer lock with context/timeout.
func (lm *LockManager) AcquireWriter(ctx context.Context) (func(), error) {
	done := make(chan struct{})
	go func() {
		lm.writerMu.Lock()
		close(done)
	}()

	select {
	case <-done:
		return func() { lm.writerMu.Unlock() }, nil
	case <-ctx.Done():
		// Spawn a goroutine to wait and release if it gets acquired later
		go func() {
			<-done
			lm.writerMu.Unlock()
		}()
		return nil, ErrBusy
	case <-time.After(lm.lockTimeout):
		go func() {
			<-done
			lm.writerMu.Unlock()
		}()
		return nil, ErrBusy
	}
}

// AcquireStateLock acquires the state lock exclusively (for apply and checkpoint).
func (lm *LockManager) AcquireStateLock() func() {
	lm.stateMu.Lock()
	return func() { lm.stateMu.Unlock() }
}

// AcquireStateRLock acquires the state lock in shared mode (for readers).
func (lm *LockManager) AcquireStateRLock() func() {
	lm.stateMu.RLock()
	return func() { lm.stateMu.RUnlock() }
}

// AcquireProcessLock acquires an exclusive non-blocking file lock on students.lock.
func (lm *LockManager) AcquireProcessLock(dataDir string) error {
	lockPath := filepath.Join(dataDir, "students.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}

	if err := lockFile(f); err != nil {
		_ = f.Close()
		return ErrLocked
	}

	lm.lockFile = f
	return nil
}

// ReleaseProcessLock releases the lock and closes students.lock.
func (lm *LockManager) ReleaseProcessLock() error {
	if lm.lockFile != nil {
		_ = unlockFile(lm.lockFile)
		err := lm.lockFile.Close()
		lm.lockFile = nil
		return err
	}
	return nil
}

