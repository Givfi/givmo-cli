// Package version holds the build-stamped version metadata for the givmo CLI.
package version

// These are overridable at build time with -ldflags, e.g.:
//
//	go build -ldflags "-X github.com/givfi/givmo-cli/internal/version.Version=1.2.3"
var (
	// Version is the semantic version of the CLI.
	Version = "0.1.0-dev"
	// Commit is the git commit the binary was built from (best-effort).
	Commit = "unknown"
	// Date is the build date (best-effort).
	Date = "unknown"
)

// UserAgent is the HTTP User-Agent the CLI presents to the Givmo backend so
// server-side telemetry can attribute traffic to the developer CLI.
func UserAgent() string {
	return "givmo-cli/" + Version
}
