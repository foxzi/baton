package scenario

import "testing"

// TestValidateStepReferences covers specification section 4, checks 5 and 6:
// a reference must name a step declared earlier, and the result of a step
// that may be skipped must be guarded.
func TestValidateStepReferences(t *testing.T) {
	tests := []struct {
		name  string
		yaml  string
		want  string // substring of the expected error, empty when valid
		warns bool
	}{
		{
			name: "reference to a later step",
			yaml: `
steps:
  - id: first
    assert:
      condition: steps.second.exit_code == 0
  - id: second
    run: { argv: ["echo", "b"] }
`,
			want: "not declared before this step",
		},
		{
			name: "reference to an unknown step",
			yaml: `
steps:
  - id: first
    assert:
      condition: steps.nowhere.exit_code == 0
`,
			want: "not declared before this step",
		},
		{
			name: "self reference",
			yaml: `
steps:
  - id: first
    run:
      argv: ["echo", "{{ .steps.first.stdout }}"]
`,
			want: "not declared before this step",
		},
		{
			name: "result of a conditional step, unguarded",
			yaml: `
steps:
  - id: maybe
    when: inputs.enabled
    run: { argv: ["echo", "a"], parse: text }
  - id: use
    run:
      argv: ["echo", "{{ .steps.maybe.result }}"]
`,
			want: `runs under a when:`,
		},
		{
			name: "result of a conditional step, guarded by default",
			yaml: `
steps:
  - id: maybe
    when: inputs.enabled
    run: { argv: ["echo", "a"], parse: text }
  - id: use
    run:
      argv: ["echo", "{{ default \"none\" .steps.maybe.result }}"]
`,
		},
		{
			name: "result of a conditional step from a conditional step",
			yaml: `
steps:
  - id: maybe
    when: inputs.enabled
    run: { argv: ["echo", "a"], parse: text }
  - id: use
    when: inputs.enabled
    run:
      argv: ["echo", "{{ .steps.maybe.result }}"]
`,
		},
		{
			name: "status of a conditional step",
			yaml: `
steps:
  - id: maybe
    when: inputs.enabled
    run: { argv: ["echo", "a"], parse: text }
  - id: use
    assert:
      condition: steps.maybe.status != "failed"
`,
		},
		{
			name: "switch case referenced from a plain step",
			yaml: `
steps:
  - switch: inputs.mode
    cases:
      a: { id: deep, run: { argv: ["echo", "a"], parse: text } }
  - id: use
    run:
      argv: ["echo", "{{ .steps.deep.result }}"]
`,
			want:  `runs under a when:`,
			warns: true,
		},
		{
			name: "on_failure sees the whole run",
			yaml: `
steps:
  - id: work
    when: inputs.enabled
    run: { argv: ["echo", "a"], parse: text }
on_failure:
  - id: report
    assert:
      condition: steps.work.status == "failed"
`,
		},
		{
			name: "prose outside a template action is not a reference",
			yaml: `
steps:
  - id: first
    assert:
      condition: "true"
      message: "see steps.nowhere for the details"
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := "version: 1\nname: refs\ninputs:\n  enabled: { type: bool }\n  mode: { type: string }\n" + tt.yaml
			scn, err := Parse([]byte(source), "test.yaml")
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			res := Validate(scn)

			if tt.want == "" {
				if !res.OK() {
					t.Fatalf("OK() = false, Errors = %v", res.Errors)
				}
				if !tt.warns && len(res.Warnings) != 0 {
					t.Errorf("Warnings = %v, want none", res.Warnings)
				}
				return
			}
			if res.OK() {
				t.Fatalf("OK() = true, want an error mentioning %q", tt.want)
			}
			if !diagnosticsContain(res.Errors, tt.want) {
				t.Errorf("Errors = %v, want one mentioning %q", res.Errors, tt.want)
			}
		})
	}
}

// TestValidateReferenceGuards checks that every guard form the specification
// names disarms check 6.
func TestValidateReferenceGuards(t *testing.T) {
	tests := map[string]string{
		"template default":          `run: { argv: ["echo", '{{ default "none" .steps.maybe.result }}'] }`,
		"template coalesce":         `run: { argv: ["echo", '{{ coalesce .steps.maybe.result "none" }}'] }`,
		"expression nil coalescing": `assert: { condition: '(steps.maybe.result ?? "") == ""' }`,
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			scn, err := Parse([]byte(`
version: 1
name: refs
inputs:
  enabled: { type: bool }
steps:
  - id: maybe
    when: inputs.enabled
    run: { argv: ["echo", "a"], parse: text }
  - id: use
    `+body+`
`), "test.yaml")
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			res := Validate(scn)
			if !res.OK() {
				t.Fatalf("OK() = false, Errors = %v", res.Errors)
			}
		})
	}
}
