package cmd

import (
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/givfi/givmo-cli/internal/auth"
	"github.com/givfi/givmo-cli/internal/config"
)

// rotatingTokens is a token endpoint that rotates refresh tokens the way the
// Givmo authorization server does: test-refresh-N is good once and answers
// test-access-(N+1) with test-refresh-(N+1); a spent or unknown one answers
// invalid_grant.
type rotatingTokens struct {
	mu        sync.Mutex
	next      int
	live      map[string]bool
	refreshes int
}

func newRotatingTokens() *rotatingTokens {
	return &rotatingTokens{live: map[string]bool{"test-refresh-0": true}}
}

func (rt *rotatingTokens) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.refreshes++
	w.Header().Set("Content-Type", "application/json")
	presented := r.PostForm.Get("refresh_token")
	if !rt.live[presented] {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"invalid_grant"}`)
		return
	}
	delete(rt.live, presented)
	rt.next++
	rt.live[fmt.Sprintf("test-refresh-%d", rt.next)] = true
	fmt.Fprintf(w, `{"access_token":"test-access-%d","token_type":"Bearer","refresh_token":"test-refresh-%d","expires_in":1800}`, rt.next, rt.next)
}

func (rt *rotatingTokens) count() int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.refreshes
}

// storeSignIn saves a renewable public-client sign-in for the sandbox profile in
// the file store under the current GIVMO_HOME, its access token test-access-0
// expiring in expiresIn.
func storeSignIn(t *testing.T, issuer string, expiresIn time.Duration) {
	t.Helper()
	store, err := auth.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	cred := &auth.Credential{
		Profile:            config.ProfileSandbox,
		Type:               auth.CredTypeOAuth,
		AccessToken:        "test-access-0",
		RefreshToken:       "test-refresh-0",
		TokenType:          "Bearer",
		ExpiresAt:          time.Now().Add(expiresIn),
		ClientID:           issuer + auth.ClientDocumentPath,
		Issuer:             issuer,
		TokenEndpoint:      issuer + "/token",
		RevocationEndpoint: issuer + "/revoke",
		AuthMethod:         auth.AuthMethodNone,
		GrantedAt:          time.Now(),
	}
	if err := store.Save(cred); err != nil {
		t.Fatal(err)
	}
}
