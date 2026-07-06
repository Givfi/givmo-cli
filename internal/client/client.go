// Package client is the CLI's authenticated HTTP client for the Givmo REST
// surface. It centralizes:
//
//   - request construction (base URL, bearer/api-key auth, user-agent),
//   - the ONE error envelope (output.Error) mapped from HTTP status + the
//     backend's `{ "errors": [...] }` shape,
//   - server request_id extraction (X-Request-Id header or body),
//   - the paginated `{ "data": ..., "meta": ... }` envelope unwrap.
//
// Nothing here logs tokens; the Authorization header is never printed.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/givfi/givmo-cli/internal/auth"
	"github.com/givfi/givmo-cli/internal/output"
	"github.com/givfi/givmo-cli/internal/version"
)

// Client talks to one Givmo environment.
type Client struct {
	baseURL string
	cred    *auth.Credential
	http    *http.Client
}

// Options configure a Client.
type Options struct {
	BaseURL    string
	Credential *auth.Credential
	HTTPClient *http.Client
	Timeout    time.Duration
}

// New builds a Client. A nil credential yields an unauthenticated client
// (valid for the public catalog surface).
func New(opts Options) *Client {
	hc := opts.HTTPClient
	if hc == nil {
		to := opts.Timeout
		if to == 0 {
			to = 30 * time.Second
		}
		hc = &http.Client{Timeout: to}
	}
	return &Client{
		baseURL: strings.TrimRight(opts.BaseURL, "/"),
		cred:    opts.Credential,
		http:    hc,
	}
}

// Authenticated reports whether a usable credential is attached.
func (c *Client) Authenticated() bool {
	return c.cred != nil && c.cred.AccessToken != ""
}

// BaseURL returns the configured base URL (for diagnostics/tests).
func (c *Client) BaseURL() string { return c.baseURL }

// Response is a decoded API response with metadata surfaced for callers.
type Response struct {
	Status    int
	RequestID string
	// Data is the unwrapped payload (the `data` field if the envelope was
	// present, else the whole body). Raw JSON for the caller to decode.
	Data json.RawMessage
	// Meta is the pagination/meta object when present.
	Meta json.RawMessage
	// Raw is the full, un-unwrapped response body.
	Raw json.RawMessage
}

// BuildRequest constructs an *http.Request against the base URL with auth +
// standard headers applied. Exposed (and pure aside from header stamping) so
// tests can assert request construction. method is e.g. http.MethodGet; path is
// a leading-slash path like "/charities".
func (c *Client) BuildRequest(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return nil, output.New(output.ExitUsage, "could not build request: "+err.Error(),
			"Check the method and path; path must be a valid URL path.")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", version.UserAgent())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if h := c.cred.AuthorizationHeader(); h != "" {
		req.Header.Set("Authorization", h)
	}
	return req, nil
}

// Do performs a request and maps the outcome into a Response or the single
// error envelope. requireAuth marks endpoints that need a credential so a
// missing token fails fast with ExitAuth rather than a server round-trip.
func (c *Client) Do(ctx context.Context, method, path string, body []byte, requireAuth bool) (*Response, error) {
	if requireAuth && !c.Authenticated() {
		return nil, output.New(output.ExitAuth,
			"this command requires a consumer credential but none is stored",
			"Run `givmo login` to authenticate, or set GIVMO_API_KEY for non-interactive use.")
	}
	req, err := c.BuildRequest(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, output.New(output.ExitNetwork,
			"could not reach the Givmo API: "+err.Error(),
			"Check connectivity and the active profile's api_base (`givmo config view`). "+
				"Note: some endpoints are not live yet (ready-inert) and will fail until enabled.")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	reqID := requestIDFrom(resp, raw)

	if resp.StatusCode >= 400 {
		return nil, mapHTTPError(resp.StatusCode, raw, reqID)
	}

	data, meta := unwrapEnvelope(raw)
	return &Response{
		Status:    resp.StatusCode,
		RequestID: reqID,
		Data:      data,
		Meta:      meta,
		Raw:       json.RawMessage(raw),
	}, nil
}

// DecodeInto unmarshals the unwrapped data payload into v.
func (r *Response) DecodeInto(v any) error {
	if r == nil || len(r.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(r.Data, v); err != nil {
		return output.New(output.ExitGeneric,
			"could not decode the API response: "+err.Error(),
			"The server returned an unexpected shape; report the request_id to Givmo support.").
			WithRequestID(r.RequestID)
	}
	return nil
}

// requestIDFrom extracts a server correlation id from common header names or
// the error body.
func requestIDFrom(resp *http.Response, body []byte) string {
	for _, h := range []string{"X-Request-Id", "X-Request-ID", "Request-Id", "X-Correlation-Id"} {
		if v := resp.Header.Get(h); v != "" {
			return v
		}
	}
	// Some backends echo it in the body.
	var probe struct {
		RequestID string `json:"request_id"`
		Meta      struct {
			RequestID string `json:"request_id"`
		} `json:"meta"`
	}
	if json.Unmarshal(body, &probe) == nil {
		if probe.RequestID != "" {
			return probe.RequestID
		}
		if probe.Meta.RequestID != "" {
			return probe.Meta.RequestID
		}
	}
	return ""
}

// unwrapEnvelope returns (data, meta) from a `{ "data": ..., "meta": ... }`
// paginated envelope, or (wholeBody, nil) for a singleton response.
func unwrapEnvelope(body []byte) (json.RawMessage, json.RawMessage) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return json.RawMessage(body), nil
	}
	var env struct {
		Data json.RawMessage `json:"data"`
		Meta json.RawMessage `json:"meta"`
	}
	if err := json.Unmarshal(trimmed, &env); err == nil && env.Data != nil {
		return env.Data, env.Meta
	}
	return json.RawMessage(body), nil
}

// mapHTTPError converts an HTTP error status + body into the single envelope
// with a stable exit code and an agent-actionable remediation.
func mapHTTPError(status int, body []byte, reqID string) *output.Error {
	msg, remediation := serverErrorMessage(body)

	var code int
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		code = output.ExitAuth
		if remediation == "" {
			remediation = "Run `givmo login` to obtain a valid consumer token, then retry."
		}
	case http.StatusNotFound:
		code = output.ExitNotFound
		if remediation == "" {
			remediation = "Verify the resource id; use the corresponding `search`/`list` command to find valid ids."
		}
	case http.StatusTooManyRequests:
		code = output.ExitRateLimited
		if remediation == "" {
			remediation = "Back off and retry after the Retry-After interval."
		}
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		code = output.ExitValidation
		if remediation == "" {
			remediation = "Fix the request per the message and retry."
		}
	default:
		code = output.ExitGeneric
		if remediation == "" {
			remediation = "Retry; if it persists, report the request_id to Givmo support."
		}
	}
	if msg == "" {
		msg = fmt.Sprintf("the Givmo API returned HTTP %d", status)
	}
	return output.New(code, msg, remediation).WithRequestID(reqID)
}

// serverErrorMessage extracts a human message from the backend's
// `{ "errors": [{ "code", "title", "detail" }] }` shape (falling back to a
// plain `{ "message" }` or `{ "error" }`).
func serverErrorMessage(body []byte) (msg, remediation string) {
	var jsonapi struct {
		Errors []struct {
			Title  string `json:"title"`
			Detail string `json:"detail"`
		} `json:"errors"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(body, &jsonapi) == nil {
		if len(jsonapi.Errors) > 0 {
			e := jsonapi.Errors[0]
			if e.Detail != "" {
				return e.Detail, ""
			}
			if e.Title != "" {
				return e.Title, ""
			}
		}
		if jsonapi.Message != "" {
			return jsonapi.Message, ""
		}
		if jsonapi.Error != "" {
			return jsonapi.Error, ""
		}
	}
	return "", ""
}
