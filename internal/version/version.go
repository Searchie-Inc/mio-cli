// Package version exposes build metadata for the mio CLI.
//
// The three variables below are overridden at build time via -ldflags, e.g.:
//
//	go build -ldflags "\
//	  -X github.com/Searchie-Inc/mio-cli/internal/version.Version=1.2.3 \
//	  -X github.com/Searchie-Inc/mio-cli/internal/version.Commit=$(git rev-parse --short HEAD) \
//	  -X github.com/Searchie-Inc/mio-cli/internal/version.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
//
// When built without ldflags (e.g. `go run`, `go test`), they keep their
// "dev"/"none"/"unknown" defaults so the binary never reports a bogus version.
package version

// Build-time injected metadata. Do not reassign at runtime.
var (
	// Version is the semantic version of the build (e.g. "1.2.3"), or "dev".
	Version = "dev"
	// Commit is the short git SHA the build was cut from, or "none".
	Commit = "none"
	// Date is the RFC-3339 build timestamp, or "unknown".
	Date = "unknown"
)

// String returns a single-line human-readable version string.
func String() string {
	return "mio " + Version + " (commit " + Commit + ", built " + Date + ")"
}
