package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestBuildAuthorizeURL_Deterministic(t *testing.T) {
	pk := &PKCE{Verifier: "v", Challenge: "chal", Method: "S256", State: "st4te"}
	got, err := BuildAuthorizeURL(AuthorizeParams{
		AuthorizationEndpoint: "https://connect.givmo.io/oauth/authorize",
		ClientID:              "givmo-cli",
		RedirectURI:           "http://127.0.0.1:54321/callback",
		Scopes:                []string{"givmo.donations.read", "givmo.receipts.read"},
		PKCE:                  pk,
		Resource:              "https://mcp.givmo.io/mcp",
	})
	if err != nil {
		t.Fatalf("BuildAuthorizeURL: %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("result is not a URL: %v", err)
	}
	q := u.Query()
	checks := map[string]string{
		"response_type":         "code",
		"client_id":             "givmo-cli",
		"redirect_uri":          "http://127.0.0.1:54321/callback",
		"scope":                 "givmo.donations.read givmo.receipts.read",
		"state":                 "st4te",
		"code_challenge":        "chal",
		"code_challenge_method": "S256",
		"resource":              "https://mcp.givmo.io/mcp",
	}
	for k, want := range checks {
		if got := q.Get(k); got != want {
			t.Errorf("query %s = %q, want %q", k, got, want)
		}
	}
	if u.Host != "connect.givmo.io" || u.Path != "/oauth/authorize" {
		t.Errorf("endpoint not preserved: host=%s path=%s", u.Host, u.Path)
	}
}

func TestBuildAuthorizeURL_RequiresPKCE(t *testing.T) {
	if _, err := BuildAuthorizeURL(AuthorizeParams{AuthorizationEndpoint: "https://x/y"}); err == nil {
		t.Fatal("expected error when PKCE is missing")
	}
	if _, err := BuildAuthorizeURL(AuthorizeParams{PKCE: &PKCE{}}); err == nil {
		t.Fatal("expected error when authorization_endpoint is missing")
	}
}

func TestBuildExchangeForm_Fields(t *testing.T) {
	form := BuildExchangeForm(ExchangeParams{
		TokenEndpoint: "https://connect.givmo.io/oauth/token",
		ClientID:      "givmo-cli",
		ClientSecret:  "test-client-secret",
		Code:          "the-code",
		RedirectURI:   "http://127.0.0.1:9/callback",
		CodeVerifier:  "the-verifier",
		Resource:      "https://mcp.givmo.io/mcp",
	})
	checks := map[string]string{
		"grant_type":    "authorization_code",
		"code":          "the-code",
		"redirect_uri":  "http://127.0.0.1:9/callback",
		"client_id":     "givmo-cli",
		"code_verifier": "the-verifier",
		"resource":      "https://mcp.givmo.io/mcp",
	}
	for k, want := range checks {
		if got := form.Get(k); got != want {
			t.Errorf("form %s = %q, want %q", k, got, want)
		}
	}
	// Round-trip through url encoding to be sure it is a valid body.
	if !strings.Contains(form.Encode(), "grant_type=authorization_code") {
		t.Errorf("encoded form missing grant_type: %s", form.Encode())
	}
	if got := form.Get("client_secret"); got != "" {
		t.Errorf("client_secret = %q, want absent", got)
	}
	if strings.Contains(form.Encode(), "client_secret") {
		t.Errorf("encoded form contains client_secret: %s", form.Encode())
	}
}

func TestClientSecretBasicHeader_EncodeThenBase64(t *testing.T) {
	const secret = "s3cr3t+/=value"
	header := clientSecretBasicHeader("givmo-cli", secret)
	const wantHeader = "Basic Z2l2bW8tY2xpOnMzY3IzdCUyQiUyRiUzRHZhbHVl"
	if header != wantHeader {
		t.Errorf("header = %q, want %q", header, wantHeader)
	}
	if !strings.HasPrefix(header, "Basic ") {
		t.Fatalf("header = %q, want Basic prefix", header)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(header, "Basic "))
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	username, password, ok := strings.Cut(string(decoded), ":")
	if !ok {
		t.Fatalf("decoded header %q has no credential separator", decoded)
	}
	if want := url.QueryEscape("givmo-cli"); username != want {
		t.Errorf("encoded username = %q, want %q", username, want)
	}
	gotSecret, err := url.QueryUnescape(password)
	if err != nil {
		t.Fatalf("unescape password: %v", err)
	}
	if gotSecret != secret {
		t.Errorf("decoded secret = %q, want %q", gotSecret, secret)
	}
}

func TestNewLoopbackListener_RedirectAndOccupiedPort(t *testing.T) {
	ephemeral, err := NewLoopbackListener("/callback", "st", 0)
	if err != nil {
		t.Fatalf("NewLoopbackListener ephemeral: %v", err)
	}
	port := ephemeral.ln.Addr().(*net.TCPAddr).Port
	wantEphemeralRedirect := fmt.Sprintf("http://127.0.0.1:%d/callback", port)
	if ephemeral.RedirectURI != wantEphemeralRedirect {
		t.Errorf("ephemeral RedirectURI = %q, want %q", ephemeral.RedirectURI, wantEphemeralRedirect)
	}
	if err := ephemeral.ln.Close(); err != nil {
		t.Fatalf("close ephemeral listener: %v", err)
	}

	fixed, err := NewLoopbackListener("/callback", "st", port)
	if err != nil {
		t.Fatalf("NewLoopbackListener fixed: %v", err)
	}
	defer fixed.ln.Close()
	wantRedirect := fmt.Sprintf("http://127.0.0.1:%d/callback", port)
	if fixed.RedirectURI != wantRedirect {
		t.Errorf("fixed RedirectURI = %q, want %q", fixed.RedirectURI, wantRedirect)
	}

	_, err = NewLoopbackListener("/callback", "st", port)
	if err == nil {
		t.Fatal("expected second bind on occupied port to fail")
	}
	wantAddr := fmt.Sprintf("127.0.0.1:%d", port)
	if !strings.Contains(err.Error(), wantAddr) {
		t.Errorf("bind error %q does not name address %q", err, wantAddr)
	}
}

func TestExchangeCode_ClientSecretBasicWireShape(t *testing.T) {
	type capturedRequest struct {
		authorization string
		contentType   string
		form          url.Values
	}
	captured := make(chan capturedRequest, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		captured <- capturedRequest{
			authorization: r.Header.Get("Authorization"),
			contentType:   r.Header.Get("Content-Type"),
			form:          r.PostForm,
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"at","token_type":"Bearer","expires_in":3600,"scope":"givmo.donations.read"}`)
	}))
	defer server.Close()

	const secret = "s3cr3t+/=value"
	params := ExchangeParams{
		TokenEndpoint: server.URL,
		ClientID:      "gci-test-override",
		ClientSecret:  secret,
		AuthMethod:    AuthMethodClientSecretBasic,
		Code:          "the-code",
		RedirectURI:   "http://127.0.0.1:8765/callback",
		CodeVerifier:  "the-verifier",
	}
	if _, err := ExchangeCode(context.Background(), server.Client(), params); err != nil {
		t.Fatalf("ExchangeCode with secret: %v", err)
	}
	got := <-captured
	if !strings.HasPrefix(got.authorization, "Basic ") {
		t.Fatalf("Authorization = %q, want Basic prefix", got.authorization)
	}
	encoded := strings.TrimPrefix(got.authorization, "Basic ")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode Authorization header: %v", err)
	}
	username, password, ok := strings.Cut(string(decoded), ":")
	if !ok {
		t.Fatalf("decoded Authorization %q has no credential separator", decoded)
	}
	if want := url.QueryEscape("gci-test-override"); username != want {
		t.Errorf("encoded username = %q, want %q", username, want)
	}
	if want := url.QueryEscape(secret); password != want {
		t.Errorf("encoded password = %q, want %q", password, want)
	}
	if got.contentType != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q", got.contentType)
	}
	checks := map[string]string{
		"grant_type":    "authorization_code",
		"code":          params.Code,
		"code_verifier": params.CodeVerifier,
		"client_id":     params.ClientID,
	}
	for key, want := range checks {
		if value := got.form.Get(key); value != want {
			t.Errorf("form %s = %q, want %q", key, value, want)
		}
	}
	if got.form.Has("client_secret") {
		t.Errorf("form unexpectedly contains client_secret")
	}

	params.ClientSecret = ""
	params.AuthMethod = AuthMethodNone
	if _, err := ExchangeCode(context.Background(), server.Client(), params); err != nil {
		t.Fatalf("ExchangeCode without secret: %v", err)
	}
	if got := <-captured; got.authorization != "" {
		t.Errorf("Authorization without secret = %q, want empty", got.authorization)
	}
}

func TestCredentialFromToken(t *testing.T) {
	tr := &TokenResponse{
		AccessToken:  "at",
		RefreshToken: "rt",
		TokenType:    "Bearer",
		ExpiresIn:    3600,
		Scope:        "givmo.donations.read givmo.receipts.read",
	}
	c := CredentialFromToken("sandbox", tr)
	if c.Profile != "sandbox" || c.Type != CredTypeOAuth {
		t.Errorf("profile/type wrong: %+v", c)
	}
	if c.AccessToken != "at" || c.RefreshToken != "rt" {
		t.Errorf("tokens not carried")
	}
	if len(c.Scopes) != 2 {
		t.Errorf("scopes = %v", c.Scopes)
	}
	if c.ExpiresAt.IsZero() {
		t.Error("expires_at should be set from expires_in")
	}
	if c.Expired() {
		t.Error("a token 1h out should not be expired")
	}
}

// TestLoopbackListener_ChecksIss pins RFC 9207 at the real listener, the callback
// read off the wire: a response's iss must name the issuer the login started at;
// where the server advertises iss, a response without one is refused; and an iss
// that is present is checked even where the server does not advertise it. A
// refused response also gets the failure page.
func TestLoopbackListener_ChecksIss(t *testing.T) {
	const issuer = "https://mcp.example.test"
	for _, tc := range []struct {
		name        string
		advertised  bool
		iss         string // "" sends no iss
		wantRefused bool
	}{
		{name: "advertised, the issuer's own", advertised: true, iss: issuer},
		{name: "advertised, missing", advertised: true, wantRefused: true},
		{name: "advertised, another issuer's", advertised: true, iss: "https://elsewhere.example.test", wantRefused: true},
		{name: "not advertised, missing", advertised: false},
		{name: "not advertised, another issuer's", advertised: false, iss: "https://elsewhere.example.test", wantRefused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, err := NewLoopbackListener("/callback", "st", 0)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			l.ExpectIssuer(issuer, tc.advertised)
			l.Serve()

			q := url.Values{"code": {"c1"}, "state": {"st"}}
			if tc.iss != "" {
				q.Set("iss", tc.iss)
			}
			resp, err := http.Get(l.RedirectURI + "?" + q.Encode())
			if err != nil {
				t.Fatal(err)
			}
			page, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			res, err := l.Wait(ctx)
			var ie *IssuerError
			if tc.wantRefused {
				if !errors.As(err, &ie) {
					t.Fatalf("Wait = %+v, %v; want an IssuerError", res, err)
				}
				if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(page), "failed") {
					t.Errorf("a refused response got HTTP %d %q, want the failure page", resp.StatusCode, page)
				}
				return
			}
			if err != nil || res.Code != "c1" {
				t.Fatalf("Wait = %+v, %v; want the code", res, err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Errorf("an accepted response got HTTP %d", resp.StatusCode)
			}
		})
	}
}

// TestProtectedResourceMetadata_DescribesTheSameResourceOnly pins which
// spellings of a resource a protected-resource document may use (RFC 9728
// section 3.3): the ones the Givmo authorization server reads as the same
// resource, but for the few it forgives that no one writes (Describes), and
// never another scheme, host, port or path.
func TestProtectedResourceMetadata_DescribesTheSameResourceOnly(t *testing.T) {
	const resource = "https://mcp.givmo.io/mcp"
	for _, tc := range []struct {
		name, document, resource string // resource "" is the constant
		want                     bool
	}{
		{name: "the same identifier", document: resource, want: true},
		{name: "the scheme and the host in upper case", document: "HTTPS://MCP.Givmo.IO/mcp", want: true},
		{name: "the default port written out", document: "https://mcp.givmo.io:443/mcp", want: true},
		{name: "one trailing slash", document: "https://mcp.givmo.io/mcp/", want: true},
		{name: "the login's own spelling in upper case", document: resource, resource: "https://MCP.givmo.io/mcp", want: true},
		{name: "another scheme", document: "http://mcp.givmo.io/mcp"},
		{name: "another host", document: "https://mcp-dev.givmo.io/mcp"},
		{name: "another port", document: "https://mcp.givmo.io:8443/mcp"},
		{name: "http's default port is not https's", document: "https://mcp.givmo.io:80/mcp"},
		{name: "another path", document: "https://mcp.givmo.io/other/mcp"},
		{name: "a path in another case", document: "https://mcp.givmo.io/MCP"},
		{name: "the bare origin", document: "https://mcp.givmo.io"},
		{name: "two trailing slashes", document: "https://mcp.givmo.io/mcp//"},
		{name: "a query", document: "https://mcp.givmo.io/mcp?x=1"},
		{name: "a fragment", document: "https://mcp.givmo.io/mcp#x"},
		{name: "a user part", document: "https://someone@mcp.givmo.io/mcp"},
		{name: "white space around it", document: " " + resource},
		{name: "no resource at all", document: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.resource
			if want == "" {
				want = resource
			}
			doc := &ProtectedResourceMetadata{Resource: tc.document}
			if got := doc.Describes(want); got != tc.want {
				t.Errorf("a document naming %q describes %s: %v, want %v", tc.document, want, got, tc.want)
			}
		})
	}
}
