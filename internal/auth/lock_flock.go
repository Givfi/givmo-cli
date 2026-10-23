//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package auth

import (
	"os"
	"syscall"
)

// fileLocking is true: flock serializes the CLI's renewals here.
const fileLocking = true

// lockFile takes an exclusive flock on f, waiting until it is free.
func lockFile(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			return err
		}
	}
}

// unlockFile releases f's flock.
func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
