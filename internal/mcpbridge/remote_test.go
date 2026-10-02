package mcpbridge

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
)

func TestHTTPRemote_ForwardJSONResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Assert the bearer was injected.
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("missing/incorrect auth header: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"search_charities"}]}}`))
	}))
	defer srv.Close()

	rem := NewHTTPRemote(srv.URL, "Bearer tok", srv.Client())
	res, rerr := rem.Forward(context.Background(), "tools/list", nil)
	if rerr != nil {
		t.Fatalf("Forward error: %s", ErrorText(rerr))
	}
	if !strings.Contains(string(res), "search_charities") {
		t.Errorf("unexpected result: %s", res)
	}
}

func TestHTTPRemote_ForwardSSEResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"ok\":true}}\n\n"))
	}))
	defer srv.Close()

	rem := NewHTTPRemote(srv.URL, "", srv.Client())
	res, rerr := rem.Forward(context.Background(), "tools/list", nil)
	if rerr != nil {
		t.Fatalf("Forward error: %s", ErrorText(rerr))
	}
	if !strings.Contains(string(res), `"ok":true`) {
		t.Errorf("SSE data frame not extracted: %s", res)
	}
}

func TestHTTPRemote_ForwardRemoteError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"nope"}}`))
	}))
	defer srv.Close()
	rem := NewHTTPRemote(srv.URL, "", srv.Client())
	_, rerr := rem.Forward(context.Background(), "tools/call", nil)
	if rerr == nil || !strings.Contains(rerr.Message, "nope") {
		t.Fatalf("expected remote error to be surfaced, got %v", rerr)
	}
}

func TestHTTPRemote_ForwardAuthRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	rem := NewHTTPRemote(srv.URL, "", srv.Client())
	_, rerr := rem.Forward(context.Background(), "tools/list", nil)
	if rerr == nil || !strings.Contains(rerr.Message, "login") {
		t.Fatalf("expected auth-rejected error mentioning login, got %v", rerr)
	}
}

// slowServer answers every request with a JSON-RPC result after delay, giving up
// early when the client goes away.
func slowServer(t *testing.T, delay time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`))
	}))
}

func TestHTTPRemote_DefaultTimeouts(t *testing.T) {
	rem := NewHTTPRemote("http://127.0.0.1:1/mcp", "", nil)
	// Above the server's 120 s maximum per tool call, so its answer arrives first.
	if rem.CallTimeout != 150*time.Second || rem.CallTimeout <= 120*time.Second {
		t.Errorf("tools/call timeout = %s, want 150s", rem.CallTimeout)
	}
	if rem.AuxTimeout != 30*time.Second {
		t.Errorf("auxiliary timeout = %s, want 30s", rem.AuxTimeout)
	}
}

func TestHTTPRemote_ToolCallOutlivesTheAuxiliaryTimeout(t *testing.T) {
	srv := slowServer(t, 300*time.Millisecond)
	defer srv.Close()
	rem := NewHTTPRemote(srv.URL, "", srv.Client())
	rem.AuxTimeout = 100 * time.Millisecond
	rem.CallTimeout = 5 * time.Second

	if _, rerr := rem.Forward(context.Background(), "tools/call", nil); rerr != nil {
		t.Fatalf("a tools/call slower than the auxiliary timeout must still answer: %s", ErrorText(rerr))
	}
	_, rerr := rem.Forward(context.Background(), "tools/list", nil)
	if rerr == nil {
		t.Fatal("tools/list must be held to the auxiliary timeout")
	}
	if rerr.Kind == KindOutcomeUnknown {
		t.Errorf("a lost tools/list is not an unknown tool-call outcome: %+v", rerr)
	}
}

func TestHTTPRemote_ClientTimeoutDoesNotCutAToolCall(t *testing.T) {
	srv := slowServer(t, 300*time.Millisecond)
	defer srv.Close()
	client := srv.Client()
	client.Timeout = 50 * time.Millisecond // what the CLI's 30 s auxiliary client would do
	rem := NewHTTPRemote(srv.URL, "", client)
	rem.CallTimeout = 5 * time.Second

	if _, rerr := rem.Forward(context.Background(), "tools/call", nil); rerr != nil {
		t.Fatalf("the client's own timeout must not cut a tools/call: %s", ErrorText(rerr))
	}
}

// assertOutcomeUnknown checks a lost tools/call answer reads as "may have run",
// never as "unreachable".
func assertOutcomeUnknown(t *testing.T, rerr *rpcError) {
	t.Helper()
	if rerr == nil {
		t.Fatal("expected the lost answer to surface as an error")
	}
	if rerr.Kind != KindOutcomeUnknown {
		t.Errorf("kind = %v, want KindOutcomeUnknown (%s)", rerr.Kind, ErrorText(rerr))
	}
	if !strings.Contains(rerr.Message, "may have run") || !strings.Contains(rerr.Message, "read the current state before retrying") {
		t.Errorf("message must say the call may have run: %q", rerr.Message)
	}
	if strings.Contains(rerr.Message, "unreachable") {
		t.Errorf("a sent call must never read as unreachable: %q", rerr.Message)
	}
	data, _ := rerr.Data.(map[string]any)
	if data["outcome"] != "unknown" || data["attempted"] != true {
		t.Errorf("data must carry outcome unknown, attempted true: %+v", rerr.Data)
	}
}

func TestHTTPRemote_ToolCallTimeoutAfterSendIsOutcomeUnknown(t *testing.T) {
	srv := slowServer(t, 5*time.Second)
	defer srv.Close()
	rem := NewHTTPRemote(srv.URL, "", srv.Client())
	rem.CallTimeout = 100 * time.Millisecond

	_, rerr := rem.Forward(context.Background(), "tools/call", json.RawMessage(`{"name":"example_tool","arguments":{}}`))
	assertOutcomeUnknown(t, rerr)
	if !strings.Contains(rerr.Message, "stopped waiting") {
		t.Errorf("message must say the CLI stopped waiting: %q", rerr.Message)
	}
}

func TestHTTPRemote_AnswerCutOffIsOutcomeUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,`))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	rem := NewHTTPRemote(srv.URL, "", srv.Client())
	rem.CallTimeout = 200 * time.Millisecond

	_, rerr := rem.Forward(context.Background(), "tools/call", nil)
	assertOutcomeUnknown(t, rerr)
	if !strings.Contains(rerr.Message, "cut off") {
		t.Errorf("message must say the answer was cut off: %q", rerr.Message)
	}
}

func TestHTTPRemote_ConnectionBrokenAfterSendIsOutcomeUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		conn.Close()
	}))
	defer srv.Close()
	rem := NewHTTPRemote(srv.URL, "", srv.Client())

	_, rerr := rem.Forward(context.Background(), "tools/call", nil)
	assertOutcomeUnknown(t, rerr)
}

func TestHTTPRemote_ServerFailureWithoutAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}))
	defer srv.Close()
	rem := NewHTTPRemote(srv.URL, "", srv.Client())

	// A tools/call the server failed on may have run before the failure.
	_, rerr := rem.Forward(context.Background(), "tools/call", nil)
	assertOutcomeUnknown(t, rerr)
	if !strings.Contains(rerr.Message, "HTTP 500") {
		t.Errorf("message must name the status: %q", rerr.Message)
	}
	// Any other request is only a failed fetch.
	_, rerr = rem.Forward(context.Background(), "tools/list", nil)
	if rerr == nil || rerr.Kind != KindRemote {
		t.Errorf("a failed tools/list is a plain remote error, got %+v", rerr)
	}
}

func TestHTTPRemote_NothingSentIsUnreachable(t *testing.T) {
	// A port with no listener: the request never reaches a server.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	rem := NewHTTPRemote("http://"+addr+"/mcp", "", nil)

	_, rerr := rem.Forward(context.Background(), "tools/call", nil)
	if rerr == nil || rerr.Kind != KindUnreachable {
		t.Fatalf("an unsent call is unreachable, got %+v", rerr)
	}
	if !strings.Contains(rerr.Message, "unreachable") {
		t.Errorf("message = %q", rerr.Message)
	}
}
