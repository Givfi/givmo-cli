package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"regexp"
	"testing"
)

func TestS256Challenge_MatchesRFC7636(t *testing.T) {
	// RFC 7636 Appendix B worked example.
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	want := "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := S256Challenge(verifier); got != want {
		t.Fatalf("S256Challenge = %q, want %q (RFC 7636 test vector)", got, want)
	}
}

func TestNewPKCE_ValidShapeAndFreshness(t *testing.T) {
	verifierRe := regexp.MustCompile(`^[A-Za-z0-9_-]{43,128}$`)

	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		pk, err := NewPKCE()
		if err != nil {
			t.Fatalf("NewPKCE: %v", err)
		}
		if pk.Method != "S256" {
			t.Errorf("method = %q, want S256", pk.Method)
		}
		if !verifierRe.MatchString(pk.Verifier) {
			t.Errorf("verifier %q is not a valid RFC 7636 code_verifier (43..128 url-safe)", pk.Verifier)
		}
		// Challenge must be the S256 of the verifier.
		sum := sha256.Sum256([]byte(pk.Verifier))
		wantChallenge := base64.RawURLEncoding.EncodeToString(sum[:])
		if pk.Challenge != wantChallenge {
			t.Errorf("challenge mismatch: got %q want %q", pk.Challenge, wantChallenge)
		}
		if pk.State == "" {
			t.Error("state must be non-empty")
		}
		// Freshness: verifier + state must be unique across iterations.
		if seen[pk.Verifier] {
			t.Error("verifier collision — entropy source not fresh")
		}
		seen[pk.Verifier] = true
	}
}

func TestDefaultScopes_ExactlyTheFourConsumerScopes(t *testing.T) {
	got := DefaultScopes()
	want := []string{
		"givmo.donations.read",
		"givmo.receipts.read",
		"givmo.giving_summary.read",
		"givmo.donation_intents.create",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d scopes, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("scope[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
