package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/givfi/givmo-cli/internal/client"
	"github.com/givfi/givmo-cli/internal/output"
	"github.com/spf13/cobra"
)

// donationIntent is the CLI's view of a created donation intent.
//
// MONEY-SAFETY CONTRACT: the ONLY actionable field is CheckoutURL — a
// secretless, single-use, Givmo-hosted checkout link carrying an opaque `gco_`
// token. The human completes payment and accepts terms on that page. This
// struct intentionally has NO field for a card, a client_secret, a `dn_`
// donation id, or a terms-acceptance token; the CLI never handles or persists
// anything that could authorize money.
type donationIntent struct {
	IntentID    string `json:"intent_id"`
	CharityID   string `json:"charity_id"`
	AmountCents int    `json:"amount_cents"`
	Currency    string `json:"currency,omitempty"`
	// CheckoutURL is the secretless gco_ hosted-checkout URL to display/open.
	CheckoutURL string `json:"checkout_url"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	Status      string `json:"status,omitempty"`
}

var (
	diCharity string
	diAmount  int
	diOpen    bool
	diLimit   int
)

func newDonationIntentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "donation-intents",
		Aliases: []string{"donation-intent", "intents"},
		Short:   "Create and list donation intents (secretless hosted checkout)",
		Long: `Create a donation intent and list your prior intents.

MONEY SAFETY: 'create' returns a secretless, single-use hosted-checkout URL
(a gco_ token). Payment and terms acceptance happen on the Givmo-hosted page in
a browser. This CLI never handles a card, a client secret, or a donation id, and
never accepts terms — it only displays (or, with --open, opens) the checkout URL.

Requires the givmo.donation_intents.create scope (run 'givmo login').`,
	}
	cmd.AddCommand(newDonationIntentsCreateCmd(), newDonationIntentsListCmd())
	return cmd
}

// buildCreateIntentBody constructs the request body for create_donation_intent.
// Pure/deterministic so it is unit-tested without any network. It emits ONLY
// non-money-authorizing fields.
func buildCreateIntentBody(charityID string, amountCents int) ([]byte, error) {
	if strings.TrimSpace(charityID) == "" {
		return nil, output.New(output.ExitUsage, "a charity id is required",
			"Pass --charity <id>; find ids with `givmo charities search <query>`.")
	}
	if amountCents <= 0 {
		return nil, output.New(output.ExitUsage, "amount must be a positive integer number of cents",
			"Pass --amount <cents>, e.g. --amount 2500 for $25.00.")
	}
	body := map[string]any{
		"charity_id":   charityID,
		"amount_cents": amountCents,
	}
	return json.Marshal(body)
}

func newDonationIntentsCreateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create --charity <id> --amount <cents>",
		Short: "Create a donation intent and print the hosted-checkout URL",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			body, err := buildCreateIntentBody(diCharity, diAmount)
			if err != nil {
				return err
			}
			app.prodBanner("creating a donation intent")

			ctx, cancel := baseContext()
			defer cancel()

			// requireAuth=true: creating an intent needs the consumer scope.
			resp, err := app.apiClient().Do(ctx, "POST", client.PathDonationIntents, body, true)
			if err != nil {
				return err
			}
			var di donationIntent
			if err := resp.DecodeInto(&di); err != nil {
				return err
			}
			if di.CheckoutURL == "" {
				return output.New(output.ExitGeneric,
					"the server did not return a hosted-checkout URL",
					"Retry; if it persists, report the request_id to Givmo support.").
					WithRequestID(resp.RequestID)
			}

			if diOpen {
				if oerr := openBrowser(di.CheckoutURL); oerr != nil {
					fmt.Fprintf(app.Printer.Err, "(could not open browser: %v — open the URL below manually)\n", oerr)
				}
			}
			return app.Printer.Result(di, func(w io.Writer) {
				fmt.Fprintln(w, "Donation intent created. Complete payment + terms in the browser:")
				fmt.Fprintf(w, "\n  %s\n\n", di.CheckoutURL)
				output.KeyValues(w, [][2]string{
					{"intent_id", di.IntentID},
					{"charity_id", di.CharityID},
					{"amount_cents", fmt.Sprintf("%d", di.AmountCents)},
					{"expires_at", di.ExpiresAt},
				})
				fmt.Fprintln(w, "\nNote: this CLI never handles your card or accepts terms; that happens on the Givmo-hosted page.")
			})
		},
	}
	cmd.Flags().StringVar(&diCharity, "charity", "", "charity id to donate to (required)")
	cmd.Flags().IntVar(&diAmount, "amount", 0, "donation amount in cents (required)")
	cmd.Flags().BoolVar(&diOpen, "open", false, "open the hosted-checkout URL in a browser")
	return cmd
}

func newDonationIntentsListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List your donation intents",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			path := client.PathDonationIntents
			if diLimit > 0 {
				path = fmt.Sprintf("%s?page[limit]=%d", path, diLimit)
			}
			ctx, cancel := baseContext()
			defer cancel()

			resp, err := app.apiClient().Do(ctx, "GET", path, nil, true)
			if err != nil {
				return err
			}
			var list []donationIntent
			if err := resp.DecodeInto(&list); err != nil {
				return err
			}
			return app.Printer.Result(list, func(w io.Writer) {
				if len(list) == 0 {
					fmt.Fprintln(w, "no donation intents")
					return
				}
				rows := make([][]string, 0, len(list))
				for _, di := range list {
					rows = append(rows, []string{di.IntentID, di.CharityID, fmt.Sprintf("%d", di.AmountCents), di.Status})
				}
				output.Table(w, []string{"INTENT_ID", "CHARITY", "AMOUNT_CENTS", "STATUS"}, rows)
			})
		},
	}
	cmd.Flags().IntVar(&diLimit, "limit", 0, "maximum results to return")
	return cmd
}
