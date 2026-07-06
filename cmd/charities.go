package cmd

import (
	"fmt"
	"io"
	"net/url"
	"strconv"

	"github.com/givfi/givmo-cli/internal/client"
	"github.com/givfi/givmo-cli/internal/output"
	"github.com/spf13/cobra"
)

// charity is the CLI's display view of a charity. It deliberately carries no
// deductibility/eligibility authority — those are resolved by the platform, not
// asserted by any manifest or client-side data.
type charity struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	EIN      string `json:"ein,omitempty"`
	State    string `json:"state,omitempty"`
	NTEE     string `json:"ntee,omitempty"`
	Mission  string `json:"mission,omitempty"`
	Website  string `json:"website,omitempty"`
	Category string `json:"category,omitempty"`
}

var (
	charitiesState string
	charitiesNTEE  string
	charitiesLimit int
)

func newCharitiesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "charities",
		Short: "Search and inspect charities in the public catalog",
		Long:  `Search the Givmo public charity catalog and fetch a single charity by EIN or id. This is public-tier data and needs no login.`,
	}
	cmd.AddCommand(newCharitiesSearchCmd(), newCharitiesGetCmd())
	return cmd
}

func newCharitiesSearchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search charities by name/keyword",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			q := url.Values{}
			q.Set("q", args[0])
			if charitiesState != "" {
				q.Set("state", charitiesState)
			}
			if charitiesNTEE != "" {
				q.Set("ntee", charitiesNTEE)
			}
			if charitiesLimit > 0 {
				q.Set("page[limit]", strconv.Itoa(charitiesLimit))
			}
			ctx, cancel := baseContext()
			defer cancel()

			resp, err := app.apiClient().Do(ctx, "GET", client.CollectionQuery(client.PathCharities, q.Encode()), nil, false)
			if err != nil {
				return err
			}
			var list []charity
			if err := resp.DecodeInto(&list); err != nil {
				return err
			}
			return app.Printer.Result(list, func(w io.Writer) {
				if len(list) == 0 {
					fmt.Fprintln(w, "no charities matched")
					return
				}
				rows := make([][]string, 0, len(list))
				for _, ch := range list {
					rows = append(rows, []string{ch.ID, ch.Name, ch.EIN, ch.State, ch.NTEE})
				}
				output.Table(w, []string{"ID", "NAME", "EIN", "STATE", "NTEE"}, rows)
			})
		},
	}
	cmd.Flags().StringVar(&charitiesState, "state", "", "filter by US state (two-letter)")
	cmd.Flags().StringVar(&charitiesNTEE, "ntee", "", "filter by NTEE code")
	cmd.Flags().IntVar(&charitiesLimit, "limit", 0, "maximum results to return")
	return cmd
}

func newCharitiesGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <ein|id>",
		Short: "Fetch a single charity by EIN or id",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			ctx, cancel := baseContext()
			defer cancel()

			resp, err := app.apiClient().Do(ctx, "GET", client.ResourcePath(client.PathCharities, args[0]), nil, false)
			if err != nil {
				return err
			}
			var ch charity
			if err := resp.DecodeInto(&ch); err != nil {
				return err
			}
			return app.Printer.Result(ch, func(w io.Writer) {
				output.KeyValues(w, [][2]string{
					{"id", ch.ID},
					{"name", ch.Name},
					{"ein", ch.EIN},
					{"state", ch.State},
					{"ntee", ch.NTEE},
					{"category", ch.Category},
					{"website", ch.Website},
					{"mission", ch.Mission},
				})
			})
		},
	}
}
