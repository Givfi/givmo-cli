package cmd

import (
	"encoding/json"
	"testing"

	"github.com/givfi/givmo-cli/internal/output"
)

func TestBuildCreateIntentBody_ValidAndInvalid(t *testing.T) {
	// Valid.
	body, err := buildCreateIntentBody("c_1", 2500)
	if err != nil {
		t.Fatalf("valid intent body errored: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if got["charity_id"] != "c_1" {
		t.Errorf("charity_id = %v", got["charity_id"])
	}
	if got["amount_cents"].(float64) != 2500 {
		t.Errorf("amount_cents = %v", got["amount_cents"])
	}
	// MONEY-SAFETY: the request body must NEVER carry anything that could
	// authorize money — no card, client_secret, token, or terms acceptance.
	for _, forbidden := range []string{"card", "client_secret", "token", "accept_terms", "dn_", "payment_method"} {
		if _, present := got[forbidden]; present {
			t.Errorf("intent body must not carry money-authorizing field %q", forbidden)
		}
	}

	// Missing charity.
	if _, err := buildCreateIntentBody("", 100); err == nil {
		t.Error("expected error for missing charity")
	} else if output.AsError(err).Code != output.ExitUsage {
		t.Errorf("missing charity should be usage error, got %d", output.AsError(err).Code)
	}
	// Non-positive amount.
	if _, err := buildCreateIntentBody("c_1", 0); err == nil {
		t.Error("expected error for zero amount")
	}
	if _, err := buildCreateIntentBody("c_1", -5); err == nil {
		t.Error("expected error for negative amount")
	}
}

func TestParseOverrides(t *testing.T) {
	got, err := parseOverrides([]string{
		"amount_cents=2500",
		"charity.id=c_9",
		"charity.name=Water Fund",
		"flag=true",
	})
	if err != nil {
		t.Fatalf("parseOverrides: %v", err)
	}
	if got["amount_cents"].(float64) != 2500 {
		t.Errorf("amount_cents = %v (want number)", got["amount_cents"])
	}
	if got["flag"] != true {
		t.Errorf("flag = %v (want bool true)", got["flag"])
	}
	charity, ok := got["charity"].(map[string]any)
	if !ok {
		t.Fatalf("charity should nest into a map, got %T", got["charity"])
	}
	if charity["id"] != "c_9" || charity["name"] != "Water Fund" {
		t.Errorf("nested override wrong: %+v", charity)
	}

	// Missing '=' is a usage error.
	if _, err := parseOverrides([]string{"noequals"}); err == nil {
		t.Error("expected error for override without '='")
	}
	// Empty key is a usage error.
	if _, err := parseOverrides([]string{"=v"}); err == nil {
		t.Error("expected error for empty key")
	}
}

func TestValidateReceiptFormat(t *testing.T) {
	for _, ok := range []string{"", "json", "pdf"} {
		if err := validateReceiptFormat(ok); err != nil {
			t.Errorf("format %q should be valid: %v", ok, err)
		}
	}
	if err := validateReceiptFormat("csv"); err == nil {
		t.Error("csv format should be rejected")
	}
}

func TestValidateTaxYear(t *testing.T) {
	if err := validateTaxYear(0); err == nil {
		t.Error("missing tax year should error")
	}
	if err := validateTaxYear(1999); err == nil {
		t.Error("out-of-range low year should error")
	}
	if err := validateTaxYear(2024); err != nil {
		t.Errorf("2024 should be valid: %v", err)
	}
}

func TestBuildLogsQuery(t *testing.T) {
	q, err := buildLogsQuery([]string{"tool=create_donation_intent", "outcome=denied"})
	if err != nil {
		t.Fatalf("buildLogsQuery: %v", err)
	}
	if q.Get("tool") != "create_donation_intent" || q.Get("outcome") != "denied" {
		t.Errorf("query wrong: %v", q)
	}
	// Unknown key rejected.
	if _, err := buildLogsQuery([]string{"badkey=x"}); err == nil {
		t.Error("unknown filter key should be rejected")
	}
	// Missing '=' rejected.
	if _, err := buildLogsQuery([]string{"tool"}); err == nil {
		t.Error("filter without '=' should be rejected")
	}
}

func TestSignWebhook_DeterministicHMAC(t *testing.T) {
	body := []byte(`{"event":"donation.succeeded"}`)
	got := SignWebhook(DevWebhookSecret, body)
	if got == "" || got[:7] != "sha256=" {
		t.Errorf("signature format wrong: %q", got)
	}
	// Deterministic.
	if SignWebhook(DevWebhookSecret, body) != got {
		t.Error("HMAC signature must be deterministic")
	}
	// Different body -> different signature.
	if SignWebhook(DevWebhookSecret, []byte(`{"event":"other"}`)) == got {
		t.Error("different bodies must produce different signatures")
	}
}

func TestValidHTTPMethods(t *testing.T) {
	for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD"} {
		if !validHTTPMethods[m] {
			t.Errorf("%s should be a valid method", m)
		}
	}
	if validHTTPMethods["TRACE"] {
		t.Error("TRACE should not be allowed")
	}
}

func TestBundledFixtures_Present(t *testing.T) {
	f := bundledFixtures()
	if len(f) == 0 {
		t.Fatal("expected bundled fixtures")
	}
	if fixtureByName(f, "single-donation") == nil {
		t.Error("single-donation fixture must exist")
	}
	if fixtureByName(f, "nonexistent") != nil {
		t.Error("fixtureByName should return nil for unknown")
	}
}

func TestKnownSandboxEvents(t *testing.T) {
	if len(KnownSandboxEvents) == 0 {
		t.Fatal("expected known sandbox events")
	}
	found := false
	for _, e := range KnownSandboxEvents {
		if e == "donation.succeeded" {
			found = true
		}
	}
	if !found {
		t.Error("donation.succeeded should be a known event")
	}
}
