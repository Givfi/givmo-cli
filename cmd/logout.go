package cmd

import (
	"fmt"
	"io"

	"github.com/givfi/givmo-cli/internal/output"
	"github.com/spf13/cobra"
)

func newLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Clear stored credentials for the active profile",
		Long: `Remove the stored consumer token or API key for the active profile from the
OS keychain (or the 0600 file fallback). Logging out of a profile with no stored
credential is a no-op (idempotent).`,
		RunE: func(_ *cobra.Command, _ []string) error {
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			if derr := app.Store.Delete(app.Profile.Name); derr != nil {
				return output.New(output.ExitGeneric, "could not clear credentials: "+derr.Error(), "")
			}
			return app.Printer.Result(
				map[string]any{"profile": app.Profile.Name, "logged_out": true},
				func(w io.Writer) {
					fmt.Fprintf(w, "Cleared credentials for profile %q.\n", app.Profile.Name)
				},
			)
		},
	}
}
