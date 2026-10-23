package auth

import (
	"context"
	"os"
	"path/filepath"
)

// profileLock is an exclusive lock on one profile's lock file (see
// Store.lockFilePath). Every givmo process takes it before it renews, replaces or
// deletes the profile's stored sign-in (see TokenSource), so those run one at a
// time across the computer. The operating system releases it when its holder
// exits, so a crash leaves no stale lock.
type profileLock struct{ f *os.File }

// lockFilePath is where the profile's lock file lives: beside the sign-in it
// guards, so every process that can renew that sign-in waits for the same lock. A
// file-backed sign-in lives under the CLI's state directory (GIVMO_HOME), and so
// does its lock, ~/.givmo/locks/<profile>.lock. A keychain sign-in is one item per
// user whatever GIVMO_HOME says, so its lock is per user too, under the home
// directory, ~/.givmo/locks/keychain-<profile>.lock: under GIVMO_HOME, two
// processes with different values (a shell that exports it, an IDE that does not)
// would renew the one keychain sign-in at once, and the server would end it.
func (s *Store) lockFilePath(profile string) (string, error) {
	name := sanitizedProfile(profile) + ".lock"
	if !s.useKeychain {
		return filepath.Join(s.fileDir, "locks", name), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".givmo", "locks", "keychain-"+name), nil
}

// lockProfile takes the lock file at path, waiting until it is free or ctx is
// done. Each call opens the file anew, so two holders in one process exclude each
// other exactly as two processes do.
func lockProfile(ctx context.Context, path string) (*profileLock, error) {
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
