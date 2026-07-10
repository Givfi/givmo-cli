package client

import "net/url"

// Backend path contract for the CLI's REST-style surfaces — the SINGLE source of
// truth for the paths the CLI speaks directly over HTTP.
//
// IMPORTANT: the CONSUMER catalog / giving / donation operations are NOT REST.
// They are MCP tools on the /mcp surface (search_charities, get_charity_profile,
// list_cause_etfs, get_cause_etf, get_receipt, create_donation_intent), invoked
// over the CLI's own MCP bridge (internal/mcpbridge + cmd/toolcall.go). There is
// no consumer-scope (givmo.*) REST route on the backend; the un-prefixed
// /charities, /donation-intents, /receipts, /cause-etfs paths the CLI once
// assumed do not exist on the consumer surface (root /charities is the separate
// Firebase mobile API). Hence those consts are gone.
//
// The paths that remain here are the REST surfaces the CLI still calls directly:
// the internal-operator audit-log tail on the Connect mount, the sandbox
// developer-loop, and (via `openapi pull`) the published specs.
const (
	// Sandbox developer-loop paths (sandbox profile).
	PathSandboxSeed          = "/sandbox/seed"
	PathSandboxReset         = "/sandbox/reset"
	PathSandboxEvents        = "/sandbox/events"
	PathSandboxFixturesRun   = "/sandbox/fixtures/run"
	PathSandboxManifestBatch = "/sandbox/manifests/batch"

	// PathInternalAuditLogsTail is the internal-operator audit-log tail on the
	// Connect mount: GET /connect/audit-logs (partner_scope internal.audit.read).
	PathInternalAuditLogsTail = "/connect/audit-logs"
)

// OpenAPIConnectPath is the Connect partner-API spec — the surface this CLI
// targets, and the default for `openapi pull`.
const OpenAPIConnectPath = "/connect/openapi.json"

// OpenAPIRootPath is the root spec (the main/mobile-app FastAPI spec). It is
// served in all environments but describes a different surface; `openapi pull`
// reaches it only via the explicit --root flag.
const OpenAPIRootPath = "/openapi.json"

// ResourcePath joins a collection path and a single resource id, URL-escaping
// the id. e.g. ResourcePath("/connect/charities", "ch_1") -> "/connect/charities/ch_1".
func ResourcePath(collection, id string) string {
	return collection + "/" + url.PathEscape(id)
}

// CollectionQuery appends an already-encoded query string to a collection path
// (omitting the "?" when empty). e.g. CollectionQuery("/connect/audit-logs", "limit=50").
func CollectionQuery(collection, encodedQuery string) string {
	if encodedQuery == "" {
		return collection
	}
	return collection + "?" + encodedQuery
}
