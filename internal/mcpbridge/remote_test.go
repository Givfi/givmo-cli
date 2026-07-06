package mcpbridge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
