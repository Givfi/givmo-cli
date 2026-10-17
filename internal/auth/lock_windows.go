//go:build windows

package auth

import (
	"os"
	"syscall"
	"unsafe"
)

// The standard library's syscall package has no LockFileEx, so it is called from
// kernel32 directly.
var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

// fileLocking is true: LockFileEx serializes the CLI's renewals here.
const fileLocking = true

// lockfileExclusiveLock is LOCKFILE_EXCLUSIVE_LOCK.
const lockfileExclusiveLock = 0x2

// lockFile takes an exclusive lock on f's first byte, waiting until it is free.
func lockFile(f *os.File) error {
	var ol syscall.Overlapped
	r, _, err := procLockFileEx.Call(f.Fd(), lockfileExclusiveLock, 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
	if r == 0 {
		return err
	}
	return nil
}

// unlockFile releases the lock lockFile took.
func unlockFile(f *os.File) error {
	var ol syscall.Overlapped
	r, _, err := procUnlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
	if r == 0 {
		return err
	}
	return nil
}
