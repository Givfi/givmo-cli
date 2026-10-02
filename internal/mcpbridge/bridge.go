// Package mcpbridge implements a local stdio MCP server (JSON-RPC 2.0 over
// newline-delimited stdio) that BRIDGES tool calls to the remote Givmo MCP over
// HTTP using the CLI's stored session. An agent IDE spawns `givmo mcp serve`
// and speaks JSON-RPC to it; the bridge forwards initialize/tools-list/tools-
// call to the remote Streamable-HTTP MCP endpoint, injecting the CLI's bearer.
//
// This keeps the agent's MCP session tied to the developer's authenticated CLI
// session without the agent ever handling the token.
package mcpbridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ProtocolVersion is the MCP protocol version the bridge advertises.
const ProtocolVersion = "2025-06-18"

// JSON-RPC 2.0 standard error codes.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// rpcRequest is an incoming JSON-RPC 2.0 request or notification.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"` // absent => notification
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcResponse is a JSON-RPC 2.0 response.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
	// Kind classifies a failure the remote detected itself, for callers that
	// branch on it (the CLI's own commands). It never reaches the wire.
	Kind ErrorKind `json:"-"`
	// RetryAfter is a rate-limited answer's Retry-After header, verbatim.
	RetryAfter string `json:"-"`
}

// ErrorKind classifies a failure of a request to the remote MCP.
type ErrorKind int

const (
	// KindRemote is the remote's own JSON-RPC error, or a failure not classified
	// below.
	KindRemote ErrorKind = iota
	// KindUnreachable: the request never reached the remote (DNS, connect, TLS,
	// or a timeout before it was sent), so nothing ran.
	KindUnreachable
	// KindOutcomeUnknown: a tools/call was sent but no answer arrived (the CLI
	// stopped waiting, the connection broke, or the remote failed without a
	// JSON-RPC answer), so the call may have run.
	KindOutcomeUnknown
	// KindRateLimited: the remote answered HTTP 429 before reading the request,
	// so nothing ran; RetryAfter says when to try again.
	KindRateLimited
)

// RemoteInvoker forwards a raw JSON-RPC message to the remote Givmo MCP and
// returns the remote's raw JSON-RPC response body. It is an interface so tests
// can drive the stdio framing without any network.
type RemoteInvoker interface {
	Forward(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *rpcError)
}

// Server is the stdio JSON-RPC server.
type Server struct {
	in       io.Reader
	out      io.Writer
	remote   RemoteInvoker
	toolFilt map[string]bool // if non-empty, only these tools are exposed
	// serverInfo advertised in initialize.
	name    string
	version string
}

// Config configures the stdio server.
type Config struct {
	In      io.Reader
	Out     io.Writer
	Remote  RemoteInvoker
	Tools   []string // optional allowlist filter for tools/list
	Name    string
	Version string
}

// NewServer builds a stdio bridge server.
func NewServer(cfg Config) *Server {
	s := &Server{
		in:      cfg.In,
		out:     cfg.Out,
		remote:  cfg.Remote,
		name:    cfg.Name,
		version: cfg.Version,
	}
	if s.name == "" {
		s.name = "givmo-cli-bridge"
	}
	if len(cfg.Tools) > 0 {
		s.toolFilt = map[string]bool{}
		for _, t := range cfg.Tools {
			s.toolFilt[t] = true
		}
	}
	return s
}

// Serve reads newline-delimited JSON-RPC messages from stdin and writes
// responses to stdout until EOF. Notifications (no id) get no response.
func (s *Server) Serve(ctx context.Context) error {
	sc := bufio.NewScanner(s.in)
	// Allow large frames (tool payloads can be sizable).
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		s.handleLine(ctx, line)
	}
	return sc.Err()
}

// handleLine processes one framed message.
func (s *Server) handleLine(ctx context.Context, line []byte) {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		s.write(rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: codeParseError, Message: "parse error: " + err.Error()}})
		return
	}
	if req.JSONRPC != "2.0" {
		if req.ID != nil {
			s.write(rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: codeInvalidRequest, Message: "jsonrpc must be \"2.0\""}})
		}
		return
	}
	isNotification := req.ID == nil

	result, rerr := s.dispatch(ctx, req)
	if isNotification {
		return // notifications never get a response
	}
	if rerr != nil {
		s.write(rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: rerr})
		return
	}
	s.write(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
}

// dispatch routes a method to its handler.
func (s *Server) dispatch(ctx context.Context, req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req.Params), nil
	case "notifications/initialized", "initialized":
		return struct{}{}, nil
	case "ping":
		return struct{}{}, nil
	case "tools/list":
		return s.handleToolsList(ctx)
	case "tools/call":
		return s.handleToolsCall(ctx, req.Params)
	default:
		return nil, &rpcError{Code: codeMethodNotFound, Message: "method not found: " + req.Method}
	}
}

// InitializeResult is the MCP initialize response.
type InitializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ServerInfo      map[string]any `json:"serverInfo"`
}

func (s *Server) handleInitialize(_ json.RawMessage) InitializeResult {
	return InitializeResult{
		ProtocolVersion: ProtocolVersion,
		Capabilities: map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		ServerInfo: map[string]any{
			"name":    s.name,
			"version": s.version,
		},
	}
}

// tool is a single tools/list entry.
type tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

func (s *Server) handleToolsList(ctx context.Context) (any, *rpcError) {
	raw, rerr := s.remote.Forward(ctx, "tools/list", nil)
	if rerr != nil {
		return nil, rerr
	}
	// The remote returns a JSON-RPC result object { "tools": [...] }. Apply the
	// optional allowlist filter.
	var res struct {
		Tools []tool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, &rpcError{Code: codeInternalError, Message: "remote tools/list decode failed: " + err.Error()}
	}
	if s.toolFilt != nil {
		filtered := res.Tools[:0]
		for _, t := range res.Tools {
			if s.toolFilt[t.Name] {
				filtered = append(filtered, t)
			}
		}
		res.Tools = filtered
	}
	return map[string]any{"tools": res.Tools}, nil
}

func (s *Server) handleToolsCall(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	// Enforce the allowlist locally before forwarding.
	if s.toolFilt != nil {
		var probe struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(params, &probe); err != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call params must include a tool name"}
		}
		if !s.toolFilt[probe.Name] {
			return nil, &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf("tool %q is not exposed by this bridge (see --tools)", probe.Name)}
		}
	}
	raw, rerr := s.remote.Forward(ctx, "tools/call", params)
	if rerr != nil {
		return nil, rerr
	}
	// Forward the remote result object verbatim.
	var res any
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, &rpcError{Code: codeInternalError, Message: "remote tools/call decode failed: " + err.Error()}
	}
	return res, nil
}

// write emits a JSON-RPC response as a single newline-terminated line.
func (s *Server) write(resp rpcResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		// Last-ditch: a marshal error becomes an internal error frame.
		data = []byte(fmt.Sprintf(`{"jsonrpc":"2.0","error":{"code":%d,"message":"response marshal failed"}}`, codeInternalError))
	}
	// Newline-delimited framing.
	_, _ = s.out.Write(append(data, '\n'))
}

// NewRPCError builds an rpcError for callers implementing RemoteInvoker.
func NewRPCError(code int, msg string) *rpcError {
	return &rpcError{Code: code, Message: msg}
}

// Standard JSON-RPC codes exported for RemoteInvoker implementations.
const (
	CodeInvalidRequest = codeInvalidRequest
	CodeInternalError  = codeInternalError
	CodeMethodNotFound = codeMethodNotFound
)

// ErrorText returns a printable form of an rpcError (nil-safe), for logging in
// bridge callers.
func ErrorText(e *rpcError) string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("[%d] %s", e.Code, strings.TrimSpace(e.Message))
}
