package mcpbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Per-request timeouts. The server allows its slowest tools up to 120 s per
// call, so the CLI waits longer than that for a tools/call answer: whatever the
// server decides, a result or its own timeout refusal, arrives before the CLI
// gives up. Every other request is an auxiliary fetch.
const (
	// ToolCallTimeout bounds one tools/call request.
	ToolCallTimeout = 150 * time.Second
	// AuxiliaryTimeout bounds every other request to the remote MCP.
	AuxiliaryTimeout = 30 * time.Second
)

// HTTPRemote is a RemoteInvoker that forwards JSON-RPC calls to the remote
// Givmo MCP endpoint (Streamable HTTP, stateless) at <endpoint>, injecting the
// stored bearer as Authorization. The remote is stateless per request, matching
// the platform's stateless Streamable-HTTP MCP.
type HTTPRemote struct {
	endpoint string
	authHdr  string
	client   *http.Client
	nextID   int
	// CallTimeout bounds one tools/call request; AuxTimeout every other request.
	CallTimeout time.Duration
	AuxTimeout  time.Duration
}

// NewHTTPRemote builds an HTTP remote. endpoint is the full MCP URL (e.g.
// https://mcp.givmo.io/mcp); authHeader is the full "Bearer …" value (may be
// empty for the public tier). client supplies the transport. The remote sets
// each request's deadline itself (CallTimeout or AuxTimeout), so a Timeout on
// client is not used: it would cut a tools/call the server is still allowed to
// be running.
func NewHTTPRemote(endpoint, authHeader string, client *http.Client) *HTTPRemote {
	c := &http.Client{}
	if client != nil {
		copied := *client
		copied.Timeout = 0
		c = &copied
	}
	return &HTTPRemote{
		endpoint:    endpoint,
		authHdr:     authHeader,
		client:      c,
		CallTimeout: ToolCallTimeout,
		AuxTimeout:  AuxiliaryTimeout,
	}
}

// Forward posts a JSON-RPC request to the remote MCP and returns the `result`
// (or maps the remote `error`) as raw JSON.
func (r *HTTPRemote) Forward(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *rpcError) {
	r.nextID++
	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"id":      r.nextID,
		"method":  method,
	}
	if params != nil {
		reqBody["params"] = params
	}
	buf, err := json.Marshal(reqBody)
	if err != nil {
		return nil, &rpcError{Code: codeInternalError, Message: "marshal remote request: " + err.Error()}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := r.AuxTimeout
	toolCall := method == "tools/call"
	if toolCall {
		timeout = r.CallTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// sent turns true once the whole request has been written to the connection.
	// From then on the server may be running the call, so a failure is no longer
	// "unreachable": for a tools/call its outcome is unknown.
	var sent atomic.Bool
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				sent.Store(true)
			}
		},
	})
	start := time.Now()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(buf))
	if err != nil {
		return nil, &rpcError{Code: codeInternalError, Message: err.Error()}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// Streamable HTTP MCP accepts both JSON and SSE; we consume JSON.
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	if r.authHdr != "" {
		httpReq.Header.Set("Authorization", r.authHdr)
	}
	resp, err := r.client.Do(httpReq)
	if err != nil {
		switch {
		case !sent.Load():
			return nil, &rpcError{Code: codeInternalError, Kind: KindUnreachable,
				Message: "remote MCP unreachable: " + err.Error() +
					" (the /mcp endpoint may not be reachable or enabled in this environment)"}
		case toolCall:
			return nil, toolCallUnanswered(lostAnswerCause(err, start))
		default:
			return nil, &rpcError{Code: codeInternalError, Message: "remote MCP did not answer " + method + ": " + err.Error()}
		}
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 16<<20))

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, &rpcError{Code: codeInvalidRequest, Message: fmt.Sprintf("remote MCP rejected auth (HTTP %d); run `givmo login`", resp.StatusCode)}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, rateLimited(resp.Header, body)
	}
	if readErr != nil {
		if toolCall {
			return nil, toolCallUnanswered("its answer was cut off: " + lostAnswerCause(readErr, start))
		}
		return nil, &rpcError{Code: codeInternalError, Message: "reading the remote MCP answer failed: " + readErr.Error()}
	}

	// A server failure with no JSON-RPC answer, after a tools/call was sent, leaves
	// the call's outcome unknown: the tool may have run before the failure.
	unanswered := func() *rpcError {
		if toolCall && resp.StatusCode >= http.StatusInternalServerError {
			return toolCallUnanswered(fmt.Sprintf("the remote answered HTTP %d with no JSON-RPC result", resp.StatusCode))
		}
		return nil
	}
	payload, err := extractJSONRPCPayload(body, resp.Header.Get("Content-Type"))
	if err != nil {
		if u := unanswered(); u != nil {
			return nil, u
		}
		return nil, &rpcError{Code: codeInternalError, Message: err.Error()}
	}

	var rpc struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	nonRPC := func() *rpcError {
		if u := unanswered(); u != nil {
			return u
		}
		return &rpcError{Code: codeInternalError, Message: "remote returned non-JSON-RPC body"}
	}
	if err := json.Unmarshal(payload, &rpc); err != nil {
		return nil, nonRPC()
	}
	if len(rpc.Error) > 0 && string(rpc.Error) != "null" {
		var jsonRPC rpcError
		if json.Unmarshal(rpc.Error, &jsonRPC) == nil {
			return nil, &jsonRPC
		}
		// Not a JSON-RPC error object: the transport's compact envelope, whose code
		// is a string. It answers a request refused before it was read.
		if c, ok := compactError(payload); ok {
			if u := unanswered(); u != nil {
				return nil, u
			}
			return nil, &rpcError{Code: codeInternalError,
				Message: fmt.Sprintf("remote MCP refused the request (HTTP %d, %s): %s", resp.StatusCode, c.Code, c.Message)}
		}
		return nil, nonRPC()
	}
	return rpc.Result, nil
}

// compactErrorBody is the transport's compact error envelope, which the remote
// answers a request it refuses before reading it with (an HTTP 429, for one):
// {"error": {"code": "rate_limited", "message": "…"}}. Unlike a JSON-RPC
// error, its code is a string.
type compactErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// compactError decodes body as the compact error envelope; ok reports whether
// it is one.
func compactError(body []byte) (compactErrorBody, bool) {
	var env struct {
		Error compactErrorBody `json:"error"`
	}
	if json.Unmarshal(bytes.TrimSpace(body), &env) != nil || (env.Error.Code == "" && env.Error.Message == "") {
		return compactErrorBody{}, false
	}
	return env.Error, true
}

// rateLimited is the error for an HTTP 429 from the remote MCP. The remote
// refuses before it reads the request, so nothing ran. The message carries the
// server's own words and its Retry-After; the data carries Retry-After and the
// RateLimit-* fields as sent.
func rateLimited(h http.Header, body []byte) *rpcError {
	msg := "remote MCP rate-limited the request (HTTP 429)"
	if c, ok := compactError(body); ok && c.Message != "" {
		msg += ": " + c.Message
	}
	retryAfter := strings.TrimSpace(h.Get("Retry-After"))
	data := map[string]any{"http_status": http.StatusTooManyRequests}
	if retryAfter != "" {
		msg += " (Retry-After: " + RetryAfterText(retryAfter) + ")"
		data["retry_after"] = retryAfter
	}
	for _, f := range []string{"RateLimit-Limit", "RateLimit-Remaining", "RateLimit-Reset"} {
		if v := strings.TrimSpace(h.Get(f)); v != "" {
			data[strings.ToLower(strings.ReplaceAll(f, "-", "_"))] = v
		}
	}
	return &rpcError{Code: codeInternalError, Kind: KindRateLimited, Message: msg, Data: data, RetryAfter: retryAfter}
}

// RetryAfterText renders a Retry-After value for a person: whole seconds as
// "N seconds", an HTTP date as it was sent.
func RetryAfterText(v string) string {
	n, err := strconv.Atoi(v)
	switch {
	case err != nil || n < 0:
		return v
	case n == 1:
		return "1 second"
	default:
		return strconv.Itoa(n) + " seconds"
	}
}

// toolCallUnanswered is the error for a tools/call that was sent but never
// answered: the server may have run the call. It never says "unreachable", which
// reads as "it never ran" and invites a retry that could apply a write twice. Its
// data carries the fields the server itself uses for an unknown outcome.
func toolCallUnanswered(cause string) *rpcError {
	return &rpcError{
		Code: codeInternalError,
		Kind: KindOutcomeUnknown,
		Message: "the tool call was sent to the remote MCP but its result never arrived (" + cause +
			"); the call may have run and its outcome is unknown: read the current state before retrying",
		Data: map[string]any{"outcome": "unknown", "attempted": true},
	}
}

// lostAnswerCause describes why a sent request's answer never arrived: the
// CLI's own deadline, or the transport error.
func lostAnswerCause(err error, start time.Time) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "the CLI stopped waiting after " + time.Since(start).Round(time.Second).String()
	}
	return err.Error()
}

// extractJSONRPCPayload returns the JSON-RPC object from either a plain JSON
// body or the last `data:` frame of an SSE stream.
func extractJSONRPCPayload(body []byte, contentType string) ([]byte, error) {
	trimmed := bytes.TrimSpace(body)
	if strings.Contains(contentType, "text/event-stream") || bytes.HasPrefix(trimmed, []byte("event:")) || bytes.HasPrefix(trimmed, []byte("data:")) {
		var last []byte
		for _, line := range strings.Split(string(trimmed), "\n") {
			line = strings.TrimSpace(line)
			if after, ok := strings.CutPrefix(line, "data:"); ok {
				last = []byte(strings.TrimSpace(after))
			}
		}
		if len(last) == 0 {
			return nil, fmt.Errorf("SSE response contained no data frame")
		}
		return last, nil
	}
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty response body from remote MCP")
	}
	return trimmed, nil
}
