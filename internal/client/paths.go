package client

import "net/url"

// REST resource paths — the SINGLE source of truth for the CLI's backend path
// contract.
//
// These are the platform's not-yet-live, ASSUMED paths (the CLI is ready-inert;
// see the package/README notes). Collecting them here means the real backend
// contract reconciles in ONE file at go-live, instead of chasing string
// literals across cmd/*.go. Hosts and the OAuth endpoints are single-sourced
// elsewhere (config profiles / auth discovery); this file owns the REST paths.
const (
	// PathCharities is the charity catalog collection (public tier).
	PathCharities = "/charities"
	// PathCauseETFs is the Cause ETF collection (public tier).
	PathCauseETFs = "/cause-etfs"
	// PathDonationIntents is the donation-intent collection (consumer tier).
	PathDonationIntents = "/donation-intents"
	// PathReceipts is the tax-receipt collection (consumer tier).
	PathReceipts = "/receipts"

	// Sandbox developer-loop paths (sandbox profile).
	PathSandboxSeed          = "/sandbox/seed"
	PathSandboxReset         = "/sandbox/reset"
	PathSandboxEvents        = "/sandbox/events"
	PathSandboxFixturesRun   = "/sandbox/fixtures/run"
	PathSandboxManifestBatch = "/sandbox/manifests/batch"

	// PathInternalAuditLogsTail is the internal-tier audit logs-tail API.
	PathInternalAuditLogsTail = "/internal/audit-logs/tail"
)

// OpenAPIPaths are the conventional locations the backend may publish its spec.
// Ordered by preference; the first that returns 200 wins.
var OpenAPIPaths = []string{"/openapi.json", "/api/openapi.json", "/.well-known/openapi.json"}

// ResourcePath joins a collection path and a single resource id, URL-escaping
// the id. e.g. ResourcePath(PathCharities, "12-3456789") -> "/charities/12-3456789".
func ResourcePath(collection, id string) string {
	return collection + "/" + url.PathEscape(id)
}

// CollectionQuery appends an already-encoded query string to a collection path
// (omitting the "?" when empty). e.g. CollectionQuery(PathCharities, "q=water").
func CollectionQuery(collection, encodedQuery string) string {
	if encodedQuery == "" {
		return collection
	}
	return collection + "?" + encodedQuery
}
