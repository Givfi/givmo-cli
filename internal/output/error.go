package output

import (
	"errors"
	"fmt"
)

// DocsBaseURL is where the CLI points users/agents for remediation guidance.
// Every error carries a doc_url derived from this base.
const DocsBaseURL = "https://developers.givmo.io/cli/errors"

// Error is the ONE error envelope used across every command. It renders
// identically whether the failure originated locally (validation, config) or
// from the Givmo backend (an errors[] payload). AI agents can parse the JSON
// form of this envelope and act on `remediation` mechanically.
//
// Fields:
//   - Code:       a stable exit code (see exit.go). Machine-branchable.
//   - Message:    a human/agent-readable description of what went wrong.
//   - Remediation: an ACTIONABLE next step, phrased imperatively so an agent
//     can execute it (e.g. "Run `givmo login` to obtain a consumer token.").
//   - RequestID:  the server correlation id, when the response carried one.
//   - DocURL:     a link to remediation docs for this error class.
type Error struct {
	Code        int    `json:"code"`
	Message     string `json:"message"`
	Remediation string `json:"remediation,omitempty"`
	RequestID   string `json:"request_id,omitempty"`
	DocURL      string `json:"doc_url,omitempty"`
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Remediation != "" {
		return fmt.Sprintf("%s (%s)", e.Message, e.Remediation)
	}
	return e.Message
}

// ExitCode returns the stable exit code carried by this error.
func (e *Error) ExitCode() int { return e.Code }

// New builds an Error with a stable code, message and remediation. The doc_url
// is filled in automatically from the code's documented name.
func New(code int, message, remediation string) *Error {
	return &Error{
		Code:        code,
		Message:     message,
		Remediation: remediation,
		DocURL:      DocsBaseURL + "#" + CodeName(code),
	}
}

// WithRequestID returns a copy of e annotated with the server request id.
func (e *Error) WithRequestID(id string) *Error {
	if id == "" {
		return e
	}
	clone := *e
	clone.RequestID = id
	return &clone
}

// AsError extracts an *Error from err (unwrapping), or synthesizes a generic
// one so that every failure path renders through the single envelope. This is
// the funnel the root command uses to guarantee uniform output + exit codes.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{
		Code:        ExitGeneric,
		Message:     err.Error(),
		Remediation: "Re-run with --json for a machine-readable envelope; consult the doc_url for this error class.",
		DocURL:      DocsBaseURL + "#" + CodeName(ExitGeneric),
	}
}
