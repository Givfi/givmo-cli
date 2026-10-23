package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/givfi/givmo-cli/internal/output"
)

// TestWhoami_RenewsFirst: whoami's "expired" must describe the token the next
// command will use. With the stored access token about to expire, whoami renews it
// and reports it unexpired. Reporting a live sign-in as expired sends an agent to
// `givmo login`, which opens a browser for nothing.
func TestWhoami_RenewsFirst(t *testing.T) {
	tokens := newRotatingTokens()
	srv := httptest.NewServer(tokens)
	defer srv.Close()
	app := newTestAppCtx(t, srv.URL)
	t.Setenv("GIVMO_API_KEY", "")
	storeSignIn(t, srv.URL, 10*time.Second)
	// storeSignIn points the sign-in's token endpoint at <issuer>/token; this
	// server answers every path.
	var out bytes.Buffer
	app.Printer = output.NewPrinter(&out, io.Discard, true)

	if err := runWhoami(context.Background(), app); err != nil {
		t.Fatalf("whoami: %v", err)
	}
	var view struct {
		Expired   bool   `json:"expired"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(out.Bytes(), &view); err != nil {
		t.Fatalf("whoami output %q: %v", out.String(), err)
	}
	if view.Expired {
		t.Errorf("whoami reported a live sign-in as expired")
	}
	if at, err := time.Parse(time.RFC3339, view.ExpiresAt); err != nil || time.Until(at) < 20*time.Minute {
		t.Errorf("expires_at = %q, want the renewed token's", view.ExpiresAt)
	}
	if n := tokens.count(); n != 1 {
		t.Errorf("%d renewals, want 1", n)
	}
}
