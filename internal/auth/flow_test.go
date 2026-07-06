package auth

import (
	"net/url"
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

func TestBuildExchangeForm_Fields(t *testing.T) {
	form := BuildExchangeForm(ExchangeParams{
		TokenEndpoint: "https://connect.givmo.io/oauth/token",
		ClientID:      "givmo-cli",
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
