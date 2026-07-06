package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestExitCodes_AreStable(t *testing.T) {
	// Pin the documented contract so a reorder can't silently change codes.
	pairs := map[int]int{
		ExitOK:          0,
		ExitGeneric:     1,
		ExitUsage:       2,
		ExitAuth:        3,
		ExitNotFound:    4,
		ExitRateLimited: 5,
		ExitNetwork:     6,
		ExitValidation:  7,
	}
	for got, want := range pairs {
		if got != want {
			t.Errorf("exit code %d != %d", got, want)
		}
	}
}

func TestError_EnvelopeFields(t *testing.T) {
	e := New(ExitAuth, "no token", "Run `givmo login`.").WithRequestID("req-123")
	if e.Code != ExitAuth {
		t.Errorf("code = %d", e.Code)
	}
	if e.RequestID != "req-123" {
		t.Errorf("request_id = %q", e.RequestID)
	}
	if e.DocURL == "" || !strings.Contains(e.DocURL, "auth-required") {
		t.Errorf("doc_url should embed the code name: %q", e.DocURL)
	}
	if e.ExitCode() != ExitAuth {
		t.Errorf("ExitCode() = %d", e.ExitCode())
	}
}

func TestAsError_WrapsPlainError(t *testing.T) {
	plain := errors.New("boom")
	e := AsError(plain)
	if e.Code != ExitGeneric {
		t.Errorf("plain error should map to generic, got %d", e.Code)
	}
	if e.Message != "boom" {
		t.Errorf("message = %q", e.Message)
	}
	// Wrapped *Error must be preserved verbatim.
	orig := New(ExitNotFound, "missing", "")
	if got := AsError(orig); got != orig {
		t.Errorf("AsError should return the same *Error instance")
	}
}

func TestPrinter_JSONErrorEnvelopeShape(t *testing.T) {
	var out, errw bytes.Buffer
	p := NewPrinter(&out, &errw, true)
	p.Errorf(New(ExitValidation, "bad manifest", "fix it").WithRequestID("r1"))

	var got struct {
		Error struct {
			Code        int    `json:"code"`
			Message     string `json:"message"`
			Remediation string `json:"remediation"`
			RequestID   string `json:"request_id"`
			DocURL      string `json:"doc_url"`
		} `json:"error"`
	}
	if err := json.Unmarshal(errw.Bytes(), &got); err != nil {
		t.Fatalf("error envelope is not valid JSON: %v\n%s", err, errw.String())
	}
	if got.Error.Code != 7 || got.Error.Message != "bad manifest" || got.Error.Remediation != "fix it" {
		t.Errorf("envelope fields wrong: %+v", got.Error)
	}
	if got.Error.RequestID != "r1" {
		t.Errorf("request_id = %q", got.Error.RequestID)
	}
	if got.Error.DocURL == "" {
		t.Error("doc_url must be present")
	}
	if out.Len() != 0 {
		t.Errorf("errors must go to stderr, not stdout; stdout had: %s", out.String())
	}
}

func TestPrinter_ResultJSONShape(t *testing.T) {
	var out bytes.Buffer
	p := NewPrinter(&out, &bytes.Buffer{}, true)
	payload := map[string]any{"id": "c_1", "name": "Water"}
	// In JSON mode the human renderer is ignored.
	if err := p.Result(payload, nil); err != nil {
		t.Fatalf("Result: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if got["id"] != "c_1" || got["name"] != "Water" {
		t.Errorf("json result fields wrong: %+v", got)
	}
}

func TestPrinter_HumanUsedWhenNotJSON(t *testing.T) {
	var out bytes.Buffer
	p := NewPrinter(&out, &bytes.Buffer{}, false)
	err := p.Result(map[string]any{"x": 1}, func(w io.Writer) {
		io.WriteString(w, "HUMAN OUTPUT")
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "HUMAN OUTPUT") {
		t.Errorf("human renderer not used in non-json mode: %q", out.String())
	}
}

func TestCodeName(t *testing.T) {
	if CodeName(ExitAuth) != "auth-required" {
		t.Errorf("code name for auth = %q", CodeName(ExitAuth))
	}
	if CodeName(999) != "unknown" {
		t.Errorf("unknown code name = %q", CodeName(999))
	}
}
