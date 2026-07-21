package auth

import (
	"bytes"
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

func TestFileStore_ClientSecretRoundTripAndDelete(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIVMO_HOME", dir)
	t.Setenv("GIVMO_TOKEN_BACKEND", "file")

	s, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	secret, err := s.LoadClientSecret("sandbox")
	if err != nil {
		t.Fatalf("LoadClientSecret absent: %v", err)
	}
	if secret != "" {
		t.Errorf("absent client secret = %q, want empty", secret)
	}

	const testSecret = "test-client-secret"
	if err := s.SaveClientSecret("sandbox", testSecret); err != nil {
		t.Fatalf("SaveClientSecret: %v", err)
	}
	secret, err = s.LoadClientSecret("sandbox")
	if err != nil {
		t.Fatalf("LoadClientSecret: %v", err)
	}
	if secret != testSecret {
		t.Errorf("client secret = %q, want test fixture", secret)
	}

	secretPath := filepath.Join(dir, "credentials", "sandbox.client-secret")
	info, err := os.Stat(secretPath)
	if err != nil {
		t.Fatalf("stat client secret: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("client secret file perms = %o, want 0600", perm)
	}

	cred := &Credential{
		Profile:     "sandbox",
		Type:        CredTypeOAuth,
		AccessToken: "test-access-token",
	}
	if err := s.Save(cred); err != nil {
		t.Fatalf("Save credential: %v", err)
	}
	credPath := filepath.Join(dir, "credentials", "sandbox.json")
	credentialBlob, err := os.ReadFile(credPath)
	if err != nil {
		t.Fatalf("read credential blob: %v", err)
	}
	if bytes.Contains(credentialBlob, []byte(testSecret)) {
		t.Fatal("credential blob contains client secret")
	}

	if err := s.Delete("sandbox"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, path := range []string{credPath, secretPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("expected %s to be deleted, got %v", path, err)
		}
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
