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

	// The fields below are what renewing and ending an OAuth sign-in need. A
	// credential saved by an earlier givmo has none of them, so it is used until
	// it expires and never renewed.

	// ClientID is the OAuth client the sign-in was granted to: the URL of a
	// client metadata document, or a registered client's id.
	ClientID string `json:"client_id,omitempty"`
	// Issuer is the authorization server's issuer identifier.
	Issuer string `json:"issuer,omitempty"`
	// TokenEndpoint is where the refresh token is presented.
	TokenEndpoint string `json:"token_endpoint,omitempty"`
	// RevocationEndpoint is where logout ends the sign-in (RFC 7009).
	RevocationEndpoint string `json:"revocation_endpoint,omitempty"`
	// AuthMethod is how the client authenticates at those endpoints:
	// AuthMethodNone or AuthMethodClientSecretBasic.
	AuthMethod string `json:"auth_method,omitempty"`
	// GrantedAt is when the person consented, from which the authorization
	// server counts the sign-in's 90-day ceiling. Renewals carry it unchanged.
	GrantedAt time.Time `json:"granted_at,omitempty"`
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

// Renewable reports whether the credential carries what a renewal needs: an
// OAuth sign-in with its refresh token, its client, a token endpoint and a known
// authentication method.
func (c *Credential) Renewable() bool {
	return c != nil && c.Type == CredTypeOAuth && c.RefreshToken != "" && c.ClientID != "" &&
		c.TokenEndpoint != "" && (c.AuthMethod == AuthMethodNone || c.AuthMethod == AuthMethodClientSecretBasic)
}

// expiresWithin reports whether the access token expires within d. An unknown
// expiry never does.
func (c *Credential) expiresWithin(d time.Duration) bool {
	return !c.ExpiresAt.IsZero() && time.Now().Add(d).After(c.ExpiresAt)
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
