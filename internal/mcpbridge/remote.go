package mcpbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
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
}

// NewHTTPRemote builds an HTTP remote. endpoint is the full MCP URL (e.g.
// https://mcp.givmo.io/mcp); authHeader is the full "Bearer …" value (may be
// empty for the public tier).
func NewHTTPRemote(endpoint, authHeader string, client *http.Client) *HTTPRemote {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return &HTTPRemote{
		endpoint: endpoint,
		authHdr:  authHeader,
		client:   client,
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
		return nil, &rpcError{Code: codeInternalError, Message: "remote MCP unreachable: " + err.Error() +
			" (the mcp.givmo.io endpoint may not be live yet — ready-inert)"}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, &rpcError{Code: codeInvalidRequest, Message: fmt.Sprintf("remote MCP rejected auth (HTTP %d); run `givmo login`", resp.StatusCode)}
	}

	payload, err := extractJSONRPCPayload(body, resp.Header.Get("Content-Type"))
	if err != nil {
		return nil, &rpcError{Code: codeInternalError, Message: err.Error()}
	}

	var rpc struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(payload, &rpc); err != nil {
		return nil, &rpcError{Code: codeInternalError, Message: "remote returned non-JSON-RPC body"}
	}
	if rpc.Error != nil {
		return nil, rpc.Error
	}
	return rpc.Result, nil
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
