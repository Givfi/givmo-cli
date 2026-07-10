package cmd

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/givfi/givmo-cli/internal/client"
	"github.com/givfi/givmo-cli/internal/output"
	"github.com/spf13/cobra"
)

var (
	openapiOut  string
	openapiRoot bool
)

func newOpenAPICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "openapi",
		Short: "Fetch the backend OpenAPI spec",
		Long:  `Fetch the Givmo backend OpenAPI specification and write it to disk (or stdout).`,
	}
	cmd.AddCommand(newOpenAPIPullCmd())
	return cmd
}

func newOpenAPIPullCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pull [--out <file>] [--root]",
		Short: "Download the OpenAPI spec to disk",
		Long: `Download the Givmo OpenAPI specification.

By default this fetches the Connect partner-API spec at /connect/openapi.json — the
surface this CLI targets. Pass --root to fetch the root /openapi.json (the main
FastAPI/mobile-app spec, a different surface) instead.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			data, srcPath, err := fetchOpenAPI(c.Context(), app, app.httpClient())
			if err != nil {
				return err
			}
			if openapiOut == "" || openapiOut == "-" {
				// stdout
				if _, werr := app.Printer.Out.Write(data); werr != nil {
					return output.New(output.ExitGeneric, werr.Error(), "")
				}
				return nil
			}
			if werr := os.WriteFile(openapiOut, data, 0o644); werr != nil {
				return output.New(output.ExitGeneric, "could not write spec: "+werr.Error(),
					"Check the --out path is writable.")
			}
			return app.Printer.Result(
				map[string]any{"out": openapiOut, "bytes": len(data), "source": srcPath},
				func(w io.Writer) {
					fmt.Fprintf(w, "Wrote %d bytes to %s (from %s%s)\n", len(data), openapiOut, app.Profile.Endpoints.APIBase, srcPath)
				},
			)
		},
	}
	cmd.Flags().StringVar(&openapiOut, "out", "", "output file path (default: stdout; use '-' for stdout)")
	cmd.Flags().BoolVar(&openapiRoot, "root", false, "fetch the root /openapi.json (main spec) instead of /connect/openapi.json")
	return cmd
}

// fetchOpenAPI fetches the spec for the surface the CLI targets. By default that
// is the Connect spec (/connect/openapi.json); with --root it is the root spec
// (/openapi.json). Returns the body and the path it was fetched from.
func fetchOpenAPI(parent context.Context, app *appCtx, httpc *http.Client) ([]byte, string, error) {
	if parent == nil {
		parent = context.Background()
	}
	if httpc == nil {
		httpc = &http.Client{Timeout: 20 * time.Second}
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()

	specPath := client.OpenAPIConnectPath
	if openapiRoot {
		specPath = client.OpenAPIRootPath
	}
	base := app.Profile.Endpoints.APIBase
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+specPath, nil)
	if err != nil {
		return nil, "", output.New(output.ExitGeneric, "could not build the spec request: "+err.Error(), "")
	}
	req.Header.Set("Accept", "application/json")
	if h := app.credential().AuthorizationHeader(); h != "" {
		req.Header.Set("Authorization", h)
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, "", output.New(output.ExitNetwork, "could not reach the API: "+err.Error(),
			"Check connectivity and the active profile's api_base (`givmo config view`).")
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode == http.StatusOK && len(body) > 0 {
		return body, specPath, nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, "", output.New(output.ExitNotFound,
			fmt.Sprintf("no OpenAPI spec at %s (HTTP 404)", specPath),
			"Verify the api_base (`givmo config view`). The Connect spec is at /connect/openapi.json; use --root for the main /openapi.json.")
	}
	return nil, "", output.New(output.ExitGeneric,
		fmt.Sprintf("fetching %s returned HTTP %d", specPath, resp.StatusCode),
		"The spec endpoint may not be enabled in this environment, or the api_base is wrong. Verify with `givmo config view`.")
}
