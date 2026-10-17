package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/givfi/givmo-cli/internal/auth"
	"github.com/givfi/givmo-cli/internal/output"
	"github.com/spf13/cobra"
)

func newLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "End the active profile's sign-in and clear its stored credentials",
		Long: `Ask Givmo to end the active profile's sign-in on its side (RFC 7009
revocation of its refresh token), then remove the stored consumer token, API
key, and OAuth client secret from the OS keychain (or the separate 0600 file
fallbacks). The credentials on this computer are always removed. Where Givmo
can't end the sign-in from the CLI yet, or can't be reached, logout says so;
Revoke access in Connected Apps ends it there. With --json, "revoked": true
means Givmo accepted the request to end the sign-in; when it is false,
"revoke_error" says why. Logging out of a profile with no stored auth material
is a no-op (idempotent).`,
		RunE: func(_ *cobra.Command, _ []string) error {
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			ctx, cancel := baseContext()
			defer cancel()
			return runLogout(ctx, app)
		},
	}
}

// revokeTimeout bounds logout's one request to the authorization server.
const revokeTimeout = 15 * time.Second

// givmoSide is what logout learned about ending the sign-in on Givmo's side.
type givmoSide struct {
	// asked says there was an OAuth sign-in to end; revoked, that Givmo accepted
	// the request to end it (RFC 7009 answers 200 for a token it no longer knows
	// too); reason, why not.
	asked   bool
	revoked bool
	reason  string
}

// runLogout ends the profile's sign-in on Givmo's side where Givmo allows it, then
// deletes everything stored for the profile, whatever Givmo answered. Both run
// under the profile's lock, so a renewal in another process cannot store the
// sign-in again afterwards.
func runLogout(ctx context.Context, app *appCtx) error {
	var side givmoSide
	err := app.tokens().SignOut(ctx, func(cred *auth.Credential) { side = endOnGivmo(ctx, app, cred) })
	if err != nil {
		if errors.Is(err, auth.ErrLockTimeout) {
			return output.New(output.ExitGeneric, err.Error(), "Retry.")
		}
		return output.New(output.ExitGeneric, "could not clear credentials: "+err.Error(), "")
	}
	view := map[string]any{"profile": app.Profile.Name, "logged_out": true}
	if side.asked {
		view["revoked"] = side.revoked
		if !side.revoked {
			view["revoke_error"] = side.reason
		}
	}
	return app.Printer.Result(view, func(w io.Writer) {
		fmt.Fprintf(w, "Cleared credentials for profile %q.\n", app.Profile.Name)
		switch {
		case !side.asked:
		case side.revoked:
			fmt.Fprintln(w, "Asked Givmo to end the sign-in; it accepted.")
		default:
			fmt.Fprintf(w, "The sign-in is not ended on Givmo's side: %s. To end it there, use Revoke access in Connected Apps.\n", side.reason)
		}
	})
}

// endOnGivmo asks the authorization server to end cred's sign-in by revoking its
// refresh token (RFC 7009), which ends the whole sign-in, its access tokens with
// it. An API key has no sign-in to end here.
func endOnGivmo(ctx context.Context, app *appCtx, cred *auth.Credential) givmoSide {
	if cred.Type != auth.CredTypeOAuth {
		return givmoSide{}
	}
	if cred.RefreshToken == "" || cred.ClientID == "" || cred.RevocationEndpoint == "" {
		return givmoSide{asked: true, reason: "an earlier version of givmo saved it without what's needed to end it from here"}
	}
	var secret string
	if cred.AuthMethod == auth.AuthMethodClientSecretBasic {
		secret, _, _ = auth.ResolveClientSecret(app.Store, app.Profile.Name)
	}
	ctx, cancel := context.WithTimeout(ctx, revokeTimeout)
	defer cancel()
	err := auth.Revoke(ctx, app.httpClient(), auth.RevokeParams{
		Endpoint:      cred.RevocationEndpoint,
		ClientID:      cred.ClientID,
		ClientSecret:  secret,
		AuthMethod:    cred.AuthMethod,
		Token:         cred.RefreshToken,
		TokenTypeHint: "refresh_token",
	})
	var te *auth.TokenError
	var tr *auth.TransportError
	switch {
	case err == nil:
		return givmoSide{asked: true, revoked: true}
	case errors.As(err, &te) && te.Code == "invalid_client":
		// Until the authorization server offers revocation to a client metadata
		// document, it refuses the CLI's client here.
		return givmoSide{asked: true, reason: "this Givmo environment doesn't let the CLI end its sign-in yet"}
	case errors.As(err, &te):
		return givmoSide{asked: true, reason: "Givmo refused (" + te.Error() + ")"}
	case errors.As(err, &tr) && tr.Sent:
		return givmoSide{asked: true, reason: "Givmo did not answer (" + tr.Error() + ")"}
	case errors.As(err, &tr):
		return givmoSide{asked: true, reason: "Givmo could not be reached (" + tr.Error() + ")"}
	}
	return givmoSide{asked: true, reason: err.Error()}
}
