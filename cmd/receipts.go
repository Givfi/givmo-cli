package cmd

import (
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/givfi/givmo-cli/internal/output"
	"github.com/spf13/cobra"
)

// receiptSummary is the CLI's view of the get_receipt tool result — the donor's
// CONSOLIDATED tax-deductible giving to Givmo Charitable Fund for a tax year (an
// informational summary, NOT a list of per-donation receipts and NOT an official
// receipt). Money fields are decimal-dollar STRINGS (never cents), matching the
// tool's serialization.
type receiptSummary struct {
	TaxYear      int    `json:"tax_year"`
	TaxYearBasis string `json:"tax_year_basis"`
	Recipient    struct {
		Name string `json:"name"`
		EIN  string `json:"ein"`
	} `json:"recipient"`
	DeductibleTotal         string `json:"deductible_total"`
	Currency                string `json:"currency"`
	DeductibleContributions struct {
		DirectDonations struct {
			Count int    `json:"count"`
			Total string `json:"total"`
		} `json:"direct_donations"`
		WalletDeposits struct {
			Count int    `json:"count"`
			Total string `json:"total"`
		} `json:"wallet_deposits"`
		// GroupContributions is the donor's own contributions to group wallets,
		// part of deductible_total. Nil when the server's answer omits it, and
		// then not shown.
		GroupContributions *struct {
			Count int    `json:"count"`
			Total string `json:"total"`
		} `json:"group_contributions,omitempty"`
	} `json:"deductible_contributions"`
	GrantRecommendations struct {
		Count int    `json:"count"`
		Total string `json:"total"`
	} `json:"grant_recommendations"`
	IsOfficialReceipt bool   `json:"is_official_receipt"`
	Disclaimer        string `json:"disclaimer"`
}

var receiptsTaxYear int

func newReceiptsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "receipts",
		Short: "Show your tax-deductible giving summary (requires givmo.receipts.read)",
		Long: `Show your consolidated tax-deductible giving to Givmo Charitable Fund for a
tax year, via the get_receipt MCP tool.

This is a single consolidated summary (deductible total + a breakdown into
direct donations, wallet deposits and group-wallet contributions for the
recipient of record, the Givmo Charitable Fund) — NOT a list of per-donation
receipts, and NOT an official receipt. The Fund's emailed acknowledgments are the
authoritative record.

Requires the givmo.receipts.read scope (run 'givmo login').`,
	}
	cmd.AddCommand(newReceiptsSummaryCmd())
	return cmd
}

// validateTaxYear bounds an explicitly-provided tax year as the server does: from
// 2000 through the year after the current Eastern-time year. A zero year is allowed
// and means "let the server default to the current tax year" (the tool omits the
// arg). Pure → unit-tested.
func validateTaxYear(y int) error {
	return validateTaxYearAt(y, time.Now())
}

// validateTaxYearAt is validateTaxYear at the instant now.
func validateTaxYearAt(y int, now time.Time) error {
	if y == 0 {
		return nil
	}
	if y < 2000 || y > easternYear(now)+1 {
		return output.New(output.ExitValidation,
			fmt.Sprintf("tax year %d is out of range", y),
			"Pass a four-digit tax year within a reasonable range, or omit --tax-year for the current year.")
	}
	return nil
}

// easternYear is the calendar year in US Eastern time at now, the server's tax-year
// boundary. Eastern time is UTC-5 around every New Year (daylight time runs from
// March to November), so a fixed offset gives the exact year without a time-zone
// database.
func easternYear(now time.Time) int {
	return now.UTC().Add(-5 * time.Hour).Year()
}

func newReceiptsSummaryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "summary [--tax-year <YYYY>]",
		Aliases: []string{"list"},
		Short:   "Show the tax-deductible giving summary for a tax year",
		Args:    cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			if err := validateTaxYear(receiptsTaxYear); err != nil {
				return err
			}
			toolArgs := map[string]any{}
			if receiptsTaxYear != 0 {
				toolArgs["tax_year"] = receiptsTaxYear
			}
			ctx, cancel := toolContext()
			defer cancel()

			payload, err := app.callTool(ctx, "get_receipt", toolArgs, true, "givmo.receipts.read")
			if err != nil {
				return err
			}
			var r receiptSummary
			if uerr := decodeToolPayload(payload, &r); uerr != nil {
				return uerr
			}
			return app.Printer.Result(r, func(w io.Writer) { renderReceiptSummary(w, r) })
		},
	}
	cmd.Flags().IntVar(&receiptsTaxYear, "tax-year", 0, "tax year (YYYY); omit for the current year")
	return cmd
}

// renderReceiptSummary is the human form of a receipt summary.
func renderReceiptSummary(w io.Writer, r receiptSummary) {
	contributions := r.DeductibleContributions
	pairs := [][2]string{
		{"tax_year", strconv.Itoa(r.TaxYear)},
		{"tax_year_basis", r.TaxYearBasis},
		{"recipient", r.Recipient.Name},
		{"recipient_ein", r.Recipient.EIN},
		{"deductible_total", moneyWithCurrency(r.DeductibleTotal, r.Currency)},
		{"direct_donations", fmt.Sprintf("%d (%s)", contributions.DirectDonations.Count, moneyWithCurrency(contributions.DirectDonations.Total, r.Currency))},
		{"wallet_deposits", fmt.Sprintf("%d (%s)", contributions.WalletDeposits.Count, moneyWithCurrency(contributions.WalletDeposits.Total, r.Currency))},
	}
	if g := contributions.GroupContributions; g != nil {
		pairs = append(pairs, [2]string{"group_contributions", fmt.Sprintf("%d (%s)", g.Count, moneyWithCurrency(g.Total, r.Currency))})
	}
	pairs = append(pairs,
		[2]string{"grant_recommendations", fmt.Sprintf("%d (%s)", r.GrantRecommendations.Count, moneyWithCurrency(r.GrantRecommendations.Total, r.Currency))},
		[2]string{"is_official_receipt", fmt.Sprintf("%t", r.IsOfficialReceipt)},
	)
	output.KeyValues(w, pairs)
	if r.Disclaimer != "" {
		fmt.Fprintf(w, "\n%s\n", r.Disclaimer)
	}
}

// moneyWithCurrency renders a decimal-dollar money string with its currency,
// tolerating an empty value.
func moneyWithCurrency(amount, currency string) string {
	if amount == "" {
		amount = "0"
	}
	if currency == "" {
		return amount
	}
	return amount + " " + currency
}
