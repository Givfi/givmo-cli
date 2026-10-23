package cmd

import (
	"bufio"
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
	if md.RevocationEndpoint != "https://api-dev.givmo.io/connect/oauth/revoke" {
		t.Errorf("revocation endpoint = %q", md.RevocationEndpoint)
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

// recordingDiscoverer serves pr as the protected-resource document (none when
// nil), and metadata naming as its issuer whatever base it is read from,
// recording each base.
type recordingDiscoverer struct {
	pr   *auth.ProtectedResourceMetadata
	read []string
}

func (d *recordingDiscoverer) ProtectedResource(context.Context, string) (*auth.ProtectedResourceMetadata, error) {
	if d.pr == nil {
		return nil, errors.New("discovery down")
	}
	return d.pr, nil
}

func (d *recordingDiscoverer) AuthServer(_ context.Context, base string) (*auth.AuthServerMetadata, error) {
	d.read = append(d.read, base)
	return &auth.AuthServerMetadata{Issuer: base, AuthorizationEndpoint: "https://mcp-dev.givmo.io/connect/oauth/authorize",
		TokenEndpoint: "https://mcp-dev.givmo.io/connect/oauth/token"}, nil
}

// TestDiscoverAuthServer_MetadataRules pins discovery's three rules: a registered
// client falls back to the conventional Connect paths when the metadata cannot be
// read; a client metadata document cannot, since only the metadata says whether
// the server accepts one (exit 6); and on either path a metadata document whose
// issuer is not the base it was fetched from is refused (RFC 8414 section 3.3).
// Whichever way the authorization server was found, its address passes the rules
// before its metadata is read, since a sign-in stores it as its issuer. And a
// protected-resource document is used only when it names the resource the login
// asks for (RFC 9728 section 3.3); one that names another stops the login before
// any metadata is read.
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
		authBase        string // the profile's; "" is https://api-dev.givmo.io
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
		{name: "a configured auth base that isn't https is refused before its metadata is read",
			disc: &recordingDiscoverer{}, authBase: "http://auth.example.test", requireMetadata: true,
			wantExit: output.ExitValidation},
		{name: "a document for another resource stops the login before any metadata is read",
			disc: &recordingDiscoverer{pr: &auth.ProtectedResourceMetadata{Resource: "https://mcp.givmo.io/mcp",
				AuthorizationServers: []string{"https://mcp.givmo.io"}}},
			requireMetadata: true, wantExit: output.ExitValidation},
		{name: "a document naming no resource stops it too",
			disc:            &recordingDiscoverer{pr: &auth.ProtectedResourceMetadata{AuthorizationServers: []string{issuer}}},
			requireMetadata: true, wantExit: output.ExitValidation},
		{name: "a document spelling the resource another way is used",
			disc: &recordingDiscoverer{pr: &auth.ProtectedResourceMetadata{Resource: "HTTPS://MCP-DEV.GIVMO.IO:443/mcp/",
				AuthorizationServers: []string{issuer}}},
			requireMetadata: true, wantAuthorize: issuer + "/connect/oauth/authorize"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authBase := tc.authBase
			if authBase == "" {
				authBase = "https://api-dev.givmo.io"
			}
			md, err := discoverAuthServer(context.Background(), tc.disc,
				issuer+"/mcp", authBase, true, tc.requireMetadata)
			if d, ok := tc.disc.(*recordingDiscoverer); ok && tc.wantExit != 0 && len(d.read) != 0 {
				t.Errorf("metadata was read from %q", d.read)
			}
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
				prof.Endpoints.APIBase+"/mcp", prof.Endpoints.AuthBase, !prof.IsProduction(), false)
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
		{"a host in fullwidth letters rejected", "https://ｍｃｐ.givmo.io/connect/oauth/token", false, false},
		{"the same host percent-encoded rejected", "https://%EF%BD%8D%EF%BD%83%EF%BD%90.givmo.io/connect/oauth/token", false, false},
		{"an ideographic full stop rejected, even loopback-tolerant", "https://mcp%E3%80%82givmo.io/connect/oauth/token", true, false},
		{"an internationalized name in its xn-- form ok", "https://xn--mcp-dk1d.givmo.io/connect/oauth/token", false, true},
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
	// A host spelled outside ASCII is shown escaped, so a look-alike can't hide in
	// the refusal.
	err := validateAuthServerURL("https://ｍｃｐ.givmo.io/connect/oauth/token", false)
	if msg := output.AsError(err).Message; !strings.Contains(msg, `\uff4d\uff43\uff50.givmo.io`) {
		t.Errorf("refusal %q does not show the host escaped", msg)
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
	// The resource's own document (RFC 9728 section 3.1) names it. The root one
	// names another resource, unlike Givmo's: a login that read it would stop, so
	// it pins where the login reads.
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"resource": g.srv.URL + "/mcp", "authorization_servers": []string{g.srv.URL}})
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"resource": g.srv.URL + "/elsewhere/mcp", "authorization_servers": []string{g.srv.URL}})
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
		if r.PostForm.Get("grant_type") == "refresh_token" {
			io.WriteString(w, `{"access_token":"test-access-2","token_type":"Bearer","refresh_token":"test-refresh-2","expires_in":1800}`)
			return
		}
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

// signInThroughTheBrowser runs loginInteractive for app against g, playing the
// browser: it takes the authorize URL from the login's stderr and sends the
// authorization server's redirect to the CLI's listener. It returns the authorize
// URL's query (nil when the login stopped before showing one), the stderr lines
// it read, and the login's error.
func signInThroughTheBrowser(t *testing.T, g *fakeGivmo, app *appCtx) (url.Values, []string, error) {
	t.Helper()
	stderrR, stderrW := io.Pipe()
	app.Printer.Err = stderrW
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

	var seen []string
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return nil, seen, <-done
			}
			seen = append(seen, line)
			u, err := url.Parse(strings.TrimSpace(line))
			if err != nil || u.Query().Get("redirect_uri") == "" {
				continue
			}
			q := u.Query()
			callback := q.Get("redirect_uri") + "?" + url.Values{"code": {"test-code"}, "state": {q.Get("state")}, "iss": {g.srv.URL}}.Encode()
			resp, err := http.Get(callback)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			for line := range lines {
				seen = append(seen, line)
			}
			return q, seen, <-done
		case <-time.After(5 * time.Second):
			t.Fatal("no authorize URL on stderr")
		}
	}
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
			app := &appCtx{
				Profile: config.Profile{Name: config.ProfileSandbox, Endpoints: config.Endpoints{APIBase: g.srv.URL, AuthBase: g.srv.URL}},
				Config:  &config.Config{},
				Printer: output.NewPrinter(io.Discard, io.Discard, false),
				Store:   store,
			}
			q, lines, err := signInThroughTheBrowser(t, g, app)

			if !tc.cimd {
				if oe := output.AsError(err); err == nil || oe.Code != output.ExitNetwork ||
					oe.Message != "This Givmo environment doesn't accept the CLI's sign-in yet." {
					t.Fatalf("login = %v, want exit 6 saying the environment doesn't accept the sign-in", err)
				}
				for _, line := range lines {
					if strings.Contains(line, "authorize") || strings.Contains(line, "http") {
						t.Errorf("stderr showed %q; nothing may be shown before the refusal", line)
					}
				}
				if q != nil {
					t.Errorf("an authorize URL was shown: %v", q)
				}
				if n := len(g.tokenRequests()); n != 0 {
					t.Errorf("%d token requests, want none", n)
				}
				return
			}
			if err != nil {
				t.Fatalf("login: %v", err)
			}
			if q == nil {
				t.Fatalf("stderr showed no authorize URL: %q", lines)
			}
			wantClient := g.srv.URL + "/connect/oauth/clients/givmo-cli"
			if got := q.Get("client_id"); got != wantClient {
				t.Errorf("authorize client_id = %q, want Givmo's document %q", got, wantClient)
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

// TestLoginInteractive_EndsThePreviousSignIn: a login on a profile that already
// holds a sign-in stores the new one first, then asks Givmo to end the previous
// one as logout does (its refresh token, RFC 7009), and says what Givmo answered.
// The new sign-in is kept whatever the answer, and the login succeeds. Givmo ends
// a grant together with every live access token the person holds for the
// client, the new sign-in's among them, so after replacing a sign-in the new
// access token is renewed before its first use. A login with an API key replaces
// a sign-in the same way.
func TestLoginInteractive_EndsThePreviousSignIn(t *testing.T) {
	previousSignIn := func(revocation string) *auth.Credential {
		return &auth.Credential{
			Profile: config.ProfileSandbox, Type: auth.CredTypeOAuth, AccessToken: "test-access-old",
			RefreshToken: "test-refresh-old", TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour),
			ClientID: "https://mcp.example.test/connect/oauth/clients/givmo-cli", Issuer: "https://mcp.example.test",
			TokenEndpoint: "https://mcp.example.test/connect/oauth/token", RevocationEndpoint: revocation,
			AuthMethod: auth.AuthMethodNone, GrantedAt: time.Now().Add(-24 * time.Hour),
		}
	}
	const notOffered = "this Givmo environment doesn't let the CLI end its sign-in yet"
	for _, tc := range []struct {
		name string
		// previous is the credential stored before the login, given the
		// revocation server's endpoint and a closed one; nil stores none.
		previous     func(revoke, closed string) *auth.Credential
		status       int
		body         string
		apiKey       bool // log in with an API key instead of the browser
		human        bool // read the text instead of the JSON
		wantRevokes  int
		wantRevoked  bool
		wantError    string // previous_sign_in's revoke_error, a prefix; "" with wantRevoked
		noPrevious   bool   // previous_sign_in absent
		wantLine     string // the text's last line
		wantRenewed  bool   // the first use after the login renews the new sign-in
		wantInUseNow string // the access token that first use presents
	}{
		{name: "nothing stored", noPrevious: true, wantInUseNow: "test-access-1"},
		{name: "a previous sign-in Givmo ends",
			previous: func(revoke, _ string) *auth.Credential { return previousSignIn(revoke) },
			status:   http.StatusOK, body: `{}`, wantRevokes: 1, wantRevoked: true,
			wantRenewed: true, wantInUseNow: "test-access-2"},
		{name: "a previous sign-in Givmo ends, in text", human: true,
			previous: func(revoke, _ string) *auth.Credential { return previousSignIn(revoke) },
			status:   http.StatusOK, body: `{}`, wantRevokes: 1,
			wantLine:    "Asked Givmo to end the previous sign-in; it accepted.",
			wantRenewed: true, wantInUseNow: "test-access-2"},
		{name: "Givmo refuses to end it",
			previous: func(revoke, _ string) *auth.Credential { return previousSignIn(revoke) },
			status:   http.StatusUnauthorized, body: `{"error":"invalid_client"}`, wantRevokes: 1,
			wantError: notOffered, wantRenewed: true, wantInUseNow: "test-access-2"},
		{name: "Givmo refuses to end it, in text", human: true,
			previous: func(revoke, _ string) *auth.Credential { return previousSignIn(revoke) },
			status:   http.StatusUnauthorized, body: `{"error":"invalid_client"}`, wantRevokes: 1,
			wantLine:    "The previous sign-in is not ended on Givmo's side: " + notOffered + ". To end it there, use Revoke access in Connected Apps.",
			wantRenewed: true, wantInUseNow: "test-access-2"},
		{name: "Givmo can't be reached",
			previous:  func(_, closed string) *auth.Credential { return previousSignIn(closed) },
			wantError: "Givmo could not be reached (", wantRenewed: true, wantInUseNow: "test-access-2"},
		{name: "a previous sign-in an earlier givmo saved",
			previous: func(string, string) *auth.Credential {
				c := previousSignIn("")
				c.ClientID, c.TokenEndpoint, c.AuthMethod = "", "", ""
				return c
			},
			wantError:   "an earlier version of givmo saved it without what's needed to end it from here",
			wantRenewed: true, wantInUseNow: "test-access-2"},
		{name: "a previous sign-in carrying an address the rules refuse",
			previous:    func(string, string) *auth.Credential { return previousSignIn("http://revoke.example.test/revoke") },
			wantError:   `its revocation endpoint, "http://revoke.example.test/revoke", isn't https, so the CLI doesn't use it`,
			wantRenewed: true, wantInUseNow: "test-access-2"},
		{name: "a previous API key",
			previous: func(string, string) *auth.Credential {
				return &auth.Credential{Profile: config.ProfileSandbox, Type: auth.CredTypeAPIKey, AccessToken: "test-api-key-old", TokenType: "Bearer"}
			},
			noPrevious: true, wantInUseNow: "test-access-1"},
		{name: "an API-key login replacing a sign-in", apiKey: true,
			previous: func(revoke, _ string) *auth.Credential { return previousSignIn(revoke) },
			status:   http.StatusOK, body: `{}`, wantRevokes: 1, wantRevoked: true, wantInUseNow: "test-api-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newFakeGivmo(t, true)
			t.Setenv("GIVMO_HOME", t.TempDir())
			t.Setenv("GIVMO_TOKEN_BACKEND", "file")
			t.Setenv("GIVMO_CLIENT_ID", "")
			t.Setenv("GIVMO_CLIENT_SECRET", "")
			t.Setenv("GIVMO_API_KEY", "")
			store, err := auth.NewStore()
			if err != nil {
				t.Fatal(err)
			}

			// The revocation endpoint records each request, and which access token
			// the store held when it arrived.
			type revocation struct {
				form          url.Values
				authorization string
				stored        string
			}
			var mu sync.Mutex
			var revokes []revocation
			revSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				held := ""
				if c, err := store.Load(config.ProfileSandbox); err == nil {
					held = c.AccessToken
				}
				mu.Lock()
				revokes = append(revokes, revocation{form: r.PostForm, authorization: r.Header.Get("Authorization"), stored: held})
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer revSrv.Close()
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			closed := "http://" + ln.Addr().String() + "/revoke"
			ln.Close()
			var previous *auth.Credential
			if tc.previous != nil {
				previous = tc.previous(revSrv.URL+"/revoke", closed)
				if err := store.Save(previous); err != nil {
					t.Fatal(err)
				}
			}

			var out bytes.Buffer
			app := &appCtx{
				Profile: config.Profile{Name: config.ProfileSandbox, Endpoints: config.Endpoints{APIBase: g.srv.URL, AuthBase: g.srv.URL}},
				Config:  &config.Config{},
				Printer: output.NewPrinter(&out, io.Discard, !tc.human),
				Store:   store,
			}
			newAccess := "test-access-1"
			if tc.apiKey {
				newAccess = "test-api-key"
				err = loginWithAPIKey(app, newAccess)
			} else {
				_, _, err = signInThroughTheBrowser(t, g, app)
			}
			if err != nil {
				t.Fatalf("login = %v; the new sign-in must not depend on ending the previous one", err)
			}

			mu.Lock()
			got := append([]revocation(nil), revokes...)
			mu.Unlock()
			if len(got) != tc.wantRevokes {
				t.Fatalf("%d revocation requests, want %d", len(got), tc.wantRevokes)
			}
			for _, rv := range got {
				want := url.Values{"token": {"test-refresh-old"}, "token_type_hint": {"refresh_token"}, "client_id": {previous.ClientID}}
				if rv.form.Encode() != want.Encode() {
					t.Errorf("revocation form = %s, want the previous sign-in's refresh token: %s", rv.form.Encode(), want.Encode())
				}
				if rv.authorization != "" {
					t.Errorf("a public client's revocation sent Authorization %q", rv.authorization)
				}
				if rv.stored != newAccess {
					t.Errorf("when the revocation arrived the store held %q, want the new credential, stored first", rv.stored)
				}
			}

			if tc.human {
				if text := strings.TrimRight(out.String(), "\n"); !strings.HasSuffix(text, "\n"+tc.wantLine) {
					t.Errorf("login said %q, want it to end %q", text, tc.wantLine)
				}
			} else {
				var view map[string]any
				if err := json.Unmarshal(out.Bytes(), &view); err != nil {
					t.Fatalf("login --json %q: %v", out.String(), err)
				}
				prev, has := view["previous_sign_in"].(map[string]any)
				switch {
				case tc.noPrevious:
					if _, present := view["previous_sign_in"]; present {
						t.Errorf("previous_sign_in = %v, want none", view["previous_sign_in"])
					}
				case !has:
					t.Errorf("login --json = %v, want previous_sign_in", view)
				default:
					reason, _ := prev["revoke_error"].(string)
					if prev["revoked"] != tc.wantRevoked || !strings.HasPrefix(reason, tc.wantError) || (tc.wantRevoked && reason != "") {
						t.Errorf("previous_sign_in = %v, want revoked %v and revoke_error %q", prev, tc.wantRevoked, tc.wantError)
					}
				}
			}

			stored, err := store.Load(config.ProfileSandbox)
			if err != nil || stored.AccessToken != newAccess {
				t.Fatalf("stored = %+v, %v; want the new credential", stored, err)
			}
			if !tc.wantRenewed && stored.Expired() {
				t.Errorf("the new credential was stored expired; only a sign-in that replaced another is stored due")
			}
			inUse, err := app.credential(context.Background())
			if err != nil {
				t.Fatalf("the first use after the login: %v", err)
			}
			refreshes := 0
			for _, rq := range g.tokenRequests() {
				if rq.PostForm.Get("grant_type") == "refresh_token" {
					refreshes++
				}
			}
			if wantRefreshes := map[bool]int{true: 1}[tc.wantRenewed]; refreshes != wantRefreshes || inUse.AccessToken != tc.wantInUseNow {
				t.Errorf("the first use presents %q after %d renewals, want %q after %d", inUse.AccessToken, refreshes, tc.wantInUseNow, wantRefreshes)
			}
		})
	}
}
