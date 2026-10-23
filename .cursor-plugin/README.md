# Givmo MCP server for Cursor

This directory holds the **Cursor MCP configuration** for Givmo's
charitable-giving tools. It connects Cursor to the remote **Givmo MCP server**
(`https://mcp.givmo.io/mcp`).

## About the format

Cursor configures MCP servers in an `mcp.json` file under an `mcpServers`
object. A remote server is declared with just a `url` (no `type` field — the
presence of `url` marks it remote). Cursor has **no plugin marketplace**; you
place `mcp.json` in one of two locations:

- **Project scope:** `.cursor/mcp.json` in a project root (tools available in
  that project).
- **Global scope:** `~/.cursor/mcp.json` in your home directory (tools
  available everywhere).

The [`mcp.json`](./mcp.json) in this directory is ready to copy into either
location.

## What it gives an agent

Three live consumer capabilities:

- **Search charities** — find real charities by name or cause.
- **Read your linked giving** — after linking your Givmo account, review your
  own donation history.
- **Start a donation** — the agent prepares a donation and hands you a `gco_…`
  checkout link that **you** complete.

**Honest capability note:** the agent **cannot donate for you** — every donation
ends in a secretless, human-completed checkout. No autonomous/agentic giving. It
does **not** provide corpus-backed charity research, impact ratings, or
deductibility determinations; search is name/cause lookup.

## Install

Copy [`mcp.json`](./mcp.json) to your chosen location:

```bash
# Global (all projects)
mkdir -p ~/.cursor && cp mcp.json ~/.cursor/mcp.json

# or Project-scoped
mkdir -p .cursor && cp mcp.json .cursor/mcp.json
```

The file contents:

```json
{
  "mcpServers": {
    "givmo": {
      "url": "https://mcp.givmo.io/mcp"
    }
  }
}
```

If you already have an `mcp.json`, merge the `givmo` entry into your existing
`mcpServers` object rather than overwriting the file. Then reload Cursor and
check **Settings → MCP** to confirm the `givmo` server is connected.

No secrets go in this file. The Givmo tools are consumer-scoped and
authenticate through Givmo's own account-linking/consent flow.

## "Submission" is NOT done

Cursor has no plugin marketplace to submit to, so there is nothing to publish —
but note this is an **authored config file only**. It is not registered or
listed anywhere centrally; each user copies it into their own Cursor config. Do
not treat this as a hosted or listed integration.

## Schema source

The `mcp.json` shape (`mcpServers` object, remote server = `{ "url": ... }` with
no `type` field, at `.cursor/mcp.json` / `~/.cursor/mcp.json`) follows Cursor's
official MCP documentation at <https://cursor.com/docs/context/mcp>.
