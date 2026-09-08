package scenario

import (
	"reflect"
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
			wantErr: "must declare one of run, http, llm, agent, foreach, until, file, assert",
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
			name: "until with max_iterations",
			yaml: `
version: 1
name: valid
steps:
  - id: fix
    until:
      condition: "iter.exit_code == 0"
      max_iterations: 3
      step: { run: { argv: ["echo", "retry"] } }
`,
			checkOK:     true,
			checkNoWarn: true,
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
      step: { run: { argv: ["echo", "hi"] } }
`,
			wantErr: "max_iterations is required",
		},
		{
			name: "until without a body",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    until:
      condition: "true"
      max_iterations: 2
`,
			wantErr: "until.step: must declare a body",
		},
		{
			name: "until body with an id",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    until:
      condition: "true"
      max_iterations: 2
      step: { id: inner, run: { argv: ["echo", "hi"] } }
`,
			wantErr: "the body of an until has no id of its own",
		},
		{
			name: "until with an empty condition",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    until:
      condition: ""
      max_iterations: 2
      step: { run: { argv: ["echo", "hi"] } }
`,
			wantErr: "until.condition: must not be empty",
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
		{
			name: "llm without schema",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    llm:
      model: anthropic/claude-haiku-4-5
`,
			wantErr: "llm.schema: must not be empty",
		},
		{
			name: "llm model without slash",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    llm:
      model: haiku
      schema: schemas/classify.json
`,
			wantErr: `"haiku" must be <provider>/<model>`,
		},
		{
			name: "llm bad fallback model",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    llm:
      model: anthropic/claude-haiku-4-5
      schema: schemas/classify.json
      fallback_models: ["bogus"]
`,
			wantErr: `"bogus" must be <provider>/<model>`,
		},
		{
			name: "llm tools entry empty",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    llm:
      schema: schemas/classify.json
      tools: [""]
`,
			wantErr: "llm.tools[0]: must not be empty",
		},
		{
			name: "llm max_tokens negative",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    llm:
      schema: schemas/classify.json
      max_tokens: -1
`,
			wantErr: "llm.max_tokens: must not be negative",
		},
		{
			name: "llm temperature negative",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    llm:
      schema: schemas/classify.json
      temperature: -0.5
`,
			wantErr: "llm.temperature: must not be negative",
		},
		{
			name: "llm structured_mode garbage",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    llm:
      schema: schemas/classify.json
      structured_mode: weird
`,
			wantErr: `unknown value "weird"`,
		},
		{
			name: "llm prompt references secrets",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    llm:
      schema: schemas/classify.json
      prompt: "{{ .secrets.token }}"
`,
			wantErr: "secrets",
		},
		{
			name: "llm with references secrets",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    llm:
      schema: schemas/classify.json
      with:
        token: "{{ .secrets.token }}"
`,
			wantErr: "secrets",
		},
		{
			// Mirrors the example of specification section 3.5: every
			// field set and shaped correctly, so no error or warning.
			name: "llm valid step",
			yaml: `
version: 1
name: valid
steps:
  - id: diff
    run:
      argv: ["git", "diff"]
  - id: classify
    llm:
      model: anthropic/claude-haiku-4-5
      fallback_models: [openrouter/google/gemini-2.5-flash]
      system: prompts/classify.system.md
      prompt: prompts/classify.md
      with:
        diff: "{{ .steps.diff.stdout }}"
      schema: schemas/classify.json
      tools: [apis.jira.get_issue]
      max_tokens: 2000
      temperature: 0
`,
			checkOK:     true,
			checkNoWarn: true,
		},
		{
			name: "foreach without items",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    foreach:
      step:
        run:
          argv: ["echo", "hi"]
`,
			wantErr: "foreach.items: must not be empty",
		},
		{
			name: "foreach negative max_parallel",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    foreach:
      items: "{{ .inputs.repos }}"
      max_parallel: -1
      step:
        run:
          argv: ["echo", "hi"]
`,
			wantErr: "foreach.max_parallel: must not be negative",
		},
		{
			// Specification section 4, check 14.
			name: "foreach max_parallel over five warns",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    foreach:
      items: "{{ .inputs.repos }}"
      max_parallel: 6
      step:
        run:
          argv: ["echo", "hi"]
`,
			wantWarn: "more than 5 parallel items",
		},
		{
			name: "foreach on_item_error garbage",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    foreach:
      items: "{{ .inputs.repos }}"
      on_item_error: bogus
      step:
        run:
          argv: ["echo", "hi"]
`,
			wantErr: `unknown value "bogus"`,
		},
		{
			name: "foreach min_success out of range",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    foreach:
      items: "{{ .inputs.repos }}"
      on_item_error: continue
      min_success: 1.5
      step:
        run:
          argv: ["echo", "hi"]
`,
			wantErr: "foreach.min_success: must be between 0 and 1",
		},
		{
			name: "foreach min_success ignored unless on_item_error is continue",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    foreach:
      items: "{{ .inputs.repos }}"
      on_item_error: fail
      min_success: 1.0
      step:
        run:
          argv: ["echo", "hi"]
`,
			wantWarn: "ignored unless on_item_error is continue",
		},
		{
			name: "foreach missing step body",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    foreach:
      items: "{{ .inputs.repos }}"
`,
			wantErr: "foreach.step: must declare a body",
		},
		{
			name: "foreach step body carries an id",
			yaml: `
version: 1
name: valid
steps:
  - id: a
    foreach:
      items: "{{ .inputs.repos }}"
      step:
        id: nope
        run:
          argv: ["echo", "hi"]
`,
			wantErr: "the body of a foreach has no id of its own",
		},
		{
			// Mirrors the example of specification section 3.7: every
			// field set and shaped correctly, so no error or warning.
			name: "foreach valid step",
			yaml: `
version: 1
name: valid
steps:
  - id: per_repo
    foreach:
      items: "{{ .inputs.repos }}"
      as: repo
      max_parallel: 3
      on_item_error: continue
      min_success: 1.0
      step:
        run:
          argv: ["echo", "{{ .iter }}"]
`,
			checkOK:     true,
			checkNoWarn: true,
		},
		{
			// Section 9.3: the sugar form needs a message; the channel
			// itself is resolved against the global configuration at run
			// time, so it cannot be checked here.
			name: "notify valid step",
			yaml: `
version: 1
name: valid
steps:
  - id: tell
    notify: ops
    message: "run {{ .run.id }} finished"
`,
			checkOK:     true,
			checkNoWarn: true,
		},
		{
			name: "notify without message",
			yaml: `
version: 1
name: valid
steps:
  - id: tell
    notify: ops
`,
			wantErr: "notify requires a message",
		},
		{
			name: "notify with a broken message template",
			yaml: `
version: 1
name: valid
steps:
  - id: tell
    notify: ops
    message: "{{ .run.id"
`,
			wantErr: "steps[0].message",
		},
		{
			name: "apis interface unknown",
			yaml: `
version: 1
name: valid
apis:
  forge:
    pack: gitlab
    from: ../apis/
    interface: forge/v9
steps:
  - id: s
    run:
      argv: ["echo", "hi"]
`,
			wantErr: `apis.forge.interface: unknown interface "forge/v9"`,
		},
		{
			name: "apis interface known",
			yaml: `
version: 1
name: valid
apis:
  forge:
    pack: gitlab
    from: ../apis/
    interface: forge/v1
steps:
  - id: s
    run:
      argv: ["echo", "hi"]
`,
			checkOK:     true,
			checkNoWarn: true,
		},
		{
			name: "file valid read",
			yaml: `
version: 1
name: valid
steps:
  - id: s
    file:
      read: notes/a.txt
      parse: text
`,
			checkOK:     true,
			checkNoWarn: true,
		},
		{
			name: "file two operations set at once",
			yaml: `
version: 1
name: valid
steps:
  - id: s
    file:
      read: notes/a.txt
      write: notes/b.txt
      content: hi
`,
			wantErr: "must set exactly one of read, write, append, glob",
		},
		{
			name: "file write without content",
			yaml: `
version: 1
name: valid
steps:
  - id: s
    file:
      write: notes/a.txt
`,
			wantErr: "file.content: write requires content",
		},
		{
			name: "file content on a read",
			yaml: `
version: 1
name: valid
steps:
  - id: s
    file:
      read: notes/a.txt
      content: hi
`,
			wantErr: "file.content: belongs to write and append, not to read",
		},
		{
			name: "file parse on a write",
			yaml: `
version: 1
name: valid
steps:
  - id: s
    file:
      write: notes/a.txt
      content: hi
      parse: json
`,
			wantErr: "file.parse: belongs to read, not to write",
		},
		{
			name: "file absolute path is not a validation error",
			yaml: `
version: 1
name: valid
steps:
  - id: s
    file:
      read: /etc/passwd
`,
			checkOK:     true,
			checkNoWarn: true,
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

// TestParseValidateSwitchStep checks that a switch step written the way
// specification section 3.10 spells it out survives Parse and Validate: no id
// of its own, a scalar subject expression, one step per case and a default.
func TestParseValidateSwitchStep(t *testing.T) {
	yaml := `
version: 1
name: valid
steps:
  - id: classify
    run:
      argv: ["echo", "high"]
  - switch: steps.classify.result.risk
    cases:
      high: { id: deep, run: { argv: ["echo", "deep"] } }
      low:  { id: light, run: { argv: ["echo", "light"] } }
    default: { id: skip_note, run: { argv: ["echo", "skipped"] } }
`
	scn, err := Parse([]byte(yaml), "test.yaml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	// Parse expands the switch into one guarded step per case, in the order
	// the cases were written, with the default guarded by the negation.
	var ids []string
	for _, step := range scn.Steps {
		ids = append(ids, step.ID)
	}
	want := []string{"classify", "deep", "light", "skip_note"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("step ids = %v, want %v", ids, want)
	}

	subject := "steps.classify.result.risk"
	for i, cond := range map[int]string{
		1: `(` + subject + `) == "high"`,
		2: `(` + subject + `) == "low"`,
		3: `!((` + subject + `) == "high" || (` + subject + `) == "low")`,
	} {
		if scn.Steps[i].When != cond {
			t.Errorf("Steps[%d].When = %q, want %q", i, scn.Steps[i].When, cond)
		}
	}

	res := Validate(scn)
	if !res.OK() {
		t.Fatalf("OK() = false, Errors = %v", res.Errors)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none", res.Warnings)
	}
}

// TestValidateSwitchWithoutDefaultWarns checks the section 4 check 10
// fallback: without a default the validator cannot prove every value of the
// subject is covered, so it warns.
func TestValidateSwitchWithoutDefaultWarns(t *testing.T) {
	yaml := `
version: 1
name: valid
steps:
  - switch: inputs.mode
    cases:
      a: { id: a1, run: { argv: ["echo", "a"] } }
`
	scn, err := Parse([]byte(yaml), "test.yaml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	res := Validate(scn)
	if !res.OK() {
		t.Fatalf("OK() = false, Errors = %v", res.Errors)
	}
	if !diagnosticsContain(res.Warnings, "no default case") {
		t.Errorf("Warnings = %v, want a missing-default warning", res.Warnings)
	}
}
