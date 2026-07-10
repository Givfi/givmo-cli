package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/givfi/givmo-cli/internal/output"
)

// causeETFListItem is one entry from the list_cause_etfs tool (charity_count is a
// count, not the constituent list — get one ETF to see its charities).
type causeETFListItem struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	CharityCount int    `json:"charity_count"`
}

// causeETFListResult is the list_cause_etfs tool payload.
type causeETFListResult struct {
	CauseETFs []causeETFListItem `json:"cause_etfs"`
}

// causeETFConstituent is one charity inside an ETF (get_cause_etf).
type causeETFConstituent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// causeETFDetail is the cause_etf object from the get_cause_etf tool.
type causeETFDetail struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	Description string                `json:"description,omitempty"`
	Charities   []causeETFConstituent `json:"charities"`
}

// causeETFDetailResult is the get_cause_etf tool payload.
type causeETFDetailResult struct {
	CauseETF causeETFDetail `json:"cause_etf"`
}

func newCauseETFsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "cause-etfs",
		Aliases: []string{"cause-etf", "etfs"},
		Short:   "List and inspect Cause ETFs (curated charity baskets)",
		Long:    `List the Givmo Cause ETFs and fetch one by id. Public-tier catalog data served by the list_cause_etfs / get_cause_etf MCP tools; no login required.`,
	}
	cmd.AddCommand(newCauseETFsListCmd(), newCauseETFsGetCmd())
	return cmd
}

func newCauseETFsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all Cause ETFs",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			ctx, cancel := baseContext()
			defer cancel()

			payload, err := app.callTool(ctx, "list_cause_etfs", map[string]any{}, false, "")
			if err != nil {
				return err
			}
			var res causeETFListResult
			if uerr := decodeToolPayload(payload, &res); uerr != nil {
				return uerr
			}
			return app.Printer.Result(res, func(w io.Writer) {
				if len(res.CauseETFs) == 0 {
					fmt.Fprintln(w, "no cause ETFs found")
					return
				}
				rows := make([][]string, 0, len(res.CauseETFs))
				for _, e := range res.CauseETFs {
					rows = append(rows, []string{e.ID, e.Name, fmt.Sprintf("%d", e.CharityCount)})
				}
				output.Table(w, []string{"ID", "NAME", "#CHARITIES"}, rows)
			})
		},
	}
}

func newCauseETFsGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Fetch a single Cause ETF by id, including its charities",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			ctx, cancel := baseContext()
			defer cancel()

			payload, err := app.callTool(ctx, "get_cause_etf", map[string]any{"etf_id": args[0]}, false, "")
			if err != nil {
				return err
			}
			var res causeETFDetailResult
			if uerr := decodeToolPayload(payload, &res); uerr != nil {
				return uerr
			}
			e := res.CauseETF
			return app.Printer.Result(res, func(w io.Writer) {
				output.KeyValues(w, [][2]string{
					{"id", e.ID},
					{"name", e.Name},
					{"description", e.Description},
					{"charities", fmt.Sprintf("%d charities", len(e.Charities))},
				})
				if len(e.Charities) > 0 {
					fmt.Fprintln(w)
					rows := make([][]string, 0, len(e.Charities))
					for _, c := range e.Charities {
						rows = append(rows, []string{c.ID, c.Name})
					}
					output.Table(w, []string{"CHARITY_ID", "NAME"}, rows)
				}
			})
		},
	}
}
