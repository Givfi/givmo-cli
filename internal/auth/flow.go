package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// ClientDocumentPath is where each Givmo environment serves givmo-cli's client
// metadata document, under its authorization server's issuer: on production
// https://mcp.givmo.io/connect/oauth/clients/givmo-cli. The document's URL is the
// CLI's client id, a public client with no secret (D-MCP-63).
const ClientDocumentPath = "/connect/oauth/clients/givmo-cli"

// ClientDocumentURL is Givmo's client document for the authorization server whose
// issuer identifier is issuer.
func ClientDocumentURL(issuer string) string {
	return strings.TrimRight(issuer, "/") + ClientDocumentPath
}

// ProtectedResourceMetadata is the RFC 9728 document published by the MCP host
// telling clients which authorization server(s) protect the resource.
type ProtectedResourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported,omitempty"`
}

// AuthServerMetadata is the RFC 8414 authorization-server metadata document.
type AuthServerMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RevocationEndpoint                string   `json:"revocation_endpoint,omitempty"`
	ScopesSupported                   []string `json:"scopes_supported,omitempty"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported,omitempty"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported,omitempty"`
	// ClientIDMetadataDocumentSupported says the server takes a client metadata
	// document's URL as a client id.
	ClientIDMetadataDocumentSupported bool `json:"client_id_metadata_document_supported,omitempty"`
	// AuthorizationResponseIssParameterSupported says every authorization
	// response carries iss (RFC 9207).
	AuthorizationResponseIssParameterSupported bool `json:"authorization_response_iss_parameter_supported,omitempty"`
}

// AcceptsClientDocuments reports whether the server signs in a client named by a
// client metadata document as a public client: it advertises the documents, and
// "none" among its token endpoint's authentication methods.
func (m *AuthServerMetadata) AcceptsClientDocuments() bool {
	if m == nil || !m.ClientIDMetadataDocumentSupported {
		return false
	}
	for _, method := range m.TokenEndpointAuthMethodsSupported {
		if method == AuthMethodNone {
			return true
		}
	}
	return false
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
	// Iss is the authorization server's issuer the response names (RFC 9207).
	Iss string
}

// IssuerError is an authorization response that did not come from the
// authorization server the login started at: its iss names another, or it
// carried none where the server advertises iss (RFC 9207).
type IssuerError struct {
	Expected string
	Got      string
}

func (e *IssuerError) Error() string {
	if e.Got == "" {
		return fmt.Sprintf("the sign-in response did not come from %s (it carried no iss)", e.Expected)
	}
	return fmt.Sprintf("the sign-in response did not come from %s (its iss was %q)", e.Expected, e.Got)
}

// LoopbackListener runs a localhost HTTP server to catch the authorization
// redirect. The returned RedirectURI must be used in the authorize request.
// Call Wait to block for the callback (bounded by ctx).
type LoopbackListener struct {
	ln          net.Listener
	srv         *http.Server
	resultCh    chan LoopbackResult
	RedirectURI string
	expectState string
	// expectIss is the issuer a response's iss must name, when set; issRequired
	// says the server advertises iss, so a response must carry it.
	expectIss   string
	issRequired bool
}

// ExpectIssuer makes the listener check each response's iss parameter (RFC 9207
// section 2.4): an iss that is present must equal issuer, by simple string
// comparison, whether or not the server advertises iss; with required set (it
// does), a response without one is refused too. Call it before Serve.
func (l *LoopbackListener) ExpectIssuer(issuer string, required bool) {
	l.expectIss = issuer
	l.issRequired = required
}

// NewLoopbackListener binds 127.0.0.1 on port with the given callback path
// (e.g. "/callback"). Port 0 requests an ephemeral port. expectState is
// validated on receipt.
func NewLoopbackListener(path, expectState string, port int) (*LoopbackListener, error) {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("bind loopback callback listener on %s: %w", addr, err)
	}
	if path == "" {
		path = "/callback"
	}
	boundPort := ln.Addr().(*net.TCPAddr).Port
	l := &LoopbackListener{
		ln:          ln,
		resultCh:    make(chan LoopbackResult, 1),
		RedirectURI: fmt.Sprintf("http://127.0.0.1:%d%s", boundPort, path),
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
		Iss:   q.Get("iss"),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if res.Code == "" || l.check(res) != nil {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, "<h1>givmo-cli: sign-in failed.</h1><p>You can close this tab and return to the terminal.</p>")
	} else {
		io.WriteString(w, "<h1>givmo-cli: signed in.</h1><p>You can close this tab and return to the terminal.</p>")
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
// validates the returned state against the expected value (anti-CSRF), and the
// iss parameter as ExpectIssuer set (an *IssuerError when it fails).
func (l *LoopbackListener) Wait(ctx context.Context) (LoopbackResult, error) {
	select {
	case <-ctx.Done():
		return LoopbackResult{}, ctx.Err()
	case res := <-l.resultCh:
		if err := l.check(res); err != nil {
			if res.Err != "" {
				return res, err
			}
			return LoopbackResult{}, err
		}
		return res, nil
	}
}

// check validates one authorization response: the server's own error first,
// then state, then iss.
func (l *LoopbackListener) check(res LoopbackResult) error {
	if res.Err != "" {
		return fmt.Errorf("authorization server returned error %q", res.Err)
	}
	if res.State != l.expectState {
		return errors.New("state mismatch on callback (possible CSRF); aborting login")
	}
	if l.expectIss != "" && (res.Iss != "" || l.issRequired) && res.Iss != l.expectIss {
		return &IssuerError{Expected: l.expectIss, Got: res.Iss}
	}
	return nil
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
	// ClientSecret is a registered client's secret, sent only with
	// AuthMethodClientSecretBasic.
	ClientSecret string
	// AuthMethod is how the client authenticates: AuthMethodNone (a public
	// client, its client_id in the form) or AuthMethodClientSecretBasic.
	AuthMethod   string
	Code         string
	RedirectURI  string
	CodeVerifier string
	Resource     string
}

// clientSecretBasicHeader builds RFC 6749 section 2.3.1 client_secret_basic
// credentials. Each credential is form-encoded before the pair is joined and
// base64-encoded; the Givmo authorization server URL-decodes both on receipt.
func clientSecretBasicHeader(clientID, clientSecret string) string {
	credentials := url.QueryEscape(clientID) + ":" + url.QueryEscape(clientSecret)
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(credentials))
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
	// The method decides, never a secret that happens to be at hand: the server
	// refuses any secret from a public client.
	if p.AuthMethod == AuthMethodClientSecretBasic {
		req.Header.Set("Authorization", clientSecretBasicHeader(p.ClientID, p.ClientSecret))
	}
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

// The client authentication methods the CLI uses at the token and revocation
// endpoints (their RFC 8414 names).
const (
	// AuthMethodNone is a public client's: no secret, the client_id in the form.
	AuthMethodNone = "none"
	// AuthMethodClientSecretBasic is a registered confidential client's: its id
	// and secret in an HTTP Basic header (RFC 6749 section 2.3.1).
	AuthMethodClientSecretBasic = "client_secret_basic"
)

// TokenError is an error answer from the token or revocation endpoint: the OAuth
// error code and description it sent (RFC 6749 section 5.2), or only an HTTP
// status when the body named none.
type TokenError struct {
	Status      int
	Code        string
	Description string
	// RetryAfter is the answer's Retry-After header, verbatim.
	RetryAfter string
}

func (e *TokenError) Error() string {
	switch {
	case e.Code != "" && e.Description != "":
		return e.Code + ": " + e.Description
	case e.Code != "":
		return e.Code
	default:
		return fmt.Sprintf("HTTP %d", e.Status)
	}
}

// TransportError is a request to the token or revocation endpoint that got no
// answer. Sent says whether the whole request was written to the connection
// first, so the server may have acted on it.
type TransportError struct {
	Sent bool
	Err  error
}

func (e *TransportError) Error() string { return e.Err.Error() }
func (e *TransportError) Unwrap() error { return e.Err }

// postForm posts form to endpoint, authenticating the client by authMethod:
// AuthMethodClientSecretBasic sends the Basic header, AuthMethodNone nothing
// (the form carries the client_id). It returns the answer's status, header and
// body (at most 1 MiB), or a *TransportError when none arrived.
func postForm(ctx context.Context, client *http.Client, endpoint string, form url.Values, authMethod, clientID, secret string) (int, http.Header, []byte, error) {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	var sent atomic.Bool
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				sent.Store(true)
			}
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, nil, &TransportError{Err: err}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if authMethod == AuthMethodClientSecretBasic {
		req.Header.Set("Authorization", clientSecretBasicHeader(clientID, secret))
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, &TransportError{Sent: sent.Load(), Err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, nil, &TransportError{Sent: true, Err: err}
	}
	return resp.StatusCode, resp.Header, body, nil
}

// tokenError decodes an error answer's OAuth error fields.
func tokenError(status int, header http.Header, body []byte) *TokenError {
	e := &TokenError{Status: status, RetryAfter: strings.TrimSpace(header.Get("Retry-After"))}
	var fields struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if json.Unmarshal(body, &fields) == nil {
		e.Code, e.Description = fields.Error, fields.ErrorDescription
	}
	return e
}

// RefreshParams captures one renewal of a sign-in (RFC 6749 section 6).
type RefreshParams struct {
	TokenEndpoint string
	ClientID      string
	// ClientSecret is a registered client's secret, sent only with
	// AuthMethodClientSecretBasic.
	ClientSecret string
	AuthMethod   string
	RefreshToken string
}

// Refresh presents the refresh token at the token endpoint and returns the new
// tokens. The authorization server rotates the refresh token on every renewal
// and ends the whole sign-in when one is presented twice, so a caller must never
// present the same one concurrently (see TokenSource). An error answer returns a
// *TokenError, no answer a *TransportError.
func Refresh(ctx context.Context, client *http.Client, p RefreshParams) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", p.RefreshToken)
	form.Set("client_id", p.ClientID)
	status, header, body, err := postForm(ctx, client, p.TokenEndpoint, form, p.AuthMethod, p.ClientID, p.ClientSecret)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, tokenError(status, header, body)
	}
	var tr TokenResponse
	if err := json.Unmarshal(body, &tr); err != nil || tr.AccessToken == "" {
		// The server answered, and may have rotated the refresh token, but its new
		// tokens did not arrive: for the caller that is a lost answer.
		return nil, &TransportError{Sent: true, Err: errors.New("the token endpoint's answer carried no access token")}
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
