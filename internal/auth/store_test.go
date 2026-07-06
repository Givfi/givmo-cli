package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileStore_RoundTripAndPerms(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIVMO_HOME", dir)
	t.Setenv("GIVMO_TOKEN_BACKEND", "file")

	s, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if s.Backend() != "file" {
		t.Fatalf("expected file backend, got %s", s.Backend())
	}

	// Missing credential -> ErrNoCredential.
	if _, err := s.Load("sandbox"); err != ErrNoCredential {
		t.Fatalf("expected ErrNoCredential, got %v", err)
	}

	cred := &Credential{
		Profile:     "sandbox",
		Type:        CredTypeOAuth,
		AccessToken: "super-secret-token",
		Scopes:      []string{"givmo.donations.read"},
	}
	if err := s.Save(cred); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// File must be 0600 (owner-only), never world-readable.
	path := filepath.Join(dir, "credentials", "sandbox.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("credential file perms = %o, want 0600", perm)
	}

	got, err := s.Load("sandbox")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.AccessToken != "super-secret-token" {
		t.Errorf("token round-trip failed: %q", got.AccessToken)
	}

	// Delete is idempotent.
	if err := s.Delete("sandbox"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.Delete("sandbox"); err != nil {
		t.Fatalf("second Delete should be a no-op, got %v", err)
	}
	if _, err := s.Load("sandbox"); err != ErrNoCredential {
		t.Fatalf("expected ErrNoCredential after delete, got %v", err)
	}
}

func TestCredential_RedactedHidesSecrets(t *testing.T) {
	c := &Credential{AccessToken: "AT", RefreshToken: "RT", Scopes: []string{"s"}}
	r := c.Redacted()
	if r.AccessToken == "AT" || r.RefreshToken == "RT" {
		t.Fatal("Redacted must not expose raw tokens")
	}
	if r.AccessToken != "***redacted***" || r.RefreshToken != "***redacted***" {
		t.Errorf("unexpected redaction: %+v", r)
	}
}

func TestCredential_AuthorizationHeader(t *testing.T) {
	c := &Credential{AccessToken: "abc"}
	if got := c.AuthorizationHeader(); got != "Bearer abc" {
		t.Errorf("header = %q, want Bearer abc", got)
	}
	var nilc *Credential
	if got := nilc.AuthorizationHeader(); got != "" {
		t.Errorf("nil credential header = %q, want empty", got)
	}
}
