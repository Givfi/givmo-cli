package cmd

import (
	"os"
	"strings"

	"github.com/givfi/givmo-cli/internal/mcpbridge"
	"github.com/givfi/givmo-cli/internal/output"
	"github.com/givfi/givmo-cli/internal/version"
	"github.com/spf13/cobra"
)

var mcpServeTools []string

func newMCPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run a local MCP bridge or install the Givmo MCP into a client",
		Long: `Bridge an agent IDE to the remote Givmo MCP using the CLI's authenticated
session ('serve'), or write the Givmo MCP server config into a supported client
('install').`,
	}
	cmd.AddCommand(newMCPServeCmd(), newMCPInstallCmd())
	return cmd
}

func newMCPServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve [--tools t1,t2]",
		Short: "Run a local stdio MCP server bridging to the remote Givmo MCP",
		Long: `Run a local MCP server that speaks JSON-RPC 2.0 over stdio (initialize,
tools/list, tools/call) and BRIDGES calls to the remote Givmo MCP over HTTP,
injecting the CLI's stored auth. An agent IDE spawns this so it can use the
CLI's session without ever handling the token.

--tools restricts which remote tools this bridge exposes (comma-separated
allowlist). tools/call for a tool outside the allowlist is rejected locally.

Requires a stored credential for the active profile (run 'givmo login'); the
consumer bearer is forwarded to the remote MCP. The remote endpoint is
<api_base>/mcp.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			cred := app.credential()
			authHeader := ""
			if cred != nil {
				authHeader = cred.AuthorizationHeader()
			}

			endpoint := strings.TrimRight(app.Profile.Endpoints.APIBase, "/") + "/mcp"
			remote := mcpbridge.NewHTTPRemote(endpoint, authHeader, app.httpClient())

			srv := mcpbridge.NewServer(mcpbridge.Config{
				In:      os.Stdin,
				Out:     os.Stdout,
				Remote:  remote,
				Tools:   splitTools(mcpServeTools),
				Name:    "givmo-cli-bridge",
				Version: version.Version,
			})
			ctx := c.Context()
			if serr := srv.Serve(ctx); serr != nil {
				return output.New(output.ExitGeneric, "stdio bridge error: "+serr.Error(), "")
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&mcpServeTools, "tools", nil, "comma-separated allowlist of remote tools to expose")
	return cmd
}

// splitTools flattens a --tools value (already comma-split by pflag) and trims.
func splitTools(in []string) []string {
	var out []string
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}
