package cmd

import (
	"fmt"
	"io"
	"net/url"
	"strconv"
	"time"

	"github.com/givfi/givmo-cli/internal/client"
	"github.com/givfi/givmo-cli/internal/output"
	"github.com/spf13/cobra"
)

// receipt is the CLI's view of a tax receipt reference.
type receipt struct {
	ID          string `json:"id"`
	TaxYear     int    `json:"tax_year"`
	CharityName string `json:"charity_name,omitempty"`
	AmountCents int    `json:"amount_cents"`
	IssuedAt    string `json:"issued_at,omitempty"`
	PDFURL      string `json:"pdf_url,omitempty"`
}

var (
	receiptsTaxYear int
	receiptsFormat  string
)

func newReceiptsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "receipts",
		Short: "List tax receipts (requires givmo.receipts.read)",
		Long: `List your tax-deductible donation receipts for a tax year.

--format json  : receipt metadata (default machine form; also implied by --json)
--format pdf   : request the server-rendered PDF references / links

Requires the givmo.receipts.read scope (run 'givmo login').`,
	}
	cmd.AddCommand(newReceiptsListCmd())
	return cmd
}

// validateReceiptFormat validates the --format value. Pure → unit-tested.
func validateReceiptFormat(f string) error {
	switch f {
	case "", "json", "pdf":
		return nil
	default:
		return output.New(output.ExitUsage,
			fmt.Sprintf("invalid --format %q", f),
			"Use --format json or --format pdf.")
	}
}

// validateTaxYear bounds the tax year to a sane range. Pure → unit-tested.
func validateTaxYear(y int) error {
	if y == 0 {
		return output.New(output.ExitUsage, "a tax year is required",
			"Pass --tax-year <YYYY>, e.g. --tax-year "+strconv.Itoa(time.Now().Year()-1)+".")
	}
	if y < 2000 || y > time.Now().Year()+1 {
		return output.New(output.ExitValidation,
			fmt.Sprintf("tax year %d is out of range", y),
			"Pass a four-digit tax year within a reasonable range.")
	}
	return nil
}

func newReceiptsListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list --tax-year <YYYY> [--format json|pdf]",
		Short: "List tax receipts for a tax year",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			if err := validateTaxYear(receiptsTaxYear); err != nil {
				return err
			}
			if err := validateReceiptFormat(receiptsFormat); err != nil {
				return err
			}
			q := url.Values{}
			q.Set("tax_year", strconv.Itoa(receiptsTaxYear))
			if receiptsFormat != "" {
				q.Set("format", receiptsFormat)
			}
			ctx, cancel := baseContext()
			defer cancel()

			resp, err := app.apiClient().Do(ctx, "GET", client.CollectionQuery(client.PathReceipts, q.Encode()), nil, true)
			if err != nil {
				return err
			}
			var list []receipt
			if err := resp.DecodeInto(&list); err != nil {
				return err
			}
			return app.Printer.Result(list, func(w io.Writer) {
				if len(list) == 0 {
					fmt.Fprintf(w, "no receipts for tax year %d\n", receiptsTaxYear)
					return
				}
				rows := make([][]string, 0, len(list))
				for _, r := range list {
					rows = append(rows, []string{r.ID, strconv.Itoa(r.TaxYear), r.CharityName, fmt.Sprintf("%d", r.AmountCents), r.PDFURL})
				}
				output.Table(w, []string{"ID", "TAX_YEAR", "CHARITY", "AMOUNT_CENTS", "PDF_URL"}, rows)
			})
		},
	}
	cmd.Flags().IntVar(&receiptsTaxYear, "tax-year", 0, "tax year (YYYY) to list receipts for (required)")
	cmd.Flags().StringVar(&receiptsFormat, "format", "json", "output detail: json or pdf")
	return cmd
}
