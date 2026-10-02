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

This is a single consolidated summary (deductible total + a direct-vs-wallet
breakdown for the recipient of record, the Givmo Charitable Fund) — NOT a list of
per-donation receipts, and NOT an official receipt. The Fund's emailed
acknowledgments are the authoritative record.

Requires the givmo.receipts.read scope (run 'givmo login').`,
	}
	cmd.AddCommand(newReceiptsSummaryCmd())
	return cmd
}

// validateTaxYear bounds an explicitly-provided tax year. A zero year is allowed
// and means "let the server default to the current tax year" (the tool omits the
// arg). Pure → unit-tested.
func validateTaxYear(y int) error {
	if y == 0 {
		return nil
	}
	if y < 2000 || y > time.Now().Year()+1 {
		return output.New(output.ExitValidation,
			fmt.Sprintf("tax year %d is out of range", y),
			"Pass a four-digit tax year within a reasonable range, or omit --tax-year for the current year.")
	}
	return nil
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
			return app.Printer.Result(r, func(w io.Writer) {
				output.KeyValues(w, [][2]string{
					{"tax_year", strconv.Itoa(r.TaxYear)},
					{"tax_year_basis", r.TaxYearBasis},
					{"recipient", r.Recipient.Name},
					{"recipient_ein", r.Recipient.EIN},
					{"deductible_total", moneyWithCurrency(r.DeductibleTotal, r.Currency)},
					{"direct_donations", fmt.Sprintf("%d (%s)", r.DeductibleContributions.DirectDonations.Count, moneyWithCurrency(r.DeductibleContributions.DirectDonations.Total, r.Currency))},
					{"wallet_deposits", fmt.Sprintf("%d (%s)", r.DeductibleContributions.WalletDeposits.Count, moneyWithCurrency(r.DeductibleContributions.WalletDeposits.Total, r.Currency))},
					{"grant_recommendations", fmt.Sprintf("%d (%s)", r.GrantRecommendations.Count, moneyWithCurrency(r.GrantRecommendations.Total, r.Currency))},
					{"is_official_receipt", fmt.Sprintf("%t", r.IsOfficialReceipt)},
				})
				if r.Disclaimer != "" {
					fmt.Fprintf(w, "\n%s\n", r.Disclaimer)
				}
			})
		},
	}
	cmd.Flags().IntVar(&receiptsTaxYear, "tax-year", 0, "tax year (YYYY); omit for the current year")
	return cmd
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
