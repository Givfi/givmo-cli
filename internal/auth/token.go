package auth

import (
	"strings"
	"time"
)

// Credential is the stored authentication material for one profile.
//
// It supports both consumer OAuth (bearer + refresh, from `givmo login`) and
// the non-interactive CI path (a raw API key from --api-key / GIVMO_API_KEY).
// This struct is NEVER emitted in --json command output and NEVER logged.
type Credential struct {
	// Profile is the environment this credential belongs to.
	Profile string `json:"profile"`
	// Type is "oauth" (interactive PKCE) or "api_key" (CI/non-interactive).
	Type string `json:"type"`
	// AccessToken is the bearer presented to the API. For api_key type this is
	// the API key itself.
	AccessToken string `json:"access_token"`
	// RefreshToken is the OAuth refresh token (oauth type only).
	RefreshToken string `json:"refresh_token,omitempty"`
	// TokenType is normally "Bearer".
	TokenType string `json:"token_type,omitempty"`
	// Scopes are the granted scopes (oauth type).
	Scopes []string `json:"scopes,omitempty"`
	// Subject is the linked identity (opaque subject id), for `whoami`.
	Subject string `json:"subject,omitempty"`
	// ExpiresAt is when the access token expires (zero = unknown/never).
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

const (
	// CredTypeOAuth is an interactive user-delegated OAuth credential.
	CredTypeOAuth = "oauth"
	// CredTypeAPIKey is a non-interactive CI credential.
	CredTypeAPIKey = "api_key"
)

// Expired reports whether the access token is known to be expired (with a small
// clock-skew safety margin). Unknown expiry is treated as not-expired.
func (c *Credential) Expired() bool {
	if c == nil || c.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().Add(30 * time.Second).After(c.ExpiresAt)
}

// AuthorizationHeader returns the value for the HTTP Authorization header, or
// "" if no usable token is present.
func (c *Credential) AuthorizationHeader() string {
	if c == nil || c.AccessToken == "" {
		return ""
	}
	typ := c.TokenType
	if typ == "" {
		typ = "Bearer"
	}
	return typ + " " + c.AccessToken
}

// Redacted returns a copy safe to display: the access/refresh tokens are
// replaced with a fixed placeholder so a Credential can be shown in `whoami`
// diagnostics without leaking secrets. Used only for human-facing summaries;
// the raw tokens never reach --json output at all.
func (c *Credential) Redacted() Credential {
	if c == nil {
		return Credential{}
	}
	clone := *c
	if clone.AccessToken != "" {
		clone.AccessToken = "***redacted***"
	}
	if clone.RefreshToken != "" {
		clone.RefreshToken = "***redacted***"
	}
	clone.Scopes = append([]string(nil), c.Scopes...)
	return clone
}

// ScopeString joins granted scopes for display.
func (c *Credential) ScopeString() string {
	if c == nil {
		return ""
	}
	return strings.Join(c.Scopes, " ")
}
