package cmd

import (
	"errors"
	"fmt"
	"testing"

	"github.com/givfi/givmo-cli/internal/auth"
	"github.com/givfi/givmo-cli/internal/output"
)

// TestRenewalError_ExitsAndRemediations pins how a failed renewal of the stored
// sign-in reaches a person or an agent: each outcome's exit code, message and
// remediation. Agents act on the remediation, so a wrong one does harm: telling a
// caller whose wait for the lock ran out to log out would end a working sign-in on
// Givmo's side, and only a person at a browser can sign in again.
func TestRenewalError_ExitsAndRemediations(t *testing.T) {
	const lost = "Retry. If the renewal went through but its answer was lost, the retry reports that the sign-in has ended; then run `givmo login`."
	for _, tc := range []struct {
		name        string
		err         error
		code        int
		message     string
		remediation string
	}{
		{name: "the sign-in has ended", err: auth.ErrSignInEnded, code: output.ExitAuth,
			message: "Your Givmo sign-in on this computer has ended.", remediation: "Run `givmo login`."},
		{name: "it has ended, though its local copy could not be deleted",
			err:  fmt.Errorf("%w (and the stored sign-in could not be deleted: %v)", auth.ErrSignInEnded, errors.New("permission denied")),
			code: output.ExitAuth, message: "Your Givmo sign-in on this computer has ended.", remediation: "Run `givmo login`."},
		{name: "the wait for the lock ran out", err: auth.ErrLockTimeout, code: output.ExitGeneric,
			message: "timed out waiting for another givmo process to finish renewing the sign-in", remediation: "Retry."},
		{name: "the wait for the lock was cancelled", err: auth.ErrLockWaitCanceled, code: output.ExitGeneric,
			message: "stopped waiting for another givmo process to finish renewing the sign-in", remediation: "Retry."},
		{name: "the environment no longer accepts the client", err: &auth.TokenError{Status: 401, Code: "invalid_client"},
			code: output.ExitAuth, message: "this Givmo environment no longer accepts this sign-in",
			remediation: "Run `givmo login` to sign in again; if it says this environment doesn't accept the CLI's sign-in yet, try again later, or set GIVMO_CLIENT_ID and GIVMO_CLIENT_SECRET for a registered client."},
		{name: "the client is not authorized", err: &auth.TokenError{Status: 400, Code: "unauthorized_client"},
			code: output.ExitAuth, message: "this Givmo environment no longer accepts this sign-in",
			remediation: "Run `givmo login` to sign in again; if it says this environment doesn't accept the CLI's sign-in yet, try again later, or set GIVMO_CLIENT_ID and GIVMO_CLIENT_SECRET for a registered client."},
		{name: "rate-limited, with Retry-After", err: &auth.TokenError{Status: 429, Code: "rate_limited", RetryAfter: "7"},
			code: output.ExitRateLimited, message: "Givmo rate-limited the sign-in renewal (HTTP 429) (Retry-After: 7 seconds)",
			remediation: "Nothing changed. Wait 7 seconds (the server's Retry-After), then retry."},
		{name: "rate-limited, without Retry-After", err: &auth.TokenError{Status: 429, Code: "rate_limited"},
			code: output.ExitRateLimited, message: "Givmo rate-limited the sign-in renewal (HTTP 429)",
			remediation: "Nothing changed. Back off, then retry."},
		{name: "a server error", err: &auth.TokenError{Status: 503, Code: "server_error"},
			code: output.ExitNetwork, message: "Givmo did not answer the sign-in renewal: server_error", remediation: lost},
		{name: "an answer with no OAuth error", err: &auth.TokenError{Status: 502},
			code: output.ExitNetwork, message: "Givmo did not answer the sign-in renewal: HTTP 502", remediation: lost},
		{name: "another OAuth refusal", err: &auth.TokenError{Status: 400, Code: "invalid_request", Description: "bad form"},
			code: output.ExitAuth, message: "Givmo refused the sign-in renewal: invalid_request: bad form",
			remediation: "Run `givmo login` to sign in again."},
		{name: "Givmo could not be reached", err: &auth.TransportError{Err: errors.New("connection refused")},
			code: output.ExitNetwork, message: "could not reach Givmo to renew the sign-in: connection refused",
			remediation: "Check connectivity and the active profile's api_base (`givmo config view`), then retry; the stored sign-in is unchanged."},
		{name: "the answer was lost", err: &auth.TransportError{Sent: true, Err: errors.New("unexpected EOF")},
			code: output.ExitNetwork, message: "Givmo did not answer the sign-in renewal: unexpected EOF", remediation: lost},
		{name: "a stored sign-in carrying an address the CLI's rules refuse",
			err:  &auth.StoredAddressError{Field: "token endpoint", Address: "http://token.example.test/token", Err: auth.ErrNotHTTPS},
			code: output.ExitAuth, message: `the stored sign-in can't be used: its token endpoint, "http://token.example.test/token", isn't https`,
			remediation: "Run `givmo login` to sign in again."},
		{name: "a stored sign-in that cannot be used", err: errors.New("stored credential is corrupt: unexpected end of JSON input"),
			code: output.ExitGeneric, message: "the stored sign-in could not be used: stored credential is corrupt: unexpected end of JSON input",
			remediation: "Run `givmo logout`, then `givmo login`."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := output.AsError(renewalError(tc.err))
			if got.Code != tc.code || got.Message != tc.message || got.Remediation != tc.remediation {
				t.Errorf("renewalError(%v) = exit %d %q / %q\nwant exit %d %q / %q",
					tc.err, got.Code, got.Message, got.Remediation, tc.code, tc.message, tc.remediation)
			}
		})
	}
}
