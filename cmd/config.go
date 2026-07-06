package cmd

import (
	"fmt"
	"io"

	"github.com/givfi/givmo-cli/internal/config"
	"github.com/givfi/givmo-cli/internal/output"
	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect and manage CLI profiles",
		Long: `Manage the CLI's per-profile configuration. A profile selects the Givmo
environment (sandbox or production) and its API + auth-server base URLs.

Base URLs default sensibly per profile and can be overridden per profile in
~/.givmo/config.json or via GIVMO_API_BASE / GIVMO_AUTH_BASE.`,
	}
	cmd.AddCommand(newConfigUseProfileCmd(), newConfigViewCmd())
	return cmd
}

func newConfigUseProfileCmd() *cobra.Command {
	return &cobra.Command{
		Use:       "use-profile <sandbox|production>",
		Short:     "Select the active profile",
		Args:      cobra.ExactArgs(1),
		ValidArgs: config.KnownProfiles(),
		RunE: func(_ *cobra.Command, args []string) error {
			name := args[0]
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			if !config.IsKnownProfile(name) {
				return output.New(output.ExitUsage,
					fmt.Sprintf("unknown profile %q", name),
					"Use one of: sandbox, production.")
			}
			if err := app.Config.SetActiveProfile(name); err != nil {
				return output.New(output.ExitUsage, err.Error(), "Use one of: sandbox, production.")
			}
			if err := app.Config.Save(); err != nil {
				return output.New(output.ExitGeneric, "could not save config: "+err.Error(), "")
			}
			prof, _ := app.Config.Resolve("")
			return app.Printer.Result(
				map[string]any{"active_profile": name, "endpoints": prof.Endpoints},
				func(w io.Writer) {
					fmt.Fprintf(w, "Active profile is now %q.\n", name)
					output.KeyValues(w, [][2]string{
						{"api_base", prof.Endpoints.APIBase},
						{"auth_base", prof.Endpoints.AuthBase},
					})
				},
			)
		},
	}
}

func newConfigViewCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "view",
		Short: "Show the active profile and resolved endpoints",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			p, err := config.Path()
			if err != nil {
				return output.New(output.ExitGeneric, err.Error(), "")
			}
			view := map[string]any{
				"active_profile": app.Profile.Name,
				"endpoints":      app.Profile.Endpoints,
				"config_path":    p,
				"is_production":  app.Profile.IsProduction(),
			}
			return app.Printer.Result(view, func(w io.Writer) {
				output.KeyValues(w, [][2]string{
					{"active_profile", app.Profile.Name},
					{"api_base", app.Profile.Endpoints.APIBase},
					{"auth_base", app.Profile.Endpoints.AuthBase},
					{"config_path", p},
					{"is_production", boolStr(app.Profile.IsProduction())},
				})
				if app.Profile.IsProduction() {
					fmt.Fprintln(w, "\n⚠  production profile: mutating operations will print a warning banner.")
				}
			})
		},
	}
}
