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

var openapiOut string

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
		Use:   "pull [--out <file>]",
		Short: "Download the OpenAPI spec to disk",
		Args:  cobra.NoArgs,
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
	return cmd
}

// fetchOpenAPI tries the conventional spec paths against the active API base.
func fetchOpenAPI(parent context.Context, app *appCtx, httpc *http.Client) ([]byte, string, error) {
	if parent == nil {
		parent = context.Background()
	}
	if httpc == nil {
		httpc = &http.Client{Timeout: 20 * time.Second}
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()

	base := app.Profile.Endpoints.APIBase
	var lastStatus int
	for _, p := range client.OpenAPIPaths {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+p, nil)
		if err != nil {
			continue
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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK && len(body) > 0 {
			return body, p, nil
		}
		lastStatus = resp.StatusCode
	}
	return nil, "", output.New(output.ExitNotFound,
		fmt.Sprintf("no OpenAPI spec found at the conventional paths (last status %d)", lastStatus),
		"The spec endpoint may not be live yet (ready-inert), or the api_base is wrong. Verify with `givmo config view`.")
}
