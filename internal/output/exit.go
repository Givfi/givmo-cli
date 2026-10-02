// Package output is the single, documented home for the CLI's stable exit
// codes, its one error envelope, and its json/table renderers.
//
// EXIT CODES ARE PART OF THE CLI CONTRACT. They are defined here once and used
// everywhere; scripts and AI agents depend on them. Do not invent new codes
// elsewhere — add a constant here with a comment if a genuinely new class of
// failure appears.
package output

// Exit codes. These are STABLE and documented (see README + `givmo --help`).
// An AI agent driving the CLI can branch on these deterministically.
const (
	// ExitOK — command succeeded.
	ExitOK = 0
	// ExitGeneric — an unclassified runtime error.
	ExitGeneric = 1
	// ExitUsage — the invocation was malformed (bad flags/args). Cobra usage
	// errors map here.
	ExitUsage = 2
	// ExitAuth — authentication is required, missing, expired, or was rejected.
	// Remediation: run `givmo login`.
	ExitAuth = 3
	// ExitNotFound — the requested resource does not exist (HTTP 404).
	ExitNotFound = 4
	// ExitRateLimited — the server throttled the request (HTTP 429), or refused
	// a tool call it says is safe to retry (turned away as busy, or a read that
	// changed nothing). Back off, then retry.
	ExitRateLimited = 5
	// ExitNetwork — a transport-level failure (DNS, connection refused,
	// timeout) or an endpoint not enabled in the active environment. A tool
	// call lost after it was sent exits ExitOutcomeUnknown instead.
	ExitNetwork = 6
	// ExitValidation — input failed validation, or an untrusted donate.json
	// manifest was rejected/sanitized with claims dropped.
	ExitValidation = 7
	// ExitOutcomeUnknown — a tool call was sent but its outcome is unknown: it
	// may have run. The server said so, or no answer arrived (the CLI stopped
	// waiting, or the connection broke after the call was sent). Read the
	// current state before retrying; repeat the call only where the error says
	// a retry with the same arguments is safe.
	ExitOutcomeUnknown = 8
)

// CodeName returns the documented short name for an exit code, used in
// human-readable help and diagnostics.
func CodeName(code int) string {
	switch code {
	case ExitOK:
		return "ok"
	case ExitGeneric:
		return "generic-error"
	case ExitUsage:
		return "usage-error"
	case ExitAuth:
		return "auth-required"
	case ExitNotFound:
		return "not-found"
	case ExitRateLimited:
		return "rate-limited"
	case ExitNetwork:
		return "network-error"
	case ExitValidation:
		return "validation-error"
	case ExitOutcomeUnknown:
		return "outcome-unknown"
	default:
		return "unknown"
	}
}
