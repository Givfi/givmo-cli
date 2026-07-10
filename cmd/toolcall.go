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
// structured payload (a dict on success), the text content (always present; the
// only payload when isError), and the error flag.
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
	params, err := json.Marshal(map[string]any{"name": name, "arguments": args})
	if err != nil {
		return nil, output.New(output.ExitGeneric, "could not encode the MCP tool call: "+err.Error(), "")
	}
	raw, rerr := remote.Forward(ctx, "tools/call", json.RawMessage(params))
	if rerr != nil {
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
		return nil, output.New(code, "remote MCP call failed: "+strings.TrimSpace(rerr.Message), remediation)
	}
	var tr mcpToolResult
	if uerr := json.Unmarshal(raw, &tr); uerr != nil {
		return nil, output.New(output.ExitGeneric,
			"could not decode the MCP tool result: "+uerr.Error(),
			"The remote returned an unexpected shape; retry, and report to Givmo support if it persists.")
	}
	// isError => a transport/dispatch tool error (tool_not_found, invalid arguments,
	// or a refused write). Only the text content is populated.
	if tr.IsError {
		return nil, toolErrorFromText(mcpTextContent(tr), name)
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
// money tool prefixes onto its refusal text as "<code>: <human message>" — the backend
// renders f"{exc.code}: {exc.message}" (donation_intent_tools.py) for every
// DonationIntentRejection (connect/donation_intent_service.py). Each is a request/domain refusal
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
	"link_token_not_supported":        true,
	"reserved_metadata_key":           true,
	"invalid_request":                 true,
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

// toolErrorFromText maps an isError tool result (a raised dispatch error or a refused
// write) to the CLI envelope, keying on the STABLE leading error code the backend
// renders — never on human message wording. `tool_not_found` is the one genuinely
// auth-shaped code (a tool the grant's scope/audience does not include is reported
// identically to one that does not exist at all), so it → ExitAuth. Every
// donation-intent rejection and `invalid_arguments` is a request/domain refusal the
// caller fixes by changing inputs → ExitValidation with the server's message.
func toolErrorFromText(text, name string) *output.Error {
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

	switch code := leadingToken(text); {
	case code == "tool_not_found":
		return authError()
	case code == "invalid_arguments" || donationRejectionCodes[code]:
		return validationError()
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
	return validationError()
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
