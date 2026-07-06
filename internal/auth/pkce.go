// Package auth implements the consumer-tier OAuth flow (RFC 6749 + PKCE
// RFC 7636 with S256) against Givmo Connect, and the local token store.
//
// SECURITY POSTURE:
//   - The code verifier is high-entropy and never logged.
//   - Tokens are stored in the OS keychain when available, else in a 0600 file
//     under ~/.givmo. They are NEVER written to logs and NEVER included in
//     --json output.
//   - The CLI is a public client (no client secret): PKCE is the proof-of-
//     possession mechanism, and the redirect is a loopback listener per the
//     OAuth 2.0 for Native Apps BCP (RFC 8252).
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// The four consumer scopes, exactly as the platform defines them. These are the
// only scopes the CLI ever requests.
const (
	ScopeDonationsRead      = "givmo.donations.read"
	ScopeReceiptsRead       = "givmo.receipts.read"
	ScopeGivingSummaryRead  = "givmo.giving_summary.read"
	ScopeDonationIntentsNew = "givmo.donation_intents.create"
)

// DefaultScopes is the full consumer scope set requested by `givmo login`.
func DefaultScopes() []string {
	return []string{
		ScopeDonationsRead,
		ScopeReceiptsRead,
		ScopeGivingSummaryRead,
		ScopeDonationIntentsNew,
	}
}

// PKCE holds a generated PKCE code-verifier/challenge pair plus the anti-CSRF
// state value for one authorization request.
type PKCE struct {
	// Verifier is the RFC 7636 code_verifier — a high-entropy secret. NEVER log
	// or serialize this beyond the in-flight token exchange.
	Verifier string
	// Challenge is BASE64URL(SHA256(verifier)) — safe to send to the AS.
	Challenge string
	// Method is always "S256" (plain is not used).
	Method string
	// State is the opaque anti-forgery value echoed back on the redirect.
	State string
}

// randomURLSafe returns n bytes of CSPRNG entropy as base64url (no padding).
func randomURLSafe(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read entropy: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// NewPKCE generates a fresh PKCE pair with an S256 challenge and a random
// state. The verifier is 64 bytes of entropy (well above the RFC 7636 minimum),
// yielding an 86-char base64url string within the 43..128 allowed length.
func NewPKCE() (*PKCE, error) {
	verifier, err := randomURLSafe(64)
	if err != nil {
		return nil, err
	}
	state, err := randomURLSafe(24)
	if err != nil {
		return nil, err
	}
	return &PKCE{
		Verifier:  verifier,
		Challenge: S256Challenge(verifier),
		Method:    "S256",
		State:     state,
	}, nil
}

// S256Challenge computes BASE64URL-encode(SHA256(ASCII(verifier))) per
// RFC 7636 §4.2. Exported so tests can assert the transformation directly.
func S256Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
