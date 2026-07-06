package cmd

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/givfi/givmo-cli/internal/client"
	"github.com/givfi/givmo-cli/internal/donate"
	"github.com/givfi/givmo-cli/internal/output"
	"github.com/spf13/cobra"
)

// maxManifestBytes caps how much we read from a file or URL — manifests are
// small; the cap defends against a hostile publisher serving a huge body.
const maxManifestBytes = 1 << 20 // 1 MiB

func newManifestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "manifest",
		Short: "Validate and sign donate.json manifests (UNTRUSTED input)",
		Long: `Work with donate.json manifests.

donate.json manifests are treated as HOSTILE input. 'validate' runs the vendored
reference parser's strict Parse plus tolerant Sanitize and surfaces every
rejected/sanitized claim (authority overreach, unknown fields, invalid URLs).
No field of a manifest is ever mapped to who-gets-paid, tax-deductibility, or
legal/receipt copy — those are decided by the platform, not the manifest.

'sign' computes the exact signing payload via textual excision (no JSON
canonicalization) and produces a detached Ed25519 JWS attached per the manifest
Signature model.`,
	}
	cmd.AddCommand(newManifestValidateCmd(), newManifestSignCmd(), newManifestSubmitCmd())
	return cmd
}

// ---- validate ---------------------------------------------------------------

type validateReport struct {
	Source         string   `json:"source"`
	StrictAccepted bool     `json:"strict_accepted"`
	StrictError    string   `json:"strict_error,omitempty"`
	RejectedClaims []claim  `json:"rejected_claims"`
	Warnings       []string `json:"warnings"`
	// Salvaged is the display-only sanitized manifest (never carries authority).
	Salvaged *donate.Manifest `json:"salvaged,omitempty"`
}

type claim struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

func newManifestValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <file|url>",
		Short: "Validate a donate.json manifest (strict + tolerant)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			data, source, err := readManifestInput(c.Context(), args[0], app.httpClient())
			if err != nil {
				return err
			}

			// Strict parse (fails closed on hostile/malformed).
			_, strictErr := donate.Parse(data)
			// Tolerant sanitize (salvages display-safe core, reports drops).
			res := donate.Sanitize(data)

			report := validateReport{
				Source:         source,
				StrictAccepted: strictErr == nil,
				Salvaged:       res.Manifest,
			}
			if strictErr != nil {
				report.StrictError = strictErr.Error()
			}
			for _, rc := range res.RejectedClaims {
				report.RejectedClaims = append(report.RejectedClaims, claim{Path: rc.Path, Reason: rc.Reason})
			}
			report.Warnings = res.Warnings
			if report.RejectedClaims == nil {
				report.RejectedClaims = []claim{}
			}
			if report.Warnings == nil {
				report.Warnings = []string{}
			}

			renderErr := app.Printer.Result(report, func(w io.Writer) {
				renderValidateHuman(w, report)
			})
			if renderErr != nil {
				return renderErr
			}

			// Exit code: strict rejection OR any dropped claim => validation error
			// (exit 7), so scripts/agents branch on it. A clean strict parse with no
			// drops is a success (exit 0).
			if strictErr != nil || len(report.RejectedClaims) > 0 {
				return output.New(output.ExitValidation,
					"manifest failed strict validation or carried dropped/rejected claims",
					"Treat this manifest as untrusted. Do NOT map any of its fields to payee eligibility, "+
						"deductibility, or receipt/legal copy; those are resolved by the platform, not the manifest.")
			}
			return nil
		},
	}
}

func renderValidateHuman(w io.Writer, r validateReport) {
	fmt.Fprintf(w, "source: %s\n", r.Source)
	if r.StrictAccepted {
		fmt.Fprintln(w, "strict parse: ACCEPTED")
	} else {
		fmt.Fprintf(w, "strict parse: REJECTED\n  %s\n", r.StrictError)
	}
	if len(r.RejectedClaims) == 0 {
		fmt.Fprintln(w, "rejected/dropped claims: none")
	} else {
		fmt.Fprintf(w, "rejected/dropped claims (%d) — telemeter these as hostile/non-conforming:\n", len(r.RejectedClaims))
		rows := make([][]string, 0, len(r.RejectedClaims))
		for _, c := range r.RejectedClaims {
			rows = append(rows, []string{c.Path, c.Reason})
		}
		output.Table(w, []string{"PATH", "REASON"}, rows)
	}
	if len(r.Warnings) > 0 {
		fmt.Fprintf(w, "warnings (%d):\n", len(r.Warnings))
		for _, warn := range r.Warnings {
			fmt.Fprintf(w, "  - %s\n", warn)
		}
	}
	if r.Salvaged != nil {
		fmt.Fprintf(w, "salvaged (display-only) org: %s (%s)\n", r.Salvaged.Organization.LegalName, r.Salvaged.Organization.Country)
	}
}

// readManifestInput reads a manifest from a local file or an https URL, with a
// timeout and a size cap. A plain "http://" URL is rejected (must be https). Any
// argument that is not a URL is treated as a file path.
func readManifestInput(parent context.Context, arg string, httpc *http.Client) (data []byte, source string, err error) {
	if strings.HasPrefix(arg, "https://") {
		return readManifestURL(parent, arg, httpc)
	}
	if strings.HasPrefix(arg, "http://") {
		return nil, "", output.New(output.ExitUsage,
			"manifest URLs must be https",
			"Use an https URL or a local file path.")
	}
	b, rerr := os.ReadFile(arg)
	if rerr != nil {
		if errors.Is(rerr, os.ErrNotExist) {
			return nil, "", output.New(output.ExitNotFound,
				fmt.Sprintf("manifest file %q does not exist", arg),
				"Pass a path to a donate.json file, or an https URL.")
		}
		return nil, "", output.New(output.ExitGeneric, "could not read manifest: "+rerr.Error(), "")
	}
	if len(b) > maxManifestBytes {
		return nil, "", output.New(output.ExitValidation,
			"manifest file exceeds the 1 MiB size cap",
			"Manifests are small; a huge file is suspicious. Reject it.")
	}
	return b, "file:" + arg, nil
}

// errManifestRedirect is returned by the manifest fetch client's CheckRedirect
// to REFUSE following any redirect (SSRF defense).
var errManifestRedirect = errors.New("manifest fetch refused to follow a redirect")

// withNoRedirect returns an *http.Client that never follows redirects. It
// derives from base (preserving its Transport/Timeout so tests can inject one)
// or builds a fresh 15s-timeout client, always installing a CheckRedirect that
// fails closed. Returning an error from CheckRedirect makes the redirected
// response's body unreachable to the caller — the fetch fails instead of
// silently reading an attacker-chosen internal target.
func withNoRedirect(base *http.Client) *http.Client {
	c := &http.Client{Timeout: 15 * time.Second}
	if base != nil {
		c.Transport = base.Transport
		if base.Timeout > 0 {
			c.Timeout = base.Timeout
		}
	}
	c.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return errManifestRedirect
	}
	return c
}

func readManifestURL(parent context.Context, rawURL string, httpc *http.Client) ([]byte, string, error) {
	if parent == nil {
		parent = context.Background()
	}
	// SSRF DEFENSE: harden the caller-supplied client (or build one) so it
	// REFUSES to follow redirects. The https-only guard above only vets hop 0;
	// without this, a hostile publisher serving an https manifest URL could
	// 30x-redirect the fetcher to http://169.254.169.254/… (cloud metadata),
	// http://localhost:…, or any internal address. A conforming manifest is a
	// static /.well-known/donate.json, so refusing redirects outright is both
	// correct and robust. Preserve the 15s timeout + 1 MiB cap.
	httpc = withNoRedirect(httpc)
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", output.New(output.ExitUsage, "invalid manifest URL: "+err.Error(), "Pass a valid https URL.")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := httpc.Do(req)
	if err != nil {
		// A refused redirect surfaces here as a *url.Error wrapping
		// errManifestRedirect. Classify it as a validation rejection (exit 7):
		// the publisher tried to redirect us off the vetted https URL, which is
		// treated as hostile.
		if errors.Is(err, errManifestRedirect) {
			return nil, "", output.New(output.ExitValidation,
				"manifest URL attempted a redirect; refused",
				"A conforming donate.json is a static file served directly at its https URL. "+
					"Redirects (which could point at internal/metadata addresses) are refused. "+
					"Fetch and validate the final URL directly, or pass a local file.")
		}
		return nil, "", output.New(output.ExitNetwork, "could not fetch manifest: "+err.Error(),
			"Check the URL and connectivity.")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", output.New(output.ExitNotFound,
			fmt.Sprintf("manifest URL returned HTTP %d", resp.StatusCode),
			"Verify the manifest is published at the given URL.")
	}
	// Size cap: read at most maxManifestBytes+1 to detect overflow.
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestBytes+1))
	if err != nil {
		return nil, "", output.New(output.ExitNetwork, "could not read manifest body: "+err.Error(), "")
	}
	if len(b) > maxManifestBytes {
		return nil, "", output.New(output.ExitValidation,
			"remote manifest exceeds the 1 MiB size cap",
			"A huge manifest is suspicious. Reject it.")
	}
	return b, "url:" + rawURL, nil
}

// ---- sign -------------------------------------------------------------------

var manifestKeyFile string

func newManifestSignCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sign <file> --key <ed25519-private-key-file>",
		Short: "Produce a detached Ed25519 JWS and attach it to the manifest",
		Long: `Sign a donate.json manifest with an Ed25519 private key.

Signing uses the vendored parser's SigningPayload: the payload is the manifest
bytes with any existing "signature" member textually excised (no JSON
canonicalization). A detached JWS (EdDSA, b64=false, crit=[b64]) is computed over
the payload and attached as the manifest's "signature" member (alg=EdDSA, jws in
"header..signature" detached form). The signed manifest is written to stdout.

The key file must be a PKCS#8 PEM ("BEGIN PRIVATE KEY") holding an Ed25519 key,
or a 64-byte raw ed25519 seed+public key, or a 32-byte raw seed.

NOTE: a valid signature proves authorship by a key you trust out-of-band; it
NEVER confers authority on any manifest field.`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			raw, err := os.ReadFile(args[0])
			if err != nil {
				return output.New(output.ExitNotFound, "could not read manifest: "+err.Error(),
					"Pass a path to a donate.json file to sign.")
			}
			priv, err := loadEd25519PrivateKey(manifestKeyFile)
			if err != nil {
				return err
			}
			signed, err := signManifest(raw, priv)
			if err != nil {
				return err
			}
			// Emit the signed manifest bytes verbatim to stdout.
			if app.Printer.JSON {
				// In --json mode, wrap so the payload is unambiguous machine output.
				return app.Printer.Result(map[string]any{"signed_manifest": json.RawMessage(signed)}, nil)
			}
			_, werr := app.Printer.Out.Write(signed)
			return werr
		},
	}
	cmd.Flags().StringVar(&manifestKeyFile, "key", "", "path to the Ed25519 private key file (required)")
	_ = cmd.MarkFlagRequired("key")
	return cmd
}

// signManifest computes the detached Ed25519 JWS over the manifest's signing
// payload and returns the manifest with a "signature" member appended as the
// last top-level key. It requires that the input manifest not already carry a
// signature (sign the unsigned form). Pure/deterministic given priv → testable.
func signManifest(raw []byte, priv ed25519.PrivateKey) ([]byte, error) {
	// Reject an already-signed manifest to keep signing deterministic.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, output.New(output.ExitValidation, "manifest is not a JSON object: "+err.Error(),
			"Provide a well-formed donate.json object.")
	}
	if _, exists := top["signature"]; exists {
		return nil, output.New(output.ExitValidation,
			"manifest already carries a signature member",
			"Remove the existing signature and re-sign the unsigned form.")
	}

	payload, err := donate.SigningPayload(raw)
	if err != nil {
		return nil, output.New(output.ExitValidation, "could not derive signing payload: "+err.Error(),
			"Ensure the manifest is a single well-formed JSON object.")
	}

	// Detached JWS with EdDSA. Protected header: alg=EdDSA, b64=false, crit=[b64]
	// so the payload is signed unencoded (matches the parser's detached form:
	// "<protected>..<signature>"). The parser reconstructs the signing input as
	// ASCII(base64url(protectedHeader)) + "." + base64url(payload) at verify time;
	// we sign that exact string here so verification round-trips.
	header := map[string]any{"alg": "EdDSA", "b64": false, "crit": []string{"b64"}}
	hb, _ := json.Marshal(header)
	protected := base64.RawURLEncoding.EncodeToString(hb)
	signingInput := protected + "." + base64.RawURLEncoding.EncodeToString(payload)
	sig := ed25519.Sign(priv, []byte(signingInput))
	detachedJWS := protected + ".." + base64.RawURLEncoding.EncodeToString(sig)

	sigMember := donate.Signature{Alg: "EdDSA", JWS: detachedJWS}
	sigBytes, _ := json.Marshal(sigMember)

	// Append "signature" as the last top-level member so SigningPayload can later
	// excise it deterministically. We insert before the final root '}'.
	trimmed := strings.TrimRight(string(payload), " \t\r\n")
	if !strings.HasSuffix(trimmed, "}") {
		return nil, output.New(output.ExitGeneric, "internal error: signing payload is not an object", "")
	}
	body := trimmed[:len(trimmed)-1]
	out := body + `,"signature":` + string(sigBytes) + "}"
	return []byte(out), nil
}

// loadEd25519PrivateKey reads an Ed25519 private key from PEM (PKCS#8) or raw
// bytes (32-byte seed or 64-byte full key).
func loadEd25519PrivateKey(path string) (ed25519.PrivateKey, error) {
	if strings.TrimSpace(path) == "" {
		return nil, output.New(output.ExitUsage, "a private key file is required",
			"Pass --key <path-to-ed25519-private-key>.")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, output.New(output.ExitNotFound, "could not read key file: "+err.Error(),
			"Pass a readable Ed25519 private key file.")
	}
	priv, perr := parseEd25519PrivateKey(b)
	if perr != nil {
		return nil, output.New(output.ExitValidation, "invalid Ed25519 private key: "+perr.Error(),
			"Provide a PKCS#8 PEM Ed25519 key, or a 32-byte seed / 64-byte raw key.")
	}
	return priv, nil
}

// parseEd25519PrivateKey parses PEM PKCS#8 or raw key bytes. Pure → unit-tested.
func parseEd25519PrivateKey(b []byte) (ed25519.PrivateKey, error) {
	if block, _ := pem.Decode(b); block != nil {
		return parsePKCS8Ed25519(block.Bytes)
	}
	switch len(b) {
	case ed25519.SeedSize: // 32
		return ed25519.NewKeyFromSeed(b), nil
	case ed25519.PrivateKeySize: // 64
		return ed25519.PrivateKey(b), nil
	default:
		return nil, fmt.Errorf("unrecognized key encoding (%d bytes; expected PEM, 32, or 64)", len(b))
	}
}

// ---- submit (stub) ----------------------------------------------------------

func newManifestSubmitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "submit <file>",
		Short: "Submit a manifest to the sandbox batch surface (stub)",
		Long: `Submit a signed donate.json manifest to the Givmo sandbox batch-ingestion
surface for review.

STUB: this command lights up when the sandbox batch surface exists. It is wired
structurally but the ingestion endpoint is not yet live (ready-inert).`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			// Validate before we would ever submit — never submit hostile input.
			raw, source, rerr := readManifestInput(context.Background(), args[0], app.httpClient())
			if rerr != nil {
				return rerr
			}
			if _, perr := donate.Parse(raw); perr != nil {
				return output.New(output.ExitValidation,
					"refusing to submit: manifest failed strict validation",
					"Run `givmo manifest validate` and fix the rejected claims first.")
			}
			_ = source
			return output.New(output.ExitNetwork,
				"manifest submission is not yet available (the sandbox batch surface is not live)",
				"This command is structurally complete and will POST to "+client.PathSandboxManifestBatch+
					" once the batch surface is enabled. For now, validate locally with `givmo manifest validate`.")
		},
	}
}
