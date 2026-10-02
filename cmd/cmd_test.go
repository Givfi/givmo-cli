package cmd

import (
	"testing"

	"github.com/givfi/givmo-cli/internal/output"
)

func TestBuildCreateIntentArgs_ValidAndInvalid(t *testing.T) {
	// Valid (charity target).
	args, err := buildCreateIntentArgs("ch_1", "", 2500, "idem-1", "")
	if err != nil {
		t.Fatalf("valid intent args errored: %v", err)
	}
	if args["charity_id"] != "ch_1" {
		t.Errorf("charity_id = %v", args["charity_id"])
	}
	if args["amount_cents"].(int) != 2500 {
		t.Errorf("amount_cents = %v", args["amount_cents"])
	}
	if args["idempotency_key"] != "idem-1" {
		t.Errorf("idempotency_key = %v", args["idempotency_key"])
	}
	if _, present := args["cause_etf_id"]; present {
		t.Error("cause_etf_id must be absent when a charity is the target")
	}
	// MONEY-SAFETY: the tool arguments must NEVER carry anything that could
	// authorize money — no card, client_secret, token, or terms acceptance.
	for _, forbidden := range []string{"card", "client_secret", "token", "accept_terms", "dn_", "payment_method"} {
		if _, present := args[forbidden]; present {
			t.Errorf("intent args must not carry money-authorizing field %q", forbidden)
		}
	}

	// Valid (cause-etf target) + return_url.
	args, err = buildCreateIntentArgs("", "cetf_9", 500, "idem-2", "https://x.test/return")
	if err != nil {
		t.Fatalf("valid etf intent args errored: %v", err)
	}
	if args["cause_etf_id"] != "cetf_9" {
		t.Errorf("cause_etf_id = %v", args["cause_etf_id"])
	}
	if _, present := args["charity_id"]; present {
		t.Error("charity_id must be absent when a cause-etf is the target")
	}
	if args["return_url"] != "https://x.test/return" {
		t.Errorf("return_url = %v", args["return_url"])
	}

	// Exactly-one-target: neither is a usage error.
	if _, err := buildCreateIntentArgs("", "", 100, "k", ""); err == nil {
		t.Error("expected error for no target")
	} else if output.AsError(err).Code != output.ExitUsage {
		t.Errorf("no target should be usage error, got %d", output.AsError(err).Code)
	}
	// Both targets is a usage error.
	if _, err := buildCreateIntentArgs("ch_1", "cetf_1", 100, "k", ""); err == nil {
		t.Error("expected error for both targets")
	}
	// Non-positive amount.
	if _, err := buildCreateIntentArgs("ch_1", "", 0, "k", ""); err == nil {
		t.Error("expected error for zero amount")
	}
	if _, err := buildCreateIntentArgs("ch_1", "", -5, "k", ""); err == nil {
		t.Error("expected error for negative amount")
	}
	// Empty idempotency key.
	if _, err := buildCreateIntentArgs("ch_1", "", 100, "  ", ""); err == nil {
		t.Error("expected error for empty idempotency key")
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

func TestValidateTaxYear(t *testing.T) {
	// 0 is allowed now: it means "let the server default to the current year".
	if err := validateTaxYear(0); err != nil {
		t.Errorf("zero tax year should be allowed (default): %v", err)
	}
	if err := validateTaxYear(1999); err == nil {
		t.Error("out-of-range low year should error")
	}
	if err := validateTaxYear(2024); err != nil {
		t.Errorf("2024 should be valid: %v", err)
	}
}

func TestBuildLogsQuery(t *testing.T) {
	q, err := buildLogsQuery([]string{"tool_name=create_donation_intent", "outcome=error", "audience=consumer"})
	if err != nil {
		t.Fatalf("buildLogsQuery: %v", err)
	}
	if q.Get("tool_name") != "create_donation_intent" || q.Get("outcome") != "error" {
		t.Errorf("query wrong: %v", q)
	}
	// The audience filter (additive backend param) is accepted.
	if q.Get("audience") != "consumer" {
		t.Errorf("audience filter not passed: %v", q)
	}
	// The OLD key names are no longer accepted (renamed to backend params).
	if _, err := buildLogsQuery([]string{"tool=x"}); err == nil {
		t.Error("legacy key 'tool' should be rejected (now tool_name)")
	}
	if _, err := buildLogsQuery([]string{"principal=x"}); err == nil {
		t.Error("legacy key 'principal' should be rejected (now principal_client_id)")
	}
	// Unknown key rejected.
	if _, err := buildLogsQuery([]string{"badkey=x"}); err == nil {
		t.Error("unknown filter key should be rejected")
	}
	// Missing '=' rejected.
	if _, err := buildLogsQuery([]string{"tool_name"}); err == nil {
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

// TestBuildCreateIntentArgs_ConformToTheToolSchema pins the arguments against the
// create_donation_intent input schema: a closed object (additionalProperties false)
// whose properties are amount_cents, charity_id, cause_etf_id, idempotency_key,
// return_url and metadata, with amount_cents and idempotency_key required. A key
// outside it would be refused as invalid_arguments.
func TestBuildCreateIntentArgs_ConformToTheToolSchema(t *testing.T) {
	properties := map[string]bool{
		"amount_cents": true, "charity_id": true, "cause_etf_id": true,
		"idempotency_key": true, "return_url": true, "metadata": true,
	}
	for _, args := range []map[string]any{
		mustArgs(t, "ch_1", "", 2500, "k-1", ""),
		mustArgs(t, "", "cetf_1", 500, "k-2", "https://pay.givmo.io/thanks"),
	} {
		for key := range args {
			if !properties[key] {
				t.Errorf("argument %q is not in the tool's input schema", key)
			}
		}
		for _, required := range []string{"amount_cents", "idempotency_key"} {
			if _, ok := args[required]; !ok {
				t.Errorf("required argument %q missing: %+v", required, args)
			}
		}
		if _, ok := args["amount_cents"].(int); !ok {
			t.Errorf("amount_cents must be an integer: %T", args["amount_cents"])
		}
	}
}

func mustArgs(t *testing.T, charity, etf string, amount int, key, returnURL string) map[string]any {
	t.Helper()
	args, err := buildCreateIntentArgs(charity, etf, amount, key, returnURL)
	if err != nil {
		t.Fatalf("buildCreateIntentArgs: %v", err)
	}
	return args
}
