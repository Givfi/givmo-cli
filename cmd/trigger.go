package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/givfi/givmo-cli/internal/client"
	"github.com/givfi/givmo-cli/internal/output"
	"github.com/spf13/cobra"
)

var triggerOverrides []string

// KnownSandboxEvents is the set of test events `trigger` can fire. This is a
// developer-ergonomics allowlist; the sandbox is authoritative once live.
var KnownSandboxEvents = []string{
	"donation_intent.created",
	"donation_intent.completed",
	"donation.succeeded",
	"receipt.issued",
	"giving_summary.updated",
}

func newTriggerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trigger <event> [--override k=v ...]",
		Short: "Fire a sandbox test event",
		Long: `Fire a test event into the Connect sandbox so you can exercise webhook
handlers. Use --override k=v (repeatable) to set fields on the synthesized event
payload; dotted keys nest (a.b=1), and values are parsed as JSON when possible,
else treated as strings.

Structurally complete; delivery is verified when the sandbox goes live
(ready-inert).

Known events:
  ` + strings.Join(KnownSandboxEvents, "\n  "),
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			event := args[0]
			overrides, err := parseOverrides(triggerOverrides)
			if err != nil {
				return err
			}
			payload := map[string]any{
				"event":     event,
				"overrides": overrides,
			}
			body, _ := json.Marshal(payload)

			app.prodBanner("firing a sandbox event")
			ctx, cancel := baseContext()
			defer cancel()

			resp, err := app.apiClient().Do(ctx, "POST", client.PathSandboxEvents, body, true)
			if err != nil {
				return err
			}
			var out map[string]any
			_ = resp.DecodeInto(&out)
			if out == nil {
				out = map[string]any{"event": event, "status": "accepted"}
			}
			return app.Printer.Result(out, func(w io.Writer) {
				fmt.Fprintf(w, "Fired sandbox event %q.\n", event)
				if len(overrides) > 0 {
					b, _ := json.MarshalIndent(overrides, "  ", "  ")
					fmt.Fprintf(w, "  overrides: %s\n", string(b))
				}
			})
		},
	}
	cmd.Flags().StringArrayVar(&triggerOverrides, "override", nil, "set an event field (k=v; repeatable; dotted keys nest)")
	return cmd
}

// parseOverrides converts ["a=1","b.c=hi"] into a nested map. Values are parsed
// as JSON when valid, else kept as strings. Pure/deterministic → unit-tested.
func parseOverrides(pairs []string) (map[string]any, error) {
	root := map[string]any{}
	for _, p := range pairs {
		eq := strings.IndexByte(p, '=')
		if eq < 0 {
			return nil, output.New(output.ExitUsage,
				fmt.Sprintf("invalid --override %q", p),
				"Use k=v form, e.g. --override amount_cents=2500.")
		}
		key := strings.TrimSpace(p[:eq])
		rawVal := p[eq+1:]
		if key == "" {
			return nil, output.New(output.ExitUsage,
				fmt.Sprintf("invalid --override %q: empty key", p),
				"Use k=v form with a non-empty key.")
		}
		val := parseScalar(rawVal)
		if err := setNested(root, strings.Split(key, "."), val); err != nil {
			return nil, err
		}
	}
	return root, nil
}

// parseScalar interprets a raw override value as JSON when possible, else a
// string. Bare bareword bool/number are handled by the JSON path.
func parseScalar(raw string) any {
	var v any
	if json.Unmarshal([]byte(raw), &v) == nil {
		return v
	}
	// Fall back: try common non-JSON forms already covered by json, else string.
	if b, err := strconv.ParseBool(raw); err == nil {
		return b
	}
	return raw
}

// setNested writes val at the dotted path within m, creating intermediate maps.
func setNested(m map[string]any, path []string, val any) error {
	for i := 0; i < len(path)-1; i++ {
		seg := path[i]
		if seg == "" {
			return output.New(output.ExitUsage, "invalid override key: empty path segment", "Use dotted keys like a.b.c=1.")
		}
		next, ok := m[seg].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[seg] = next
		}
		m = next
	}
	m[path[len(path)-1]] = val
	return nil
}
