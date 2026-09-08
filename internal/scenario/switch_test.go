package scenario

import (
	"reflect"
	"strings"
	"testing"
)

// parseSwitch parses a scenario whose steps section is the given body.
func parseSwitch(t *testing.T, steps string) (*Scenario, error) {
	t.Helper()
	return Parse([]byte("version: 1\nname: sw\nsteps:\n"+steps), "test.yaml")
}

// TestExpandSwitchGroupsLabels checks that two labels leading to the same step
// declare it once, guarded by both comparisons.
func TestExpandSwitchGroupsLabels(t *testing.T) {
	scn, err := parseSwitch(t, `
  - switch: inputs.mode
    cases:
      review: &deep { id: deep, run: { argv: ["echo", "deep"] } }
      audit: *deep
      quick: { id: light, run: { argv: ["echo", "light"] } }
`)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	var ids []string
	for _, step := range scn.Steps {
		ids = append(ids, step.ID)
	}
	if want := []string{"deep", "light"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("step ids = %v, want %v", ids, want)
	}
	want := `(inputs.mode) == "review" || (inputs.mode) == "audit"`
	if scn.Steps[0].When != want {
		t.Errorf("When = %q, want %q", scn.Steps[0].When, want)
	}
}

// TestExpandSwitchNonStringLabels checks that numbers and booleans are
// compared as literals, not as strings.
func TestExpandSwitchNonStringLabels(t *testing.T) {
	scn, err := parseSwitch(t, `
  - switch: steps.probe.result.code
    cases:
      200: { id: ok, run: { argv: ["echo", "ok"] } }
      true: { id: yes_, run: { argv: ["echo", "yes"] } }
`)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	for i, want := range []string{
		"(steps.probe.result.code) == 200",
		"(steps.probe.result.code) == true",
	} {
		if scn.Steps[i].When != want {
			t.Errorf("Steps[%d].When = %q, want %q", i, scn.Steps[i].When, want)
		}
	}
}

// TestExpandSwitchKeepsOuterControl checks that the when and the needs of the
// switch itself carry over to every case.
func TestExpandSwitchKeepsOuterControl(t *testing.T) {
	scn, err := parseSwitch(t, `
  - id: probe
    run: { argv: ["echo", "a"] }
  - switch: inputs.mode
    when: "inputs.enabled"
    needs: [probe]
    cases:
      a: { id: only, needs: [probe], run: { argv: ["echo", "a"] } }
`)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	step := scn.Steps[1]
	want := `(inputs.enabled) && ((inputs.mode) == "a")`
	if step.When != want {
		t.Errorf("When = %q, want %q", step.When, want)
	}
	if got := []string{"probe", "probe"}; !reflect.DeepEqual(step.Needs, got) {
		t.Errorf("Needs = %v, want %v", step.Needs, got)
	}
}

// TestExpandSwitchInOnFailure checks that on_failure branches too.
func TestExpandSwitchInOnFailure(t *testing.T) {
	scn, err := Parse([]byte(`
version: 1
name: sw
steps:
  - id: work
    run: { argv: ["echo", "a"] }
on_failure:
  - switch: run.status
    cases:
      failed: { id: report, run: { argv: ["echo", "failed"] } }
`), "test.yaml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(scn.OnFailure) != 1 || scn.OnFailure[0].ID != "report" {
		t.Fatalf("on_failure = %v, want the expanded case", scn.OnFailure)
	}
	res := Validate(scn)
	if !diagnosticsContain(res.Warnings, "no default case") {
		t.Errorf("Warnings = %v, want a missing-default warning", res.Warnings)
	}
}

func TestExpandSwitchErrors(t *testing.T) {
	tests := []struct {
		name  string
		steps string
		want  string
	}{
		{
			name: "id of its own",
			steps: `
  - id: branch
    switch: inputs.mode
    cases:
      a: { id: a1, run: { argv: ["echo", "a"] } }
`,
			want: "has no id of its own",
		},
		{
			name: "another body",
			steps: `
  - switch: inputs.mode
    run: { argv: ["echo", "a"] }
    cases:
      a: { id: a1, run: { argv: ["echo", "a"] } }
`,
			want: "must be the only body",
		},
		{
			name: "step-level control",
			steps: `
  - switch: inputs.mode
    timeout: 10s
    cases:
      a: { id: a1, run: { argv: ["echo", "a"] } }
`,
			want: "cannot carry timeout",
		},
		{
			name: "no cases",
			steps: `
  - switch: inputs.mode
    cases: {}
`,
			want: "at least one case",
		},
		{
			name: "case without an id",
			steps: `
  - switch: inputs.mode
    cases:
      a: { run: { argv: ["echo", "a"] } }
`,
			want: "needs an id",
		},
		{
			name: "same id, different bodies",
			steps: `
  - switch: inputs.mode
    cases:
      a: { id: same, run: { argv: ["echo", "a"] } }
      b: { id: same, run: { argv: ["echo", "b"] } }
`,
			want: "must declare the same body",
		},
		{
			name: "body of a foreach",
			steps: `
  - id: loop
    foreach:
      items: inputs.list
      step:
        switch: item
        cases:
          a: { id: a1, run: { argv: ["echo", "a"] } }
`,
			want: "cannot be the body of a foreach",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseSwitch(t, tt.steps)
			if err == nil {
				t.Fatalf("Parse() error = nil, want %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Parse() error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}
