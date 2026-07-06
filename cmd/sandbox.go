package cmd

import (
	"fmt"
	"io"

	"github.com/givfi/givmo-cli/internal/client"
	"github.com/givfi/givmo-cli/internal/config"
	"github.com/givfi/givmo-cli/internal/output"
	"github.com/spf13/cobra"
)

func newSandboxCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Seed or reset sandbox data",
		Long:  `Seed the sandbox with baseline test data, or reset it to a clean state. Only valid on the sandbox profile.`,
	}
	cmd.AddCommand(newSandboxSeedCmd(), newSandboxResetCmd())
	return cmd
}

// requireSandboxProfile guards sandbox-mutating operations so they cannot run
// against production by accident.
func requireSandboxProfile(app *appCtx) error {
	if app.Profile.Name != config.ProfileSandbox {
		return output.New(output.ExitUsage,
			fmt.Sprintf("sandbox operations require the sandbox profile (active: %q)", app.Profile.Name),
			"Switch with `givmo config use-profile sandbox` or pass --profile sandbox.")
	}
	return nil
}

func newSandboxSeedCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "seed",
		Short: "Seed the sandbox with baseline test data",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			if err := requireSandboxProfile(app); err != nil {
				return err
			}
			ctx, cancel := baseContext()
			defer cancel()
			resp, err := app.apiClient().Do(ctx, "POST", client.PathSandboxSeed, []byte(`{}`), true)
			if err != nil {
				return err
			}
			var out map[string]any
			_ = resp.DecodeInto(&out)
			if out == nil {
				out = map[string]any{"status": "seeded"}
			}
			return app.Printer.Result(out, func(w io.Writer) {
				fmt.Fprintln(w, "Sandbox seeded.")
			})
		},
	}
}

func newSandboxResetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reset",
		Short: "Reset the sandbox to a clean state",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			if err := requireSandboxProfile(app); err != nil {
				return err
			}
			ctx, cancel := baseContext()
			defer cancel()
			resp, err := app.apiClient().Do(ctx, "POST", client.PathSandboxReset, []byte(`{}`), true)
			if err != nil {
				return err
			}
			var out map[string]any
			_ = resp.DecodeInto(&out)
			if out == nil {
				out = map[string]any{"status": "reset"}
			}
			return app.Printer.Result(out, func(w io.Writer) {
				fmt.Fprintln(w, "Sandbox reset to clean state.")
			})
		},
	}
}
