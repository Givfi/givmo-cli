package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// RenewWindow is how close to its expiry an access token is renewed before it is
// used. Renewing ahead also keeps a stale bearer from costing a refused request.
const RenewWindow = 60 * time.Second

// SignInLifetime is the authorization server's ceiling on one sign-in, counted
// from consent however often it is renewed (REFRESH_FAMILY_ABSOLUTE_LIFETIME).
// 14 days without a renewal end it sooner.
const SignInLifetime = 90 * 24 * time.Hour

// signInEndingNotice is how long before SignInLifetime runs out the CLI starts
// saying when the sign-in ends.
const signInEndingNotice = 7 * 24 * time.Hour

// lockWait bounds the wait for another givmo process to finish with the profile's
// lock: its work is one request to the authorization server.
const lockWait = 60 * time.Second

var (
	// ErrSignInEnded: the authorization server answered invalid_grant, so the
	// sign-in is over (revoked, its refresh token presented twice, unused for 14
	// days, or past its 90-day ceiling). The stored credential has been deleted.
	ErrSignInEnded = errors.New("the sign-in has ended")
	// ErrLockTimeout: another givmo process held the profile's lock until the wait's
	// deadline, lockWait or the caller's own, whichever came first.
	ErrLockTimeout = errors.New("timed out waiting for another givmo process to finish renewing the sign-in")
	// ErrLockWaitCanceled: the caller cancelled while another givmo process held
	// the profile's lock.
	ErrLockWaitCanceled = errors.New("stopped waiting for another givmo process to finish renewing the sign-in")
)

// TokenSource gives every command and the MCP bridge the active profile's
// credential, renewing its access token before it expires.
//
// THE INVARIANT: this computer never presents one refresh token twice. The
// authorization server rotates the refresh token on every renewal and revokes the
// whole sign-in, every successor included, when one is presented again (plan
// §2.10). So a renewal runs under the profile's lock file, re-reads the store
// under it, and presents the stored refresh token only if no other process has
// renewed the sign-in meanwhile; and every write or delete of the stored sign-in
// takes the same lock, so none can interleave with a renewal.
type TokenSource struct {
	store   *Store
	profile string
	client  *http.Client
	// notices receives the line saying when the sign-in ends, once per source.
	notices io.Writer
	noticed atomic.Bool
}

// NewTokenSource builds the token source for profile's stored credential. client
// sends the renewals; notices receives the sign-in's ending date in its last week
// (nil: nowhere).
func NewTokenSource(store *Store, profile string, client *http.Client, notices io.Writer) *TokenSource {
	return &TokenSource{store: store, profile: profile, client: client, notices: notices}
}

// Credential returns the stored credential, renewed first when its access token
// expires within RenewWindow. It returns (nil, nil) when nothing is stored. A
// credential that cannot be renewed (an API key, or a sign-in saved by an earlier
// givmo) is returned as stored.
func (s *TokenSource) Credential(ctx context.Context) (*Credential, error) {
	c, err := s.store.Load(s.profile)
	if errors.Is(err, ErrNoCredential) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if c.Renewable() && c.expiresWithin(RenewWindow) {
		if c, err = s.renew(ctx, c.AuthorizationHeader(), false); err != nil || c == nil {
			return c, err
		}
	}
	s.notice(c)
	return c, nil
}

// Rejected renews the sign-in after the server refused the request authorized
// with authorization (an HTTP 401), unless another process has renewed it since.
// It returns the credential to repeat the request with: the stored one when it
// cannot be renewed, nil when nothing is stored any more.
func (s *TokenSource) Rejected(ctx context.Context, authorization string) (*Credential, error) {
	return s.renew(ctx, authorization, true)
}

// renew renews the stored sign-in under the profile's lock. seen is the
// Authorization header the caller used; force renews even a token that is not
// about to expire, when the server refused seen and the stored token is still it.
func (s *TokenSource) renew(ctx context.Context, seen string, force bool) (*Credential, error) {
	var out *Credential
	err := s.withLock(ctx, func() error {
		// Re-read under the lock: another process may have renewed, replaced or
		// deleted the sign-in since the caller read it.
		c, err := s.store.Load(s.profile)
		if errors.Is(err, ErrNoCredential) {
			return nil
		}
		if err != nil {
			return err
		}
		due := c.expiresWithin(RenewWindow) || (force && c.AuthorizationHeader() == seen)
		if !due || !c.Renewable() || !fileLocking {
			out = c
			return nil
		}
		out, err = s.refresh(ctx, c)
		return err
	})
	return out, err
}

// refresh presents c's refresh token and stores the new tokens. The caller holds
// the profile's lock.
func (s *TokenSource) refresh(ctx context.Context, c *Credential) (*Credential, error) {
	var secret string
	if c.AuthMethod == AuthMethodClientSecretBasic {
		var err error
		if secret, _, err = ResolveClientSecret(s.store, s.profile); err != nil {
			return nil, err
		}
		if secret == "" {
			return nil, errors.New("renewing this sign-in needs its registered client's secret, and neither GIVMO_CLIENT_SECRET nor the stored one is set")
		}
	}
	// Once the refresh token is sent the server may have rotated it, so its answer
	// is waited for even past the caller's own deadline: abandoned, it would leave
	// the spent token stored for the next renewal to present, ending the sign-in.
	// The client's own timeout still bounds the request.
	tr, err := Refresh(context.WithoutCancel(ctx), s.client, RefreshParams{
		TokenEndpoint: c.TokenEndpoint,
		ClientID:      c.ClientID,
		ClientSecret:  secret,
		AuthMethod:    c.AuthMethod,
		RefreshToken:  c.RefreshToken,
	})
	if err != nil {
		var te *TokenError
		if errors.As(err, &te) && te.Code == "invalid_grant" {
			if derr := s.store.DeleteCredential(s.profile); derr != nil {
				return nil, fmt.Errorf("%w (and the stored sign-in could not be deleted: %v)", ErrSignInEnded, derr)
			}
			return nil, ErrSignInEnded
		}
		// Anything else leaves the stored sign-in as it was: a server that refused
		// the client, a rate limit, or a lost answer. After a lost answer the next
		// renewal either succeeds (this one never landed) or is told invalid_grant
		// (it landed and its new tokens are gone), and the sign-in has ended either
		// way.
		return nil, err
	}
	next := c.renewedBy(tr)
	if err := s.store.Save(next); err != nil {
		// The server has rotated the refresh token, so the stored one is spent:
		// this command can still use the new tokens, the next one cannot.
		if s.notices != nil {
			fmt.Fprintf(s.notices, "warning: renewed the sign-in but could not store it (%v); the next command will need `givmo login`\n", err)
		}
	}
	return next, nil
}

// renewedBy returns c with the tokens of a renewal. A renewal that sent no new
// refresh token leaves the old one valid (RFC 6749 section 6). The client fields
// and GrantedAt are carried unchanged.
func (c *Credential) renewedBy(tr *TokenResponse) *Credential {
	next := *c
	next.AccessToken = tr.AccessToken
	if tr.TokenType != "" {
		next.TokenType = tr.TokenType
	}
	if tr.RefreshToken != "" {
		next.RefreshToken = tr.RefreshToken
	}
	if tr.Scope != "" {
		next.Scopes = strings.Fields(tr.Scope)
	}
	next.ExpiresAt = time.Time{}
	if tr.ExpiresIn > 0 {
		next.ExpiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	return &next
}

// Save stores c as the profile's sign-in under the profile's lock, so a login
// never interleaves with another process's renewal of the sign-in it replaces.
func (s *TokenSource) Save(ctx context.Context, c *Credential) error {
	return s.withLock(ctx, func() error { return s.store.Save(c) })
}

// SignOut deletes everything stored for the profile under the profile's lock,
// after handing end the credential it re-read there, if one is stored, to end the
// sign-in on the authorization server's side. The delete happens whatever end
// does, and the lock keeps a renewal in flight from storing the sign-in again.
func (s *TokenSource) SignOut(ctx context.Context, end func(*Credential)) error {
	return s.withLock(ctx, func() error {
		if c, err := s.store.Load(s.profile); err == nil {
			end(c)
		}
		return s.store.Delete(s.profile)
	})
}

// withLock runs fn holding the profile's lock, waiting at most lockWait for it. A
// wait that ends unfinished is ErrLockTimeout when a deadline ended it, the lock's
// own or the caller's, and ErrLockWaitCanceled when the caller cancelled it, never
// a bare context error: the CLI's catch-all for those says to log out, which would
// end a working sign-in.
func (s *TokenSource) withLock(ctx context.Context, fn func() error) error {
	path, err := s.store.lockFilePath(s.profile)
	if err != nil {
		return err
	}
	waitCtx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	l, err := lockProfile(waitCtx, path)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return ErrLockTimeout
	case errors.Is(err, context.Canceled):
		return ErrLockWaitCanceled
	case err != nil:
		return err
	}
	defer l.unlock()
	return fn()
}

// notice writes, once per source, the date the sign-in ends when that is less
// than signInEndingNotice away.
func (s *TokenSource) notice(c *Credential) {
	if s.notices == nil || c == nil || c.Type != CredTypeOAuth || c.GrantedAt.IsZero() {
		return
	}
	ends := c.GrantedAt.Add(SignInLifetime)
	if left := time.Until(ends); left <= 0 || left > signInEndingNotice {
		return
	}
	if s.noticed.CompareAndSwap(false, true) {
		fmt.Fprintf(s.notices, "Your Givmo sign-in on this computer ends on %s. Run `givmo login` before then.\n", ends.Local().Format("2006-01-02"))
	}
}
