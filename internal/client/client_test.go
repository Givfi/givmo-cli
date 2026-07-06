package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/givfi/givmo-cli/internal/auth"
	"github.com/givfi/givmo-cli/internal/output"
)

func TestBuildRequest_AttachesAuthAndHeaders(t *testing.T) {
	c := New(Options{
		BaseURL:    "https://api.example.test",
		Credential: &auth.Credential{AccessToken: "tok-123"},
	})
	req, err := c.BuildRequest(context.Background(), http.MethodPost, "charities", []byte(`{}`))
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if req.URL.String() != "https://api.example.test/charities" {
		t.Errorf("url = %q", req.URL.String())
	}
	if got := req.Header.Get("Authorization"); got != "Bearer tok-123" {
		t.Errorf("auth header = %q", got)
	}
	if req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("content-type missing on body request")
	}
	if req.Header.Get("User-Agent") == "" {
		t.Error("user-agent must be set")
	}
}

func TestDo_UnwrapsDataEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "srv-req-1")
		w.WriteHeader(200)
		w.Write([]byte(`{"data":[{"id":"c_1","name":"Water"}],"meta":{"total":1}}`))
	}))
	defer srv.Close()

	c := New(Options{BaseURL: srv.URL})
	resp, err := c.Do(context.Background(), "GET", "/charities", nil, false)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.RequestID != "srv-req-1" {
		t.Errorf("request_id = %q", resp.RequestID)
	}
	var list []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := resp.DecodeInto(&list); err != nil {
		t.Fatalf("DecodeInto: %v", err)
	}
	if len(list) != 1 || list[0].ID != "c_1" {
		t.Errorf("unwrapped data wrong: %+v", list)
	}
	if len(resp.Meta) == 0 {
		t.Error("meta should be surfaced")
	}
}

func TestDo_SingletonNoEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"c_9","name":"Solo"}`))
	}))
	defer srv.Close()
	c := New(Options{BaseURL: srv.URL})
	resp, err := c.Do(context.Background(), "GET", "/charities/c_9", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	var obj struct {
		ID string `json:"id"`
	}
	if err := resp.DecodeInto(&obj); err != nil {
		t.Fatal(err)
	}
	if obj.ID != "c_9" {
		t.Errorf("singleton decode wrong: %+v", obj)
	}
}

func TestDo_MapsStatusToExitCodes(t *testing.T) {
	cases := []struct {
		status   int
		wantCode int
		body     string
	}{
		{http.StatusUnauthorized, output.ExitAuth, `{"errors":[{"detail":"expired"}]}`},
		{http.StatusForbidden, output.ExitAuth, `{}`},
		{http.StatusNotFound, output.ExitNotFound, `{"errors":[{"title":"not found"}]}`},
		{http.StatusTooManyRequests, output.ExitRateLimited, `{}`},
		{http.StatusBadRequest, output.ExitValidation, `{"message":"bad"}`},
		{http.StatusInternalServerError, output.ExitGeneric, `{}`},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Request-Id", "rq")
			w.WriteHeader(tc.status)
			w.Write([]byte(tc.body))
		}))
		c := New(Options{BaseURL: srv.URL})
		_, err := c.Do(context.Background(), "GET", "/x", nil, false)
		srv.Close()
		if err == nil {
			t.Errorf("status %d: expected error", tc.status)
			continue
		}
		oe := output.AsError(err)
		if oe.Code != tc.wantCode {
			t.Errorf("status %d -> exit %d, want %d", tc.status, oe.Code, tc.wantCode)
		}
		if oe.RequestID != "rq" {
			t.Errorf("status %d: request_id not propagated (%q)", tc.status, oe.RequestID)
		}
		if oe.Remediation == "" {
			t.Errorf("status %d: remediation must be non-empty (agent-actionable)", tc.status)
		}
	}
}

func TestDo_RequireAuthFailsFastWithoutCredential(t *testing.T) {
	c := New(Options{BaseURL: "https://api.example.test"})
	_, err := c.Do(context.Background(), "POST", "/donation-intents", []byte(`{}`), true)
	if err == nil {
		t.Fatal("expected auth error when no credential and requireAuth=true")
	}
	oe := output.AsError(err)
	if oe.Code != output.ExitAuth {
		t.Errorf("exit = %d, want %d", oe.Code, output.ExitAuth)
	}
}

func TestDo_NetworkErrorMapsToExitNetwork(t *testing.T) {
	// Point at a closed server to force a transport error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	c := New(Options{BaseURL: url})
	_, err := c.Do(context.Background(), "GET", "/x", nil, false)
	if err == nil {
		t.Fatal("expected network error")
	}
	if output.AsError(err).Code != output.ExitNetwork {
		t.Errorf("exit = %d, want ExitNetwork", output.AsError(err).Code)
	}
}

func TestRequestID_FromBodyWhenNoHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"data":{"ok":true},"meta":{"request_id":"body-req"}}`))
	}))
	defer srv.Close()
	c := New(Options{BaseURL: srv.URL})
	resp, err := c.Do(context.Background(), "GET", "/x", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if resp.RequestID != "body-req" {
		t.Errorf("request_id from body meta = %q", resp.RequestID)
	}
	_ = json.RawMessage(resp.Raw)
}
