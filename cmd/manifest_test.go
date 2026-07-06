package cmd

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/givfi/givmo-cli/internal/donate"
	"github.com/givfi/givmo-cli/internal/output"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "internal", "donate", "testdata_examples", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// TestSignManifest_RoundTripVerifies is the load-bearing test: sign the valid
// fixture, then verify the attached detached JWS through the VENDORED parser's
// VerifySignature using the CLI's Ed25519 verifier. This proves the CLI's
// signing input matches exactly what the parser reconstructs at verify time.
func TestSignManifest_RoundTripVerifies(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw := fixture(t, "valid.donate.json")

	signed, err := signManifest(raw, priv)
	if err != nil {
		t.Fatalf("signManifest: %v", err)
	}
	if !strings.Contains(string(signed), `"signature"`) {
		t.Fatal("signed manifest must contain a signature member")
	}

	// The signed manifest must still strict-parse (signature is last member,
	// alg is allowlisted, JWS is detached).
	m, err := donate.Parse(signed)
	if err != nil {
		t.Fatalf("signed manifest must strict-parse: %v", err)
	}

	// Cryptographic verification through the vendored parser + CLI verifier.
	verifier := NewEd25519Verifier(pub)
	if err := donate.VerifySignature(signed, m, verifier); err != nil {
		t.Fatalf("round-trip verification failed: %v", err)
	}

	// Tampering with the payload must break verification.
	tampered := strings.Replace(string(signed), "Example Relief Foundation", "Evil Corp", 1)
	tm, perr := donate.Parse([]byte(tampered))
	if perr == nil {
		if err := donate.VerifySignature([]byte(tampered), tm, verifier); err == nil {
			t.Error("verification should FAIL on a tampered payload")
		}
	}
}

func TestSignManifest_RejectsAlreadySigned(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	already := []byte(`{"manifest_version":"1.0","organization":{"legal_name":"X","country":"US"},"signature":{"alg":"EdDSA","jws":"aa..bb"}}`)
	if _, err := signManifest(already, priv); err == nil {
		t.Fatal("expected refusal to re-sign an already-signed manifest")
	}
}

func TestParseEd25519PrivateKey_Forms(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)

	// 64-byte raw.
	if got, err := parseEd25519PrivateKey(priv); err != nil || len(got) != ed25519.PrivateKeySize {
		t.Fatalf("64-byte parse failed: %v", err)
	}
	// 32-byte seed.
	seed := priv.Seed()
	if got, err := parseEd25519PrivateKey(seed); err != nil {
		t.Fatalf("32-byte seed parse failed: %v", err)
	} else if !got.Equal(priv) {
		t.Error("seed-derived key must equal original")
	}
	// Garbage length.
	if _, err := parseEd25519PrivateKey([]byte("too short")); err == nil {
		t.Error("expected error for unrecognized key length")
	}
}

func TestReadManifestInput_FileAndErrors(t *testing.T) {
	// Local file.
	data, source, err := readManifestInput(nil, filepath.Join("..", "internal", "donate", "testdata_examples", "valid.donate.json"), nil)
	if err != nil {
		t.Fatalf("read local file: %v", err)
	}
	if !strings.HasPrefix(source, "file:") || len(data) == 0 {
		t.Errorf("unexpected source/data: %s / %d bytes", source, len(data))
	}
	// http:// (non-https) must be rejected as a usage error.
	if _, _, err := readManifestInput(nil, "http://insecure.example/donate.json", nil); err == nil {
		t.Error("expected rejection of non-https URL")
	}
	// Missing file -> not-found.
	if _, _, err := readManifestInput(nil, "/no/such/manifest.json", nil); err == nil {
		t.Error("expected error for missing file")
	}
}

// TestValidateHostileFixture_DropsAuthority ties the CLI's validate path to the
// parser's guarantees: the hostile fixture is strict-rejected and its salvaged
// form carries no authority fields (the type makes them unrepresentable).
func TestValidateHostileFixture_DropsAuthority(t *testing.T) {
	raw := fixture(t, "hostile.donate.json")
	if _, err := donate.Parse(raw); err == nil {
		t.Fatal("hostile fixture must be strict-rejected")
	}
	res := donate.Sanitize(raw)
	if res.Manifest == nil {
		t.Fatal("expected salvaged core")
	}
	// Every dropped authority claim we care about is reported.
	dropped := map[string]bool{}
	for _, c := range res.RejectedClaims {
		dropped[c.Path] = true
	}
	for _, want := range []string{
		"organization.tax_deductible", "organization.receipt_text",
		"donation.action_url", "x_extensions.authorization",
	} {
		if !dropped[want] {
			t.Errorf("expected dropped claim %q to be reported", want)
		}
	}
}

// TestReadManifestURL_RefusesRedirect_SSRF is the SSRF regression test. A
// hostile publisher serves an https manifest URL that 302-redirects to an
// http:// loopback target holding a secret body. The fetch MUST refuse to
// follow the redirect and never read the redirected body.
func TestReadManifestURL_RefusesRedirect_SSRF(t *testing.T) {
	const secret = "INTERNAL_SECRET_METADATA_TOKEN_SHOULD_NEVER_BE_READ"

	// The "internal" target the attacker wants us to reach (an http loopback,
	// standing in for http://169.254.169.254/… or http://localhost/…).
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(secret))
	}))
	defer internal.Close()

	// The publisher's endpoint: it 302-redirects to the internal http target.
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL, http.StatusFound)
	}))
	defer redirector.Close()

	// Use the redirector's own client so the httptest transport is honored;
	// withNoRedirect preserves the injected Transport but installs the refusing
	// CheckRedirect.
	data, _, err := readManifestURL(nil, redirector.URL, redirector.Client())
	if err == nil {
		t.Fatalf("expected the fetch to REFUSE the redirect, but it succeeded and read %d bytes", len(data))
	}
	if strings.Contains(string(data), secret) {
		t.Fatalf("SSRF: the redirected internal body was read: %q", string(data))
	}
	// It must classify as a validation rejection (exit 7), not a silent success.
	oe := output.AsError(err)
	if oe.Code != output.ExitValidation {
		t.Errorf("refused redirect should map to ExitValidation (7), got %d: %s", oe.Code, oe.Message)
	}
	if !strings.Contains(strings.ToLower(oe.Message), "redirect") {
		t.Errorf("error message should mention the redirect refusal, got %q", oe.Message)
	}
}

// TestWithNoRedirect_RefusesEvenOnHTTPSToHTTPS confirms the client refuses ANY
// redirect, not only cross-scheme ones — a static donate.json should never
// redirect at all.
func TestWithNoRedirect_RefusesEvenOnHTTPSToHTTPS(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"manifest_version":"1.0","organization":{"legal_name":"X","country":"US"}}`))
	}))
	defer final.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL, http.StatusMovedPermanently)
	}))
	defer redirector.Close()

	if _, _, err := readManifestURL(nil, redirector.URL, redirector.Client()); err == nil {
		t.Fatal("expected any redirect (even to a valid target) to be refused")
	}
}
