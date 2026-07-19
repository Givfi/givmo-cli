package cmd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/givfi/givmo-cli/internal/auth"
	"github.com/givfi/givmo-cli/internal/config"
	"github.com/givfi/givmo-cli/internal/output"
)

// newTestAppCtx builds an appCtx pointed at baseURL with an isolated, file-backed
// (empty) token store. Callers set GIVMO_API_KEY to supply a consumer credential.
func newTestAppCtx(t *testing.T, baseURL string) *appCtx {
	t.Helper()
	t.Setenv("GIVMO_HOME", t.TempDir())
	t.Setenv("GIVMO_TOKEN_BACKEND", "file")
	store, err := auth.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return &appCtx{
		Profile: config.Profile{Name: config.ProfileSandbox, Endpoints: config.Endpoints{APIBase: baseURL}},
		Config:  &config.Config{},
		Printer: output.NewPrinter(io.Discard, io.Discard, false),
		Store:   store,
	}
}

// capturedCall records what a fake MCP endpoint saw.
type capturedCall struct {
	hit  bool
	auth string
	name string
	args map[string]any
}

// rpcServer returns a fake /mcp endpoint that answers a tools/call with the given
// CallToolResult (wrapped in a JSON-RPC envelope) and records the request.
func rpcServer(t *testing.T, result any, cap *capturedCall) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cap != nil {
			cap.hit = true
			cap.auth = r.Header.Get("Authorization")
			body, _ := io.ReadAll(r.Body)
			var req struct {
				Params struct {
					Name string         `json:"name"`
					Args map[string]any `json:"arguments"`
				} `json:"params"`
			}
			_ = json.Unmarshal(body, &req)
			cap.name = req.Params.Name
			cap.args = req.Params.Args
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
}

func textBlock(s string) []map[string]any {
	return []map[string]any{{"type": "text", "text": s}}
}

func TestCallTool_PublicSuccessStructured(t *testing.T) {
	var cap capturedCall
	result := map[string]any{
		"content":           textBlock(`{"source":"givmo_catalog","count":1}`),
		"structuredContent": map[string]any{"source": "givmo_catalog", "count": 1, "charities": []any{}},
		"isError":           false,
	}
	srv := rpcServer(t, result, &cap)
	defer srv.Close()
	app := newTestAppCtx(t, srv.URL)

	payload, err := app.callTool(context.Background(), "search_charities", map[string]any{"query": "water"}, false, "")
	if err != nil {
		t.Fatalf("callTool: %v", err)
	}
	// Public tier: NO Authorization header may be attached.
	if cap.auth != "" {
		t.Errorf("public call must send no Authorization header, got %q", cap.auth)
	}
	if cap.name != "search_charities" {
		t.Errorf("tool name = %q", cap.name)
	}
	if cap.args["query"] != "water" {
		t.Errorf("arguments not forwarded: %+v", cap.args)
	}
	var res struct {
		Source string `json:"source"`
		Count  int    `json:"count"`
	}
	if uerr := json.Unmarshal(payload, &res); uerr != nil {
		t.Fatalf("payload decode: %v", uerr)
	}
	if res.Source != "givmo_catalog" || res.Count != 1 {
		t.Errorf("structured payload wrong: %+v", res)
	}
}

func TestCallTool_TextFallbackWhenNoStructured(t *testing.T) {
	result := map[string]any{
		"content": textBlock(`{"source":"givmo_catalog","count":2}`),
		"isError": false,
	}
	srv := rpcServer(t, result, nil)
	defer srv.Close()
	app := newTestAppCtx(t, srv.URL)

	payload, err := app.callTool(context.Background(), "list_cause_etfs", map[string]any{}, false, "")
	if err != nil {
		t.Fatalf("callTool: %v", err)
	}
	var res struct {
		Count int `json:"count"`
	}
	if uerr := json.Unmarshal(payload, &res); uerr != nil {
		t.Fatalf("payload decode: %v", uerr)
	}
	if res.Count != 2 {
		t.Errorf("text-fallback payload wrong: %+v", res)
	}
}

func TestCallTool_InbandErrorMapsExitCode(t *testing.T) {
	cases := []struct {
		name     string
		errCode  string
		wantExit int
	}{
		{"not_found", "not_found", output.ExitNotFound},
		{"empty_query", "empty_query", output.ExitValidation},
		{"invalid_tax_year", "invalid_tax_year", output.ExitValidation},
		{"catalog_unavailable", "catalog_unavailable", output.ExitGeneric},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := map[string]any{
				"content":           textBlock(`{"error":"` + tc.errCode + `"}`),
				"structuredContent": map[string]any{"source": "x", "error": tc.errCode},
				"isError":           false,
			}
			srv := rpcServer(t, result, nil)
			defer srv.Close()
			app := newTestAppCtx(t, srv.URL)

			_, err := app.callTool(context.Background(), "get_charity_profile", map[string]any{"charity_id": "ch_z"}, false, "")
			if err == nil {
				t.Fatalf("expected an in-band error for %q", tc.errCode)
			}
			if got := output.AsError(err).Code; got != tc.wantExit {
				t.Errorf("in-band %q -> exit %d, want %d", tc.errCode, got, tc.wantExit)
			}
		})
	}
}

func TestCallTool_IsErrorToolNotFoundMapsAuth(t *testing.T) {
	// The backend's no-leak miss (mcp_exposure.tool_not_found_result): a tool the
	// caller's audience+scopes don't include is reported identically to a nonexistent
	// one, prefixed with the stable `tool_not_found` code. This is the ONE genuinely
	// auth-shaped tool error → ExitAuth.
	result := map[string]any{
		"content": textBlock("tool_not_found: no tool named 'create_donation_intent' is available to this caller."),
		"isError": true,
	}
	srv := rpcServer(t, result, nil)
	defer srv.Close()
	app := newTestAppCtx(t, srv.URL)
	t.Setenv("GIVMO_API_KEY", "tok") // pass the credential gate so we reach the server

	_, err := app.callTool(context.Background(), "create_donation_intent", map[string]any{}, true, "givmo.donation_intents.create")
	if err == nil {
		t.Fatal("expected an isError tool result to surface as an error")
	}
	if got := output.AsError(err).Code; got != output.ExitAuth {
		t.Errorf("tool_not_found -> exit %d, want ExitAuth (%d)", got, output.ExitAuth)
	}
}

// TestCallTool_MoneyRejectionKeysOnCodeNotText pins the money-path defect: a
// donation-intent rejection is classified on its stable leading `code:` token, NOT the
// human wording. charity_inactive's message contains "not available", which must never
// be misread as an auth failure (an agent would loop on re-login instead of picking a
// live charity). Wire texts are exactly what the backend renders
// (donation_intent_tools.py f"{exc.code}: {exc.message}").
func TestCallTool_MoneyRejectionKeysOnCodeNotText(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"charity_inactive", "charity_inactive: The target charity is not available for donations."},
		{"cause_etf_inactive_reuses_charity_inactive", "charity_inactive: The target cause ETF is not available for donations."},
		{"invalid_amount", "invalid_amount: Donation must be at least $5.00."},
		{"amount_limit_exceeded", "amount_limit_exceeded: amount exceeds the per-donation cap."},
		{"amount_below_charity_minimum", "amount_below_charity_minimum: amount_cents must be at least 500 for this charity."},
		{"amount_above_charity_maximum", "amount_above_charity_maximum: amount_cents must be at most 1000000 for this charity."},
		{"invalid_request", "invalid_request: cause_etf_id is not a valid id."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := map[string]any{
				"content": textBlock(tc.text),
				"isError": true,
			}
			srv := rpcServer(t, result, nil)
			defer srv.Close()
			app := newTestAppCtx(t, srv.URL)
			t.Setenv("GIVMO_API_KEY", "tok")

			_, err := app.callTool(context.Background(), "create_donation_intent", map[string]any{}, true, "givmo.donation_intents.create")
			if err == nil {
				t.Fatal("expected a refused-write tool error")
			}
			oe := output.AsError(err)
			if oe.Code == output.ExitAuth {
				t.Fatalf("%s must NOT map to ExitAuth (money-path); got the auth exit", tc.name)
			}
			if oe.Code != output.ExitValidation {
				t.Errorf("%s -> exit %d, want ExitValidation (%d)", tc.name, oe.Code, output.ExitValidation)
			}
			// The server's real message (the actual cause) must be surfaced verbatim.
			if !strings.Contains(oe.Message, tc.text) {
				t.Errorf("message must surface the server text; got %q", oe.Message)
			}
			// Remediation must NOT send the caller to re-authenticate.
			if strings.Contains(strings.ToLower(oe.Remediation), "login") {
				t.Errorf("remediation must not tell the caller to re-login; got %q", oe.Remediation)
			}
		})
	}
}

func TestCallTool_IsErrorRejectionMapsValidation(t *testing.T) {
	result := map[string]any{
		"content": textBlock("invalid_amount: Donation must be at least $5.00."),
		"isError": true,
	}
	srv := rpcServer(t, result, nil)
	defer srv.Close()
	app := newTestAppCtx(t, srv.URL)
	t.Setenv("GIVMO_API_KEY", "tok")

	_, err := app.callTool(context.Background(), "create_donation_intent", map[string]any{}, true, "givmo.donation_intents.create")
	if err == nil {
		t.Fatal("expected a refused-write tool error")
	}
	if got := output.AsError(err).Code; got != output.ExitValidation {
		t.Errorf("rejection -> exit %d, want ExitValidation (%d)", got, output.ExitValidation)
	}
}

func TestCallTool_RequireConsumerWithoutCredentialFailsFast(t *testing.T) {
	var cap capturedCall
	srv := rpcServer(t, map[string]any{"content": textBlock("{}"), "isError": false}, &cap)
	defer srv.Close()
	app := newTestAppCtx(t, srv.URL)
	t.Setenv("GIVMO_API_KEY", "") // no consumer credential

	_, err := app.callTool(context.Background(), "get_receipt", map[string]any{}, true, "givmo.receipts.read")
	if err == nil {
		t.Fatal("expected ExitAuth without a stored consumer credential")
	}
	if got := output.AsError(err).Code; got != output.ExitAuth {
		t.Errorf("no credential -> exit %d, want ExitAuth (%d)", got, output.ExitAuth)
	}
	// The remote must NOT be contacted when the credential gate fails.
	if cap.hit {
		t.Error("server must not be called without a credential")
	}
}

func TestCallTool_ConsumerForwardsBearer(t *testing.T) {
	var cap capturedCall
	result := map[string]any{
		"content":           textBlock(`{"tax_year":2025}`),
		"structuredContent": map[string]any{"tax_year": 2025},
		"isError":           false,
	}
	srv := rpcServer(t, result, &cap)
	defer srv.Close()
	app := newTestAppCtx(t, srv.URL)
	t.Setenv("GIVMO_API_KEY", "tok")

	if _, err := app.callTool(context.Background(), "get_receipt", map[string]any{"tax_year": 2025}, true, "givmo.receipts.read"); err != nil {
		t.Fatalf("callTool: %v", err)
	}
	if cap.auth != "Bearer tok" {
		t.Errorf("consumer call must forward the bearer, got %q", cap.auth)
	}
	if cap.args["tax_year"] != float64(2025) {
		t.Errorf("tax_year not forwarded: %+v", cap.args)
	}
}
