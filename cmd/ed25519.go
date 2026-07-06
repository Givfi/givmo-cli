package cmd

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/givfi/givmo-cli/internal/donate"
)

// parsePKCS8Ed25519 parses PKCS#8 DER bytes into an Ed25519 private key.
func parsePKCS8Ed25519(der []byte) (ed25519.PrivateKey, error) {
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("PKCS#8 key is not Ed25519 (got %T)", key)
	}
	return priv, nil
}

// ed25519Verifier is a donate.Verifier that verifies a detached EdDSA JWS with
// a caller-supplied Ed25519 public key. The CLI wires this in so the vendored
// reference core stays dependency-free while the CLI can cryptographically
// verify signatures it produced. Used by the sign round-trip test.
type ed25519Verifier struct {
	pub ed25519.PublicKey
}

// Verify implements donate.Verifier. signingInput is
// ASCII(base64url(protectedHeader)) + "." + base64url(payload); jws is the
// detached compact form "<protected>..<signature>".
func (v ed25519Verifier) Verify(alg, _ string, signingInput []byte, jws string) error {
	if alg != "EdDSA" {
		return fmt.Errorf("unsupported alg %q for Ed25519 verifier", alg)
	}
	parts := strings.SplitN(jws, "..", 2)
	if len(parts) != 2 {
		return fmt.Errorf("jws is not in detached compact form")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fmt.Errorf("could not decode signature: %w", err)
	}
	if !ed25519.Verify(v.pub, signingInput, sig) {
		return fmt.Errorf("ed25519 signature verification failed")
	}
	return nil
}

// NewEd25519Verifier builds a donate.Verifier over an Ed25519 public key.
func NewEd25519Verifier(pub ed25519.PublicKey) donate.Verifier {
	return ed25519Verifier{pub: pub}
}
