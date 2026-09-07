package scenario

import (
	"strings"
	"testing"
)

// diagnosticsContain reports whether any diagnostic's rendered text contains
// substr.
func diagnosticsContain(diags []Diagnostic, substr string) bool {
	for _, d := range diags {
		if strings.Contains(d.String(), substr) {
			return true
		}
	}
	return false
}

// TestValidate runs the structural checks of Validate against minimal
// scenarios, one problem at a time.
func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		yaml string

		// wantErr and wantWarn are substrings expected among the
		// respective diagnostics; empty means the check is skipped.
		wantErr  string
		wantWarn string

		// checkOK and checkNoWarn additionally assert that the result has
		// no errors and no warnings at all; used for the valid case.
		checkOK     bool
		checkNoWarn bool
	}{
		{
			name: "valid scenario",
			yaml: `
version: 1
name: valid
steps:
  - id: s
    run:
      argv: ["echo", "hi"]
`,
			checkOK:     true,
			checkNoWarn: true,
		},
		{
			name: "version not 1",
			yaml: `
version: 2
name: valid
steps:
  - id: s
    run:
      argv: ["echo", "hi"]
`,
			wantErr: "version: must be 1",
		},
		{
			name: "empty name",
			yaml: `
version: 1
name: ""
steps:
  - id: s
    run:
      argv: ["echo", "hi"]
`,
			wantErr: "name: must not be empty",
		},
		{
			name: "empty steps",
			yaml: `
version: 1
name: valid
steps: []
`,
			wantErr: "steps: must declare at least one step",
		},
		{
			name: "step without body",
			yaml: `
version: 1
name: valid
steps:
  - id: s
`,
			wantErr: "must declare one of run, http, llm, agent, foreach, until, assert",
		},
		{
			name: "step with two bodies",
			yaml: `
version: 1
name: valid
steps:
  - id: s
    run:
      argv: ["echo", "hi"]
    assert:
      condition: "true"
`,
			wantErr: "must declare exactly one body",
		},
		{
			name: "id empty",
			yaml: `
version: 1
name: valid
steps:
  - id: ""
    run:
      argv: ["echo", "hi"]
`,
			wantErr: "steps[0].id: must not be empty",
		},
		{
			name: "id not matching pattern",
			yaml: `
version: 1
name: valid
steps:
  - id: "Bad-Id"
    run:
      argv: ["echo", "hi"]
`,
			wantErr: `"Bad-Id" must match`,
		},
		{
			name: "duplicate id",
			yaml: `
version: 1
name: valid
steps:
  - id: dup
    run:
      argv: ["echo", "hi"]
  - id: dup
    run:
      argv: ["echo", "hi"]
`,
			wantErr: `duplicate step id "dup"`,
		},
		{
			name: "needs unknown step",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    needs: [ghost]
    run:
      argv: ["echo", "hi"]
`,
			wantErr: `unknown or later step "ghost"`,
		},
		{
			name: "needs step declared later",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    needs: [b]
    run:
      argv: ["echo", "hi"]
  - id: b
    run:
      argv: ["echo", "hi"]
`,
			wantErr: `unknown or later step "b"`,
		},
		{
			name: "needs itself",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    needs: [a]
    run:
      argv: ["echo", "hi"]
`,
			wantErr: `step "a" cannot need itself`,
		},
		{
			name: "on_error fallback without fallback body",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    on_error: fallback
    run:
      argv: ["echo", "hi"]
`,
			wantErr: "on_error: fallback requires a fallback body",
		},
		{
			name: "fallback ignored when on_error is fail",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    on_error: fail
    fallback:
      run:
        argv: ["echo", "x"]
    run:
      argv: ["echo", "hi"]
`,
			wantWarn: "ignored unless on_error is fallback",
		},
		{
			name: "on_error garbage value",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    on_error: bogus
    run:
      argv: ["echo", "hi"]
`,
			wantErr: `unknown value "bogus"`,
		},
		{
			name: "retry attempts zero",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    retry:
      attempts: 0
    run:
      argv: ["echo", "hi"]
      readonly: true
`,
			wantErr: "retry.attempts: must be at least 1",
		},
		{
			name: "retry on unknown class",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    retry:
      attempts: 1
      on: ["nope"]
    run:
      argv: ["echo", "hi"]
      readonly: true
`,
			wantErr: `unknown error class "nope"`,
		},
		{
			name: "retry on class never retried",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    retry:
      attempts: 1
      on: ["budget"]
    run:
      argv: ["echo", "hi"]
      readonly: true
`,
			wantErr: `class "budget" is never retried`,
		},
		{
			name: "retry without readonly or dedupe_key",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    retry:
      attempts: 1
    run:
      argv: ["echo", "hi"]
`,
			wantWarn: "retry on a step without readonly: true or dedupe_key may repeat side effects",
		},
		{
			name: "run env references undeclared secret",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    run:
      argv: ["echo", "hi"]
      env:
        TOKEN:
          secret: missing
`,
			wantErr: `undeclared secret "missing"`,
		},
		{
			name: "run parse garbage",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    run:
      argv: ["echo", "hi"]
      parse: xml
`,
			wantErr: `unknown mode "xml"`,
		},
		{
			name: "input without type",
			yaml: `
version: 1
name: valid
inputs:
  who: {}
steps:
  - id: a
    run:
      argv: ["echo", "hi"]
`,
			wantErr: "inputs.who: must declare a type",
		},
		{
			name: "input unknown type",
			yaml: `
version: 1
name: valid
inputs:
  who:
    type: weird
steps:
  - id: a
    run:
      argv: ["echo", "hi"]
`,
			wantErr: `unknown type "weird"`,
		},
		{
			name: "pattern on non-string input",
			yaml: `
version: 1
name: valid
inputs:
  n:
    type: int
    pattern: "^[0-9]+$"
steps:
  - id: a
    run:
      argv: ["echo", "hi"]
`,
			wantErr: "pattern applies to string inputs only",
		},
		{
			name: "invalid regexp in pattern",
			yaml: `
version: 1
name: valid
inputs:
  who:
    type: string
    pattern: "["
steps:
  - id: a
    run:
      argv: ["echo", "hi"]
`,
			wantErr: "invalid regular expression",
		},
		{
			name: "secret from env without key",
			yaml: `
version: 1
name: valid
secrets:
  tok:
    from: env
steps:
  - id: a
    run:
      argv: ["echo", "hi"]
`,
			wantErr: "from: env needs key",
		},
		{
			name: "secret from file without path",
			yaml: `
version: 1
name: valid
secrets:
  tok:
    from: file
steps:
  - id: a
    run:
      argv: ["echo", "hi"]
`,
			wantErr: "from: file needs path",
		},
		{
			name: "secret unknown from",
			yaml: `
version: 1
name: valid
secrets:
  tok:
    from: vault
steps:
  - id: a
    run:
      argv: ["echo", "hi"]
`,
			wantErr: `unknown source "vault"`,
		},
		{
			name: "secret missing from",
			yaml: `
version: 1
name: valid
secrets:
  tok: {}
steps:
  - id: a
    run:
      argv: ["echo", "hi"]
`,
			wantErr: "must declare from: env or from: file",
		},
		{
			name: "until without max_iterations",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    until:
      condition: "true"
`,
			wantErr: "max_iterations is required",
		},
		{
			name: "assert with empty condition",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    assert:
      condition: ""
`,
			wantErr: "assert.condition: must not be empty",
		},
		{
			name: "string form run warns about splitting",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    run: "echo hi"
`,
			wantWarn: "command string was split on spaces",
		},
		{
			name: "when valid expression",
			yaml: `
version: 1
name: valid
inputs:
  n:
    type: int
steps:
  - id: a
    when: "inputs.n > 0"
    run:
      argv: ["echo", "hi"]
      readonly: true
`,
			checkOK:     true,
			checkNoWarn: true,
		},
		{
			name: "when fails to compile",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    when: "inputs.n >"
    run:
      argv: ["echo", "hi"]
`,
			wantErr: "when",
		},
		{
			name: "when is not boolean",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    when: "run.name"
    run:
      argv: ["echo", "hi"]
`,
			wantErr: "when",
		},
		{
			name: "when references secrets",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    when: "secrets.token == \"x\""
    run:
      argv: ["echo", "hi"]
`,
			wantErr: "secrets",
		},
		{
			name: "assert condition fails to compile",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    assert:
      condition: "true &&"
`,
			wantErr: "assert.condition",
		},
		{
			name: "run argv references secrets in a template",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    run:
      argv: ["echo", "{{ .secrets.gitlab }}"]
`,
			wantErr: "secrets",
		},
		{
			name: "run stdin references secrets in a template",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    run:
      argv: ["cat"]
      stdin: "{{ .secrets.x }}"
`,
			wantErr: "secrets",
		},
		{
			name: "assert message references secrets in a template",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    assert:
      condition: "true"
      message: "{{ .secrets.x }}"
`,
			wantErr: "secrets",
		},
		{
			name: "run env literal references secrets, secret form of a declared secret is fine",
			yaml: `
version: 1
name: valid
secrets:
  tok:
    from: env
    key: TOK
steps:
  - id: a
    run:
      argv: ["echo", "hi"]
      env:
        BAD: "{{ .secrets.tok }}"
        SAFE:
          secret: tok
`,
			wantErr: "secrets",
		},
		{
			name: "run argv has a broken template",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    run:
      argv: ["echo", "{{ .x "]
`,
			wantErr: "unclosed action",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scn, err := Parse([]byte(tc.yaml), "test.yaml")
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			res := Validate(scn)

			if tc.checkOK && !res.OK() {
				t.Errorf("OK() = false, Errors = %v", res.Errors)
			}
			if tc.checkNoWarn && len(res.Warnings) != 0 {
				t.Errorf("Warnings = %v, want none", res.Warnings)
			}
			if tc.wantErr != "" && !diagnosticsContain(res.Errors, tc.wantErr) {
				t.Errorf("Errors = %v, want an error containing %q", res.Errors, tc.wantErr)
			}
			if tc.wantWarn != "" && !diagnosticsContain(res.Warnings, tc.wantWarn) {
				t.Errorf("Warnings = %v, want a warning containing %q", res.Warnings, tc.wantWarn)
			}
		})
	}
}

// TestValidateExampleHello checks that the bundled example scenario is valid
// and free of warnings.
func TestValidateExampleHello(t *testing.T) {
	scn, err := Load("../../examples/hello.yaml")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	res := Validate(scn)
	if !res.OK() {
		t.Fatalf("OK() = false, Errors = %v", res.Errors)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none", res.Warnings)
	}
}

// TestValidateCollectsMultipleExpressionAndTemplateErrors checks that several
// broken when:, argv, stdin and condition fields each report their own
// diagnostic instead of validation stopping at the first one.
func TestValidateCollectsMultipleExpressionAndTemplateErrors(t *testing.T) {
	yaml := `
version: 1
name: valid
steps:
  - id: a
    when: "inputs.n >"
    run:
      argv: ["echo", "{{ .secrets.gitlab }}"]
      stdin: "{{ .secrets.x }}"
  - id: b
    assert:
      condition: "true &&"
`
	scn, err := Parse([]byte(yaml), "test.yaml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	res := Validate(scn)

	if len(res.Errors) < 4 {
		t.Fatalf("Errors = %v, want at least 4", res.Errors)
	}
	for _, want := range []string{
		"steps[0].when",
		"steps[0].run.argv[1]",
		"steps[0].run.stdin",
		"steps[1].assert.condition",
	} {
		if !diagnosticsContain(res.Errors, want) {
			t.Errorf("Errors = %v, want a diagnostic for %q", res.Errors, want)
		}
	}
}
