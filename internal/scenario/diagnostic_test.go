package scenario

import (
	"strings"
	"testing"
)

func TestDiagnosticLocations(t *testing.T) {
	scn, err := Parse([]byte("version: 1\nname: diagnostics\ninputs:\n  value:\n    type: invalid\n  missing: {}\nbudget:\n  tokens: -1\nsteps:\n  - id: build\n    run:\n      argv: []\n"), "scenario.yaml")
	if err != nil {
		t.Fatal(err)
	}
	result := Validate(scn)
	for _, tc := range []struct {
		path string
		line int
		hint string
	}{
		{"inputs.value", 4, "want one of"},
		{"inputs.missing", 6, "must declare a type"},
		{"budget.tokens", 8, "must not be negative"},
	} {
		found := false
		for _, d := range result.Errors {
			if d.Path == tc.path {
				found = true
				if d.Line != tc.line || !strings.Contains(d.Message, tc.hint) {
					t.Errorf("%s: got %+v", tc.path, d)
				}
			}
		}
		if !found {
			t.Errorf("missing diagnostic for %s", tc.path)
		}
	}
	found := false
	for _, d := range result.Errors {
		if strings.HasPrefix(d.Path, "steps[0]") {
			found = true
			if d.StepID != "build" || !strings.Contains(d.Format("scenario.yaml"), "(step build)") {
				t.Errorf("missing step context: %+v", d)
			}
		}
	}
	if !found {
		t.Fatal("missing step diagnostic")
	}
	if got := scn.lineOf("inputs.missing.type"); got != 6 {
		t.Errorf("parent fallback: %d", got)
	}
}

func TestDiagnosticFormat(t *testing.T) {
	d := Diagnostic{Path: "budget.tokens", Line: 8, Message: "must not be negative"}
	if got := d.Format("scenario.yaml"); got != "scenario.yaml:8: budget.tokens: must not be negative" {
		t.Fatal(got)
	}
	for _, d := range Validate(&Scenario{}).Errors {
		if d.Line != 0 {
			t.Fatalf("invented line: %+v", d)
		}
		if strings.Contains(d.Format("scenario.yaml"), ":0:") {
			t.Fatal(d.Format("scenario.yaml"))
		}
	}
}
