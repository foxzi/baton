package tools

import (
	"errors"
	"testing"

	"github.com/foxzi/baton/internal/gateway"
)

// isPolicyRefusal reports whether a tool refused the call on policy grounds,
// which is what makes the engine fail the step with class policy rather than
// let the agent try something else (section 13).
func isPolicyRefusal(err error) bool {
	var typed *gateway.PolicyError
	return errors.As(err, &typed)
}

// The path checks of section 13 - an absolute path, a .. segment, a denied
// path - are policy refusals, both for the file tools and for the command
// arguments, which share the same check.
func TestWorkspacePathRefusalsArePolicy(t *testing.T) {
	deny := []string{".git/**"}
	for _, value := range []string{"/etc/passwd", "../x", "a/../../x", ".git/config"} {
		if _, err := checkWorkspacePath("path", value, deny); !isPolicyRefusal(err) {
			t.Errorf("checkWorkspacePath(%q) error = %v, want a policy refusal", value, err)
		}
	}
}

// A malformed argument that says nothing about the step's boundary - an
// empty path, one that is too long - is a plain argument error, not a policy
// refusal: the agent can correct it and try again.
func TestWorkspacePathArgumentErrorsAreNotPolicy(t *testing.T) {
	for _, value := range []string{"", "a\nb"} {
		_, err := checkWorkspacePath("path", value, nil)
		if err == nil {
			t.Fatalf("checkWorkspacePath(%q) error = nil, want an argument error", value)
		}
		if isPolicyRefusal(err) {
			t.Errorf("checkWorkspacePath(%q) error = %v, want a plain argument error", value, err)
		}
	}
}

// A command argument that is an absolute path or reaches for a parent
// directory is refused before the pattern is even consulted, and the refusal
// is a policy one (spec sections 7.5 and 13).
func TestCommandArgumentPathRefusalsArePolicy(t *testing.T) {
	for _, value := range []string{"/etc/passwd", "../x", "a/../../x"} {
		if err := checkValue("path", value, `^.*$`); !isPolicyRefusal(err) {
			t.Errorf("checkValue(%q) error = %v, want a policy refusal", value, err)
		}
	}
}

// An argument the pattern simply does not match is a plain argument error:
// the agent can pick a different value.
func TestCommandArgumentPatternMismatchIsNotPolicy(t *testing.T) {
	err := checkValue("path", "; echo pwned", `^[\w/.-]+$`)
	if err == nil {
		t.Fatal("checkValue error = nil, want the pattern to refuse the value")
	}
	if isPolicyRefusal(err) {
		t.Errorf("checkValue error = %v, want a plain argument error", err)
	}
}

// The same holds for the glob check the fs.glob and fs.grep tools use.
func TestWorkspaceGlobRefusalsArePolicy(t *testing.T) {
	for _, value := range []string{"/etc/*", "../*.go"} {
		if err := checkWorkspaceGlob("pattern", value); !isPolicyRefusal(err) {
			t.Errorf("checkWorkspaceGlob(%q) error = %v, want a policy refusal", value, err)
		}
	}
}

// A git revision that looks like an option is a policy refusal: it is an
// attempt to make git do something the step did not ask for.
func TestGitRevisionOptionIsPolicy(t *testing.T) {
	err := checkRevision("ref", "--upload-pack=touch /tmp/x")
	if !isPolicyRefusal(err) {
		t.Errorf("checkRevision error = %v, want a policy refusal", err)
	}
}
