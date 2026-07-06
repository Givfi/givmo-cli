# givmo CLI

The developer command-line tool for the **Givmo** agentic-giving platform — a
single Go binary (built on [cobra](https://github.com/spf13/cobra)) for the
platform's **public** (catalog) and **consumer** (user-delegated OAuth)
developer tiers.

Givmo exposes one remote MCP server at `https://mcp.givmo.io/mcp` (Streamable
HTTP, stateless) plus a REST API on the same backend. This CLI is the AI-native
surface for those tiers: every command is scriptable, every output has a
machine-readable form, and every error is a structured, agent-actionable
envelope.

> **Ready-inert build.** Several live endpoints (`mcp.givmo.io`, the Connect
> sandbox, the internal audit-logs API) are not yet enabled in production. The
> **entire** command surface is implemented, and everything that does **not**
> require the network — argument parsing, request construction, output
> formatting, error/exit-code handling, manifest validation & signing, PKCE
> generation, the MCP stdio framing — is fully unit-tested. Commands that need a
> live endpoint are structurally complete and fail with a **clear, actionable
> error** (never a panic) until the endpoint lights up. The API and auth base
> URLs are configurable per profile so nothing hard-breaks at go-live.

## Install

```sh
go install github.com/givfi/givmo-cli@latest
# or from a checkout:
go build -o givmo . && mv givmo /usr/local/bin/
```

Requires Go 1.26+. The only third-party dependency is `spf13/cobra`; everything
else is the Go standard library.

## Quickstart

```sh
# 1. Pick an environment (production is the default).
givmo config use-profile sandbox
givmo config view

# 2. Authenticate (consumer OAuth via PKCE loopback). Opens your browser.
givmo login
givmo whoami                 # linked identity + granted scopes (no secrets)

#    Non-interactive / CI:
GIVMO_API_KEY=… givmo login  # stores the key for the active profile

# 3. Explore the catalog (public tier; no login needed).
givmo charities search "clean water" --state CA --limit 10
givmo charities get 12-3456789
givmo cause-etfs list

# 4. Create a donation — you get a SECRETLESS hosted-checkout URL.
givmo donation-intents create --charity c_123 --amount 2500 --open
givmo donation-intents list

# 5. Receipts.
givmo receipts list --tax-year 2025

# 6. Validate / sign a donate.json manifest (UNTRUSTED input).
givmo manifest validate ./donate.json
givmo manifest validate https://example.org/.well-known/donate.json
givmo manifest sign ./donate.json --key ./ed25519-private.pem > signed.donate.json

# 7. Local sandbox developer loop.
givmo listen --forward-to localhost:4000
givmo trigger donation.succeeded --override amount_cents=2500
givmo fixtures run --list
givmo sandbox seed

# 8. Bridge an agent IDE to the remote Givmo MCP through the CLI's session.
givmo mcp install --client claude-code
givmo mcp serve --tools search_charities,create_donation_intent

# 9. Authenticated escape hatch + spec + audit logs.
givmo api GET /charities?q=water
givmo openapi pull --out openapi.json
givmo logs tail --filter tool=create_donation_intent --filter outcome=denied

# JSON everywhere:
givmo --json charities search water
```

`--json` is available on **every** command and produces a machine-readable
alternative to the human table/text output.

## Security posture

- **Secretless money rail.** A donation is created via `create_donation_intent`,
  which returns a **single-use, secretless hosted-checkout URL** carrying only an
  opaque `gco_` token. The human completes payment **and accepts terms** on the
  Givmo-hosted page. The CLI (and any agent driving it) **never** handles a card,
  a `client_secret`, a `dn_` donation id, or accepts terms — it only *displays*
  the checkout URL (and, with `--open`, opens it). The request body the CLI
  constructs is asserted by test to carry no money-authorizing field.
- **Untrusted manifests.** `donate.json` manifests are treated as hostile input.
  `manifest validate` runs the vendored reference parser's strict `Parse` +
  tolerant `Sanitize` and surfaces **every** rejected/sanitized claim. No
  manifest field is ever mapped to who-gets-paid, tax-deductibility, or
  legal/receipt copy — the salvaged view makes those authorities *unrepresentable*.
  URL inputs are HTTPS-only (the fetch **refuses to follow redirects**, so the
  HTTPS-only guarantee holds across hops and an SSRF redirect to an
  internal/metadata address cannot be reached), time-bounded, and size-capped.
- **No secret to disk in the clear / no secret in logs.** Tokens are stored in
  the OS keychain when available (macOS `security`), else in a `0600` file under
  `~/.givmo`. Tokens are **never** logged and **never** appear in `--json` output;
  `whoami` shows identity + scopes only.
- **Production guardrail.** The `production` profile prints a loud banner before
  any mutating operation.

## Stable exit codes

Scripts and AI agents can branch on these deterministically (defined once in
`internal/output/exit.go`):

| Code | Meaning | Typical remediation |
|-----:|---------|---------------------|
| `0` | ok | — |
| `1` | generic error | retry; report `request_id` |
| `2` | usage error (bad flags/args) | see `givmo <cmd> --help` |
| `3` | auth required / failed | run `givmo login` |
| `4` | not found | check the id via a `search`/`list` |
| `5` | rate limited | back off, honor `Retry-After` |
| `6` | network error / endpoint not live (ready-inert) | check connectivity / profile |
| `7` | validation / rejected manifest | fix input; treat manifest as untrusted |

## Error envelope

Every error — local or from the backend — renders through **one** envelope. With
`--json`:

```json
{
  "error": {
    "code": 7,
    "message": "manifest failed strict validation or carried dropped/rejected claims",
    "remediation": "Treat this manifest as untrusted. Do NOT map any of its fields to payee eligibility, deductibility, or receipt/legal copy; those are resolved by the platform, not the manifest.",
    "request_id": "…",
    "doc_url": "https://developers.givmo.io/cli/errors#validation-error"
  }
}
```

`remediation` is phrased so an AI agent can act on it. The server `request_id` is
included whenever the response carried one.

## Configuration

| Setting | Env var | Default (production) | Default (sandbox) |
|---|---|---|---|
| active profile | `GIVMO_PROFILE` | `production` | — |
| API base | `GIVMO_API_BASE` | `https://mcp.givmo.io` | `https://mcp-dev.givmo.io` |
| auth base | `GIVMO_AUTH_BASE` | `https://api.givmo.io` | `https://api-dev.givmo.io` |
| API key (CI) | `GIVMO_API_KEY` | — | — |
| internal S2S token | `GIVMO_INTERNAL_TOKEN` | — (dark) | — (dark) |
| config/state dir | `GIVMO_HOME` | `~/.givmo` | `~/.givmo` |
| token backend | `GIVMO_TOKEN_BACKEND` | keychain (macOS) / file | — |

Precedence: environment variables > `~/.givmo/config.json` > built-in defaults.

## Development

```sh
go build ./...
go vet ./...
go test ./...
```

The `internal/donate` package is **vendored verbatim** from the donate.json spec
repo (see `internal/donate/VENDORED.md`); the `manifest` commands wrap it and do
not fork its logic.

## License

Apache-2.0 (see `LICENSE`).
