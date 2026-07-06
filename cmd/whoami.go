package cmd

import (
	"fmt"
	"io"

	"github.com/givfi/givmo-cli/internal/auth"
	"github.com/givfi/givmo-cli/internal/output"
	"github.com/spf13/cobra"
)

func newWhoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the linked identity and granted scopes (no secrets)",
		Long: `Print the identity and scopes of the credential stored for the active profile.
Secrets are never shown: access/refresh tokens are omitted entirely from output
(both human and --json forms).`,
		RunE: func(_ *cobra.Command, _ []string) error {
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			cred := app.credential()
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
				subject := cred.Subject
				if subject == "" {
					subject = "(not reported)"
				}
				scopes := cred.ScopeString()
				if scopes == "" && cred.Type == auth.CredTypeAPIKey {
					scopes = "(api-key; scopes determined server-side)"
				}
				output.KeyValues(w, [][2]string{
					{"profile", app.Profile.Name},
					{"type", cred.Type},
					{"subject", subject},
					{"scopes", scopes},
					{"expired", boolStr(cred.Expired())},
					{"storage", app.Store.Backend()},
				})
			})
		},
	}
}

func boolStr(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
