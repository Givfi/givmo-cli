package cmd

import (
	"fmt"
	"io"

	"github.com/givfi/givmo-cli/internal/client"
	"github.com/givfi/givmo-cli/internal/output"
	"github.com/spf13/cobra"
)

// causeETF is the CLI's display view of a Cause ETF (a curated bundle of
// charities donated to as one basket).
type causeETF struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Category    string   `json:"category,omitempty"`
	Charities   []string `json:"charities,omitempty"`
}

func newCauseETFsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "cause-etfs",
		Aliases: []string{"cause-etf", "etfs"},
		Short:   "List and inspect Cause ETFs (curated charity baskets)",
		Long:    `List the Givmo Cause ETFs and fetch one by id. Public-tier data; no login required.`,
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

			resp, err := app.apiClient().Do(ctx, "GET", client.PathCauseETFs, nil, false)
			if err != nil {
				return err
			}
			var list []causeETF
			if err := resp.DecodeInto(&list); err != nil {
				return err
			}
			return app.Printer.Result(list, func(w io.Writer) {
				if len(list) == 0 {
					fmt.Fprintln(w, "no cause ETFs found")
					return
				}
				rows := make([][]string, 0, len(list))
				for _, e := range list {
					rows = append(rows, []string{e.ID, e.Name, e.Category, fmt.Sprintf("%d", len(e.Charities))})
				}
				output.Table(w, []string{"ID", "NAME", "CATEGORY", "#CHARITIES"}, rows)
			})
		},
	}
}

func newCauseETFsGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Fetch a single Cause ETF by id",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			ctx, cancel := baseContext()
			defer cancel()

			resp, err := app.apiClient().Do(ctx, "GET", client.ResourcePath(client.PathCauseETFs, args[0]), nil, false)
			if err != nil {
				return err
			}
			var e causeETF
			if err := resp.DecodeInto(&e); err != nil {
				return err
			}
			return app.Printer.Result(e, func(w io.Writer) {
				output.KeyValues(w, [][2]string{
					{"id", e.ID},
					{"name", e.Name},
					{"category", e.Category},
					{"description", e.Description},
					{"charities", fmt.Sprintf("%d charities", len(e.Charities))},
				})
			})
		},
	}
}
