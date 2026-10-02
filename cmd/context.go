package cmd

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/givfi/givmo-cli/internal/auth"
	"github.com/givfi/givmo-cli/internal/client"
	"github.com/givfi/givmo-cli/internal/config"
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

// credential loads the stored credential for the active profile, honoring the
// non-interactive GIVMO_API_KEY / --api-key path. Returns nil (no error) when
// no credential is present — callers that require auth check Authenticated().
func (a *appCtx) credential() *auth.Credential {
	if key := apiKeyFromEnvOrFlag(); key != "" {
		return &auth.Credential{
			Profile:     a.Profile.Name,
			Type:        auth.CredTypeAPIKey,
			AccessToken: key,
			TokenType:   "Bearer",
		}
	}
	c, err := a.Store.Load(a.Profile.Name)
	if err != nil {
		return nil
	}
	return c
}

// apiClient builds an authenticated REST client for the active profile.
func (a *appCtx) apiClient() *client.Client {
	return client.New(client.Options{
		BaseURL:    a.Profile.Endpoints.APIBase,
		Credential: a.credential(),
		Timeout:    30 * time.Second,
	})
}

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
