//go:build windows

package concurrency

import (
	"os"
	"syscall"
)

var (
	modkernel32    = syscall.NewLazyDLL("kernel32.dll")
	procLockFile   = modkernel32.NewProc("LockFile")
	procUnlockFile = modkernel32.NewProc("UnlockFile")
)

func lockFile(f *os.File) error {
	// LockFile(hFile, dwFileOffsetLow, dwFileOffsetHigh, nNumberOfBytesToLockLow, nNumberOfBytesToLockHigh)
	r1, _, err := procLockFile.Call(f.Fd(), 0, 0, 1, 0)
	if r1 == 0 {
		return err
	}
	return nil
}

func unlockFile(f *os.File) error {
	r1, _, err := procUnlockFile.Call(f.Fd(), 0, 0, 1, 0)
	if r1 == 0 {
		return err
	}
	return nil
}
