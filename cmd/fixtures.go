package cmd

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/givfi/givmo-cli/internal/client"
	"github.com/givfi/givmo-cli/internal/output"
	"github.com/spf13/cobra"
)

// fixtureScenario is one bundled end-to-end scenario the CLI can run against
// the sandbox. Scenarios are declarative so they can be listed offline and
// executed once the sandbox is live.
type fixtureScenario struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Steps are the sandbox event sequence the scenario fires.
	Steps []string `json:"steps"`
}

// bundledFixtures are the scenarios shipped with the CLI.
func bundledFixtures() []fixtureScenario {
	return []fixtureScenario{
		{
			Name:        "single-donation",
			Description: "Create a donation intent, complete checkout, receive the donation-succeeded + receipt events.",
			Steps:       []string{"donation_intent.created", "donation_intent.completed", "donation.succeeded", "receipt.issued"},
		},
		{
			Name:        "cause-etf-donation",
			Description: "Donate to a Cause ETF basket and fan out to constituent receipts.",
			Steps:       []string{"donation_intent.created", "donation_intent.completed", "donation.succeeded", "receipt.issued", "giving_summary.updated"},
		},
		{
			Name:        "receipt-only",
			Description: "Issue a standalone receipt and observe the giving-summary update.",
			Steps:       []string{"receipt.issued", "giving_summary.updated"},
		},
	}
}

var fixturesList bool

func newFixturesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fixtures",
		Short: "Run bundled fixture scenarios against the sandbox",
		Long:  `Run bundled end-to-end fixture scenarios against the Connect sandbox to exercise the full donation/receipt event flow.`,
	}
	cmd.AddCommand(newFixturesRunCmd())
	return cmd
}

func newFixturesRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run [scenario]",
		Short: "Run one or all bundled fixture scenarios",
		Long: `Run a bundled fixture scenario (or, with no argument, all of them) against the
sandbox. Use --list to print the available scenarios without running them.

Structurally complete; execution requires the live sandbox (ready-inert).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			all := bundledFixtures()

			if fixturesList {
				return app.Printer.Result(all, func(w io.Writer) {
					rows := make([][]string, 0, len(all))
					for _, f := range all {
						rows = append(rows, []string{f.Name, fmt.Sprintf("%d", len(f.Steps)), f.Description})
					}
					output.Table(w, []string{"SCENARIO", "STEPS", "DESCRIPTION"}, rows)
				})
			}

			selected := all
			if len(args) == 1 {
				match := fixtureByName(all, args[0])
				if match == nil {
					return output.New(output.ExitNotFound,
						fmt.Sprintf("no bundled fixture named %q", args[0]),
						"Run `givmo fixtures run --list` to see available scenarios.")
				}
				selected = []fixtureScenario{*match}
			}

			app.prodBanner("running fixture scenarios")
			ctx, cancel := baseContext()
			defer cancel()

			body, _ := json.Marshal(map[string]any{"scenarios": selected})
			resp, err := app.apiClient().Do(ctx, "POST", client.PathSandboxFixturesRun, body, true)
			if err != nil {
				return err
			}
			var out map[string]any
			_ = resp.DecodeInto(&out)
			if out == nil {
				out = map[string]any{"ran": scenarioNames(selected), "status": "accepted"}
			}
			return app.Printer.Result(out, func(w io.Writer) {
				fmt.Fprintf(w, "Ran %d fixture scenario(s): %v\n", len(selected), scenarioNames(selected))
			})
		},
	}
	cmd.Flags().BoolVar(&fixturesList, "list", false, "list available scenarios without running them")
	return cmd
}

func fixtureByName(all []fixtureScenario, name string) *fixtureScenario {
	for i := range all {
		if all[i].Name == name {
			return &all[i]
		}
	}
	return nil
}

func scenarioNames(s []fixtureScenario) []string {
	out := make([]string, len(s))
	for i, f := range s {
		out[i] = f.Name
	}
	return out
}
