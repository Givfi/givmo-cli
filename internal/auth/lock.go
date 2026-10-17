package auth

import (
	"context"
	"os"
	"path/filepath"
)

// profileLock is an exclusive lock on one profile's lock file,
// ~/.givmo/locks/<profile>.lock. Every givmo process takes it before it renews,
// replaces or deletes the profile's stored sign-in (see TokenSource), so those
// run one at a time across the computer. The operating system releases it when
// its holder exits, so a crash leaves no stale lock.
type profileLock struct{ f *os.File }

// lockPath is the profile's lock file under the CLI's state directory.
func lockPath(dir, profile string) string {
	return filepath.Join(dir, "locks", sanitizedProfile(profile)+".lock")
}

// lockProfile takes the profile's lock, waiting until it is free or ctx is done.
// Each call opens the file anew, so two holders in one process exclude each other
// exactly as two processes do.
func lockProfile(ctx context.Context, dir, profile string) (*profileLock, error) {
	path := lockPath(dir, profile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	locked := make(chan error, 1)
	go func() { locked <- lockFile(f) }()
	select {
	case err := <-locked:
		if err != nil {
			f.Close()
			return nil, err
		}
		return &profileLock{f: f}, nil
	case <-ctx.Done():
		// Abandoned: whenever the wait does take the lock, release it at once, so
		// it never outlives this call.
		go func() {
			if <-locked == nil {
				_ = unlockFile(f)
			}
			f.Close()
		}()
		return nil, ctx.Err()
	}
}

// unlock releases the lock.
func (l *profileLock) unlock() {
	_ = unlockFile(l.f)
	_ = l.f.Close()
}
