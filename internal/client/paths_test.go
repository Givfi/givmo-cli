package client

import "testing"

func TestResourcePath_EscapesID(t *testing.T) {
	if got := ResourcePath(PathCharities, "12-3456789"); got != "/charities/12-3456789" {
		t.Errorf("ResourcePath = %q", got)
	}
	// An id with URL-significant characters must be escaped so it cannot break
	// out of the path.
	if got := ResourcePath(PathCauseETFs, "a/b?c"); got != "/cause-etfs/a%2Fb%3Fc" {
		t.Errorf("ResourcePath escaping = %q", got)
	}
}

func TestCollectionQuery(t *testing.T) {
	if got := CollectionQuery(PathReceipts, ""); got != "/receipts" {
		t.Errorf("empty query = %q", got)
	}
	if got := CollectionQuery(PathCharities, "q=water"); got != "/charities?q=water" {
		t.Errorf("with query = %q", got)
	}
}

func TestPathsAreSingleSourced(t *testing.T) {
	// A cheap guard that the centralized consts exist and are leading-slash
	// paths (so client.BuildRequest's join is correct).
	for _, p := range []string{
		PathCharities, PathCauseETFs, PathDonationIntents, PathReceipts,
		PathSandboxSeed, PathSandboxReset, PathSandboxEvents, PathSandboxFixturesRun,
		PathSandboxManifestBatch, PathInternalAuditLogsTail,
	} {
		if len(p) == 0 || p[0] != '/' {
			t.Errorf("path %q must be a non-empty leading-slash path", p)
		}
	}
	if len(OpenAPIPaths) == 0 {
		t.Error("OpenAPIPaths must be non-empty")
	}
}
