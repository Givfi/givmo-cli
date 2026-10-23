package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/givfi/givmo-cli/internal/auth"
	"github.com/givfi/givmo-cli/internal/config"
	"github.com/givfi/givmo-cli/internal/output"
)

// TestLogout_EndsTheSignInOnGivmosSideWhereItCan pins logout's two halves. It
// asks Givmo to end the sign-in by revoking its refresh token, the one token
// whose revocation ends the whole sign-in (an access token's would end only that
// bearer); it always clears the credentials on this computer; and it says which
// happened, in its JSON and its text. Before the authorization server offers
// revocation to a client metadata document it answers invalid_client, the case
// of every environment today.
func TestLogout_EndsTheSignInOnGivmosSideWhereItCan(t *testing.T) {
	closed := func(t *testing.T) string {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		ln.Close()
		return "http://" + addr + "/revoke"
	}
	for _, tc := range []struct {
		name        string
		status      int
		body        string
		unreachable bool
		earlier     bool // a credential saved by an earlier givmo
		// production stores the sign-in under the production profile, where its
		// revocation endpoint, this test's loopback http server, is refused.
		production  bool
		wantRevoked bool
		wantReason  string
	}{
		{name: "Givmo ends it", status: http.StatusOK, body: `{}`, wantRevoked: true},
		{name: "Givmo does not offer it to the CLI yet", status: http.StatusUnauthorized,
			body:       `{"error":"invalid_client","error_description":"Client cannot authenticate."}`,
			wantReason: "this Givmo environment doesn't let the CLI end its sign-in yet"},
		{name: "Givmo cannot be reached", unreachable: true, wantReason: "Givmo could not be reached"},
		{name: "a sign-in saved by an earlier givmo", earlier: true,
			wantReason: "an earlier version of givmo saved it without what's needed to end it from here"},
		{name: "a sign-in whose revocation endpoint the production profile refuses", production: true,
			status: http.StatusOK, body: `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var requests []*http.Request
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				mu.Lock()
				requests = append(requests, r.Clone(context.Background()))
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			app := newTestAppCtx(t, srv.URL)
			t.Setenv("GIVMO_API_KEY", "")
			revocation := srv.URL + "/revoke"
			if tc.unreachable {
				revocation = closed(t)
			}
			profile, wantReason := config.ProfileSandbox, tc.wantReason
			if tc.production {
				profile = config.ProfileProduction
				app.Profile.Name = profile
				wantReason = fmt.Sprintf("its revocation endpoint, %q, isn't https, so the CLI doesn't use it", revocation)
			}
			signIn := func() {
				cred := &auth.Credential{
					Profile: profile, Type: auth.CredTypeOAuth, AccessToken: "test-access-0",
					RefreshToken: "test-refresh-0", ExpiresAt: time.Now().Add(time.Hour),
				}
				if !tc.earlier {
					cred.ClientID = srv.URL + auth.ClientDocumentPath
					cred.TokenEndpoint = srv.URL + "/token"
					cred.RevocationEndpoint = revocation
					cred.AuthMethod = auth.AuthMethodNone
					cred.GrantedAt = time.Now()
				}
				if tc.production {
					cred.TokenEndpoint = "https://mcp.example.test/connect/oauth/token"
				}
				if err := app.Store.Save(cred); err != nil {
					t.Fatal(err)
				}
			}

			for _, asJSON := range []bool{true, false} {
				signIn()
				var out bytes.Buffer
				app.Printer = output.NewPrinter(&out, io.Discard, asJSON)
				if err := runLogout(context.Background(), app); err != nil {
					t.Fatalf("logout: %v", err)
				}
				if _, err := app.Store.Load(profile); !errors.Is(err, auth.ErrNoCredential) {
					t.Errorf("the sign-in is still stored on this computer (Load: %v)", err)
				}
				if asJSON {
					var view map[string]any
					if err := json.Unmarshal(out.Bytes(), &view); err != nil {
						t.Fatalf("logout --json %q: %v", out.String(), err)
					}
					if view["logged_out"] != true || view["revoked"] != tc.wantRevoked {
						t.Errorf("logout --json = %v, want logged_out and revoked %v", view, tc.wantRevoked)
					}
					reason, _ := view["revoke_error"].(string)
					if !strings.HasPrefix(reason, wantReason) || (tc.wantRevoked && reason != "") {
						t.Errorf("revoke_error = %q, want %q", reason, wantReason)
					}
					continue
				}
				want := fmt.Sprintf("Cleared credentials for profile %q.\n", profile)
				if tc.wantRevoked {
					want += "Asked Givmo to end the sign-in; it accepted.\n"
				} else {
					want += "The sign-in is not ended on Givmo's side: " + wantReason
				}
				if got := out.String(); !strings.HasPrefix(got, want) {
					t.Errorf("logout said %q, want %q…", got, want)
				}
			}

			mu.Lock()
			defer mu.Unlock()
			if tc.earlier || tc.unreachable || tc.production {
				if len(requests) != 0 {
					t.Errorf("%d revocation requests reached the server, want none", len(requests))
				}
				return
			}
			if len(requests) != 2 {
				t.Fatalf("%d revocation requests, want one per logout", len(requests))
			}
			rq := requests[0]
			if got := rq.Header.Get("Authorization"); got != "" {
				t.Errorf("a public client's revocation sent Authorization %q", got)
			}
			want := url.Values{"token": {"test-refresh-0"}, "token_type_hint": {"refresh_token"},
				"client_id": {srv.URL + auth.ClientDocumentPath}}
			if rq.PostForm.Encode() != want.Encode() {
				t.Errorf("revocation form = %s, want %s", rq.PostForm.Encode(), want.Encode())
			}
		})
	}
}
