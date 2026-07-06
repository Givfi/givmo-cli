# Givmo plugin for Claude Code

This directory is the **Claude Code plugin + marketplace manifest** for Givmo's
charitable-giving tools. Installing it connects Claude Code to the remote
**Givmo MCP server** (`https://mcp.givmo.io/mcp`) and bundles the
[`givmo-giving`](../skills/givmo-giving/SKILL.md) skill.

## What it gives an agent

Three live consumer capabilities:

- **Search charities** — find real charities by name or cause.
- **Read your linked giving** — after you link your Givmo account, review your
  own donation history.
- **Start a donation** — the agent prepares a donation and hands you a `gco_…`
  checkout link that **you** complete.

**Honest capability note:** the agent **cannot donate for you**. There is no
autonomous/agentic giving — every donation ends in a secretless, human-completed
checkout. It also does **not** provide corpus-backed charity research, impact
ratings, or deductibility determinations; charity search is name/cause lookup.

## Install

This marketplace is **not yet submitted to any public catalog** (see below), so
install it directly from the repo:

```
# Add the marketplace (repo root contains .claude-plugin/marketplace.json)
/plugin marketplace add givfi/givmo-cli

# Install the plugin
/plugin install givmo@givmo
```

Or point at a local checkout while developing:

```
/plugin marketplace add /path/to/givmo-cli
/plugin install givmo@givmo
```

After install, the `givmo` MCP server connects automatically and the
`givmo:givmo-giving` skill becomes available. To read your own giving, link your
account first (`givmo login` with the CLI, or the server's linking flow).

## Files

| File | Purpose |
|------|---------|
| `marketplace.json` | Marketplace catalog listing the `givmo` plugin |
| `plugin.json` | Plugin manifest: metadata + the remote MCP server (`type: http`, `url: https://mcp.givmo.io/mcp`) |
| `../skills/givmo-giving/SKILL.md` | The bundled giving skill (loaded from the plugin root `skills/` dir) |

## Marketplace submission is NOT done

These are **authored manifests only**. This plugin has **not** been submitted to
or published in any Claude Code marketplace (including any official Anthropic
catalog). Submission/publishing is a separate, gated step and is intentionally
left undone here. Do not treat the presence of these files as a live listing.

Validate before any future submission:

```
claude plugin validate . --strict
```

## Schema source

Field names and structure follow the official Claude Code plugin + marketplace
reference at <https://code.claude.com/docs/en/plugins-reference> and
<https://code.claude.com/docs/en/plugin-marketplaces>, and the remote-HTTP MCP
server shape (`type: http` + `url`) from
<https://code.claude.com/docs/en/mcp>.
