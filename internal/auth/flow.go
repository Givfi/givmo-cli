package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ClientID is the CLI's public OAuth client id registered with Givmo Connect.
// A public native client carries no secret; PKCE is the proof mechanism.
const ClientID = "givmo-cli"

// ProtectedResourceMetadata is the RFC 9728 document published by the MCP host
// telling clients which authorization server(s) protect the resource.
type ProtectedResourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported,omitempty"`
}

// AuthServerMetadata is the RFC 8414 authorization-server metadata document.
type AuthServerMetadata struct {
	Issuer                        string   `json:"issuer"`
	AuthorizationEndpoint         string   `json:"authorization_endpoint"`
	TokenEndpoint                 string   `json:"token_endpoint"`
	ScopesSupported               []string `json:"scopes_supported,omitempty"`
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported,omitempty"`
}

// Discoverer fetches OAuth metadata. It is an interface so tests can inject a
// fake without touching the network.
type Discoverer interface {
	ProtectedResource(ctx context.Context, apiBase string) (*ProtectedResourceMetadata, error)
	AuthServer(ctx context.Context, authBase string) (*AuthServerMetadata, error)
}

// httpDiscoverer is the production Discoverer over net/http.
type httpDiscoverer struct{ client *http.Client }

// NewDiscoverer returns an HTTP-backed Discoverer with a bounded timeout.
func NewDiscoverer() Discoverer {
	return &httpDiscoverer{client: &http.Client{Timeout: 15 * time.Second}}
}

func (d *httpDiscoverer) ProtectedResource(ctx context.Context, apiBase string) (*ProtectedResourceMetadata, error) {
	u := strings.TrimRight(apiBase, "/") + "/.well-known/oauth-protected-resource"
	var m ProtectedResourceMetadata
	if err := getJSON(ctx, d.client, u, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (d *httpDiscoverer) AuthServer(ctx context.Context, authBase string) (*AuthServerMetadata, error) {
	u := strings.TrimRight(authBase, "/") + "/.well-known/oauth-authorization-server"
	var m AuthServerMetadata
	if err := getJSON(ctx, d.client, u, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func getJSON(ctx context.Context, c *http.Client, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("metadata %s returned HTTP %d", u, resp.StatusCode)
	}
	return json.Unmarshal(body, out)
}

// AuthorizeParams captures everything needed to build an authorize URL.
type AuthorizeParams struct {
	AuthorizationEndpoint string
	ClientID              string
	RedirectURI           string
	Scopes                []string
	PKCE                  *PKCE
	Resource              string // RFC 8707 resource indicator (the MCP host)
}

// BuildAuthorizeURL constructs the RFC 6749 authorization-code + PKCE request
// URL. It is pure/deterministic given its inputs, so tests assert it exactly.
func BuildAuthorizeURL(p AuthorizeParams) (string, error) {
	if p.AuthorizationEndpoint == "" {
		return "", errors.New("missing authorization_endpoint")
	}
	if p.PKCE == nil {
		return "", errors.New("missing PKCE parameters")
	}
	base, err := url.Parse(p.AuthorizationEndpoint)
	if err != nil {
		return "", fmt.Errorf("invalid authorization_endpoint: %w", err)
	}
	q := base.Query()
	q.Set("response_type", "code")
	q.Set("client_id", p.ClientID)
	q.Set("redirect_uri", p.RedirectURI)
	q.Set("scope", strings.Join(p.Scopes, " "))
	q.Set("state", p.PKCE.State)
	q.Set("code_challenge", p.PKCE.Challenge)
	q.Set("code_challenge_method", p.PKCE.Method)
	if p.Resource != "" {
		q.Set("resource", p.Resource)
	}
	base.RawQuery = q.Encode()
	return base.String(), nil
}

// LoopbackResult is what the callback listener captured.
type LoopbackResult struct {
	Code  string
	State string
	Err   string // the OAuth `error` param, if the AS reported one
}

// LoopbackListener runs a localhost HTTP server on an ephemeral port to catch
// the authorization redirect. The returned RedirectURI must be used in the
// authorize request. Call Wait to block for the callback (bounded by ctx).
type LoopbackListener struct {
	ln          net.Listener
	srv         *http.Server
	resultCh    chan LoopbackResult
	RedirectURI string
	expectState string
}

// NewLoopbackListener binds 127.0.0.1 on an ephemeral port with the given
// callback path (e.g. "/callback"). expectState is validated on receipt.
func NewLoopbackListener(path, expectState string) (*LoopbackListener, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("bind loopback listener: %w", err)
	}
	if path == "" {
		path = "/callback"
	}
	l := &LoopbackListener{
		ln:          ln,
		resultCh:    make(chan LoopbackResult, 1),
		RedirectURI: fmt.Sprintf("http://%s%s", ln.Addr().String(), path),
		expectState: expectState,
	}
	mux := http.NewServeMux()
	mux.HandleFunc(path, l.handle)
	l.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	return l, nil
}

func (l *LoopbackListener) handle(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	res := LoopbackResult{
		Code:  q.Get("code"),
		State: q.Get("state"),
		Err:   q.Get("error"),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if res.Err != "" || res.Code == "" {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, "<h1>Givmo CLI login failed</h1><p>You can close this window and return to the terminal.</p>")
	} else {
		io.WriteString(w, "<h1>Givmo CLI login complete</h1><p>You can close this window and return to the terminal.</p>")
	}
	// Deliver exactly once.
	select {
	case l.resultCh <- res:
	default:
	}
}

// Serve starts accepting connections. It returns immediately; the server runs
// until Close.
func (l *LoopbackListener) Serve() {
	go func() { _ = l.srv.Serve(l.ln) }()
}

// Wait blocks until the callback arrives, ctx is done, or timeout elapses. It
// validates the returned state against the expected value (anti-CSRF).
func (l *LoopbackListener) Wait(ctx context.Context) (LoopbackResult, error) {
	select {
	case <-ctx.Done():
		return LoopbackResult{}, ctx.Err()
	case res := <-l.resultCh:
		if res.Err != "" {
			return res, fmt.Errorf("authorization server returned error %q", res.Err)
		}
		if res.State != l.expectState {
			return LoopbackResult{}, errors.New("state mismatch on callback (possible CSRF); aborting login")
		}
		return res, nil
	}
}

// Close shuts down the loopback server.
func (l *LoopbackListener) Close() error { return l.srv.Close() }

// TokenResponse is the RFC 6749 token endpoint response.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	// Error fields per RFC 6749 §5.2.
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// ExchangeParams captures the code-for-token exchange inputs.
type ExchangeParams struct {
	TokenEndpoint string
	ClientID      string
	Code          string
	RedirectURI   string
	CodeVerifier  string
	Resource      string
}

// BuildExchangeForm builds the x-www-form-urlencoded body for the token
// exchange. Pure/deterministic → unit-tested without network.
func BuildExchangeForm(p ExchangeParams) url.Values {
	v := url.Values{}
	v.Set("grant_type", "authorization_code")
	v.Set("code", p.Code)
	v.Set("redirect_uri", p.RedirectURI)
	v.Set("client_id", p.ClientID)
	v.Set("code_verifier", p.CodeVerifier)
	if p.Resource != "" {
		v.Set("resource", p.Resource)
	}
	return v
}

// ExchangeCode posts the authorization code + verifier to the token endpoint
// and returns the token response. Errors from the AS are surfaced verbatim.
func ExchangeCode(ctx context.Context, client *http.Client, p ExchangeParams) (*TokenResponse, error) {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	form := BuildExchangeForm(p)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tr TokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("token endpoint returned HTTP %d with unparseable body", resp.StatusCode)
	}
	if tr.Error != "" {
		return nil, fmt.Errorf("token exchange failed: %s: %s", tr.Error, tr.ErrorDescription)
	}
	if resp.StatusCode != http.StatusOK || tr.AccessToken == "" {
		return nil, fmt.Errorf("token endpoint returned HTTP %d with no access_token", resp.StatusCode)
	}
	return &tr, nil
}

// CredentialFromToken converts a TokenResponse into a stored Credential.
func CredentialFromToken(profile string, tr *TokenResponse) *Credential {
	c := &Credential{
		Profile:      profile,
		Type:         CredTypeOAuth,
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		TokenType:    tr.TokenType,
	}
	if tr.Scope != "" {
		c.Scopes = strings.Fields(tr.Scope)
	}
	if tr.ExpiresIn > 0 {
		c.ExpiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	return c
}
