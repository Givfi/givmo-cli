package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/givfi/givmo-cli/internal/output"
	"github.com/spf13/cobra"
)

// donationIntent is the CLI's view of a created donation intent, mapped from the
// create_donation_intent MCP tool result.
//
// MONEY-SAFETY CONTRACT: the ONLY actionable field is CheckoutURL — a secretless,
// single-use, Givmo-hosted checkout link carrying an opaque `gco_` token. The
// human completes payment and accepts terms on that page. This struct has NO field
// for a card, a client_secret, or a terms-acceptance token; the tool never returns
// a client_secret on this path. DonationIntentID (a `dn_…` id) is a non-secret
// resource reference — surfaced for correlation, NEVER an authorizer and never
// placed in a URL.
type donationIntent struct {
	DonationIntentID string `json:"donation_intent_id"`
	Status           string `json:"status,omitempty"`
	AmountCents      int    `json:"amount_cents"`
	Currency         string `json:"currency,omitempty"`
	CharityID        string `json:"charity_id,omitempty"`
	CauseETFID       string `json:"cause_etf_id,omitempty"`
	// CheckoutURL is the secretless gco_ hosted-checkout URL to display/open. It is
	// empty on an idempotent replay of an already-closed intent (Status carries the
	// real state, e.g. "succeeded").
	CheckoutURL string `json:"checkout_url"`
	ExpiresAt   string `json:"expires_at,omitempty"`
}

var (
	diCharity   string
	diCauseETF  string
	diAmount    int
	diIdemKey   string
	diReturnURL string
	diOpen      bool
)

func newDonationIntentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "donation-intents",
		Aliases: []string{"donation-intent", "intents"},
		Short:   "Create a donation intent (secretless hosted checkout)",
		Long: `Create a donation intent via the create_donation_intent MCP tool.

MONEY SAFETY: 'create' returns a secretless, single-use hosted-checkout URL (a
gco_ token). Payment and terms acceptance happen on the Givmo-hosted page in a
browser. This CLI never handles a card, a client secret, or a donation id, and
never accepts terms — it only displays (or, with --open, opens) the checkout URL.

Requires the givmo.donation_intents.create scope (run 'givmo login').`,
	}
	cmd.AddCommand(newDonationIntentsCreateCmd(), newDonationIntentsListCmd())
	return cmd
}

// buildCreateIntentArgs constructs the create_donation_intent tool arguments.
// Pure/deterministic so it is unit-tested without any network. It emits ONLY
// non-money-authorizing fields (amount, exactly-one target, idempotency key,
// optional return_url) — never anything that could authorize money.
func buildCreateIntentArgs(charityID, causeETFID string, amountCents int, idemKey, returnURL string) (map[string]any, error) {
	charityID = strings.TrimSpace(charityID)
	causeETFID = strings.TrimSpace(causeETFID)
	if (charityID == "") == (causeETFID == "") {
		which := "neither"
		if charityID != "" {
			which = "both"
		}
		return nil, output.New(output.ExitUsage,
			"provide exactly one donation target (you provided "+which+")",
			"Pass --charity <ch_id> OR --cause-etf <cetf_id> (exactly one). Find ids with `givmo charities search` / `givmo cause-etfs list`.")
	}
	if amountCents <= 0 {
		return nil, output.New(output.ExitUsage, "amount must be a positive integer number of cents",
			"Pass --amount <cents>, e.g. --amount 2500 for $25.00 (minimum $5.00).")
	}
	if strings.TrimSpace(idemKey) == "" {
		return nil, output.New(output.ExitGeneric, "an idempotency key is required", "")
	}
	args := map[string]any{
		"amount_cents":    amountCents,
		"idempotency_key": strings.TrimSpace(idemKey),
	}
	if charityID != "" {
		args["charity_id"] = charityID
	} else {
		args["cause_etf_id"] = causeETFID
	}
	if returnURL = strings.TrimSpace(returnURL); returnURL != "" {
		args["return_url"] = returnURL
	}
	return args, nil
}

// newIdempotencyKey mints a random idempotency key for a create when the caller
// did not supply one. It is echoed back so a retry can reuse it (reusing the same
// key returns the same donation instead of creating a second charge-able one).
func newIdempotencyKey() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "cli-" + hex.EncodeToString(buf), nil
}

func newDonationIntentsCreateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create (--charity <ch_id> | --cause-etf <cetf_id>) --amount <cents>",
		Short: "Create a donation intent and print the hosted-checkout URL",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			idemKey := strings.TrimSpace(diIdemKey)
			generated := false
			if idemKey == "" {
				idemKey, err = newIdempotencyKey()
				if err != nil {
					return output.New(output.ExitGeneric, "could not generate an idempotency key: "+err.Error(), "")
				}
				generated = true
			}
			toolArgs, err := buildCreateIntentArgs(diCharity, diCauseETF, diAmount, idemKey, diReturnURL)
			if err != nil {
				return err
			}
			app.prodBanner("creating a donation intent")

			ctx, cancel := baseContext()
			defer cancel()

			payload, err := app.callTool(ctx, "create_donation_intent", toolArgs, true, "givmo.donation_intents.create")
			if err != nil {
				return err
			}
			var di donationIntent
			if uerr := decodeToolPayload(payload, &di); uerr != nil {
				return uerr
			}

			// checkout_url is empty ONLY on an idempotent replay of an already-closed
			// intent — Status carries the real state (e.g. "succeeded"). Treat that as
			// an informational success, not a failure.
			if di.CheckoutURL == "" {
				if di.Status != "" {
					return app.Printer.Result(di, func(w io.Writer) {
						fmt.Fprintf(w, "This donation intent is already %q; no checkout is needed.\n", di.Status)
						donationIntentKeyValues(w, di)
					})
				}
				return output.New(output.ExitGeneric,
					"the server did not return a hosted-checkout URL",
					"Retry; if it persists, report the request to Givmo support.")
			}

			if diOpen {
				if oerr := openBrowser(di.CheckoutURL); oerr != nil {
					fmt.Fprintf(app.Printer.Err, "(could not open browser: %v — open the URL below manually)\n", oerr)
				}
			}
			return app.Printer.Result(di, func(w io.Writer) {
				fmt.Fprintln(w, "Donation intent created. Complete payment + terms in the browser:")
				fmt.Fprintf(w, "\n  %s\n\n", di.CheckoutURL)
				donationIntentKeyValues(w, di)
				if generated {
					fmt.Fprintf(w, "idempotency_key: %s (reuse it on a retry to avoid a second charge)\n", idemKey)
				}
				fmt.Fprintln(w, "\nNote: this CLI never handles your card or accepts terms; that happens on the Givmo-hosted page.")
			})
		},
	}
	cmd.Flags().StringVar(&diCharity, "charity", "", "opaque charity id (ch_…) to donate to")
	cmd.Flags().StringVar(&diCauseETF, "cause-etf", "", "opaque cause-ETF id (cetf_…) to donate to")
	cmd.Flags().IntVar(&diAmount, "amount", 0, "donation amount in cents (required; minimum 500 = $5.00)")
	cmd.Flags().StringVar(&diIdemKey, "idempotency-key", "", "idempotency key (auto-generated if omitted; reuse on retry to avoid a double charge)")
	cmd.Flags().StringVar(&diReturnURL, "return-url", "", "optional https URL to return to after checkout")
	cmd.Flags().BoolVar(&diOpen, "open", false, "open the hosted-checkout URL in a browser")
	return cmd
}

// donationIntentKeyValues renders the non-secret fields of a created intent.
func donationIntentKeyValues(w io.Writer, di donationIntent) {
	target := di.CharityID
	if target == "" {
		target = di.CauseETFID
	}
	output.KeyValues(w, [][2]string{
		{"donation_intent_id", di.DonationIntentID},
		{"status", di.Status},
		{"target", target},
		{"amount_cents", fmt.Sprintf("%d", di.AmountCents)},
		{"currency", di.Currency},
		{"expires_at", di.ExpiresAt},
	})
}

func newDonationIntentsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List donation intents (not available to a consumer credential)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			// Honest degradation: there is no consumer MCP tool to list a donor's own
			// donation intents. Listing intents is a partner (S2S) Connect operation
			// (GET /connect/donation-intents), which a consumer credential cannot call.
			return output.New(output.ExitUsage,
				"listing donation intents is not available on the consumer surface",
				"Listing donation intents is a partner (server-to-server) Connect operation "+
					"(GET /connect/donation-intents) that a consumer credential cannot call. "+
					"To review your completed giving, run `givmo receipts summary --tax-year <YYYY>`.")
		},
	}
}
