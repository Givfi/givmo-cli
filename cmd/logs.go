package cmd

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/givfi/givmo-cli/internal/client"
	"github.com/givfi/givmo-cli/internal/output"
	"github.com/spf13/cobra"
)

var logsFilters []string

func newLogsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Consume the internal audit logs-tail API",
		Long: `Tail the Givmo internal audit log (tool-call telemetry).

This uses the INTERNAL credential, which is dark until configured. Set the
credential via GIVMO_INTERNAL_TOKEN (S2S). Without it, the command fails with a
clear, actionable error rather than a partial/empty result.`,
	}
	cmd.AddCommand(newLogsTailCmd())
	return cmd
}

// buildLogsQuery converts --filter k=v pairs into a query string. Pure →
// unit-tested. Only a documented allowlist of filter keys is accepted.
func buildLogsQuery(filters []string) (url.Values, error) {
	allowed := map[string]bool{"tool": true, "outcome": true, "principal": true, "since": true}
	q := url.Values{}
	for _, f := range filters {
		eq := strings.IndexByte(f, '=')
		if eq < 0 {
			return nil, output.New(output.ExitUsage,
				fmt.Sprintf("invalid --filter %q", f),
				"Use k=v form, e.g. --filter tool=create_donation_intent --filter outcome=denied.")
		}
		k := strings.TrimSpace(f[:eq])
		v := f[eq+1:]
		if !allowed[k] {
			return nil, output.New(output.ExitUsage,
				fmt.Sprintf("unknown filter key %q", k),
				"Allowed filter keys: tool, outcome, principal, since.")
		}
		q.Add(k, v)
	}
	return q, nil
}

// internalToken resolves the internal S2S credential (dark until configured).
func internalToken() string {
	return strings.TrimSpace(os.Getenv("GIVMO_INTERNAL_TOKEN"))
}

func newLogsTailCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tail [--filter tool=… --filter outcome=…]",
		Short: "Tail the internal audit log",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			q, err := buildLogsQuery(logsFilters)
			if err != nil {
				return err
			}
			tok := internalToken()
			if tok == "" {
				return output.New(output.ExitAuth,
					"the internal audit logs-tail API requires an internal S2S credential, which is not configured",
					"Set GIVMO_INTERNAL_TOKEN to your internal service credential. "+
						"This surface is internal-tier and dark until credentialed.")
			}

			// Build an internal-tier request: internal token in Authorization.
			path := client.PathInternalAuditLogsTail
			if enc := q.Encode(); enc != "" {
				path += "?" + enc
			}
			ctx, cancel := baseContext()
			defer cancel()

			req, berr := app.apiClient().BuildRequest(ctx, "GET", path, nil)
			if berr != nil {
				return berr
			}
			req.Header.Set("Authorization", "Bearer "+tok)
			resp, derr := app.httpClient().Do(req)
			if derr != nil {
				return output.New(output.ExitNetwork, "could not reach the internal logs API: "+derr.Error(),
					"The internal audit-logs surface may not be live yet (ready-inert).")
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
			if resp.StatusCode == 401 || resp.StatusCode == 403 {
				return output.New(output.ExitAuth,
					fmt.Sprintf("internal logs API rejected the credential (HTTP %d)", resp.StatusCode),
					"Verify GIVMO_INTERNAL_TOKEN is a valid internal S2S credential.")
			}
			if resp.StatusCode >= 400 {
				return output.New(output.ExitGeneric,
					fmt.Sprintf("internal logs API returned HTTP %d", resp.StatusCode),
					"Retry; the audit-logs surface may not be enabled yet.")
			}
			// Stream/print the body as-is (NDJSON or JSON array).
			if _, werr := app.Printer.Out.Write(body); werr != nil {
				return output.New(output.ExitGeneric, werr.Error(), "")
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&logsFilters, "filter", nil, "filter events (k=v; repeatable; keys: tool, outcome, principal, since)")
	return cmd
}
