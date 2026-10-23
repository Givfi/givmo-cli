package cmd

import (
	"context"
	"fmt"
	"io"

	"github.com/givfi/givmo-cli/internal/auth"
	"github.com/givfi/givmo-cli/internal/output"
	"github.com/spf13/cobra"
)

func newWhoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the granted scopes and access-token expiry (no secrets)",
		Long: `Print the scopes and access-token expiry of the credential stored for the active
profile. Secrets are never shown: access/refresh tokens are omitted entirely
from output (both human and --json forms). When the stored sign-in's access
token is about to expire, whoami renews it first, so "expired" describes the
token the next command will use.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			ctx, cancel := baseContext()
			defer cancel()
			return runWhoami(ctx, app)
		},
	}
}

// runWhoami prints the active profile's credential, renewed first by the token
// source when its access token is about to expire.
func runWhoami(ctx context.Context, app *appCtx) error {
	cred, err := app.credential(ctx)
	if err != nil {
		return err
	}
	if cred == nil || cred.AccessToken == "" {
		return output.New(output.ExitAuth,
			fmt.Sprintf("no credential stored for profile %q", app.Profile.Name),
			"Run `givmo login` (or set GIVMO_API_KEY) to authenticate.")
	}

	// Build a secret-free view.
	view := map[string]any{
		"profile":  app.Profile.Name,
		"type":     cred.Type,
		"scopes":   cred.Scopes,
		"subject":  cred.Subject,
		"expired":  cred.Expired(),
		"storage":  app.Store.Backend(),
		"api_base": app.Profile.Endpoints.APIBase,
	}
	if !cred.ExpiresAt.IsZero() {
		view["expires_at"] = cred.ExpiresAt.Format("2006-01-02T15:04:05Z07:00")
	}

	return app.Printer.Result(view, func(w io.Writer) {
		scopes := cred.ScopeString()
		if scopes == "" && cred.Type == auth.CredTypeAPIKey {
			scopes = "(api-key; scopes determined server-side)"
		}
		output.KeyValues(w, [][2]string{
			{"profile", app.Profile.Name},
			{"type", cred.Type},
			{"scopes", scopes},
			{"expired", boolStr(cred.Expired())},
			{"storage", app.Store.Backend()},
		})
	})
}

func boolStr(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
