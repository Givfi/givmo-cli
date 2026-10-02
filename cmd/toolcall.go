package cmd

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/givfi/givmo-cli/internal/mcpbridge"
	"github.com/givfi/givmo-cli/internal/output"
)

// This file is the CLI's OWN client for the remote Givmo MCP surface: the
// consumer catalog / giving / donation commands are backed by MCP tools
// (search_charities, get_charity_profile, list_cause_etfs, get_cause_etf,
// get_receipt, create_donation_intent), NOT by REST resource routes — no
// consumer-scope REST route exists on the backend. Each command maps to one
// tools/call over mcpbridge.HTTPRemote (POST <api_base>/mcp) and renders the
// tool's structured result through the usual table/--json conventions.

// mcpEndpoint returns the remote MCP URL for the active profile (<api_base>/mcp).
func (a *appCtx) mcpEndpoint() string {
	return strings.TrimRight(a.Profile.Endpoints.APIBase, "/") + "/mcp"
}

// mcpToolResult is the subset of an MCP CallToolResult the CLI reads: the
// structured payload (a dict on success, a refusal's machine-readable fields on
// an error), the text content (always present), and the error flag.
type mcpToolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent"`
	IsError           bool            `json:"isError"`
}

// callTool invokes a remote Givmo MCP tool by name over the CLI's own MCP bridge
// and returns the tool's structured JSON payload (ready to json.Unmarshal into a
// command's view struct).
//
// requireConsumer selects the tier the request resolves to on /mcp:
//   - false — the public catalog tools. NO Authorization header is attached, so
//     the request resolves to the anonymous PUBLIC principal (which serves exactly
//     the catalog tools). This is deliberate: the /mcp auth layer NEVER downgrades
//     a present-but-invalid bearer to public — it 401s — so attaching a (possibly
//     stale) stored token to a public read would break it. A public read needs no
//     login, matching the catalog's public tier.
//   - true — a consumer self-read / money tool needing a user-delegated givmo.*
//     grant (get_receipt, create_donation_intent). A stored consumer credential is
//     required and forwarded as the bearer; scopeHint names the scope for the
//     no-credential remediation.
func (a *appCtx) callTool(ctx context.Context, name string, args map[string]any, requireConsumer bool, scopeHint string) (json.RawMessage, error) {
	authHeader := ""
	if requireConsumer {
		cred := a.credential()
		if cred == nil || cred.AuthorizationHeader() == "" {
			return nil, output.New(output.ExitAuth,
				"this command requires a consumer credential but none is stored",
				"Run `givmo login` to authenticate with the "+scopeHint+" scope, or set GIVMO_API_KEY for non-interactive use.")
		}
		authHeader = cred.AuthorizationHeader()
	}
	remote := mcpbridge.NewHTTPRemote(a.mcpEndpoint(), authHeader, a.httpClient())
	if a.toolCallTimeout > 0 {
		remote.CallTimeout = a.toolCallTimeout
	}
	params, err := json.Marshal(map[string]any{"name": name, "arguments": args})
	if err != nil {
		return nil, output.New(output.ExitGeneric, "could not encode the MCP tool call: "+err.Error(), "")
	}
	raw, rerr := remote.Forward(ctx, "tools/call", json.RawMessage(params))
	if rerr != nil {
		msg := "remote MCP call failed: " + strings.TrimSpace(rerr.Message)
		switch rerr.Kind {
		case mcpbridge.KindOutcomeUnknown:
			// Sent, never answered: the call may have run. Never "unreachable".
			return nil, output.New(output.ExitOutcomeUnknown, msg,
				"Read the current state before retrying (for example with the matching get or list command); a read-only command is safe to re-run.")
		case mcpbridge.KindUnreachable:
			return nil, output.New(output.ExitNetwork, msg,
				"Check connectivity and the active profile's api_base (`givmo config view`); the MCP endpoint may not be enabled in this environment. Nothing was sent, so a retry is safe.")
		case mcpbridge.KindRateLimited:
			remediation := "Nothing ran. Back off, then retry."
			if rerr.RetryAfter != "" {
				remediation = "Nothing ran. Wait " + mcpbridge.RetryAfterText(rerr.RetryAfter) + " (the server's Retry-After), then retry."
			}
			return nil, output.New(output.ExitRateLimited, msg, remediation)
		}
		// The bridge maps 401/403 to a message mentioning login; other transport
		// failures are internal/invalid-request codes.
		lower := strings.ToLower(rerr.Message)
		code := output.ExitGeneric
		remediation := "Retry; if it persists, report the failure to Givmo support."
		switch {
		case strings.Contains(lower, "login") || strings.Contains(lower, "auth"):
			code = output.ExitAuth
			remediation = "Run `givmo login` to obtain a valid consumer token, then retry."
		case rerr.Code == mcpbridge.CodeInvalidRequest:
			code = output.ExitValidation
			remediation = "Fix the request per the message and retry."
		}
		return nil, output.New(code, msg, remediation)
	}
	var tr mcpToolResult
	if uerr := json.Unmarshal(raw, &tr); uerr != nil {
		return nil, output.New(output.ExitGeneric,
			"could not decode the MCP tool result: "+uerr.Error(),
			"The remote returned an unexpected shape; retry, and report to Givmo support if it persists.")
	}
	// isError => a dispatch error (tool_not_found, invalid arguments) or a refusal.
	// The text is always populated; a refusal may also carry structuredContent.
	if tr.IsError {
		return nil, toolErrorFromResult(tr, name)
	}
	// Prefer the structured payload; fall back to parsing the text content as JSON.
	payload := tr.StructuredContent
	if len(payload) == 0 {
		if t := mcpTextContent(tr); t != "" {
			payload = json.RawMessage(t)
		}
	}
	// A corrective in-band error (not_found, empty_query, invalid_tax_year, …)
	// arrives as a SUCCESS result whose dict carries an `error` key.
	if oerr := inbandToolError(payload); oerr != nil {
		return nil, oerr
	}
	return payload, nil
}

// decodeToolPayload unmarshals a tool payload into v, mapping a decode failure to
// the CLI envelope (the remote returned an unexpected tool-result shape).
func decodeToolPayload(payload json.RawMessage, v any) error {
	if len(payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, v); err != nil {
		return output.New(output.ExitGeneric,
			"could not decode the MCP tool result: "+err.Error(),
			"The remote returned an unexpected shape; retry, and report to Givmo support if it persists.")
	}
	return nil
}

// mcpTextContent returns the first non-empty text block of a tool result.
func mcpTextContent(tr mcpToolResult) string {
	for _, c := range tr.Content {
		if c.Text != "" {
			return c.Text
		}
	}
	return ""
}

// donationRejectionCodes are the stable Connect error codes the create_donation_intent
// money tool refuses with: it prefixes them onto its refusal text as "<code>: <human
// message>" — the backend renders f"{exc.code}: {exc.message}" (donation_intent_tools.py)
// for every DonationIntentRejection (connect/donation_intent_service.py) — and sends them
// as structuredContent's refusal. Each is a request/domain refusal
// the caller self-repairs by CHANGING INPUTS (a different charity, a valid amount) —
// never by re-authenticating. Classification MUST key on this stable code, not the human
// wording: charity_inactive's message ("...not available for donations.") would otherwise
// be misread as an auth failure and send an agent into a re-login loop.
var donationRejectionCodes = map[string]bool{
	"invalid_amount":                  true,
	"currency_not_supported":          true,
	"contribution_type_not_available": true,
	"charity_inactive":                true, // covers an inactive charity AND an inactive cause ETF
	"invalid_email":                   true,
	"amount_limit_exceeded":           true,
	"amount_below_charity_minimum":    true, // CT-RM-8: below the direct charity's advertised min_donation_cents
	"amount_above_charity_maximum":    true, // CT-RM-8: above the direct charity's advertised max_donation_cents
	"link_token_not_supported":        true,
	"reserved_metadata_key":           true,
	"invalid_request":                 true,
	"return_url_not_givmo":            true, // return_url must be an https page on a Givmo host
	"invalid_metadata":                true,
}

// leadingToken returns the maximal leading run of snake_case token characters
// ([a-z0-9_]) — the stable error code the backend prefixes onto a tool error
// ("<code>: <message>", or "invalid_arguments for '<tool>': …"). Returns "" when the
// text does not start with such a token (e.g. a plain human sentence). Pure.
func leadingToken(text string) string {
	i := 0
	for i < len(text) {
		c := text[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' {
			i++
			continue
		}
		break
	}
	return text[:i]
}

// toolRefusal is the machine-readable part of a refusal: the fields of an isError
// result's structuredContent the CLI branches on. The server states there what a
// caller may do next, so the CLI reads its verdict instead of a list of refusal
// codes, and a refusal code it has never seen maps the same way. Every field is
// optional.
type toolRefusal struct {
	// Refusal is the refusal's stable code.
	Refusal string `json:"refusal"`
	// SafeToRetry is the server's verdict on repeating the call as it was made.
	SafeToRetry *bool `json:"safe_to_retry"`
	// Outcome is "not_applied" when the call did not run and "unknown" when it
	// may have.
	Outcome string `json:"outcome"`
	// Attempted says whether the call was sent on to the operation behind the tool.
	Attempted *bool `json:"attempted"`
}

// mayHaveRun reports whether the refusal says the call may have run: its outcome
// is unknown, or it was attempted and is not said to have been left unapplied.
func (r toolRefusal) mayHaveRun() bool {
	if r.Outcome == "unknown" {
		return true
	}
	return r.Attempted != nil && *r.Attempted && r.Outcome != "not_applied"
}

// retrySafe reports whether the server says repeating the call is safe.
func (r toolRefusal) retrySafe() bool {
	return r.SafeToRetry != nil && *r.SafeToRetry
}

// toolErrorFromResult maps an isError tool result to the CLI envelope, by its
// structured fields first:
//   - a call that may have run → ExitOutcomeUnknown: read before retrying, and
//     repeat it only where the server says a same-arguments retry is safe;
//   - a call the server says is safe to retry (turned away as busy, never sent,
//     or a read that changed nothing) → ExitRateLimited: back off, then retry;
//   - anything else keeps the code-based mapping below, keyed on the structured
//     refusal code when there is one, else on the text's leading code.
func toolErrorFromResult(tr mcpToolResult, name string) *output.Error {
	text := strings.TrimSpace(mcpTextContent(tr))
	if text == "" {
		text = "the MCP tool '" + name + "' returned an error"
	}
	var r toolRefusal
	if len(tr.StructuredContent) > 0 {
		// A field of the wrong type stays unset, and an unset field claims nothing.
		_ = json.Unmarshal(tr.StructuredContent, &r)
	}
	switch {
	case r.mayHaveRun():
		remediation := "Do not retry blindly: read the current state first (with the matching read or list command), then act on what it says."
		if r.retrySafe() {
			remediation = "The server says repeating the call with exactly the same arguments is safe, but the first call may still land: read the current state before changing it again."
		}
		return output.New(output.ExitOutcomeUnknown,
			"the MCP tool '"+name+"' may have run; its outcome is unknown: "+text,
			remediation)
	case r.retrySafe():
		return output.New(output.ExitRateLimited,
			"the MCP tool '"+name+"' did not complete: "+text,
			"The server says a retry is safe: wait briefly, then retry.")
	}
	return toolErrorFromText(text, name, r.Refusal)
}

// toolErrorFromText maps an isError tool result (a raised dispatch error or a refused
// write) to the CLI envelope, keying on the STABLE error code the backend sends — the
// structured refusal code when the result carries one, else the code it prefixes onto
// the text — never on human message wording. `tool_not_found` is the one genuinely
// auth-shaped code (a tool the grant's scope/audience does not include is reported
// identically to one that does not exist at all), so it → ExitAuth. Every
// donation-intent rejection and `invalid_arguments` is a request/domain refusal the
// caller fixes by changing inputs → ExitValidation with the server's message. A
// refusal with no recognized code also exits ExitValidation, but its remediation
// claims nothing the CLI cannot know.
func toolErrorFromText(text, name, refusal string) *output.Error {
	text = strings.TrimSpace(text)
	if text == "" {
		text = "the MCP tool '" + name + "' returned an error"
	}
	authError := func() *output.Error {
		return output.New(output.ExitAuth,
			"the MCP tool '"+name+"' is not available to this credential: "+text,
			"Your consumer grant may lack the tool's scope, or the surface is not enabled in this environment. Re-run `givmo login` to (re)authorize.")
	}
	validationError := func() *output.Error {
		return output.New(output.ExitValidation,
			"the MCP tool '"+name+"' rejected the request: "+text,
			"Adjust the request per the message and retry; this is a request/validation problem, not an authentication failure.")
	}

	for _, code := range []string{refusal, leadingToken(text)} {
		switch {
		case code == "tool_not_found":
			return authError()
		case code == "donor_unavailable":
			// The credential's account is gone: no change to the request fixes it.
			return output.New(output.ExitAuth,
				"the MCP tool '"+name+"' found no signed-in donor: "+text,
				"The Givmo account behind this credential may no longer exist. Run `givmo login` with an active account, then retry.")
		case code == "invalid_arguments" || donationRejectionCodes[code]:
			return validationError()
		}
	}

	// No recognized code prefix: fall back to a conservative heuristic for auth-shaped
	// phrasings from any surface that does not prefix a stable code. Deliberately does
	// NOT trigger on "not available": the money rail's charity_inactive message
	// ("...not available for donations.") carries that phrase and is a validation
	// refusal, not an auth failure.
	lower := strings.ToLower(text)
	if strings.Contains(lower, "not found") || strings.Contains(lower, "unknown tool") {
		return authError()
	}
	// Still no recognized code. The refusal may not be the request's fault, and one
	// that states no outcome may follow a write that already ran, so the remediation
	// says only what the CLI knows.
	return output.New(output.ExitValidation,
		"the MCP tool '"+name+"' rejected the request: "+text,
		"Act on the server's message. This CLI does not recognize this refusal, so it cannot say whether changing the request will help or whether a write already ran: read the current state before repeating a write.")
}

// inbandToolError inspects a successful tool payload for a corrective `error`
// code (the catalog/account/etf tools return these instead of raising) and maps
// it to the CLI envelope. Returns nil when the payload carries no error.
func inbandToolError(payload json.RawMessage) *output.Error {
	if len(payload) == 0 {
		return nil
	}
	var probe struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(payload, &probe) != nil || probe.Error == "" {
		return nil
	}
	msg := probe.Message
	if msg == "" {
		msg = "the tool reported '" + probe.Error + "'"
	}
	switch probe.Error {
	case "not_found":
		return output.New(output.ExitNotFound, msg,
			"Verify the id; use the corresponding search/list command to find valid ids.")
	case "empty_query", "invalid_etf_id", "invalid_tax_year", "tax_year_out_of_range", "invalid_request":
		return output.New(output.ExitValidation, msg, "Fix the input per the message and retry.")
	case "catalog_unavailable":
		return output.New(output.ExitGeneric, msg,
			"The catalog service is not available right now; retry later.")
	default:
		return output.New(output.ExitGeneric, msg,
			"Retry; if it persists, report the error to Givmo support.")
	}
}
