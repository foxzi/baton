package version_test

import (
	"runtime"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/version"
)

func TestStringCarriesTheBuildIdentity(t *testing.T) {
	// The release build injects these through ldflags; set them here to check
	// that every one of them reaches the output.
	defer restore(version.Version, version.Commit, version.Date)
	version.Version = "1.2.3"
	version.Commit = "abcdef1"
	version.Date = "2026-09-09T10:00:00Z"

	got := version.String()
	for _, want := range []string{"baton", "1.2.3", "abcdef1", "2026-09-09T10:00:00Z", runtime.Version()} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, want it to carry %q", got, want)
		}
	}
	if strings.Contains(got, "\n") {
		t.Errorf("String() = %q, want a single line", got)
	}
}

func TestStringDefaultsForALocalBuild(t *testing.T) {
	got := version.String()
	if !strings.Contains(got, "dev") || !strings.Contains(got, "none") || !strings.Contains(got, "unknown") {
		t.Errorf("String() = %q, want the dev/none/unknown defaults of a go build", got)
	}
}

func restore(v, c, d string) {
	version.Version, version.Commit, version.Date = v, c, d
}
