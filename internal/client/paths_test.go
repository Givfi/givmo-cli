package client

import "testing"

func TestResourcePath_EscapesID(t *testing.T) {
	if got := ResourcePath("/connect/charities", "ch_123"); got != "/connect/charities/ch_123" {
		t.Errorf("ResourcePath = %q", got)
	}
	// An id with URL-significant characters must be escaped so it cannot break
	// out of the path.
	if got := ResourcePath("/connect/cause-etfs", "a/b?c"); got != "/connect/cause-etfs/a%2Fb%3Fc" {
		t.Errorf("ResourcePath escaping = %q", got)
	}
}

func TestCollectionQuery(t *testing.T) {
	if got := CollectionQuery(PathInternalAuditLogsTail, ""); got != "/connect/audit-logs" {
		t.Errorf("empty query = %q", got)
	}
	if got := CollectionQuery(PathInternalAuditLogsTail, "limit=25"); got != "/connect/audit-logs?limit=25" {
		t.Errorf("with query = %q", got)
	}
}

func TestPathsAreSingleSourced(t *testing.T) {
	// A cheap guard that the centralized consts exist and are leading-slash
	// paths (so client.BuildRequest's join is correct).
	for _, p := range []string{
		PathSandboxSeed, PathSandboxReset, PathSandboxEvents, PathSandboxFixturesRun,
		PathSandboxManifestBatch, PathInternalAuditLogsTail,
		OpenAPIConnectPath, OpenAPIRootPath,
	} {
		if len(p) == 0 || p[0] != '/' {
			t.Errorf("path %q must be a non-empty leading-slash path", p)
		}
	}
	// The audit-log tail is on the Connect mount, not an un-prefixed root path.
	if PathInternalAuditLogsTail != "/connect/audit-logs" {
		t.Errorf("audit-logs path = %q, want /connect/audit-logs", PathInternalAuditLogsTail)
	}
	// The CLI targets the Connect spec by default.
	if OpenAPIConnectPath != "/connect/openapi.json" {
		t.Errorf("connect openapi path = %q", OpenAPIConnectPath)
	}
}
