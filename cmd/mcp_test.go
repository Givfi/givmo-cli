package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMCPServe_RenewsAndReplaysOnce drives the real `givmo mcp serve` over its
// stdio against a fake Givmo. The stored access token is about to expire, so the
// first call goes out with a renewed one and never with the expiring one; a 401 to
// a bearer brings one renewal and one repeat of the call; and a remote that
// refuses every bearer gets one repeat, then the error, never a loop.
func TestMCPServe_RenewsAndReplaysOnce(t *testing.T) {
	tokens := newRotatingTokens()
	var mu sync.Mutex
	seen := map[string][]string{} // tool name -> the bearers its requests carried
	mux := http.NewServeMux()
	mux.Handle("/token", tokens)
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		var rq struct {
			ID     json.RawMessage `json:"id"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&rq)
		bearer := r.Header.Get("Authorization")
		mu.Lock()
		seen[rq.Params.Name] = append(seen[rq.Params.Name], bearer)
		mu.Unlock()
		refused := bearer == "Bearer test-access-0" || // expired by now on the server's clock
			(rq.Params.Name == "call_two" && bearer == "Bearer test-access-1") || // revoked
			rq.Params.Name == "call_three"
		if refused {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"ok"}],"isError":false}}`, rq.ID)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("GIVMO_HOME", t.TempDir())
	t.Setenv("GIVMO_TOKEN_BACKEND", "file")
	t.Setenv("GIVMO_PROFILE", "sandbox")
	t.Setenv("GIVMO_API_BASE", srv.URL)
	t.Setenv("GIVMO_AUTH_BASE", srv.URL)
	t.Setenv("GIVMO_API_KEY", "")
	storeSignIn(t, srv.URL, 10*time.Second)

	var in strings.Builder
	for i, name := range []string{"call_one", "call_two", "call_three"} {
		fmt.Fprintf(&in, `{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":{}}}`+"\n", i+1, name)
	}
	var out bytes.Buffer
	cmd := newMCPServeCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetIn(strings.NewReader(in.String()))
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("mcp serve: %v", err)
	}

	answers := map[int]map[string]json.RawMessage{}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var resp struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("bridge output %q: %v", line, err)
		}
		answers[resp.ID] = map[string]json.RawMessage{"result": resp.Result, "error": resp.Error}
	}
	for id, want := range map[int]bool{1: true, 2: true, 3: false} {
		if got := answers[id]["result"] != nil; got != want {
			t.Errorf("call %d answered %v; want a result: %v", id, answers[id], want)
		}
	}
	if e := string(answers[3]["error"]); !strings.Contains(e, "rejected auth (HTTP 401)") {
		t.Errorf("call 3's error = %s, want the remote's refusal", e)
	}
	for name, want := range map[string][]string{
		"call_one":   {"Bearer test-access-1"},
		"call_two":   {"Bearer test-access-1", "Bearer test-access-2"},
		"call_three": {"Bearer test-access-2", "Bearer test-access-3"},
	} {
		if got := seen[name]; !reflect.DeepEqual(got, want) {
			t.Errorf("%s carried %q, want %q", name, got, want)
		}
	}
	if n := tokens.count(); n != 3 {
		t.Errorf("%d renewals, want 3 (one before call 1, one after each 401)", n)
	}
}
