package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/givfi/givmo-cli/internal/mcpbridge"
	"github.com/givfi/givmo-cli/internal/output"
	"github.com/spf13/cobra"
)

var (
	mcpInstallClient string
	mcpInstallPath   string
)

func newMCPInstallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "install --client claude-code|cursor",
		Short: "Write the Givmo MCP server config into a client",
		Long: `Register the Givmo MCP server in a supported client's config so the client can
use Givmo tools through the CLI's authenticated bridge ('givmo mcp serve').

Idempotent: an identical existing entry is a no-op. A differing entry is
overwritten AFTER backing up the config file. Other servers/keys are preserved.

Supported clients: claude-code (~/.claude.json), cursor (~/.cursor/mcp.json).`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			kind := mcpbridge.ClientKind(mcpInstallClient)
			if kind != mcpbridge.ClientClaudeCode && kind != mcpbridge.ClientCursor {
				return output.New(output.ExitUsage,
					fmt.Sprintf("unsupported --client %q", mcpInstallClient),
					"Use --client claude-code or --client cursor.")
			}

			// Resolve the config path (explicit override wins).
			configPath := mcpInstallPath
			if configPath == "" {
				p, perr := mcpbridge.ConfigPathFor(kind, os.Getenv("GIVMO_HOME"))
				if perr != nil {
					return output.New(output.ExitGeneric, perr.Error(), "")
				}
				configPath = p
			}

			// The bridge runs THIS binary. Resolve its absolute path.
			cliPath, perr := os.Executable()
			if perr != nil {
				cliPath = "givmo" // fall back to PATH lookup at runtime
			}
			spec := mcpbridge.DefaultServerSpec(cliPath, app.Profile.Name)

			res, ierr := mcpbridge.InstallServer(configPath, spec)
			if ierr != nil {
				return output.New(output.ExitGeneric, "could not install MCP config: "+ierr.Error(),
					"Ensure the config file is valid JSON and writable.")
			}

			specJSON, _ := json.MarshalIndent(spec, "  ", "  ")
			return app.Printer.Result(res, func(w io.Writer) {
				action := "unchanged (already installed)"
				if res.Created {
					action = "created config and installed"
				} else if res.Updated {
					action = "updated existing entry"
				} else {
					action = "already present (no change)"
				}
				fmt.Fprintf(w, "Givmo MCP server %q: %s\n", res.ServerName, action)
				fmt.Fprintf(w, "  config: %s\n", res.ConfigPath)
				if res.BackupPath != "" {
					fmt.Fprintf(w, "  backup: %s\n", res.BackupPath)
				}
				fmt.Fprintf(w, "  wrote:  %s\n", string(specJSON))
			})
		},
	}
	cmd.Flags().StringVar(&mcpInstallClient, "client", "", "client to install into (claude-code|cursor) (required)")
	cmd.Flags().StringVar(&mcpInstallPath, "config-path", "", "override the client config path (advanced/testing)")
	_ = cmd.MarkFlagRequired("client")
	return cmd
}
