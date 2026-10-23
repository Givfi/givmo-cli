package auth

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// useFakeSecurity makes the keychain backend run script in place of
// /usr/bin/security for the rest of the test: a stand-in that touches no keychain.
// It also puts a trap alone on PATH, a `security` that records being run, and
// returns whether it ran: the store reaches its keychain tool by path alone, so
// the trap must never run.
func useFakeSecurity(t *testing.T, script string) (trapped func() bool) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("the keychain backend runs only on macOS")
	}
	fake := filepath.Join(t.TempDir(), "security")
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	prev := securityPath
	securityPath = fake
	t.Cleanup(func() { securityPath = prev })

	mark := filepath.Join(t.TempDir(), "trap-ran")
	trapDir := t.TempDir()
	trap := "#!/bin/sh\n: > '" + mark + "'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(trapDir, "security"), []byte(trap), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", trapDir)
	return func() bool {
		_, err := os.Stat(mark)
		return err == nil
	}
}

// TestNewStore_FindsTheKeychainToolAtItsSystemPath: the store finds macOS's
// keychain tool at /usr/bin/security whatever PATH holds, so with no `security`
// on PATH at all it still keeps sign-ins in the keychain. Nothing is run.
func TestNewStore_FindsTheKeychainToolAtItsSystemPath(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the keychain backend runs only on macOS")
	}
	if _, err := os.Stat("/usr/bin/security"); err != nil {
		t.Skip("this Mac has no /usr/bin/security")
	}
	t.Setenv("PATH", t.TempDir())
	t.Setenv("GIVMO_HOME", t.TempDir())
	t.Setenv("GIVMO_TOKEN_BACKEND", "")
	s, err := NewStore()
	if err != nil {
		t.Fatal(err)
	}
	if s.Backend() != "keychain" {
		t.Errorf("backend = %s with no security on PATH, want keychain: the store's keychain tool is /usr/bin/security, not a PATH lookup", s.Backend())
	}
}

// fakeSecurity is a stand-in for macOS's keychain tool: a shell script, run in
// place of /usr/bin/security, that records its arguments and standard input under
// dir and exits 0, or, with FAKE_SECURITY_FAIL set, echoes its input in an error
// and exits 1, as the real one does for a line it cannot run. It touches no
// keychain.
const fakeSecurity = `#!/bin/sh
: > "$FAKE_SECURITY_OUT/argv"
for a in "$@"; do printf '%s\n' "$a" >> "$FAKE_SECURITY_OUT/argv"; done
: > "$FAKE_SECURITY_OUT/stdin"
last=""
while IFS= read -r line || [ -n "$line" ]; do
  printf '%s\n' "$line" >> "$FAKE_SECURITY_OUT/stdin"
  last="$line"
done
if [ -n "$FAKE_SECURITY_FAIL" ]; then
  printf 'security: unknown command "%s"\n' "$last" >&2
  exit 1
fi
exit 0
`

// TestKeychainStore_KeepsTheCredentialOffTheCommandLine drives the keychain
// backend against fakeSecurity. The credential must reach security on its
// standard input, hex-encoded, and never in its arguments, which ps shows to
// every process of the user; a credential too long for security's input buffer
// must not be sent at all, since security would store a truncated item and echo
// the rest; and security's own output, which can echo its input, must never
// reach the error. The store runs its keychain tool by path, never a `security`
// found on PATH, where a writable directory ahead of /usr/bin could hold another.
func TestKeychainStore_KeepsTheCredentialOffTheCommandLine(t *testing.T) {
	trapped := useFakeSecurity(t, fakeSecurity)
	t.Setenv("GIVMO_HOME", t.TempDir())
	t.Setenv("GIVMO_TOKEN_BACKEND", "")

	const accessToken, refreshToken = "test-access-token-not-real", "test-refresh-token-not-real"
	cases := []struct {
		name      string
		access    string
		fail      bool
		wantSent  bool
		wantError bool
	}{
		{name: "a credential is written through standard input", access: accessToken, wantSent: true},
		{name: "a credential too long for security's buffer is not sent", access: strings.Repeat("a", 3000), wantError: true},
		{name: "security's echo of its input stays out of the error", access: accessToken, fail: true, wantSent: true, wantError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := t.TempDir()
			t.Setenv("FAKE_SECURITY_OUT", out)
			if tc.fail {
				t.Setenv("FAKE_SECURITY_FAIL", "1")
			} else {
				t.Setenv("FAKE_SECURITY_FAIL", "")
			}
			s, err := NewStore()
			if err != nil {
				t.Fatal(err)
			}
			if s.Backend() != "keychain" {
				t.Fatalf("backend = %s, want keychain", s.Backend())
			}
			cred := &Credential{Profile: "sandbox", Type: CredTypeOAuth, AccessToken: tc.access, RefreshToken: refreshToken}
			saveErr := s.Save(cred)
			if trapped() {
				t.Fatal("the store ran the security found on PATH; it must run its keychain tool by path alone")
			}
			if (saveErr != nil) != tc.wantError {
				t.Fatalf("Save error = %v, want error %v", saveErr, tc.wantError)
			}
			argv, argvErr := os.ReadFile(filepath.Join(out, "argv"))
			if !tc.wantSent {
				if argvErr == nil {
					t.Fatalf("security ran with %q; a too-long line must not be sent", argv)
				}
				return
			}
			if argvErr != nil {
				t.Fatalf("security did not run: %v", argvErr)
			}
			if got := string(argv); got != "-i\n" {
				t.Errorf("security arguments = %q, want only -i", got)
			}
			stdin, err := os.ReadFile(filepath.Join(out, "stdin"))
			if err != nil {
				t.Fatal(err)
			}
			want, _ := json.Marshal(cred)
			wantLine := "add-generic-password -U -a sandbox -s io.givmo.cli -X " + hex.EncodeToString(want) + "\n"
			if string(stdin) != wantLine {
				t.Errorf("security standard input = %q, want %q", stdin, wantLine)
			}
			for _, secret := range []string{tc.access, refreshToken, hex.EncodeToString([]byte(tc.access))} {
				if strings.Contains(string(argv), secret) {
					t.Errorf("security arguments carry a secret: %q", argv)
				}
				if saveErr != nil && strings.Contains(saveErr.Error(), secret) {
					t.Errorf("the error carries a secret: %v", saveErr)
				}
			}
			if saveErr != nil && strings.Contains(saveErr.Error(), hex.EncodeToString(want)[:16]) {
				t.Errorf("the error carries security's echo of its input: %v", saveErr)
			}
		})
	}
}
