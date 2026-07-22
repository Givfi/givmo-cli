package cmd

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
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

Interactive (default): runs the OAuth 2.0 authorization-code + PKCE (S256) flow
as a confidential client against Givmo Connect. The CLI binds a loopback
listener on 127.0.0.1 using an OS-assigned ephemeral port by default (RFC 8252
loopback redirect), opens your browser to the authorize URL, and receives the
redirect at http://127.0.0.1:<port>/callback for whichever port it bound. The
Givmo authorization server accepts any loopback port. Pass --port <N> to pin it
for an authorization server that lacks loopback port flexibility or a
registered redirect that fixes the port.

GIVMO_CLIENT_ID overrides the built-in first-party client id (givmo-cli). Set it
together with GIVMO_CLIENT_SECRET to authenticate as your own registered
confidential client. The client secret is read from GIVMO_CLIENT_SECRET first,
then from the OS keychain or 0600 file store. It is never accepted as a flag. A
secret sourced from the environment is stored after the first successful login.
The Givmo authorization server currently supports confidential clients only;
dynamic and public client registration are future capabilities.

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
	if err := app.Store.Save(cred); err != nil {
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

	clientID := auth.ResolveClientID()
	clientSecret, secretSource, err := auth.ResolveClientSecret(app.Store, app.Profile.Name)
	if err != nil {
		return output.New(output.ExitGeneric, "could not resolve OAuth client secret: "+err.Error(),
			"Check access to the OS keychain or 0600 Givmo credential store, then re-run `givmo login`.")
	}
	if clientSecret == "" {
		return output.New(output.ExitAuth,
			"the Givmo authorization server requires a confidential client secret and none was found",
			"Set GIVMO_CLIENT_SECRET to your registered client's secret and re-run `givmo login`; dynamic and public client registration are not yet supported by the Givmo authorization server.")
	}

	app.prodBanner("logging in")

	// 1) Discover the authorization server via the MCP host's protected-resource
	//    metadata, then the AS metadata. If discovery fails (metadata unreachable),
	//    fall back to the profile's configured auth base with the conventional
	//    Connect paths.
	disc := auth.NewDiscoverer()
	authBase := app.Profile.Endpoints.AuthBase
	resource := app.Profile.Endpoints.APIBase + "/mcp"

	// allowLoopback: a discovered http://127.0.0.1|localhost AS is tolerated only
	// off production (sandbox/dev), never in production.
	asMeta, err := discoverAuthServer(ctx, disc, app.Profile.Endpoints.APIBase, authBase, !app.Profile.IsProduction())
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
	listener.Serve()

	// 4) Build the authorize URL.
	authorizeURL, err := auth.BuildAuthorizeURL(auth.AuthorizeParams{
		AuthorizationEndpoint: asMeta.AuthorizationEndpoint,
		ClientID:              clientID,
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
		return output.New(output.ExitAuth, "login was not completed: "+err.Error(),
			"Re-run `givmo login`; if the browser did not open, use --no-browser and open the URL manually.")
	}

	// 7) Exchange the code for tokens.
	tok, err := auth.ExchangeCode(ctx, app.httpClient(), auth.ExchangeParams{
		TokenEndpoint: asMeta.TokenEndpoint,
		ClientID:      clientID,
		ClientSecret:  clientSecret,
		Code:          res.Code,
		RedirectURI:   listener.RedirectURI,
		CodeVerifier:  pk.Verifier,
		Resource:      resource,
	})
	if err != nil {
		return output.New(output.ExitAuth, "token exchange failed: "+err.Error(),
			"Confirm the active profile's auth_base is correct (`givmo config view`) and the Connect endpoint is live.")
	}
	if secretSource == "env" {
		if err := app.Store.SaveClientSecret(app.Profile.Name, clientSecret); err != nil {
			fmt.Fprintln(app.Printer.Err, "warning: OAuth client secret could not be stored for future logins")
		}
	}

	// 8) Persist the credential.
	cred := auth.CredentialFromToken(app.Profile.Name, tok)
	if err := app.Store.Save(cred); err != nil {
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

// discoverAuthServer resolves the AS metadata, falling back to the conventional
// Connect endpoints when discovery is impossible (metadata unreachable).
//
// DEFENSE-IN-DEPTH: a discovered authorization_servers[0] / authorize / token
// endpoint is otherwise adopted verbatim. We reject any non-https discovered AS
// URL so a compromised or spoofed protected-resource document cannot point the
// login flow (and the PKCE code) at a plaintext or attacker-controlled origin.
// A loopback http URL (127.0.0.1 / localhost / ::1) is permitted ONLY when
// allowLoopback is true (sandbox/dev profiles), never in production.
func discoverAuthServer(ctx context.Context, disc auth.Discoverer, apiBase, authBase string, allowLoopback bool) (*auth.AuthServerMetadata, error) {
	// Try the protected-resource doc to learn the AS, then its metadata.
	if pr, err := disc.ProtectedResource(ctx, apiBase); err == nil && len(pr.AuthorizationServers) > 0 {
		cand := strings.TrimRight(pr.AuthorizationServers[0], "/")
		if serr := validateAuthServerURL(cand, allowLoopback); serr != nil {
			return nil, serr
		}
		authBase = cand
	}
	if md, err := disc.AuthServer(ctx, authBase); err == nil && md.AuthorizationEndpoint != "" && md.TokenEndpoint != "" {
		// Validate the discovered endpoints too — the metadata doc could carry
		// endpoints on a different (plaintext/attacker) origin than authBase.
		for _, ep := range []string{md.AuthorizationEndpoint, md.TokenEndpoint} {
			if serr := validateAuthServerURL(ep, allowLoopback); serr != nil {
				return nil, serr
			}
		}
		return md, nil
	}
	// Fallback to conventional Connect endpoints under the (already-validated or
	// profile-supplied) auth base.
	base := strings.TrimRight(authBase, "/")
	if serr := validateAuthServerURL(base, allowLoopback); serr != nil {
		return nil, serr
	}
	return fallbackAuthServerMetadata(base), nil
}

// fallbackAuthServerMetadata builds the conventional Givmo Connect AS metadata for
// the discovery-down path: the real endpoints live under the /connect mount
// (/connect/oauth/authorize + /connect/oauth/token). Pure → unit-tested.
func fallbackAuthServerMetadata(base string) *auth.AuthServerMetadata {
	base = strings.TrimRight(base, "/")
	return &auth.AuthServerMetadata{
		Issuer:                base,
		AuthorizationEndpoint: base + "/connect/oauth/authorize",
		TokenEndpoint:         base + "/connect/oauth/token",
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
