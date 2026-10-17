package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/givfi/givmo-cli/internal/auth"
	"github.com/givfi/givmo-cli/internal/config"
	"github.com/givfi/givmo-cli/internal/output"
)

// failingDiscoverer fails both discovery calls, forcing the conventional-endpoint
// fallback path in discoverAuthServer.
type failingDiscoverer struct{}

func (failingDiscoverer) ProtectedResource(context.Context, string) (*auth.ProtectedResourceMetadata, error) {
	return nil, errors.New("discovery down")
}

func TestLoginCommand_RejectsInvalidPort(t *testing.T) {
	for _, port := range []string{"0", "65536"} {
		t.Run(port, func(t *testing.T) {
			cmd := newLoginCmd()
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			cmd.SetArgs([]string{"--port", port})
			err := cmd.Execute()
			if err == nil {
				t.Fatal("expected invalid port error")
			}
			if got := output.AsError(err).Code; got != output.ExitValidation {
				t.Errorf("exit code = %d, want %d", got, output.ExitValidation)
			}
			if !strings.Contains(err.Error(), port) {
				t.Errorf("error %q does not name bad port %s", err, port)
			}
		})
	}
}

func TestLoginCommand_DefaultPortIsEphemeral(t *testing.T) {
	cmd := newLoginCmd()
	portFlag := cmd.Flags().Lookup("port")
	if portFlag == nil {
		t.Fatal("port flag not found")
	}
	if got := portFlag.DefValue; got != "0" {
		t.Errorf("port default = %q, want %q", got, "0")
	}
}

func (failingDiscoverer) AuthServer(context.Context, string) (*auth.AuthServerMetadata, error) {
	return nil, errors.New("discovery down")
}

func TestFallbackAuthServerMetadata_ConnectPaths(t *testing.T) {
	md := fallbackAuthServerMetadata("https://api-dev.givmo.io/")
	if md.AuthorizationEndpoint != "https://api-dev.givmo.io/connect/oauth/authorize" {
		t.Errorf("authorize endpoint = %q", md.AuthorizationEndpoint)
	}
	if md.TokenEndpoint != "https://api-dev.givmo.io/connect/oauth/token" {
		t.Errorf("token endpoint = %q", md.TokenEndpoint)
	}
	if md.Issuer != "https://api-dev.givmo.io" {
		t.Errorf("issuer = %q", md.Issuer)
	}
}

// stubDiscoverer serves fixed metadata documents; a nil one fails to fetch.
type stubDiscoverer struct {
	pr *auth.ProtectedResourceMetadata
	as *auth.AuthServerMetadata
}

func (d stubDiscoverer) ProtectedResource(context.Context, string) (*auth.ProtectedResourceMetadata, error) {
	if d.pr == nil {
		return nil, errors.New("discovery down")
	}
	return d.pr, nil
}

func (d stubDiscoverer) AuthServer(context.Context, string) (*auth.AuthServerMetadata, error) {
	if d.as == nil {
		return nil, errors.New("discovery down")
	}
	return d.as, nil
}

// TestDiscoverAuthServer_MetadataRules pins discovery's three rules: a registered
// client falls back to the conventional Connect paths when the metadata cannot be
// read; a client metadata document cannot, since only the metadata says whether
// the server accepts one (exit 6); and on either path a metadata document whose
// issuer is not the base it was fetched from is refused (RFC 8414 section 3.3).
func TestDiscoverAuthServer_MetadataRules(t *testing.T) {
	const issuer = "https://mcp-dev.givmo.io"
	served := func(iss string) stubDiscoverer {
		return stubDiscoverer{
			pr: &auth.ProtectedResourceMetadata{Resource: issuer + "/mcp", AuthorizationServers: []string{issuer}},
			as: &auth.AuthServerMetadata{Issuer: iss, AuthorizationEndpoint: issuer + "/connect/oauth/authorize",
				TokenEndpoint: issuer + "/connect/oauth/token", RevocationEndpoint: issuer + "/connect/oauth/revoke"},
		}
	}
	for _, tc := range []struct {
		name            string
		disc            auth.Discoverer
		requireMetadata bool
		wantExit        int
		wantAuthorize   string
	}{
		{name: "a registered client falls back when discovery is down", disc: failingDiscoverer{},
			wantAuthorize: "https://api-dev.givmo.io/connect/oauth/authorize"},
		{name: "a client document needs the metadata", disc: failingDiscoverer{}, requireMetadata: true,
			wantExit: output.ExitNetwork},
		{name: "the issuer's own metadata is adopted", disc: served(issuer), requireMetadata: true,
			wantAuthorize: issuer + "/connect/oauth/authorize"},
		{name: "metadata naming another issuer is refused", disc: served("https://elsewhere.example.test"), requireMetadata: true,
			wantExit: output.ExitValidation},
		{name: "a registered client refuses it too", disc: served("https://elsewhere.example.test"),
			wantExit: output.ExitValidation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			md, err := discoverAuthServer(context.Background(), tc.disc,
				"https://mcp-dev.givmo.io", "https://api-dev.givmo.io", true, tc.requireMetadata)
			if tc.wantExit != 0 {
				if got := output.AsError(err); err == nil || got.Code != tc.wantExit {
					t.Fatalf("discoverAuthServer = %+v, %v; want exit %d", md, err, tc.wantExit)
				}
				return
			}
			if err != nil {
				t.Fatalf("discoverAuthServer: %v", err)
			}
			if md.AuthorizationEndpoint != tc.wantAuthorize {
				t.Errorf("authorize endpoint = %q, want %q", md.AuthorizationEndpoint, tc.wantAuthorize)
			}
		})
	}
}

// TestDiscoverAuthServer_FallbackIsTheIssuer: with discovery down, each
// profile's login falls back to its authorization server's issuer, the MCP
// host, whose token endpoint is where the code is exchanged.
func TestDiscoverAuthServer_FallbackIsTheIssuer(t *testing.T) {
	t.Setenv("GIVMO_PROFILE", "")
	t.Setenv("GIVMO_API_BASE", "")
	t.Setenv("GIVMO_AUTH_BASE", "")
	for _, tc := range []struct{ profile, issuer string }{
		{config.ProfileProduction, "https://mcp.givmo.io"},
		{config.ProfileSandbox, "https://mcp-dev.givmo.io"},
	} {
		t.Run(tc.profile, func(t *testing.T) {
			prof, err := (&config.Config{}).Resolve(tc.profile)
			if err != nil {
				t.Fatal(err)
			}
			md, err := discoverAuthServer(context.Background(), failingDiscoverer{},
				prof.Endpoints.APIBase, prof.Endpoints.AuthBase, !prof.IsProduction(), false)
			if err != nil {
				t.Fatalf("discoverAuthServer: %v", err)
			}
			if md.Issuer != tc.issuer {
				t.Errorf("fallback issuer = %q, want %s", md.Issuer, tc.issuer)
			}
			if md.TokenEndpoint != tc.issuer+"/connect/oauth/token" {
				t.Errorf("fallback token endpoint = %q", md.TokenEndpoint)
			}
		})
	}
}

func TestValidateAuthServerURL_SchemeGuard(t *testing.T) {
	cases := []struct {
		name          string
		url           string
		allowLoopback bool
		wantOK        bool
	}{
		{"https always ok", "https://connect.givmo.io/oauth/authorize", false, true},
		{"https ok on sandbox too", "https://connect-dev.givmo.io", true, true},
		{"plain http rejected in prod", "http://connect.givmo.io/oauth/token", false, false},
		{"http loopback 127 allowed off-prod", "http://127.0.0.1:8080/authorize", true, true},
		{"http localhost allowed off-prod", "http://localhost:8080/authorize", true, true},
		{"http ::1 allowed off-prod", "http://[::1]:8080/authorize", true, true},
		{"http loopback REJECTED in prod", "http://127.0.0.1:8080/authorize", false, false},
		{"http non-loopback rejected even off-prod", "http://169.254.169.254/latest", true, false},
		{"http public host rejected even off-prod", "http://evil.example/authorize", true, false},
		{"garbage rejected", "::::not a url", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAuthServerURL(tc.url, tc.allowLoopback)
			if tc.wantOK && err != nil {
				t.Errorf("expected OK, got error: %v", err)
			}
			if !tc.wantOK {
				if err == nil {
					t.Errorf("expected rejection for %q (allowLoopback=%v)", tc.url, tc.allowLoopback)
				} else if output.AsError(err).Code != output.ExitValidation {
					t.Errorf("rejection should be ExitValidation, got %d", output.AsError(err).Code)
				}
			}
		})
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for _, h := range []string{"localhost", "127.0.0.1", "::1", "[::1]", "127.5.5.5"} {
		if !isLoopbackHost(h) {
			t.Errorf("%q should be loopback", h)
		}
	}
	for _, h := range []string{"169.254.169.254", "evil.example", "8.8.8.8", "givmo.io"} {
		if isLoopbackHost(h) {
			t.Errorf("%q should NOT be loopback", h)
		}
	}
}

// TestChooseClient pins which OAuth client a login signs in as: Givmo's client
// document by default, a public client that never carries a secret, even one left
// in the store by an earlier registered login; an https GIVMO_CLIENT_ID, that
// document; any other id, a registered confidential client. A client document
// needs a server that accepts one, or the login stops with exit 6 (its consent
// page would only refuse); the combinations no server can fix stop with exit 2.
func TestChooseClient(t *testing.T) {
	const issuer = "https://mcp.givmo.io"
	allMethods := []string{"client_secret_basic", "client_secret_post", "private_key_jwt", "none"}
	cimdOn := &auth.AuthServerMetadata{Issuer: issuer, ClientIDMetadataDocumentSupported: true, TokenEndpointAuthMethodsSupported: allMethods}
	cimdOff := &auth.AuthServerMetadata{Issuer: issuer, TokenEndpointAuthMethodsSupported: allMethods[:3]}
	noNone := &auth.AuthServerMetadata{Issuer: issuer, ClientIDMetadataDocumentSupported: true, TokenEndpointAuthMethodsSupported: allMethods[:3]}
	const document = "https://tools.example.test/oauth/client.json"
	for _, tc := range []struct {
		name     string
		env      clientEnv
		md       *auth.AuthServerMetadata
		want     clientChoice
		wantExit int
	}{
		{name: "by default, Givmo's document, never with a stored secret", env: clientEnv{stored: "test-stored-secret"}, md: cimdOn,
			want: clientChoice{id: issuer + "/connect/oauth/clients/givmo-cli", authMethod: auth.AuthMethodNone}},
		{name: "by default, where CIMD is off", md: cimdOff, wantExit: output.ExitNetwork},
		{name: "by default, where a public client cannot authenticate", md: noNone, wantExit: output.ExitNetwork},
		{name: "a secret without an id", env: clientEnv{secret: "test-env-secret"}, md: cimdOn, wantExit: output.ExitUsage},
		{name: "an https id names that document", env: clientEnv{id: document}, md: cimdOn,
			want: clientChoice{id: document, authMethod: auth.AuthMethodNone}},
		{name: "an https id where CIMD is off", env: clientEnv{id: document}, md: cimdOff, wantExit: output.ExitNetwork},
		{name: "an https id with a secret", env: clientEnv{id: document, secret: "test-env-secret"}, md: cimdOn, wantExit: output.ExitUsage},
		{name: "a registered id and secret, even where CIMD is off", env: clientEnv{id: "gci-test", secret: "test-env-secret", stored: "test-stored-secret"}, md: cimdOff,
			want: clientChoice{id: "gci-test", authMethod: auth.AuthMethodClientSecretBasic, secret: "test-env-secret", secretFromEnv: true}},
		{name: "a registered id with its stored secret", env: clientEnv{id: "gci-test", stored: "test-stored-secret"}, md: cimdOn,
			want: clientChoice{id: "gci-test", authMethod: auth.AuthMethodClientSecretBasic, secret: "test-stored-secret"}},
		{name: "a registered id with no secret", env: clientEnv{id: "gci-test"}, md: cimdOn, wantExit: output.ExitUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := chooseClient(tc.env, tc.md)
			if tc.wantExit != 0 {
				if oe := output.AsError(err); err == nil || oe.Code != tc.wantExit {
					t.Fatalf("chooseClient = %+v, %v; want exit %d", got, err, tc.wantExit)
				}
				return
			}
			if err != nil {
				t.Fatalf("chooseClient: %v", err)
			}
			if got != tc.want {
				t.Errorf("chooseClient = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// fakeGivmo is an httptest stand-in for a Givmo environment's discovery documents
// and token endpoint, recording every token request.
type fakeGivmo struct {
	srv     *httptest.Server
	cimd    bool
	mu      sync.Mutex
	tokenRq []*http.Request
}

func newFakeGivmo(t *testing.T, cimd bool) *fakeGivmo {
	t.Helper()
	g := &fakeGivmo{cimd: cimd}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"resource": g.srv.URL + "/mcp", "authorization_servers": []string{g.srv.URL}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		md := map[string]any{
			"issuer":                                g.srv.URL,
			"authorization_endpoint":                g.srv.URL + "/authorize",
			"token_endpoint":                        g.srv.URL + "/token",
			"revocation_endpoint":                   g.srv.URL + "/revoke",
			"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post", "private_key_jwt"},
			"authorization_response_iss_parameter_supported": g.cimd,
		}
		if g.cimd {
			md["client_id_metadata_document_supported"] = true
			md["token_endpoint_auth_methods_supported"] = []string{"client_secret_basic", "client_secret_post", "private_key_jwt", "none"}
		}
		json.NewEncoder(w).Encode(md)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		g.mu.Lock()
		g.tokenRq = append(g.tokenRq, r.Clone(context.Background()))
		g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"test-access-1","token_type":"Bearer","refresh_token":"test-refresh-1","expires_in":1800,"scope":"givmo.donations.read givmo.receipts.read givmo.giving_summary.read givmo.donation_intents.create"}`)
	})
	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGivmo) tokenRequests() []*http.Request {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]*http.Request(nil), g.tokenRq...)
}

// TestLoginInteractive_SignsInWithGivmosClientDocument runs the real login against
// fakeGivmo, the browser played by a GET of the redirect the CLI printed. A client
// secret is left in the store by an earlier registered login, the case where
// today's CLI pairs it with its built-in id in a Basic header, which the server
// refuses from a public client after the person has consented. Where the
// environment does not accept client documents, the login must stop before it
// shows anyone an authorize URL.
func TestLoginInteractive_SignsInWithGivmosClientDocument(t *testing.T) {
	for _, tc := range []struct {
		name string
		cimd bool
	}{
		{name: "where CIMD is on, a public sign-in with no secret", cimd: true},
		{name: "where CIMD is off, stopped before any authorize URL", cimd: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newFakeGivmo(t, tc.cimd)
			home := t.TempDir()
			t.Setenv("GIVMO_HOME", home)
			t.Setenv("GIVMO_TOKEN_BACKEND", "file")
			t.Setenv("GIVMO_CLIENT_ID", "")
			t.Setenv("GIVMO_CLIENT_SECRET", "")
			t.Setenv("GIVMO_API_KEY", "")
			store, err := auth.NewStore()
			if err != nil {
				t.Fatal(err)
			}
			if err := store.SaveClientSecret(config.ProfileSandbox, "test-stored-secret"); err != nil {
				t.Fatal(err)
			}
			stderrR, stderrW := io.Pipe()
			app := &appCtx{
				Profile: config.Profile{Name: config.ProfileSandbox, Endpoints: config.Endpoints{APIBase: g.srv.URL, AuthBase: g.srv.URL}},
				Config:  &config.Config{},
				Printer: output.NewPrinter(io.Discard, stderrW, false),
				Store:   store,
			}
			prevNoBrowser, prevTimeout, prevPort := loginNoBrowser, loginTimeout, loginPort
			loginNoBrowser, loginTimeout, loginPort = true, 5*time.Second, 0
			defer func() { loginNoBrowser, loginTimeout, loginPort = prevNoBrowser, prevTimeout, prevPort }()

			lines := make(chan string, 32)
			go func() {
				sc := bufio.NewScanner(stderrR)
				for sc.Scan() {
					lines <- sc.Text()
				}
				close(lines)
			}()
			done := make(chan error, 1)
			go func() { done <- loginInteractive(context.Background(), app); stderrW.Close() }()

			if !tc.cimd {
				err := <-done
				if oe := output.AsError(err); err == nil || oe.Code != output.ExitNetwork ||
					oe.Message != "This Givmo environment doesn't accept the CLI's sign-in yet." {
					t.Fatalf("login = %v, want exit 6 saying the environment doesn't accept the sign-in", err)
				}
				for line := range lines {
					if strings.Contains(line, "authorize") || strings.Contains(line, "http") {
						t.Errorf("stderr showed %q; nothing may be shown before the refusal", line)
					}
				}
				if n := len(g.tokenRequests()); n != 0 {
					t.Errorf("%d token requests, want none", n)
				}
				return
			}

			// Play the browser: take the authorize URL from stderr and send the
			// authorization server's redirect to the CLI's listener.
			var authorize *url.URL
			for authorize == nil {
				select {
				case line, ok := <-lines:
					if !ok {
						t.Fatalf("stderr closed without an authorize URL; login = %v", <-done)
					}
					if u, err := url.Parse(strings.TrimSpace(line)); err == nil && u.Query().Get("redirect_uri") != "" {
						authorize = u
					}
				case <-time.After(5 * time.Second):
					t.Fatal("no authorize URL on stderr")
				}
			}
			q := authorize.Query()
			wantClient := g.srv.URL + "/connect/oauth/clients/givmo-cli"
			if got := q.Get("client_id"); got != wantClient {
				t.Errorf("authorize client_id = %q, want Givmo's document %q", got, wantClient)
			}
			callback := q.Get("redirect_uri") + "?" + url.Values{"code": {"test-code"}, "state": {q.Get("state")}, "iss": {g.srv.URL}}.Encode()
			resp, err := http.Get(callback)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if err := <-done; err != nil {
				t.Fatalf("login: %v", err)
			}

			reqs := g.tokenRequests()
			if len(reqs) != 1 {
				t.Fatalf("%d token requests, want one", len(reqs))
			}
			rq := reqs[0]
			if got := rq.Header.Get("Authorization"); got != "" {
				t.Errorf("the public exchange sent Authorization %q; a public client sends no secret", got)
			}
			if rq.PostForm.Has("client_secret") {
				t.Errorf("the public exchange sent a client_secret")
			}
			for key, want := range map[string]string{
				"grant_type": "authorization_code", "code": "test-code", "client_id": wantClient,
				"redirect_uri": q.Get("redirect_uri"), "resource": g.srv.URL + "/mcp",
			} {
				if got := rq.PostForm.Get(key); got != want {
					t.Errorf("exchange %s = %q, want %q", key, got, want)
				}
			}
			if rq.PostForm.Get("code_verifier") == "" {
				t.Errorf("exchange carried no code_verifier")
			}

			// What renewing and ending the sign-in will need is stored with it.
			raw, err := os.ReadFile(filepath.Join(home, "credentials", "sandbox.json"))
			if err != nil {
				t.Fatal(err)
			}
			var stored map[string]any
			if err := json.Unmarshal(raw, &stored); err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]string{
				"client_id": wantClient, "issuer": g.srv.URL, "token_endpoint": g.srv.URL + "/token",
				"revocation_endpoint": g.srv.URL + "/revoke", "auth_method": "none", "refresh_token": "test-refresh-1",
			} {
				if got := fmt.Sprint(stored[key]); got != want {
					t.Errorf("stored %s = %q, want %q", key, got, want)
				}
			}
			granted, err := time.Parse(time.RFC3339Nano, fmt.Sprint(stored["granted_at"]))
			if err != nil || time.Since(granted) > time.Minute {
				t.Errorf("stored granted_at = %v (%v), want the consent time", stored["granted_at"], err)
			}
		})
	}
}
