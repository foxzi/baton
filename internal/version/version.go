// Package version reports the build identity of the binary.
//
// Values are injected at link time by the release build; the defaults below
// apply to `go build` and `go run` during development.
package version

import (
	"fmt"
	"runtime"
)

var (
	// Version is the semantic version of the release, or "dev" for local builds.
	Version = "dev"
	// Commit is the git revision the binary was built from.
	Commit = "none"
	// Date is the build timestamp in RFC 3339 format.
	Date = "unknown"
)

// String renders the full build identity on a single line.
func String() string {
	return fmt.Sprintf("baton %s (commit %s, built %s, %s)",
		Version, Commit, Date, runtime.Version())
}
