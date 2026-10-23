package auth

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// The ways an authorization-server address fails CheckAddress. Each completes a
// sentence about the address: "its token endpoint, …, isn't https".
var (
	ErrNotURL       = errors.New("isn't a valid URL")
	ErrNotHTTPS     = errors.New("isn't https")
	ErrHostNotASCII = errors.New("spells its host with characters outside ASCII")
)

// CheckAddress applies the rules every authorization-server address the CLI uses
// must meet: the authorization server a login adopts and its authorization,
// token and revocation endpoints, and the same addresses again whenever a stored
// sign-in is loaded. Login and load apply this one rule, so a sign-in a login
// stored is one a load accepts.
//
//   - It parses, with a host.
//   - Its host, as url.Parse decodes it (percent-encoding included), is ASCII.
//     Any other host is mapped to an ASCII one before it is dialed, so the
//     address checked or shown would not be the one reached. An
//     internationalized name in its xn-- form is ASCII.
//   - It is https, or, where allowLoopback is set, http to a loopback address.
//     allowLoopback is never set for the production profile.
func CheckAddress(raw string, allowLoopback bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ErrNotURL
	}
	host := u.Hostname()
	for i := 0; i < len(host); i++ {
		if host[i] >= 0x80 {
			return ErrHostNotASCII
		}
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && allowLoopback && isLoopbackHost(host) {
		return nil
	}
	return ErrNotHTTPS
}

// isLoopbackHost reports whether host is a loopback address/name.
func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// StoredAddressError is a stored sign-in carrying an address CheckAddress
// refuses. Such a sign-in is not used at all: no access token is handed out, and
// nothing is sent to any of its addresses, its refresh token least of all. It
// stays stored, so a login that replaces it, or a logout, can say it was not
// ended on Givmo's side.
type StoredAddressError struct {
	// Field names the address: "authorization server", "token endpoint" or
	// "revocation endpoint".
	Field string
	// Address is the address as stored.
	Address string
	// Err is CheckAddress's reason: ErrNotURL, ErrNotHTTPS or ErrHostNotASCII.
	Err error
}

// Error reads "its token endpoint, "http://…", isn't https", the address escaped
// to ASCII so a look-alike character shows as \uXXXX.
func (e *StoredAddressError) Error() string {
	return fmt.Sprintf("its %s, %s, %v", e.Field, strconv.QuoteToASCII(e.Address), e.Err)
}

func (e *StoredAddressError) Unwrap() error { return e.Err }

// CheckAddresses applies CheckAddress to each address an OAuth sign-in adopted at
// login, returning the first that fails as a *StoredAddressError. An API key
// carries none, and a sign-in an earlier givmo saved may carry none either.
func (c *Credential) CheckAddresses(allowLoopback bool) error {
	if c == nil || c.Type != CredTypeOAuth {
		return nil
	}
	for _, a := range []struct{ field, address string }{
		{"authorization server", c.Issuer},
		{"token endpoint", c.TokenEndpoint},
		{"revocation endpoint", c.RevocationEndpoint},
	} {
		if a.address == "" {
			continue
		}
		if err := CheckAddress(a.address, allowLoopback); err != nil {
			return &StoredAddressError{Field: a.field, Address: a.address, Err: err}
		}
	}
	return nil
}
