# Givmo MCP server for OpenAI Codex CLI

This directory holds the **Codex CLI configuration** for Givmo's
charitable-giving tools. It connects Codex to the remote **Givmo MCP server**
(`https://mcp.givmo.io/mcp`).

## About the format

Codex CLI has **no plugin-marketplace or plugin-manifest mechanism** — unlike
Claude Code, there is no directory Codex auto-discovers. MCP servers are
declared in `~/.codex/config.toml` under `[mcp_servers.<name>]` tables. So
`config.toml` in this directory is a **snippet you merge** into your own
`~/.codex/config.toml`; the `.codex-plugin/` folder is just where Givmo
distributes it.

## What it gives an agent

Three live consumer capabilities:

- **Search charities** — find real charities by name or cause.
- **Read your linked giving** — after linking your Givmo account, review your
  own donation history.
- **Start a donation** — payment happens only on Givmo's own checkout page, which
  is not available yet, so no donation can be completed through an agent today.

**Honest capability note:** the agent **cannot donate for you**; payment happens
only on Givmo's own checkout page, which is not available yet. No autonomous/agentic giving. It
does **not** provide corpus-backed charity research, impact ratings, or
deductibility determinations; search is name/cause lookup.

## Install

Merge the `givmo` table from [`config.toml`](./config.toml) into your
`~/.codex/config.toml` (append it — do **not** replace the whole file):

```toml
[mcp_servers.givmo]
url = "https://mcp.givmo.io/mcp"
```

Then restart Codex. To confirm it loaded, list your configured MCP servers with
`codex mcp list` (or the equivalent in your Codex version).

No secrets go in this file. The Givmo tools are consumer-scoped and
authenticate through Givmo's own account-linking/consent flow.

## "Submission" is NOT done

Codex has no marketplace to submit to, so there is nothing to publish — but note
that this is an **authored config snippet only**. It is not registered anywhere
centrally; each user merges it into their own `config.toml`. Do not treat this
as a hosted or listed integration.

## Schema source

The TOML shape (`[mcp_servers.NAME]` with `url` for a remote streamable-HTTP
server) follows the official OpenAI Codex configuration reference at
<https://developers.openai.com/codex/config-reference>. Optional keys Codex
supports on a remote server include `bearer_token_env_var`, `http_headers`,
`env_http_headers`, `startup_timeout_sec`, `tool_timeout_sec`, and `enabled` —
none are needed for the basic Givmo connection above.
