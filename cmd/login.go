package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/givfi/givmo-cli/internal/auth"
	"github.com/givfi/givmo-cli/internal/output"
	"github.com/spf13/cobra"
)

var (
	loginNoBrowser bool
	loginTimeout   time.Duration
	loginPort      int
)

func newLoginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate to Givmo (consumer OAuth via PKCE loopback)",
		Long: `Authenticate the CLI to Givmo.

Interactive (default): signs in as a public native client, named by Givmo's
client document for the profile's environment
(<issuer>/connect/oauth/clients/givmo-cli), with no client secret. It runs the
OAuth 2.0 authorization-code + PKCE (S256) flow: the CLI binds a loopback
listener on 127.0.0.1 using an OS-assigned ephemeral port by default (RFC 8252
loopback redirect), opens your browser to the authorize URL, and receives the
redirect at http://127.0.0.1:<port>/callback for whichever port it bound. The
Givmo authorization server accepts any loopback port. Pass --port <N> to pin it
for an authorization server that lacks loopback port flexibility or a
registered redirect that fixes the port. Where the authorization server
advertises it, the callback's iss must name that server (RFC 9207).

The CLI then stays signed in by renewing its access token, one renewal at a time
across every givmo process on this computer, for up to 90 days from consent (14
days unused ends it sooner). Where an environment doesn't accept the CLI's
sign-in yet, login stops before opening a browser, exit 6.

GIVMO_CLIENT_ID and GIVMO_CLIENT_SECRET together sign in as your own registered
confidential client. The client secret is read from GIVMO_CLIENT_SECRET first,
then from the OS keychain or 0600 file store. It is never accepted as a flag. A
secret sourced from the environment is stored after the first successful login.
GIVMO_CLIENT_ID alone, as an https URL, names your own client metadata document.

The resulting consumer token is scoped to:
  givmo.donations.read  givmo.receipts.read
  givmo.giving_summary.read  givmo.donation_intents.create

Non-interactive (CI): pass --api-key or set GIVMO_API_KEY; no browser is used
and the key is stored for the active profile.

Tokens are stored in the OS keychain when available, else a 0600 file under
~/.givmo. Tokens are never logged and never appear in --json output.`,
		RunE: func(c *cobra.Command, _ []string) error {
			if c.Flags().Changed("port") && (loginPort < 1 || loginPort > 65535) {
				return output.New(output.ExitValidation,
					fmt.Sprintf("login callback port %d is outside valid range 1-65535", loginPort),
					"Pass `--port <N>` with a value from 1 through 65535 whose callback redirect URI is registered for your Givmo OAuth client.")
			}
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			// Non-interactive path: store the API key for the profile.
			if key := apiKeyFromEnvOrFlag(); key != "" {
				return loginWithAPIKey(app, key)
			}
			return loginInteractive(c.Context(), app)
		},
	}
	cmd.Flags().BoolVar(&loginNoBrowser, "no-browser", false, "print the authorize URL instead of opening a browser")
	cmd.Flags().DurationVar(&loginTimeout, "timeout", 3*time.Minute, "how long to wait for the browser callback")
	cmd.Flags().IntVar(&loginPort, "port", 0, "pin the loopback callback port for an authorization server that lacks RFC 8252 loopback port flexibility or a registered redirect that fixes the port (default: OS-assigned ephemeral port)")
	return cmd
}

// loginWithAPIKey stores a CI API key credential.
func loginWithAPIKey(app *appCtx, key string) error {
	cred := &auth.Credential{
		Profile:     app.Profile.Name,
		Type:        auth.CredTypeAPIKey,
		AccessToken: key,
		TokenType:   "Bearer",
	}
	ctx, cancel := baseContext()
	defer cancel()
	if err := app.tokens().Save(ctx, cred); err != nil {
		return output.New(output.ExitGeneric, "could not store API key: "+err.Error(), "")
	}
	return app.Printer.Result(
		map[string]any{"profile": app.Profile.Name, "type": auth.CredTypeAPIKey, "backend": app.Store.Backend()},
		func(w io.Writer) {
			fmt.Fprintf(w, "Stored API key for profile %q (backend: %s).\n", app.Profile.Name, app.Store.Backend())
		},
	)
}

// loginInteractive runs the PKCE loopback flow.
func loginInteractive(parent context.Context, app *appCtx) error {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, loginTimeout)
	defer cancel()

	// The client the login signs in as: Givmo's client document unless the
	// environment names another. Malformed choices stop here, before any request.
	env := clientEnv{id: strings.TrimSpace(os.Getenv("GIVMO_CLIENT_ID")), secret: os.Getenv("GIVMO_CLIENT_SECRET")}
	if err := env.check(); err != nil {
		return err
	}
	if env.registered() && env.secret == "" {
		stored, err := app.Store.LoadClientSecret(app.Profile.Name)
		if err != nil {
			return output.New(output.ExitGeneric, "could not resolve OAuth client secret: "+err.Error(),
				"Check access to the OS keychain or 0600 Givmo credential store, then re-run `givmo login`.")
		}
		env.stored = stored
	}

	app.prodBanner("logging in")

	// 1) Discover the authorization server via the MCP host's protected-resource
	//    metadata, then the AS metadata. A client metadata document needs the
	//    metadata (it says whether the server accepts one); only a registered
	//    client falls back to the profile's configured auth base with the
	//    conventional Connect paths when discovery fails.
	disc := auth.NewDiscoverer()
	authBase := app.Profile.Endpoints.AuthBase
	resource := app.Profile.Endpoints.APIBase + "/mcp"

	// allowLoopback: a discovered http://127.0.0.1|localhost AS is tolerated only
	// off production (sandbox/dev), never in production.
	asMeta, err := discoverAuthServer(ctx, disc, app.Profile.Endpoints.APIBase, authBase, !app.Profile.IsProduction(), !env.registered())
	if err != nil {
		return err
	}
	// Where the server does not accept the client, stop before a listener is
	// bound or a browser opened: its consent page would only refuse.
	client, err := chooseClient(env, asMeta)
	if err != nil {
		return err
	}

	// 2) Generate PKCE.
	pk, err := auth.NewPKCE()
	if err != nil {
		return output.New(output.ExitGeneric, "could not generate PKCE parameters: "+err.Error(), "")
	}

	// 3) Start the loopback listener.
	listener, err := auth.NewLoopbackListener("/callback", pk.State, loginPort)
	if err != nil {
		remediation := "Ensure localhost binding is permitted (no restrictive firewall on 127.0.0.1)."
		if loginPort != 0 {
			remediation = fmt.Sprintf("Free port %d if another process is using it, or pass `--port <N>` where http://127.0.0.1:<N>/callback is registered for your Givmo OAuth client.", loginPort)
		}
		return output.New(output.ExitGeneric, "could not start loopback listener: "+err.Error(), remediation)
	}
	defer listener.Close()
	listener.ExpectIssuer(asMeta.Issuer, asMeta.AuthorizationResponseIssParameterSupported)
	listener.Serve()

	// 4) Build the authorize URL.
	authorizeURL, err := auth.BuildAuthorizeURL(auth.AuthorizeParams{
		AuthorizationEndpoint: asMeta.AuthorizationEndpoint,
		ClientID:              client.id,
		RedirectURI:           listener.RedirectURI,
		Scopes:                auth.DefaultScopes(),
		PKCE:                  pk,
		Resource:              resource,
	})
	if err != nil {
		return output.New(output.ExitGeneric, "could not build authorize URL: "+err.Error(), "")
	}

	// 5) Open (or print) the URL.
	if loginNoBrowser {
		fmt.Fprintf(app.Printer.Err, "Open this URL to authorize:\n\n  %s\n\n", authorizeURL)
	} else {
		fmt.Fprintf(app.Printer.Err, "Opening your browser to authorize... (if it does not open, visit)\n\n  %s\n\n", authorizeURL)
		if oerr := openBrowser(authorizeURL); oerr != nil {
			fmt.Fprintf(app.Printer.Err, "(could not auto-open browser: %v — open the URL above manually)\n", oerr)
		}
	}

	// 6) Wait for the callback.
	res, err := listener.Wait(ctx)
	if err != nil {
		var ie *auth.IssuerError
		if errors.As(err, &ie) {
			return output.New(output.ExitAuth, ie.Error(),
				"Nothing was exchanged. Re-run `givmo login`; if this repeats, check the active profile (`givmo config view`).")
		}
		return output.New(output.ExitAuth, "login was not completed: "+err.Error(),
			"Re-run `givmo login`; if the browser did not open, use --no-browser and open the URL manually.")
	}

	// 7) Exchange the code for tokens.
	tok, err := auth.ExchangeCode(ctx, app.httpClient(), auth.ExchangeParams{
		TokenEndpoint: asMeta.TokenEndpoint,
		ClientID:      client.id,
		ClientSecret:  client.secret,
		AuthMethod:    client.authMethod,
		Code:          res.Code,
		RedirectURI:   listener.RedirectURI,
		CodeVerifier:  pk.Verifier,
		Resource:      resource,
	})
	if err != nil {
		return output.New(output.ExitAuth, "token exchange failed: "+err.Error(),
			"Confirm the active profile's auth_base is correct (`givmo config view`) and the Connect endpoint is live.")
	}
	if client.secretFromEnv {
		if err := app.Store.SaveClientSecret(app.Profile.Name, client.secret); err != nil {
			fmt.Fprintln(app.Printer.Err, "warning: OAuth client secret could not be stored for future logins")
		}
	}

	// 8) Persist the credential, with what renewing and ending it need.
	cred := auth.CredentialFromToken(app.Profile.Name, tok)
	cred.ClientID = client.id
	cred.Issuer = asMeta.Issuer
	cred.TokenEndpoint = asMeta.TokenEndpoint
	cred.RevocationEndpoint = asMeta.RevocationEndpoint
	cred.AuthMethod = client.authMethod
	cred.GrantedAt = time.Now()
	if err := app.tokens().Save(ctx, cred); err != nil {
		return output.New(output.ExitGeneric, "authenticated, but could not store the token: "+err.Error(), "")
	}

	return app.Printer.Result(
		map[string]any{
			"profile": app.Profile.Name,
			"type":    auth.CredTypeOAuth,
			"scopes":  cred.Scopes,
			"backend": app.Store.Backend(),
		},
		func(w io.Writer) {
			fmt.Fprintf(w, "Logged in to profile %q.\n", app.Profile.Name)
			fmt.Fprintf(w, "  scopes:  %s\n", strings.Join(cred.Scopes, " "))
			fmt.Fprintf(w, "  storage: %s\n", app.Store.Backend())
		},
	)
}

// discoverAuthServer resolves the AS metadata. With requireMetadata unset (a
// registered client) it falls back to the conventional Connect endpoints when
// discovery is impossible (metadata unreachable); with it set (a client metadata
// document, which only the metadata says the server accepts) it stops instead,
// exit 6.
//
// DEFENSE-IN-DEPTH: a discovered authorization_servers[0] / authorize / token /
// revocation endpoint is otherwise adopted verbatim. We reject any non-https
// discovered AS URL so a compromised or spoofed protected-resource document
// cannot point the login flow (and the PKCE code) at a plaintext or
// attacker-controlled origin. A loopback http URL (127.0.0.1 / localhost / ::1)
// is permitted ONLY when allowLoopback is true (sandbox/dev profiles), never in
// production. And a metadata document must name, as its issuer, the base it was
// fetched from (RFC 8414 section 3.3).
func discoverAuthServer(ctx context.Context, disc auth.Discoverer, apiBase, authBase string, allowLoopback, requireMetadata bool) (*auth.AuthServerMetadata, error) {
	// Try the protected-resource doc to learn the AS, then its metadata.
	if pr, err := disc.ProtectedResource(ctx, apiBase); err == nil && len(pr.AuthorizationServers) > 0 {
		cand := strings.TrimRight(pr.AuthorizationServers[0], "/")
		if serr := validateAuthServerURL(cand, allowLoopback); serr != nil {
			return nil, serr
		}
		authBase = cand
	}
	md, err := disc.AuthServer(ctx, authBase)
	if err == nil && (md.AuthorizationEndpoint == "" || md.TokenEndpoint == "") {
		err = errors.New("it names no authorization or token endpoint")
	}
	if err == nil {
		if strings.TrimRight(md.Issuer, "/") != strings.TrimRight(authBase, "/") {
			return nil, output.New(output.ExitValidation,
				fmt.Sprintf("the authorization server's metadata names its issuer as %s, not %s, the address it was fetched from", md.Issuer, authBase),
				"The metadata is malformed or spoofed; verify the profile's api_base and auth_base (`givmo config view`) and the Connect deployment.")
		}
		// Validate the discovered endpoints too — the metadata doc could carry
		// endpoints on a different (plaintext/attacker) origin than authBase.
		endpoints := []string{md.AuthorizationEndpoint, md.TokenEndpoint}
		if md.RevocationEndpoint != "" {
			endpoints = append(endpoints, md.RevocationEndpoint)
		}
		for _, ep := range endpoints {
			if serr := validateAuthServerURL(ep, allowLoopback); serr != nil {
				return nil, serr
			}
		}
		return md, nil
	}
	if requireMetadata {
		return nil, output.New(output.ExitNetwork,
			"could not read the Givmo authorization server's metadata: "+err.Error(),
			"Check connectivity and the active profile's api_base (`givmo config view`), then re-run `givmo login`.")
	}
	// Fallback to conventional Connect endpoints under the (already-validated or
	// profile-supplied) auth base.
	base := strings.TrimRight(authBase, "/")
	if serr := validateAuthServerURL(base, allowLoopback); serr != nil {
		return nil, serr
	}
	return fallbackAuthServerMetadata(base), nil
}

// clientEnv is what the environment says about the OAuth client: GIVMO_CLIENT_ID,
// GIVMO_CLIENT_SECRET, and the profile's stored client secret, which is read only
// for a registered client id.
type clientEnv struct {
	id, secret, stored string
}

// registered reports whether GIVMO_CLIENT_ID names a registered client rather
// than a client metadata document (an https URL, as the server tells them apart).
func (e clientEnv) registered() bool {
	return e.id != "" && !strings.HasPrefix(e.id, "https://")
}

// check refuses the combinations no request can fix, exit 2.
func (e clientEnv) check() error {
	switch {
	case e.id == "" && e.secret != "":
		return output.New(output.ExitUsage, "GIVMO_CLIENT_SECRET is set without GIVMO_CLIENT_ID",
			"Unset GIVMO_CLIENT_SECRET to sign in with Givmo's client document, or set GIVMO_CLIENT_ID to your registered client's id.")
	case e.id != "" && !e.registered() && e.secret != "":
		return output.New(output.ExitUsage, "GIVMO_CLIENT_ID names a client metadata document, which takes no secret",
			"Unset GIVMO_CLIENT_SECRET, or set GIVMO_CLIENT_ID to a registered client's id.")
	}
	return nil
}

// clientChoice is the OAuth client a login signs in as.
type clientChoice struct {
	id         string
	authMethod string
	// secret is a registered client's; secretFromEnv says it came from
	// GIVMO_CLIENT_SECRET, so a successful login stores it.
	secret        string
	secretFromEnv bool
}

// chooseClient picks the client a login signs in as (design §Q5):
//   - by default, Givmo's client document at the server's issuer, a public client
//     with no secret;
//   - GIVMO_CLIENT_ID as an https URL, that client metadata document, likewise;
//   - any other GIVMO_CLIENT_ID, a registered confidential client, with
//     GIVMO_CLIENT_SECRET or else the profile's stored secret.
//
// A client metadata document needs a server that accepts one; where md says it
// does not, the login stops with exit 6 before any listener or browser, since
// the consent page would only refuse. Pure: login calls it once discovery is done.
func chooseClient(e clientEnv, md *auth.AuthServerMetadata) (clientChoice, error) {
	if err := e.check(); err != nil {
		return clientChoice{}, err
	}
	if e.registered() {
		switch {
		case e.secret != "":
			return clientChoice{id: e.id, authMethod: auth.AuthMethodClientSecretBasic, secret: e.secret, secretFromEnv: true}, nil
		case e.stored != "":
			return clientChoice{id: e.id, authMethod: auth.AuthMethodClientSecretBasic, secret: e.stored}, nil
		}
		return clientChoice{}, output.New(output.ExitUsage, "a registered client needs GIVMO_CLIENT_SECRET",
			"Set GIVMO_CLIENT_SECRET to the secret of the client GIVMO_CLIENT_ID names, or unset GIVMO_CLIENT_ID to sign in with Givmo's client document.")
	}
	if !md.AcceptsClientDocuments() {
		return clientChoice{}, output.New(output.ExitNetwork, "This Givmo environment doesn't accept the CLI's sign-in yet.",
			"Try again later. With a registered client, set GIVMO_CLIENT_ID and GIVMO_CLIENT_SECRET.")
	}
	id := e.id
	if id == "" {
		id = auth.ClientDocumentURL(md.Issuer)
	}
	return clientChoice{id: id, authMethod: auth.AuthMethodNone}, nil
}

// fallbackAuthServerMetadata builds the conventional Givmo Connect AS metadata for
// the discovery-down path: the real endpoints live under the /connect mount
// (/connect/oauth/authorize, /connect/oauth/token, /connect/oauth/revoke). Pure →
// unit-tested.
func fallbackAuthServerMetadata(base string) *auth.AuthServerMetadata {
	base = strings.TrimRight(base, "/")
	return &auth.AuthServerMetadata{
		Issuer:                base,
		AuthorizationEndpoint: base + "/connect/oauth/authorize",
		TokenEndpoint:         base + "/connect/oauth/token",
		RevocationEndpoint:    base + "/connect/oauth/revoke",
	}
}

// validateAuthServerURL enforces https on an authorization-server URL, allowing
// http only for loopback hosts and only when allowLoopback is set. Pure →
// unit-tested.
func validateAuthServerURL(raw string, allowLoopback bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return output.New(output.ExitValidation,
			"discovered authorization server URL is not a valid URL: "+raw,
			"The auth-server metadata is malformed; verify the profile's auth_base and the Connect deployment.")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && allowLoopback && isLoopbackHost(u.Hostname()) {
		return nil
	}
	return output.New(output.ExitValidation,
		"refusing a non-https authorization server URL: "+raw,
		"The discovered auth server must use https (loopback http is allowed only on the sandbox/dev profile). "+
			"This guards against a spoofed protected-resource document redirecting login to a plaintext or attacker origin.")
}

// isLoopbackHost reports whether host is a loopback address/name.
func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
