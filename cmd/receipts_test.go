package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// receiptWithGroups is a get_receipt answer whose deductible total includes the
// donor's group-wallet contributions (100.00 + 50.00 + 30.00).
const receiptWithGroups = `{
	"source": "givmo_account",
	"tax_year": 2025,
	"tax_year_basis": "donor-local charge date (America/New_York when unavailable)",
	"recipient": {"name": "Givmo Charitable Fund", "ein": "99-2418877"},
	"deductible_total": "180.00",
	"currency": "USD",
	"deductible_contributions": {
		"direct_donations": {"count": 3, "total": "100.00"},
		"wallet_deposits": {"count": 2, "total": "50.00"},
		"group_contributions": {"count": 4, "total": "30.00"}
	},
	"grant_recommendations": {"count": 1, "total": "25.00"},
	"is_official_receipt": false,
	"disclaimer": "Informational only."
}`

func TestReceiptSummary_RendersGroupContributions(t *testing.T) {
	var r receiptSummary
	mustUnmarshal(t, receiptWithGroups, &r)

	var human bytes.Buffer
	renderReceiptSummary(&human, r)
	if !strings.Contains(human.String(), "group_contributions:") || !strings.Contains(human.String(), "4 (30.00 USD)") {
		t.Errorf("the table must show group contributions:\n%s", human.String())
	}
	// Breakdown order: direct, wallet, group, then grant recommendations.
	text := human.String()
	if !(strings.Index(text, "wallet_deposits") < strings.Index(text, "group_contributions") &&
		strings.Index(text, "group_contributions") < strings.Index(text, "grant_recommendations")) {
		t.Errorf("group contributions belong after wallet deposits:\n%s", text)
	}

	out, _ := json.Marshal(r)
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	contributions, _ := back["deductible_contributions"].(map[string]any)
	group, _ := contributions["group_contributions"].(map[string]any)
	if group["count"] != float64(4) || group["total"] != "30.00" {
		t.Errorf("--json must carry group_contributions: %s", out)
	}
}

func TestReceiptSummary_OmitsGroupContributionsTheServerDidNotSend(t *testing.T) {
	const older = `{
		"tax_year": 2025,
		"deductible_total": "150.00",
		"currency": "USD",
		"deductible_contributions": {
			"direct_donations": {"count": 3, "total": "100.00"},
			"wallet_deposits": {"count": 2, "total": "50.00"}
		}
	}`
	var r receiptSummary
	mustUnmarshal(t, older, &r)
	if r.DeductibleContributions.GroupContributions != nil {
		t.Fatal("an absent group_contributions must stay unset")
	}
	var human bytes.Buffer
	renderReceiptSummary(&human, r)
	if strings.Contains(human.String(), "group_contributions") {
		t.Errorf("the table must not invent group contributions:\n%s", human.String())
	}
	out, _ := json.Marshal(r)
	if strings.Contains(string(out), "group_contributions") {
		t.Errorf("--json must not invent group contributions: %s", out)
	}
}

// TestValidateTaxYear_EasternTimeBoundary pins the server's rule at New Year: the
// latest accepted year is the year after the current Eastern-time year, whatever
// the local clock says.
func TestValidateTaxYear_EasternTimeBoundary(t *testing.T) {
	// 2026-12-31 23:30 in Honolulu is already 2027-01-01 in New York.
	honolulu := time.FixedZone("HST", -10*60*60)
	lateDecember := time.Date(2026, 12, 31, 23, 30, 0, 0, honolulu)
	if err := validateTaxYearAt(2028, lateDecember); err != nil {
		t.Errorf("2028 is next year in Eastern time and must pass: %v", err)
	}
	if err := validateTaxYearAt(2029, lateDecember); err == nil {
		t.Error("2029 is two years ahead in Eastern time and must fail")
	}

	// 2027-01-01 00:30 in Tokyo is still 2026-12-31 in New York.
	tokyo := time.FixedZone("JST", 9*60*60)
	earlyJanuary := time.Date(2027, 1, 1, 0, 30, 0, 0, tokyo)
	if err := validateTaxYearAt(2027, earlyJanuary); err != nil {
		t.Errorf("2027 is next year in Eastern time and must pass: %v", err)
	}
	if err := validateTaxYearAt(2028, earlyJanuary); err == nil {
		t.Error("2028 is two years ahead in Eastern time and must fail")
	}
	if err := validateTaxYearAt(1999, earlyJanuary); err == nil {
		t.Error("before 2000 must fail")
	}
}
