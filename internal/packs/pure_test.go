package packs

import (
	"strings"
	"testing"
)

// TestCompileJQRejectsImpureExpressions checks that a pack expression using a
// name that reaches outside the response is refused when the pack loads (spec
// section 13, check 15).
func TestCompileJQRejectsImpureExpressions(t *testing.T) {
	cases := []struct {
		name   string
		source string
		want   string
	}{
		{"env", "env", "env is not allowed"},
		{"env in a pipe", ".items | env.TOKEN", "env is not allowed"},
		{"ENV variable", "$ENV.TOKEN", "$ENV is not allowed"},
		{"input", "input", "input is not allowed"},
		{"inputs", "[inputs]", "inputs is not allowed"},
		{"loc", "$__loc__", "$__loc__ is not allowed"},
		{"nested in a call", ".a | map(env)", "env is not allowed"},
		{"nested in an object", "{token: env.TOKEN}", "env is not allowed"},
		{"nested in a string", `"\(env.TOKEN)"`, "env is not allowed"},
		{"nested in a definition", "def f: env; f", "env is not allowed"},
		{"nested in an array", "[.a, env]", "env is not allowed"},
		{"nested in a try", "try env catch .", "env is not allowed"},
		{"nested in an index", ".[env]", "env is not allowed"},
		{"nested in a reduce", "reduce .[] as $x (0; . + $ENV.N)", "$ENV is not allowed"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := compileJQ("transform", tc.source)
			if err == nil {
				t.Fatalf("compileJQ(%q) error = nil, want a refusal", tc.source)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("compileJQ(%q) error = %q, want mention of %q", tc.source, err, tc.want)
			}
			if !strings.HasPrefix(err.Error(), "transform: ") {
				t.Errorf("compileJQ(%q) error = %q, want the field prefix", tc.source, err)
			}
		})
	}
}

// TestCompileJQKeepsPureExpressions checks that the purity check does not
// touch ordinary transforms, including ones that only mention a forbidden
// name as data.
func TestCompileJQKeepsPureExpressions(t *testing.T) {
	sources := []string{
		".items",
		"{id: .id, title: .title}",
		`.[] | select(.state == "opened")`,
		"{env: .environment}",
		`.["env"]`,
		`.data | map({name: .name}) | sort_by(.name)`,
		"reduce .[] as $x (0; . + $x.count)",
	}

	for _, source := range sources {
		if _, err := compileJQ("transform", source); err != nil {
			t.Errorf("compileJQ(%q) error = %v, want it accepted", source, err)
		}
	}
}
