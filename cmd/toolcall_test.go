package cmd

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
		{"return_url_not_givmo", "return_url_not_givmo: return_url must be a Givmo page: an https address on a Givmo host. Omit return_url to send the donor back to the Givmo pay page; after checkout a donor is only ever sent to a Givmo page."},
		{"invalid_metadata", "invalid_metadata: metadata may hold at most 20 keys; you sent 21."},
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

// refusalResult is an isError CallToolResult carrying a refusal's text and its
// structuredContent, the shape the server sends when it refuses a call.
func refusalResult(text string, structured map[string]any) map[string]any {
	return map[string]any{
		"content":           textBlock(text),
		"structuredContent": structured,
		"isError":           true,
	}
}

// TestCallTool_RefusalMapsByStructuredFields pins the mapping of a refusal by the
// fields the server states, never by its code: the structuredContent keys and values
// below are the server's own (safe_to_retry, outcome, attempted, retry_safety,
// budget_seconds), while the refusal codes, sources and texts are illustrative. A
// code the CLI has never seen maps by the same fields.
func TestCallTool_RefusalMapsByStructuredFields(t *testing.T) {
	cases := []struct {
		name       string
		text       string
		structured map[string]any
		wantExit   int
		wantRemedy string
	}{
		{
			name: "busy read: nothing sent, safe to retry",
			text: "ExampleBusy: the read was not sent: this kind of tool already has as many calls in flight on this server as it may hold, and none finished within 5s. A read changes nothing, so it is safe to retry, preferably after a short wait.",
			structured: map[string]any{
				"source": "example_family", "refusal": "ExampleBusy", "safe_to_retry": true,
			},
			wantExit:   output.ExitRateLimited,
			wantRemedy: "retry",
		},
		{
			name: "busy write: not applied, not attempted",
			text: "ExampleBusy: the operation was not sent, so it did not run: this kind of tool already has as many calls in flight on this server as it may hold, and none finished within 5s. It is safe to retry, preferably after a short wait.",
			structured: map[string]any{
				"source": "example_family", "refusal": "ExampleBusy",
				"outcome": "not_applied", "attempted": false, "safe_to_retry": true,
			},
			wantExit:   output.ExitRateLimited,
			wantRemedy: "retry",
		},
		{
			name: "timed-out read: a read changes nothing",
			text: "ExampleDeadline: the read did not answer within its 30s budget. A read changes nothing, so it is safe to retry.",
			structured: map[string]any{
				"source": "example_family", "refusal": "ExampleDeadline",
				"safe_to_retry": true, "budget_seconds": 30,
			},
			wantExit:   output.ExitRateLimited,
			wantRemedy: "retry",
		},
		{
			name: "timed-out write that never reached the operation",
			text: "ExampleDeadline: the operation's 30s budget ran out before the request reached it, so it did not run. It is safe to retry.",
			structured: map[string]any{
				"source": "example_family", "refusal": "ExampleDeadline",
				"outcome": "not_applied", "attempted": false, "safe_to_retry": true, "budget_seconds": 30,
			},
			wantExit:   output.ExitRateLimited,
			wantRemedy: "retry",
		},
		{
			name: "timed-out write, outcome unknown, idempotent class",
			text: "ExampleDeadline: the operation was sent and did not answer within its 30s budget. IT MAY HAVE COMPLETED — the outcome is unknown. This act is declared idempotent (retry class A): retrying it with exactly the same arguments converges on the same result and cannot apply it twice. Do not change the arguments to retry. The first call may still be running and may land after this retry; read the record before changing it again.",
			structured: map[string]any{
				"source": "example_family", "refusal": "ExampleDeadline",
				"outcome": "unknown", "attempted": true, "safe_to_retry": true,
				"retry_safety": "A", "budget_seconds": 30,
			},
			wantExit:   output.ExitOutcomeUnknown,
			wantRemedy: "exactly the same arguments",
		},
		{
			name: "broken connection after send, outcome unknown, fenced class",
			text: "ExampleOutcomeUnknown: the operation was sent and did not answer. IT MAY HAVE COMPLETED — the outcome is unknown. This act is fenced (retry class B): retrying it with exactly the same arguments cannot apply it twice.",
			structured: map[string]any{
				"source": "example_family", "refusal": "ExampleOutcomeUnknown",
				"outcome": "unknown", "attempted": true, "safe_to_retry": true, "retry_safety": "B",
			},
			wantExit:   output.ExitOutcomeUnknown,
			wantRemedy: "exactly the same arguments",
		},
		{
			name: "write with no retry class, outcome unknown",
			text: "ExampleOutcomeUnknown: the operation was sent and did not answer. IT MAY HAVE COMPLETED — the outcome is unknown, and failing on this side does not stop the operation on the other. Do not retry it. Read the current state with the matching read tool first, and act on what that says.",
			structured: map[string]any{
				"source": "example_family", "refusal": "ExampleOutcomeUnknown",
				"outcome": "unknown", "attempted": true, "safe_to_retry": false,
			},
			wantExit:   output.ExitOutcomeUnknown,
			wantRemedy: "Do not retry blindly",
		},
		{
			name: "a refusal code the CLI has never seen, outcome unknown",
			text: "SomeFutureRefusal: the call was accepted and its answer was lost.",
			structured: map[string]any{
				"source": "some_future_family", "refusal": "SomeFutureRefusal",
				"outcome": "unknown", "attempted": true, "safe_to_retry": false,
			},
			wantExit:   output.ExitOutcomeUnknown,
			wantRemedy: "Do not retry blindly",
		},
		{
			name: "attempted with no outcome stated",
			text: "SomeFutureRefusal: the call was sent; its result is not known.",
			structured: map[string]any{
				"source": "some_future_family", "refusal": "SomeFutureRefusal", "attempted": true,
			},
			wantExit:   output.ExitOutcomeUnknown,
			wantRemedy: "Do not retry blindly",
		},
		{
			name: "a refusal code the CLI has never seen, safe to retry",
			text: "AnotherFutureRefusal: turned away; try again shortly.",
			structured: map[string]any{
				"source": "some_future_family", "refusal": "AnotherFutureRefusal", "safe_to_retry": true,
			},
			wantExit:   output.ExitRateLimited,
			wantRemedy: "retry",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := rpcServer(t, refusalResult(tc.text, tc.structured), nil)
			defer srv.Close()
			app := newTestAppCtx(t, srv.URL)
			t.Setenv("GIVMO_API_KEY", "tok")

			_, err := app.callTool(context.Background(), "example_tool", map[string]any{}, true, "example.scope")
			if err == nil {
				t.Fatal("expected the refusal to surface as an error")
			}
			oe := output.AsError(err)
			if oe.Code != tc.wantExit {
				t.Errorf("exit %d (%s), want %d (%s)", oe.Code, output.CodeName(oe.Code), tc.wantExit, output.CodeName(tc.wantExit))
			}
			if !strings.Contains(oe.Message, tc.text) {
				t.Errorf("message must carry the server's text verbatim; got %q", oe.Message)
			}
			if !strings.Contains(oe.Remediation, tc.wantRemedy) {
				t.Errorf("remediation %q does not say %q", oe.Remediation, tc.wantRemedy)
			}
			for _, wrong := range []string{"Adjust the request", "login", "unreachable"} {
				if strings.Contains(oe.Remediation, wrong) {
					t.Errorf("remediation must not say %q; got %q", wrong, oe.Remediation)
				}
			}
		})
	}
}

// TestCallTool_RefusalWithoutDispositionKeepsCodeMapping pins the boundary: a
// refusal whose structuredContent states neither an outcome nor a retry verdict
// keeps the code-based mapping it had before the structured fields were read. A
// recognized code keeps its remediation; a code the CLI does not recognize gets one
// that claims nothing the CLI cannot know (the request may not be at fault, and a
// write may already have run).
func TestCallTool_RefusalWithoutDispositionKeepsCodeMapping(t *testing.T) {
	const unrecognized = "This CLI does not recognize this refusal"
	cases := []struct {
		name       string
		text       string
		structured map[string]any
		wantExit   int
		wantRemedy string
		notRemedy  string
	}{
		{
			// The money tool's refusal: its structured code is the one it prefixes.
			name: "donation refusal",
			text: "charity_inactive: The target charity is not available for donations.",
			structured: map[string]any{
				"source": "givmo_donation_intent", "refusal": "charity_inactive", "param": "charity_id",
			},
			wantExit:   output.ExitValidation,
			wantRemedy: "Adjust the request",
			notRemedy:  unrecognized,
		},
		{
			name:       "a domain refusal the CLI has no code for",
			text:       "ExampleDomainRefusal: that record is not in a state that allows this change.",
			structured: map[string]any{"source": "example_family", "refusal": "ExampleDomainRefusal"},
			wantExit:   output.ExitValidation,
			wantRemedy: unrecognized,
			notRemedy:  "request/validation problem",
		},
		{
			name:       "a safe_to_retry of the wrong type claims nothing",
			text:       "ExampleDomainRefusal: refused.",
			structured: map[string]any{"source": "example_family", "refusal": "ExampleDomainRefusal", "safe_to_retry": "yes"},
			wantExit:   output.ExitValidation,
			wantRemedy: unrecognized,
			notRemedy:  "request/validation problem",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := rpcServer(t, refusalResult(tc.text, tc.structured), nil)
			defer srv.Close()
			app := newTestAppCtx(t, srv.URL)
			t.Setenv("GIVMO_API_KEY", "tok")

			_, err := app.callTool(context.Background(), "example_tool", map[string]any{}, true, "example.scope")
			if err == nil {
				t.Fatal("expected the refusal to surface as an error")
			}
			oe := output.AsError(err)
			if oe.Code != tc.wantExit {
				t.Errorf("exit %d, want %d", oe.Code, tc.wantExit)
			}
			if !strings.Contains(oe.Remediation, tc.wantRemedy) {
				t.Errorf("remediation %q does not say %q", oe.Remediation, tc.wantRemedy)
			}
			if strings.Contains(oe.Remediation, tc.notRemedy) {
				t.Errorf("remediation must not say %q; got %q", tc.notRemedy, oe.Remediation)
			}
		})
	}
}

// TestCallTool_StructuredRefusalCodeOutranksText pins that the structured refusal
// code is read before the text: the money tool's own refusals carry their code only
// in structuredContent, and text without a code prefix must not fall to the wording
// heuristic (here "not found", which alone would read as an auth failure).
func TestCallTool_StructuredRefusalCodeOutranksText(t *testing.T) {
	result := refusalResult(
		"the requested charity was not found among those open for donations.",
		map[string]any{"source": "givmo_donation_intent", "refusal": "charity_inactive", "param": "charity_id"},
	)
	srv := rpcServer(t, result, nil)
	defer srv.Close()
	app := newTestAppCtx(t, srv.URL)
	t.Setenv("GIVMO_API_KEY", "tok")

	_, err := app.callTool(context.Background(), "create_donation_intent", map[string]any{}, true, "givmo.donation_intents.create")
	if err == nil {
		t.Fatal("expected a refused-write tool error")
	}
	if got := output.AsError(err).Code; got != output.ExitValidation {
		t.Errorf("structured charity_inactive -> exit %d, want ExitValidation (%d)", got, output.ExitValidation)
	}
}

// TestCallTool_ClientTimeoutIsOutcomeUnknownNeverUnreachable pins the CLI's own
// timeout: a tool call the CLI stopped waiting for was sent, so it may have run.
// It exits 8 and never reads as "unreachable" (which says nothing ran).
func TestCallTool_ClientTimeoutIsOutcomeUnknownNeverUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	app := newTestAppCtx(t, srv.URL)
	app.toolCallTimeout = 100 * time.Millisecond
	t.Setenv("GIVMO_API_KEY", "tok")

	_, err := app.callTool(context.Background(), "example_write", map[string]any{}, true, "example.scope")
	if err == nil {
		t.Fatal("expected the lost answer to surface as an error")
	}
	oe := output.AsError(err)
	if oe.Code != output.ExitOutcomeUnknown {
		t.Errorf("client-side timeout -> exit %d, want ExitOutcomeUnknown (%d)", oe.Code, output.ExitOutcomeUnknown)
	}
	if !strings.Contains(oe.Message, "may have run") {
		t.Errorf("message must say the call may have run: %q", oe.Message)
	}
	if !strings.Contains(oe.Remediation, "Read the current state before retrying") {
		t.Errorf("remediation must say to read before retrying: %q", oe.Remediation)
	}
	for _, s := range []string{oe.Message, oe.Remediation} {
		if strings.Contains(strings.ToLower(s), "unreachable") {
			t.Errorf("a sent call must never read as unreachable: %q", s)
		}
	}
}

// TestCallTool_UnreachableIsNetworkExit pins the other side: a call that never
// reached the server exits 6, the documented network exit, and says nothing ran.
func TestCallTool_UnreachableIsNetworkExit(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	app := newTestAppCtx(t, "http://"+addr)

	_, cerr := app.callTool(context.Background(), "search_charities", map[string]any{"query": "water"}, false, "")
	if cerr == nil {
		t.Fatal("expected an unreachable endpoint to fail")
	}
	oe := output.AsError(cerr)
	if oe.Code != output.ExitNetwork {
		t.Errorf("unreachable -> exit %d, want ExitNetwork (%d)", oe.Code, output.ExitNetwork)
	}
	if !strings.Contains(oe.Remediation, "Nothing was sent") {
		t.Errorf("remediation must say nothing was sent: %q", oe.Remediation)
	}
}

func TestToolContext_HasNoDeadline(t *testing.T) {
	ctx, cancel := toolContext()
	defer cancel()
	if d, ok := ctx.Deadline(); ok {
		t.Errorf("a tool command's context must not cut its calls; it has deadline %s", d)
	}
}

// TestDonationIntentsCreate_UnknownOutcomeNamesTheIdempotencyKey: no consumer tool
// reads a donation intent back, so on an unknown outcome the safe next step is a
// retry with the same idempotency key, which the remediation must name.
func TestDonationIntentsCreate_UnknownOutcomeNamesTheIdempotencyKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		conn.Close() // the call was received; its answer is lost
	}))
	defer srv.Close()
	t.Setenv("GIVMO_HOME", t.TempDir())
	t.Setenv("GIVMO_TOKEN_BACKEND", "file")
	t.Setenv("GIVMO_PROFILE", "sandbox")
	t.Setenv("GIVMO_API_BASE", srv.URL)
	t.Setenv("GIVMO_API_KEY", "tok")

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"caller's key", []string{"--charity", "ch_1", "--amount", "2500", "--idempotency-key", "my-key-1"}, "--idempotency-key my-key-1"},
		{"generated key", []string{"--charity", "ch_1", "--amount", "2500"}, "--idempotency-key cli-"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newDonationIntentsCreateCmd()
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			cmd.SetArgs(tc.args)
			err := cmd.Execute()
			if err == nil {
				t.Fatal("expected the lost answer to fail the create")
			}
			oe := output.AsError(err)
			if oe.Code != output.ExitOutcomeUnknown {
				t.Errorf("exit %d, want ExitOutcomeUnknown (%d)", oe.Code, output.ExitOutcomeUnknown)
			}
			if !strings.Contains(oe.Remediation, tc.want) {
				t.Errorf("remediation must name the key (%q): %q", tc.want, oe.Remediation)
			}
		})
	}
}

// TestCallTool_RateLimitedExitsFiveWithRetryAfter: a 429 from the remote MCP exits
// 5 with the server's Retry-After in the message and the wait in the remediation.
func TestCallTool_RateLimitedExitsFiveWithRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "55")
		w.Header().Set("RateLimit-Limit", "60")
		w.Header().Set("RateLimit-Remaining", "0")
		w.Header().Set("RateLimit-Reset", "55")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"code":"rate_limited","message":"Rate limit exceeded. Retry after the Retry-After period."}}`))
	}))
	defer srv.Close()
	app := newTestAppCtx(t, srv.URL)

	_, err := app.callTool(context.Background(), "search_charities", map[string]any{"query": "water"}, false, "")
	if err == nil {
		t.Fatal("expected the 429 to surface as an error")
	}
	oe := output.AsError(err)
	if oe.Code != output.ExitRateLimited {
		t.Errorf("429 -> exit %d, want ExitRateLimited (%d)", oe.Code, output.ExitRateLimited)
	}
	if !strings.Contains(oe.Message, "Retry-After: 55 seconds") {
		t.Errorf("message must carry the server's Retry-After: %q", oe.Message)
	}
	if !strings.Contains(oe.Remediation, "Wait 55 seconds") {
		t.Errorf("remediation must say how long to wait: %q", oe.Remediation)
	}
}

// TestCallTool_DonorUnavailableIsAnAuthFailure: the money tool's own refusal for a
// credential whose account is gone carries its code only in structuredContent
// (the text has no code prefix). No change to the request fixes it.
func TestCallTool_DonorUnavailableIsAnAuthFailure(t *testing.T) {
	result := refusalResult(
		"no signed-in donor is available for this donation; the account may no longer exist.",
		map[string]any{"source": "givmo_donation_intent", "refusal": "donor_unavailable"},
	)
	srv := rpcServer(t, result, nil)
	defer srv.Close()
	app := newTestAppCtx(t, srv.URL)
	t.Setenv("GIVMO_API_KEY", "tok")

	_, err := app.callTool(context.Background(), "create_donation_intent", map[string]any{}, true, "givmo.donation_intents.create")
	if err == nil {
		t.Fatal("expected a refused-write tool error")
	}
	oe := output.AsError(err)
	if oe.Code != output.ExitAuth {
		t.Errorf("donor_unavailable -> exit %d, want ExitAuth (%d)", oe.Code, output.ExitAuth)
	}
	if strings.Contains(oe.Remediation, "Adjust the request") {
		t.Errorf("no change to the request fixes a missing account: %q", oe.Remediation)
	}
}
