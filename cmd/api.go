package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/givfi/givmo-cli/internal/output"
	"github.com/spf13/cobra"
)

var apiData string

// validHTTPMethods is the set of methods the escape hatch accepts.
var validHTTPMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "HEAD": true,
}

func newAPICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "api <METHOD> <path> [--data <json>]",
		Short: "Authenticated escape hatch to the Givmo REST API",
		Long: `Make an authenticated raw request to the Givmo REST API against the active
profile. Useful for endpoints without a dedicated command.

The stored credential (or GIVMO_API_KEY) is attached automatically. The response
body is printed; errors render through the standard envelope with a stable exit
code. Non-GET methods print the production banner when the production profile is
active.

Examples:
  givmo api GET /charities?q=water
  givmo api POST /donation-intents --data '{"charity_id":"c_1","amount_cents":2500}'`,
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			method := strings.ToUpper(args[0])
			path := args[1]
			if !validHTTPMethods[method] {
				return output.New(output.ExitUsage,
					fmt.Sprintf("unsupported HTTP method %q", args[0]),
					"Use one of: GET, POST, PUT, PATCH, DELETE, HEAD.")
			}
			var body []byte
			if apiData != "" {
				if !json.Valid([]byte(apiData)) {
					return output.New(output.ExitUsage, "--data is not valid JSON",
						"Pass a valid JSON object/array to --data.")
				}
				body = []byte(apiData)
			}
			if method != "GET" && method != "HEAD" {
				app.prodBanner(method + " " + path)
			}
			ctx, cancel := baseContext()
			defer cancel()

			// requireAuth=true: the escape hatch is the authenticated surface.
			resp, err := app.apiClient().Do(ctx, method, path, body, true)
			if err != nil {
				return err
			}
			return app.Printer.Result(json.RawMessage(resp.Raw), func(w io.Writer) {
				// Pretty-print for humans.
				var pretty any
				if json.Unmarshal(resp.Raw, &pretty) == nil {
					enc := json.NewEncoder(w)
					enc.SetIndent("", "  ")
					enc.SetEscapeHTML(false)
					_ = enc.Encode(pretty)
				} else {
					w.Write(resp.Raw)
				}
				if resp.RequestID != "" {
					fmt.Fprintf(w, "\n(request_id: %s)\n", resp.RequestID)
				}
			})
		},
	}
	cmd.Flags().StringVar(&apiData, "data", "", "JSON request body for non-GET methods")
	return cmd
}
