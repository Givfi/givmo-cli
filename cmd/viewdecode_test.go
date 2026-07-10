package cmd

import (
	"encoding/json"
	"testing"
)

// These tests pin the CLI view-struct JSON tags against the REAL MCP tool output
// shapes. Each fixture's field names are copied verbatim from the backend tool source
// (givmo-backend-python origin/development):
//
//   - search_charities / get_charity_profile → app/application/assistant/catalog_tools.py
//     (_catalog_result + the search/profile envelopes; irs_pub78_status snapshot)
//   - list_cause_etfs / get_cause_etf        → app/application/assistant/catalog_tools.py
//   - get_receipt                            → app/application/assistant/account_tools.py
//
// Every field the CLI actually DISPLAYS is set to a distinctive value and asserted after
// decode, so a future tag typo (e.g. `charity_id`→`charityId`, `deductible_total`→
// `deductibleTotal`) leaves the field at its zero value and fails CI — turning a silent
// blank column into a red build.

func mustUnmarshal(t *testing.T, data string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(data), v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
}

func TestDecode_CharityProfile(t *testing.T) {
	// get_charity_profile output: {source, provenance, charity{…, irs_pub78_status{…}}}.
	const payload = `{
		"source": "givmo_catalog",
		"provenance": "Givmo platform catalog",
		"charity": {
			"charity_id": "ch_abc",
			"name": "Clean Water Fund",
			"ein": "12-3456789",
			"categories": ["Environment", "Water"],
			"state": "CA",
			"mission": "Bring clean water to all.",
			"website": "https://example.org",
			"logo_url": "https://example.org/logo.png",
			"irs_pub78_status": {
				"pub78_listed": true,
				"deductibility_codes": "PC",
				"auto_revoked": true,
				"as_of": "2026-01-15"
			}
		}
	}`
	var res charityProfileResult
	mustUnmarshal(t, payload, &res)

	if res.Provenance != "Givmo platform catalog" {
		t.Errorf("provenance = %q", res.Provenance)
	}
	ch := res.Charity
	if ch.CharityID != "ch_abc" {
		t.Errorf("charity_id = %q", ch.CharityID)
	}
	if ch.Name != "Clean Water Fund" {
		t.Errorf("name = %q", ch.Name)
	}
	if ch.EIN != "12-3456789" {
		t.Errorf("ein = %q", ch.EIN)
	}
	if len(ch.Categories) != 2 || ch.Categories[0] != "Environment" {
		t.Errorf("categories = %+v", ch.Categories)
	}
	if ch.State != "CA" {
		t.Errorf("state = %q", ch.State)
	}
	if ch.Mission == "" {
		t.Error("mission did not decode")
	}
	if ch.Website == "" {
		t.Error("website did not decode")
	}
	if ch.LogoURL == "" {
		t.Error("logo_url did not decode")
	}
	if ch.Pub78 == nil {
		t.Fatal("irs_pub78_status did not decode")
	}
	if !ch.Pub78.Pub78Listed {
		t.Error("pub78_listed did not decode")
	}
	if ch.Pub78.DeductibilityCodes != "PC" {
		t.Errorf("deductibility_codes = %q", ch.Pub78.DeductibilityCodes)
	}
	if !ch.Pub78.AutoRevoked {
		t.Error("auto_revoked did not decode")
	}
	if ch.Pub78.AsOf != "2026-01-15" {
		t.Errorf("as_of = %q", ch.Pub78.AsOf)
	}
}

func TestDecode_CharitySearch(t *testing.T) {
	// search_charities output: {source, provenance, query, ein, count, charities[…]}.
	const payload = `{
		"source": "givmo_catalog",
		"provenance": "Givmo platform catalog",
		"query": "water",
		"ein": "12-3456789",
		"count": 1,
		"charities": [
			{"charity_id": "ch_abc", "name": "Clean Water Fund", "ein": "12-3456789",
			 "categories": ["Water"], "state": "CA", "mission": "m",
			 "website": "https://example.org", "logo_url": "https://example.org/l.png"}
		]
	}`
	var res charitySearchResult
	mustUnmarshal(t, payload, &res)

	if res.Provenance == "" {
		t.Error("provenance did not decode")
	}
	if res.Query != "water" {
		t.Errorf("query = %q", res.Query)
	}
	if res.EIN != "12-3456789" {
		t.Errorf("ein = %q", res.EIN)
	}
	if res.Count != 1 {
		t.Errorf("count = %d", res.Count)
	}
	if len(res.Charities) != 1 {
		t.Fatalf("charities len = %d", len(res.Charities))
	}
	c := res.Charities[0]
	if c.CharityID != "ch_abc" || c.Name == "" || c.EIN == "" || c.State != "CA" || len(c.Categories) != 1 {
		t.Errorf("charity row decoded wrong: %+v", c)
	}
}

func TestDecode_CauseETFList(t *testing.T) {
	// list_cause_etfs output: {source, cause_etfs[{id, name, description, charity_count}]}.
	const payload = `{
		"source": "givmo_catalog",
		"cause_etfs": [
			{"id": "cetf_1", "name": "Climate Basket", "description": "A curated basket.", "charity_count": 7}
		]
	}`
	var res causeETFListResult
	mustUnmarshal(t, payload, &res)

	if len(res.CauseETFs) != 1 {
		t.Fatalf("cause_etfs len = %d", len(res.CauseETFs))
	}
	e := res.CauseETFs[0]
	if e.ID != "cetf_1" {
		t.Errorf("id = %q", e.ID)
	}
	if e.Name != "Climate Basket" {
		t.Errorf("name = %q", e.Name)
	}
	if e.Description == "" {
		t.Error("description did not decode")
	}
	if e.CharityCount != 7 {
		t.Errorf("charity_count = %d", e.CharityCount)
	}
}

func TestDecode_CauseETFDetail(t *testing.T) {
	// get_cause_etf output: {source, cause_etf{id, name, description, charities[{id, name}]}}.
	const payload = `{
		"source": "givmo_catalog",
		"cause_etf": {
			"id": "cetf_1",
			"name": "Climate Basket",
			"description": "A curated basket.",
			"charities": [
				{"id": "ch_a", "name": "Alpha Org"},
				{"id": "ch_b", "name": "Beta Org"}
			]
		}
	}`
	var res causeETFDetailResult
	mustUnmarshal(t, payload, &res)

	e := res.CauseETF
	if e.ID != "cetf_1" {
		t.Errorf("id = %q", e.ID)
	}
	if e.Name != "Climate Basket" {
		t.Errorf("name = %q", e.Name)
	}
	if e.Description == "" {
		t.Error("description did not decode")
	}
	if len(e.Charities) != 2 {
		t.Fatalf("charities len = %d", len(e.Charities))
	}
	if e.Charities[0].ID != "ch_a" || e.Charities[0].Name != "Alpha Org" {
		t.Errorf("constituent decoded wrong: %+v", e.Charities[0])
	}
}

func TestDecode_ReceiptSummary(t *testing.T) {
	// get_receipt output (account_tools.py). Money fields are decimal-dollar STRINGS.
	const payload = `{
		"source": "givmo_account",
		"tax_year": 2025,
		"tax_year_basis": "America/New_York charge date",
		"recipient": {"name": "Givmo Charitable Fund", "ein": "99-2418877"},
		"deductible_total": "150.00",
		"currency": "USD",
		"deductible_contributions": {
			"direct_donations": {"count": 3, "total": "100.00"},
			"wallet_deposits": {"count": 2, "total": "50.00"}
		},
		"grant_recommendations": {"count": 1, "total": "25.00"},
		"is_official_receipt": true,
		"disclaimer": "This is not an official tax receipt."
	}`
	var r receiptSummary
	mustUnmarshal(t, payload, &r)

	if r.TaxYear != 2025 {
		t.Errorf("tax_year = %d", r.TaxYear)
	}
	if r.TaxYearBasis == "" {
		t.Error("tax_year_basis did not decode")
	}
	if r.Recipient.Name != "Givmo Charitable Fund" {
		t.Errorf("recipient.name = %q", r.Recipient.Name)
	}
	if r.Recipient.EIN != "99-2418877" {
		t.Errorf("recipient.ein = %q", r.Recipient.EIN)
	}
	if r.DeductibleTotal != "150.00" {
		t.Errorf("deductible_total = %q", r.DeductibleTotal)
	}
	if r.Currency != "USD" {
		t.Errorf("currency = %q", r.Currency)
	}
	if r.DeductibleContributions.DirectDonations.Count != 3 || r.DeductibleContributions.DirectDonations.Total != "100.00" {
		t.Errorf("direct_donations decoded wrong: %+v", r.DeductibleContributions.DirectDonations)
	}
	if r.DeductibleContributions.WalletDeposits.Count != 2 || r.DeductibleContributions.WalletDeposits.Total != "50.00" {
		t.Errorf("wallet_deposits decoded wrong: %+v", r.DeductibleContributions.WalletDeposits)
	}
	if r.GrantRecommendations.Count != 1 || r.GrantRecommendations.Total != "25.00" {
		t.Errorf("grant_recommendations decoded wrong: %+v", r.GrantRecommendations)
	}
	if !r.IsOfficialReceipt {
		t.Error("is_official_receipt did not decode")
	}
	if r.Disclaimer == "" {
		t.Error("disclaimer did not decode")
	}
}
