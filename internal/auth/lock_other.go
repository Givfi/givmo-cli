//go:build !(darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd || windows)

package auth

import "os"

// fileLocking is false: this platform has no file lock the CLI can take, so the
// CLI never renews a sign-in here (a sign-in lasts until its access token
// expires), and with no renewal to race, the lock is a no-op.
const fileLocking = false

func lockFile(*os.File) error { return nil }

func unlockFile(*os.File) error { return nil }
