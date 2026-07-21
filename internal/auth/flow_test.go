package auth

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
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

func TestBuildAuthorizeURL_ClientIDOverride(t *testing.T) {
	t.Setenv("GIVMO_CLIENT_ID", "gci-test-override")
	got, err := BuildAuthorizeURL(AuthorizeParams{
		AuthorizationEndpoint: "https://connect.givmo.io/oauth/authorize",
		ClientID:              ResolveClientID(),
		PKCE:                  &PKCE{Challenge: "chal", Method: "S256", State: "state"},
	})
	if err != nil {
		t.Fatalf("BuildAuthorizeURL: %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("result is not a URL: %v", err)
	}
	if got := u.Query().Get("client_id"); got != "gci-test-override" {
		t.Errorf("client_id = %q, want %q", got, "gci-test-override")
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
	if matched := regexp.MustCompile(`^http://127\.0\.0\.1:\d+/callback$`).MatchString(ephemeral.RedirectURI); !matched {
		t.Errorf("ephemeral RedirectURI = %q", ephemeral.RedirectURI)
	}
	port := ephemeral.ln.Addr().(*net.TCPAddr).Port
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
