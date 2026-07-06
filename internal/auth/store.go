package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/givfi/givmo-cli/internal/config"
)

// keychainService is the macOS Keychain service label under which credentials
// are stored. The account is the profile name.
const keychainService = "io.givmo.cli"

// ErrNoCredential means no credential is stored for the requested profile.
var ErrNoCredential = errors.New("no stored credential for profile")

// Store persists per-profile Credentials. It prefers the OS keychain (macOS
// `security`) when available and falls back to a 0600 file under ~/.givmo.
// Secrets never touch logs or --json output.
type Store struct {
	// useKeychain is set when the platform keychain backend is usable.
	useKeychain bool
	// fileDir is the fallback directory (~/.givmo).
	fileDir string
}

// NewStore builds a Store, detecting keychain availability. On non-macOS or
// when `security` is unavailable it uses the file backend.
func NewStore() (*Store, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	s := &Store{fileDir: dir}
	s.useKeychain = keychainAvailable()
	// Allow explicit override for tests / users who prefer the file backend.
	if os.Getenv("GIVMO_TOKEN_BACKEND") == "file" {
		s.useKeychain = false
	}
	return s, nil
}

// keychainAvailable reports whether the macOS `security` CLI is usable.
func keychainAvailable() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	_, err := exec.LookPath("security")
	return err == nil
}

// Backend returns a short name for the active backend, for diagnostics.
func (s *Store) Backend() string {
	if s.useKeychain {
		return "keychain"
	}
	return "file"
}

// Save stores the credential for its profile.
func (s *Store) Save(c *Credential) error {
	if c == nil || c.Profile == "" {
		return errors.New("credential is missing a profile")
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if s.useKeychain {
		return s.keychainSet(c.Profile, data)
	}
	return s.fileSet(c.Profile, data)
}

// Load returns the credential for a profile, or ErrNoCredential.
func (s *Store) Load(profile string) (*Credential, error) {
	var data []byte
	var err error
	if s.useKeychain {
		data, err = s.keychainGet(profile)
	} else {
		data, err = s.fileGet(profile)
	}
	if err != nil {
		return nil, err
	}
	var c Credential
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("stored credential is corrupt: %w", err)
	}
	return &c, nil
}

// Delete removes the credential for a profile. Deleting a missing credential is
// not an error (idempotent logout).
func (s *Store) Delete(profile string) error {
	if s.useKeychain {
		return s.keychainDelete(profile)
	}
	return s.fileDelete(profile)
}

// ---- file backend -----------------------------------------------------------

func (s *Store) credPath(profile string) string {
	// One file per profile so profiles are isolated. Name is sanitized.
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, profile)
	return filepath.Join(s.fileDir, "credentials", safe+".json")
}

func (s *Store) fileSet(profile string, data []byte) error {
	p := s.credPath(profile)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	// 0600: owner read/write only. Never world-readable.
	if err := os.WriteFile(p, data, 0o600); err != nil {
		return err
	}
	return nil
}

func (s *Store) fileGet(profile string) ([]byte, error) {
	data, err := os.ReadFile(s.credPath(profile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoCredential
	}
	return data, err
}

func (s *Store) fileDelete(profile string) error {
	err := os.Remove(s.credPath(profile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// ---- macOS keychain backend (shells to `security`) --------------------------

func (s *Store) keychainSet(profile string, data []byte) error {
	// -U updates if present; -w takes the secret as the password value.
	cmd := exec.Command("security", "add-generic-password",
		"-a", profile,
		"-s", keychainService,
		"-w", string(data),
		"-U",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("keychain store failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s *Store) keychainGet(profile string) ([]byte, error) {
	cmd := exec.Command("security", "find-generic-password",
		"-a", profile,
		"-s", keychainService,
		"-w",
	)
	out, err := cmd.Output()
	if err != nil {
		// `security` exits non-zero when the item is absent.
		return nil, ErrNoCredential
	}
	return []byte(strings.TrimRight(string(out), "\n")), nil
}

func (s *Store) keychainDelete(profile string) error {
	cmd := exec.Command("security", "delete-generic-password",
		"-a", profile,
		"-s", keychainService,
	)
	if err := cmd.Run(); err != nil {
		// Absent item -> treat as already-deleted (idempotent).
		return nil
	}
	return nil
}
