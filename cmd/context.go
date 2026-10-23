package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/givfi/givmo-cli/internal/auth"
	"github.com/givfi/givmo-cli/internal/client"
	"github.com/givfi/givmo-cli/internal/config"
	"github.com/givfi/givmo-cli/internal/mcpbridge"
	"github.com/givfi/givmo-cli/internal/output"
)

// appCtx carries the resolved runtime dependencies for a command invocation. It
// is built lazily in the root PersistentPreRunE so every subcommand shares one
// profile resolution, one printer, one token store.
type appCtx struct {
	Profile config.Profile
	Config  *config.Config
	Printer *output.Printer
	Store   *auth.Store
	// toolCallTimeout, when non-zero, replaces mcpbridge.ToolCallTimeout for this
	// context's tool calls (tests use a short one).
	toolCallTimeout time.Duration
	// tokenSource renews the profile's stored sign-in; built on first use.
	tokenSource *auth.TokenSource
}

// tokens returns the profile's token source: every command and the bridge take
// the stored credential from it, so renewals run one at a time across processes.
// The sign-in's ending date goes to stderr.
func (a *appCtx) tokens() *auth.TokenSource {
	if a.tokenSource == nil {
		a.tokenSource = auth.NewTokenSource(a.Store, a.Profile.Name, a.httpClient(), a.Printer.Err)
	}
	return a.tokenSource
}

// resolveAppCtx builds the appCtx from global flags + config + env.
func resolveAppCtx() (*appCtx, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, output.New(output.ExitGeneric, "could not load config: "+err.Error(),
			"Inspect or remove ~/.givmo/config.json if it is corrupt.")
	}
	prof, err := cfg.Resolve(flagProfile)
	if err != nil {
		return nil, output.New(output.ExitUsage, err.Error(),
			"Use `givmo config use-profile sandbox|production` to select a valid profile.")
	}
	store, err := auth.NewStore()
	if err != nil {
		return nil, output.New(output.ExitGeneric, "could not open token store: "+err.Error(), "")
	}
	return &appCtx{
		Profile: prof,
		Config:  cfg,
		Printer: output.NewPrinter(os.Stdout, os.Stderr, flagJSON),
		Store:   store,
	}, nil
}

// credential returns the active profile's credential, honoring the
// non-interactive GIVMO_API_KEY / --api-key path, else the stored one from the
// token source, renewed first when its access token is about to expire. Returns
// nil (no error) when no credential is present — callers that require auth check
// Authenticated(). An error is the token source's, in the CLI's envelope.
func (a *appCtx) credential(ctx context.Context) (*auth.Credential, error) {
	if key := apiKeyFromEnvOrFlag(); key != "" {
		return &auth.Credential{
			Profile:     a.Profile.Name,
			Type:        auth.CredTypeAPIKey,
			AccessToken: key,
			TokenType:   "Bearer",
		}, nil
	}
	c, err := a.tokens().Credential(ctx)
	if err != nil {
		return nil, renewalError(err)
	}
	return c, nil
}

// apiClient builds an authenticated REST client for the active profile.
func (a *appCtx) apiClient(ctx context.Context) (*client.Client, error) {
	cred, err := a.credential(ctx)
	if err != nil {
		return nil, err
	}
	return client.New(client.Options{
		BaseURL:    a.Profile.Endpoints.APIBase,
		Credential: cred,
		Timeout:    30 * time.Second,
	}), nil
}

// mcpAuthorizer authorizes the CLI's requests to the remote MCP with the active
// profile's credential, taken from the token source for each request and renewed
// once after a 401. required makes a missing credential an error (a consumer
// tool call) rather than an anonymous request (the bridge, whose public tools
// need none); scopeHint names the scope in that error's remediation.
type mcpAuthorizer struct {
	app       *appCtx
	required  bool
	scopeHint string
}

var _ mcpbridge.Authorizer = mcpAuthorizer{}

func (m mcpAuthorizer) Authorization(ctx context.Context) (string, error) {
	cred, err := m.app.credential(ctx)
	if err != nil {
		return "", err
	}
	header := cred.AuthorizationHeader()
	if header == "" && m.required {
		return "", output.New(output.ExitAuth,
			"this command requires a consumer credential but none is stored",
			"Run `givmo login` to authenticate with the "+m.scopeHint+" scope, or set GIVMO_API_KEY for non-interactive use.")
	}
	return header, nil
}

func (m mcpAuthorizer) Rejected(ctx context.Context, header string) (string, error) {
	if apiKeyFromEnvOrFlag() != "" {
		return "", nil // an API key is not renewed
	}
	cred, err := m.app.tokens().Rejected(ctx, header)
	if err != nil {
		return "", renewalError(err)
	}
	return cred.AuthorizationHeader(), nil
}

// renewalError maps a token source failure to the CLI's envelope: an ended
// sign-in, a client the environment no longer accepts and another refusal exit 3;
// a rate limit 5; a server that could not be reached or did not answer 6; a wait
// for another process's renewal that ended unfinished 1, with a retry.
func renewalError(err error) error {
	var te *auth.TokenError
	var tr *auth.TransportError
	switch {
	case errors.Is(err, auth.ErrSignInEnded):
		return output.New(output.ExitAuth, "Your Givmo sign-in on this computer has ended.", "Run `givmo login`.")
	case errors.Is(err, auth.ErrLockTimeout), errors.Is(err, auth.ErrLockWaitCanceled):
		return output.New(output.ExitGeneric, err.Error(), "Retry.")
	case errors.As(err, &te) && (te.Code == "invalid_client" || te.Code == "unauthorized_client"):
		return output.New(output.ExitAuth, "this Givmo environment no longer accepts this sign-in",
			"Run `givmo login` to sign in again; if it says this environment doesn't accept the CLI's sign-in yet, try again later, or set GIVMO_CLIENT_ID and GIVMO_CLIENT_SECRET for a registered client.")
	case errors.As(err, &te) && te.Status == http.StatusTooManyRequests:
		msg, remediation := "Givmo rate-limited the sign-in renewal (HTTP 429)", "Nothing changed. Back off, then retry."
		if te.RetryAfter != "" {
			wait := mcpbridge.RetryAfterText(te.RetryAfter)
			msg += " (Retry-After: " + wait + ")"
			remediation = "Nothing changed. Wait " + wait + " (the server's Retry-After), then retry."
		}
		return output.New(output.ExitRateLimited, msg, remediation)
	case errors.As(err, &te) && (te.Code == "" || te.Status >= http.StatusInternalServerError):
		return output.New(output.ExitNetwork, "Givmo did not answer the sign-in renewal: "+te.Error(), lostRenewalRemediation)
	case errors.As(err, &te):
		return output.New(output.ExitAuth, "Givmo refused the sign-in renewal: "+te.Error(), "Run `givmo login` to sign in again.")
	case errors.As(err, &tr) && !tr.Sent:
		return output.New(output.ExitNetwork, "could not reach Givmo to renew the sign-in: "+tr.Error(),
			"Check connectivity and the active profile's api_base (`givmo config view`), then retry; the stored sign-in is unchanged.")
	case errors.As(err, &tr):
		return output.New(output.ExitNetwork, "Givmo did not answer the sign-in renewal: "+tr.Error(), lostRenewalRemediation)
	}
	// A stored credential that cannot be read, or a registered client's renewal
	// with no secret to hand.
	return output.New(output.ExitGeneric, "the stored sign-in could not be used: "+err.Error(),
		"Run `givmo logout`, then `givmo login`.")
}

// lostRenewalRemediation: after a renewal whose answer was lost, the next one
// either succeeds or, if the lost one landed, finds the sign-in ended.
const lostRenewalRemediation = "Retry. If the renewal went through but its answer was lost, the retry reports that the sign-in has ended; then run `givmo login`."

// httpClient returns a plain bounded http client for auxiliary fetches
// (metadata, openapi, manifest-over-URL).
func (a *appCtx) httpClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

// prodBanner prints a loud warning to stderr before a mutating operation runs
// against the production environment.
func (a *appCtx) prodBanner(action string) {
	if !a.Profile.IsProduction() {
		return
	}
	fmt.Fprintln(os.Stderr, "============================================================")
	fmt.Fprintf(os.Stderr, "  ⚠  PRODUCTION profile active — %s\n", action)
	fmt.Fprintln(os.Stderr, "  This targets the LIVE Givmo platform (api_base:")
	fmt.Fprintf(os.Stderr, "  %s).\n", a.Profile.Endpoints.APIBase)
	fmt.Fprintln(os.Stderr, "============================================================")
}

// baseContext returns a cancelable context for network operations.
func baseContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}

// toolContext returns the context for a command backed by MCP tool calls. It
// carries no deadline of its own: each request the MCP remote sends carries its
// own (mcpbridge.ToolCallTimeout for a tools/call), and a shorter command-wide
// deadline would cut a call the server is still allowed to be running.
func toolContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}
