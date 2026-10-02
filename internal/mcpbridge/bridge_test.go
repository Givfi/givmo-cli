package mcpbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeRemote is a RemoteInvoker that returns canned tools/list + tools/call
// responses, letting us test the stdio framing with no network.
type fakeRemote struct {
	toolsListResult json.RawMessage
	callResult      json.RawMessage
	lastMethod      string
	lastParams      json.RawMessage
}

func (f *fakeRemote) Forward(_ context.Context, method string, params json.RawMessage) (json.RawMessage, *rpcError) {
	f.lastMethod = method
	f.lastParams = params
	switch method {
	case "tools/list":
		return f.toolsListResult, nil
	case "tools/call":
		return f.callResult, nil
	default:
		return nil, &rpcError{Code: codeMethodNotFound, Message: "unexpected"}
	}
}

// decodeResponses splits newline-delimited JSON-RPC responses from output.
func decodeResponses(t *testing.T, out []byte) []rpcResponse {
	t.Helper()
	var resps []rpcResponse
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r rpcResponse
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("response line not valid JSON-RPC: %v\n%s", err, line)
		}
		resps = append(resps, r)
	}
	return resps
}

func TestServe_InitializeFraming(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}` + "\n")
	var out bytes.Buffer
	s := NewServer(Config{In: in, Out: &out, Remote: &fakeRemote{}, Version: "9.9.9"})
	if err := s.Serve(context.Background()); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	resps := decodeResponses(t, out.Bytes())
	if len(resps) != 1 {
		t.Fatalf("want 1 response, got %d", len(resps))
	}
	r := resps[0]
	if r.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q", r.JSONRPC)
	}
	if string(r.ID) != "1" {
		t.Errorf("id echoed wrong: %s", r.ID)
	}
	// Result must include protocolVersion + serverInfo.
	b, _ := json.Marshal(r.Result)
	var res InitializeResult
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatalf("initialize result decode: %v", err)
	}
	if res.ProtocolVersion != ProtocolVersion {
		t.Errorf("protocolVersion = %q", res.ProtocolVersion)
	}
	if res.ServerInfo["version"] != "9.9.9" {
		t.Errorf("serverInfo.version = %v", res.ServerInfo["version"])
	}
	if _, ok := res.Capabilities["tools"]; !ok {
		t.Error("capabilities.tools should be advertised")
	}
}

func TestServe_ToolsListBridgesAndFilters(t *testing.T) {
	remote := &fakeRemote{
		toolsListResult: json.RawMessage(`{"tools":[
			{"name":"search_charities","description":"d1"},
			{"name":"create_donation_intent","description":"d2"},
			{"name":"secret_tool","description":"d3"}
		]}`),
	}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/list"}` + "\n")
	var out bytes.Buffer
	// Allowlist only two tools.
	s := NewServer(Config{In: in, Out: &out, Remote: remote, Tools: []string{"search_charities", "create_donation_intent"}})
	if err := s.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	resps := decodeResponses(t, out.Bytes())
	if len(resps) != 1 {
		t.Fatalf("want 1 response, got %d", len(resps))
	}
	b, _ := json.Marshal(resps[0].Result)
	var res struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) != 2 {
		t.Fatalf("allowlist should yield 2 tools, got %d: %+v", len(res.Tools), res.Tools)
	}
	for _, tl := range res.Tools {
		if tl.Name == "secret_tool" {
			t.Error("filtered tool leaked through")
		}
	}
	if remote.lastMethod != "tools/list" {
		t.Errorf("remote not invoked for tools/list: %q", remote.lastMethod)
	}
}

func TestServe_ToolsCallRejectsOutsideAllowlist(t *testing.T) {
	remote := &fakeRemote{callResult: json.RawMessage(`{"content":[]}`)}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"secret_tool","arguments":{}}}` + "\n")
	var out bytes.Buffer
	s := NewServer(Config{In: in, Out: &out, Remote: remote, Tools: []string{"search_charities"}})
	if err := s.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	resps := decodeResponses(t, out.Bytes())
	if len(resps) != 1 || resps[0].Error == nil {
		t.Fatalf("expected an error response for a filtered tool: %+v", resps)
	}
	if resps[0].Error.Code != codeInvalidParams {
		t.Errorf("error code = %d, want invalidParams", resps[0].Error.Code)
	}
	if remote.lastMethod == "tools/call" {
		t.Error("filtered tool must not be forwarded to remote")
	}
}

func TestServe_ToolsCallForwards(t *testing.T) {
	remote := &fakeRemote{callResult: json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`)}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"search_charities","arguments":{"q":"water"}}}` + "\n")
	var out bytes.Buffer
	s := NewServer(Config{In: in, Out: &out, Remote: remote})
	if err := s.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	resps := decodeResponses(t, out.Bytes())
	if len(resps) != 1 || resps[0].Error != nil {
		t.Fatalf("expected a successful call response, got %+v", resps)
	}
	if remote.lastMethod != "tools/call" {
		t.Errorf("remote not invoked for tools/call")
	}
	// Params must have been forwarded intact.
	if !strings.Contains(string(remote.lastParams), "water") {
		t.Errorf("params not forwarded: %s", remote.lastParams)
	}
}

func TestServe_NotificationGetsNoResponse(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n")
	var out bytes.Buffer
	s := NewServer(Config{In: in, Out: &out, Remote: &fakeRemote{}})
	if err := s.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "" {
		t.Errorf("notification must produce no response, got: %q", out.String())
	}
}

func TestServe_UnknownMethodErrors(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":8,"method":"does/not/exist"}` + "\n")
	var out bytes.Buffer
	s := NewServer(Config{In: in, Out: &out, Remote: &fakeRemote{}})
	if err := s.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	resps := decodeResponses(t, out.Bytes())
	if len(resps) != 1 || resps[0].Error == nil || resps[0].Error.Code != codeMethodNotFound {
		t.Fatalf("expected method-not-found error, got %+v", resps)
	}
}

func TestServe_ParseErrorFraming(t *testing.T) {
	in := strings.NewReader("this is not json\n")
	var out bytes.Buffer
	s := NewServer(Config{In: in, Out: &out, Remote: &fakeRemote{}})
	if err := s.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	resps := decodeResponses(t, out.Bytes())
	if len(resps) != 1 || resps[0].Error == nil || resps[0].Error.Code != codeParseError {
		t.Fatalf("expected parse error, got %+v", resps)
	}
}

// TestServe_ToolsCallForwardsRefusalVerbatim pins that a refusal reaches the agent
// IDE exactly as the server sent it: isError, the text, and every structuredContent
// field a client branches on (the keys and values are the server's; the codes and
// text are illustrative).
func TestServe_ToolsCallForwardsRefusalVerbatim(t *testing.T) {
	refusal := `{"content":[{"type":"text","text":"ExampleOutcomeUnknown: the operation was sent and did not answer. IT MAY HAVE COMPLETED — the outcome is unknown."}],` +
		`"structuredContent":{"source":"example_family","refusal":"ExampleOutcomeUnknown","outcome":"unknown","attempted":true,"safe_to_retry":false},` +
		`"isError":true}`
	remote := &fakeRemote{callResult: json.RawMessage(refusal)}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"example_tool","arguments":{}}}` + "\n")
	var out bytes.Buffer
	s := NewServer(Config{In: in, Out: &out, Remote: remote})
	if err := s.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	resps := decodeResponses(t, out.Bytes())
	if len(resps) != 1 || resps[0].Error != nil {
		t.Fatalf("a refusal is a tool result, not a JSON-RPC error: %+v", resps)
	}
	got, _ := json.Marshal(resps[0].Result)
	var want, have any
	if err := json.Unmarshal([]byte(refusal), &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got, &have); err != nil {
		t.Fatal(err)
	}
	wantJSON, _ := json.Marshal(want)
	haveJSON, _ := json.Marshal(have)
	if !bytes.Equal(wantJSON, haveJSON) {
		t.Errorf("refusal not forwarded verbatim:\n got %s\nwant %s", haveJSON, wantJSON)
	}
}

// TestServe_RateLimitReachesTheIDE runs the stdio bridge over a real HTTPRemote
// against a remote answering 429: the agent IDE gets a JSON-RPC error carrying
// the server's message and its Retry-After, not a decode failure.
func TestServe_RateLimitReachesTheIDE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "55")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"code":"rate_limited","message":"Rate limit exceeded. Retry after the Retry-After period."}}`))
	}))
	defer srv.Close()
	in := strings.NewReader(`{"jsonrpc":"2.0","id":4,"method":"tools/list"}` + "\n")
	var out bytes.Buffer
	s := NewServer(Config{In: in, Out: &out, Remote: NewHTTPRemote(srv.URL, "", srv.Client())})
	if err := s.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	resps := decodeResponses(t, out.Bytes())
	if len(resps) != 1 || resps[0].Error == nil {
		t.Fatalf("expected a JSON-RPC error, got %+v", resps)
	}
	e := resps[0].Error
	if !strings.Contains(e.Message, "Retry-After: 55 seconds") || !strings.Contains(e.Message, "Rate limit exceeded") {
		t.Errorf("the IDE must see the server's message and Retry-After: %q", e.Message)
	}
	data, _ := e.Data.(map[string]any)
	if data["retry_after"] != "55" {
		t.Errorf("error data must carry retry_after: %+v", e.Data)
	}
}

// donorToolsList is a tools/list result in the shape the remote MCP sends an
// anonymous caller: a public catalog tool, marked noauth, and a donor's own tool,
// marked oauth2 with the scope signing in reaches. Field names and values are the
// server's; descriptions and schemas are shortened. The last tool carries fields
// a later server may add, which the bridge must forward too.
const donorToolsList = `{"tools":[
	{"name":"search_charities","title":"Search charities",
	 "description":"Search Givmo's charity catalog by name or keyword, and/or by exact EIN (tax id).",
	 "inputSchema":{"type":"object","properties":{"query":{"type":"string"},"ein":{"type":"string"}},"required":[],"additionalProperties":false},
	 "outputSchema":{"type":"object","properties":{"source":{"type":"string"},"count":{"type":"integer"}}},
	 "annotations":{"readOnlyHint":true,"destructiveHint":false,"idempotentHint":true,"openWorldHint":false},
	 "securitySchemes":[{"type":"noauth"}]},
	{"name":"get_receipt","title":"Get my tax receipt summary",
	 "description":"Get the signed-in Givmo user's tax-receipt view for a tax year.",
	 "inputSchema":{"type":"object","properties":{"tax_year":{"type":"integer"}},"additionalProperties":false},
	 "annotations":{"readOnlyHint":true,"destructiveHint":false,"idempotentHint":true,"openWorldHint":false},
	 "securitySchemes":[{"type":"oauth2","scopes":["givmo.receipts.read"]}]},
	{"name":"example_future_tool","title":"Example",
	 "description":"A tool carrying fields a later server may add.",
	 "inputSchema":{"type":"object","properties":{},"additionalProperties":false},
	 "icons":[{"src":"https://example.org/icon.png"}],"_meta":{"example":true}}
]}`

func TestServe_ToolsListForwardsEveryToolField(t *testing.T) {
	remote := &fakeRemote{toolsListResult: json.RawMessage(donorToolsList)}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n")
	var out bytes.Buffer
	s := NewServer(Config{In: in, Out: &out, Remote: remote})
	if err := s.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	resps := decodeResponses(t, out.Bytes())
	if len(resps) != 1 || resps[0].Error != nil {
		t.Fatalf("expected a tools/list result, got %+v", resps)
	}
	got, _ := json.Marshal(resps[0].Result)
	if !jsonEqual(got, []byte(donorToolsList)) {
		t.Errorf("tools/list not forwarded whole:\n got %s\nwant %s", got, donorToolsList)
	}
}

func TestServe_ToolsListFilterKeepsEveryField(t *testing.T) {
	remote := &fakeRemote{toolsListResult: json.RawMessage(donorToolsList)}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n")
	var out bytes.Buffer
	s := NewServer(Config{In: in, Out: &out, Remote: remote, Tools: []string{"get_receipt"}})
	if err := s.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	resps := decodeResponses(t, out.Bytes())
	if len(resps) != 1 || resps[0].Error != nil {
		t.Fatalf("expected a tools/list result, got %+v", resps)
	}
	got, _ := json.Marshal(resps[0].Result)
	var all struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal([]byte(donorToolsList), &all); err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(map[string]any{"tools": []json.RawMessage{all.Tools[1]}})
	if !jsonEqual(got, want) {
		t.Errorf("filtered tools/list must keep the tool whole:\n got %s\nwant %s", got, want)
	}
}

func TestServe_ToolsListEmptyIsAnArray(t *testing.T) {
	remote := &fakeRemote{toolsListResult: json.RawMessage(`{"tools":[]}`)}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n")
	var out bytes.Buffer
	s := NewServer(Config{In: in, Out: &out, Remote: remote, Tools: []string{"absent_tool"}})
	if err := s.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"tools":[]`) {
		t.Errorf("an empty list must stay an array: %s", out.String())
	}
}
