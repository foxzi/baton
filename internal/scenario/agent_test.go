package scenario

import (
	"strings"
	"testing"
)

// TestParseAgentStep checks that a full agent body and the commands block it
// draws on decode into their typed models.
func TestParseAgentStep(t *testing.T) {
	yaml := `
version: 1
name: review
secrets:
  token: { from: env, key: TOKEN }
apis:
  forge:
    pack: gitlab
    from: ./apis/
commands:
  test:
    argv: ["go", "test", "./..."]
    description: Run all tests
    timeout: 5m
    readonly: true
  lint:
    argv: ["golangci-lint", "run", "{{ .args.path }}"]
    args: { path: { pattern: '^[\w/.-]+$', default: "./..." } }
    parse: json
    readonly: true
    max_calls: 5
    env:
      GOFLAGS: -mod=mod
      TOKEN: { secret: token }
steps:
  - id: review
    agent:
      engine: claude-code
      model: claude-sonnet-4-6
      prompt: prompts/review.md
      system: You review code.
      with: { project: "{{ .inputs.project }}" }
      skills: [./skills/go-review]
      profile: review
      tools:
        fs: { read: true, write: none, deny: [".env*"] }
        git: { read: true, commit: false }
        exec: { mode: commands, commands: [test, lint] }
        apis: [forge.get_change]
        fetch: { allow: ["pkg.go.dev"], max_bytes: 300k, max_calls: 10 }
        mcp: [context7]
        state: read
      limits: { max_tool_calls: 60, max_result_bytes: 64k }
      max_turns: 25
      budget_usd: 2
      allow_unsafe: false
      env:
        TOKEN: { secret: token }
      result: schemas/findings.json
`
	scn, err := Parse([]byte(yaml), "test.yaml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	step := scn.Steps[0]
	if step.Kind() != KindAgent {
		t.Fatalf("Kind() = %q, want %q", step.Kind(), KindAgent)
	}
	agent := step.Agent
	if agent.Engine != "claude-code" || agent.Model != "claude-sonnet-4-6" {
		t.Errorf("engine/model = %q/%q, want claude-code/claude-sonnet-4-6", agent.Engine, agent.Model)
	}
	if agent.Profile != ProfileReview {
		t.Errorf("Profile = %q, want %q", agent.Profile, ProfileReview)
	}
	if agent.Result != "schemas/findings.json" {
		t.Errorf("Result = %q, want the schema path", agent.Result)
	}
	if agent.MaxTurns != 25 || agent.BudgetUSD != 2 {
		t.Errorf("max_turns/budget_usd = %d/%v, want 25/2", agent.MaxTurns, agent.BudgetUSD)
	}
	if agent.Env["TOKEN"].Secret != "token" {
		t.Errorf("env.TOKEN = %+v, want a reference to secret token", agent.Env["TOKEN"])
	}

	tools := agent.Tools
	if tools == nil {
		t.Fatalf("Tools = nil, want the overrides")
	}
	if tools.FS == nil || tools.FS.Read == nil || !*tools.FS.Read || tools.FS.Write != FSWriteNone {
		t.Errorf("fs = %+v, want read: true and write: none", tools.FS)
	}
	if tools.Git == nil || tools.Git.Commit == nil || *tools.Git.Commit {
		t.Errorf("git = %+v, want commit: false", tools.Git)
	}
	if tools.Exec == nil || tools.Exec.Mode != ExecModeCommands || len(tools.Exec.Commands) != 2 {
		t.Errorf("exec = %+v, want mode commands with two commands", tools.Exec)
	}
	if tools.Fetch == nil || tools.Fetch.MaxBytes != 300*1024 {
		t.Errorf("fetch = %+v, want max_bytes 300k", tools.Fetch)
	}
	if tools.State != StateRead {
		t.Errorf("state = %q, want %q", tools.State, StateRead)
	}
	if agent.Limits == nil || agent.Limits.MaxResultBytes != 64*1024 {
		t.Errorf("limits = %+v, want max_result_bytes 64k", agent.Limits)
	}

	lint, ok := scn.Commands["lint"]
	if !ok {
		t.Fatalf("Commands = %v, want a lint entry", scn.Commands)
	}
	if len(lint.Argv) != 3 || lint.Parse != ParseJSON || !lint.Readonly || lint.MaxCalls != 5 {
		t.Errorf("lint = %+v, want the declared argv, parse, readonly and max_calls", lint)
	}
	if lint.Args["path"].Pattern == "" || lint.Args["path"].Default != "./..." {
		t.Errorf("lint.args.path = %+v, want a pattern and a default", lint.Args["path"])
	}
	if lint.Env["TOKEN"].Secret != "token" || lint.Env["GOFLAGS"].Value != "-mod=mod" {
		t.Errorf("lint.env = %+v, want a secret reference and a literal", lint.Env)
	}
	if lint.Line == 0 {
		t.Errorf("lint.Line = 0, want the declaration line")
	}
	if scn.Commands["test"].Timeout != Duration(5*60*1e9) {
		t.Errorf("test.timeout = %v, want 5m", scn.Commands["test"].Timeout)
	}
}

// TestParseAgentUnknownFields checks that the strict-field check reaches the
// blocks nested inside an agent body and inside a commands entry, which the
// decoder's own check does not, since a step decodes itself.
func TestParseAgentUnknownFields(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "unknown agent field",
			yaml: `
version: 1
name: n
steps:
  - id: s
    agent:
      engine: fake
      script: s.yaml
      prompt: p
      result: r.json
      turns: 3
`,
			wantErr: `unknown agent field "turns"`,
		},
		{
			name: "unknown field inside tools",
			yaml: `
version: 1
name: n
steps:
  - id: s
    agent:
      engine: fake
      script: s.yaml
      prompt: p
      result: r.json
      tools:
        shell: { mode: none }
`,
			wantErr: `unknown tools field "shell"`,
		},
		{
			name: "unknown field inside tools.fs",
			yaml: `
version: 1
name: n
steps:
  - id: s
    agent:
      engine: fake
      script: s.yaml
      prompt: p
      result: r.json
      tools:
        fs: { read: true, allow: ["**"] }
`,
			wantErr: `unknown fs field "allow"`,
		},
		{
			name: "tools must be a mapping",
			yaml: `
version: 1
name: n
steps:
  - id: s
    agent:
      engine: fake
      script: s.yaml
      prompt: p
      result: r.json
      tools: [fs]
`,
			wantErr: "tools must be a mapping",
		},
		{
			name: "unknown commands entry field",
			yaml: `
version: 1
name: n
commands:
  test:
    argv: ["go", "test"]
    shell: true
steps:
  - id: s
    run:
      argv: ["echo", "hi"]
`,
			wantErr: `unknown commands entry field "shell"`,
		},
		{
			name: "unknown field inside a command argument",
			yaml: `
version: 1
name: n
commands:
  lint:
    argv: ["lint", "{{ .args.path }}"]
    args: { path: { pattern: '^\w+$', enum: [a, b] } }
steps:
  - id: s
    run:
      argv: ["echo", "hi"]
`,
			wantErr: `unknown args path field "enum"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml), "test.yaml")
			if err == nil {
				t.Fatalf("Parse() error = nil, want %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Parse() error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// TestValidateAgent runs the checks of an agent body one problem at a time.
func TestValidateAgent(t *testing.T) {
	// header declares everything the bodies below reference, so that a case
	// only fails for the reason it is testing.
	const header = `
version: 1
name: n
secrets:
  token: { from: env, key: TOKEN }
apis:
  forge: { pack: gitlab, from: ./apis/ }
commands:
  test: { argv: ["go", "test"] }
steps:
  - id: s
    agent:
`

	cases := []struct {
		name     string
		body     string
		wantErr  string
		wantWarn string
		checkOK  bool
	}{
		{
			name: "valid body",
			body: `      engine: claude-code
      prompt: prompts/review.md
      result: schemas/findings.json
      profile: review
      tools:
        exec: { mode: commands, commands: [test] }
        apis: [forge.get_change]
        fetch: { allow: ["pkg.go.dev"] }
        state: read
      limits: { max_tool_calls: 60 }
      env: { TOKEN: { secret: token } }
`,
			checkOK: true,
		},
		{
			name: "engine missing",
			body: `      prompt: p
      result: r.json
`,
			wantErr: "agent.engine: must name an engine",
		},
		{
			name: "engine unknown",
			body: `      engine: autogpt
      prompt: p
      result: r.json
`,
			wantErr: `unknown engine "autogpt"`,
		},
		{
			name: "fake without script",
			body: `      engine: fake
      prompt: p
      result: r.json
`,
			wantErr: "engine: fake needs a behaviour script",
		},
		{
			name: "script on a real engine",
			body: `      engine: claude-code
      script: script.yaml
      prompt: p
      result: r.json
`,
			wantErr: "script applies to engine: fake only",
		},
		{
			name: "inherit_auth on another engine",
			body: `      engine: claude-code
      inherit_auth: true
      prompt: p
      result: r.json
`,
			wantErr: "agent.inherit_auth: applies to engine: codex only",
		},
		{
			name: "prompt missing",
			body: `      engine: claude-code
      result: r.json
`,
			wantErr: "agent.prompt: must not be empty",
		},
		{
			name: "result missing",
			body: `      engine: claude-code
      prompt: p
`,
			wantErr: "agent.result: must name the schema",
		},
		{
			name: "profile unknown",
			body: `      engine: claude-code
      prompt: p
      result: r.json
      profile: yolo
`,
			wantErr: `unknown profile "yolo"`,
		},
		{
			name: "negative budget",
			body: `      engine: claude-code
      prompt: p
      result: r.json
      budget_usd: -1
`,
			wantErr: "agent.budget_usd: must not be negative",
		},
		{
			name: "undeclared secret in env",
			body: `      engine: claude-code
      prompt: p
      result: r.json
      env: { TOKEN: { secret: nope } }
`,
			wantErr: `undeclared secret "nope"`,
		},
		{
			name: "undeclared command",
			body: `      engine: claude-code
      prompt: p
      result: r.json
      tools:
        exec: { mode: commands, commands: [deploy] }
`,
			wantErr: `undeclared command "deploy"`,
		},
		{
			name: "commands ignored without the mode",
			body: `      engine: claude-code
      prompt: p
      result: r.json
      tools:
        exec: { mode: none, commands: [test] }
`,
			wantWarn: "ignored unless mode is commands",
		},
		{
			name: "op reference not <api>.<op>",
			body: `      engine: claude-code
      prompt: p
      result: r.json
      tools:
        apis: [get_change]
`,
			wantErr: `"get_change" must be <api>.<op>`,
		},
		{
			name: "op of an undeclared api",
			body: `      engine: claude-code
      prompt: p
      result: r.json
      tools:
        apis: [jira.get_issue]
`,
			wantErr: `undeclared api "jira"`,
		},
		{
			name: "fetch without an allow list",
			body: `      engine: claude-code
      prompt: p
      result: r.json
      tools:
        fetch: { max_calls: 3 }
`,
			wantErr: "fetch.allow: must list the allowed hosts",
		},
		{
			name: "unknown state access",
			body: `      engine: claude-code
      prompt: p
      result: r.json
      tools:
        state: write
`,
			wantErr: `unknown value "write"`,
		},
		{
			name: "negative limit",
			body: `      engine: claude-code
      prompt: p
      result: r.json
      limits: { max_tool_calls: -1 }
`,
			wantErr: "limits.max_tool_calls: must not be negative",
		},
		{
			name: "broken template in with",
			body: `      engine: claude-code
      prompt: p
      result: r.json
      with: { project: "{{ .inputs.project" }
`,
			wantErr: "agent.with.project",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scn, err := Parse([]byte(header+tc.body), "test.yaml")
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			res := Validate(scn)
			if tc.checkOK {
				if !res.OK() {
					t.Fatalf("OK() = false, Errors = %v", res.Errors)
				}
				if len(res.Warnings) != 0 {
					t.Errorf("Warnings = %v, want none", res.Warnings)
				}
			}
			if tc.wantErr != "" && !diagnosticsContain(res.Errors, tc.wantErr) {
				t.Errorf("Errors = %v, want one mentioning %q", res.Errors, tc.wantErr)
			}
			if tc.wantWarn != "" && !diagnosticsContain(res.Warnings, tc.wantWarn) {
				t.Errorf("Warnings = %v, want one mentioning %q", res.Warnings, tc.wantWarn)
			}
		})
	}
}

// TestValidateCommands runs the checks of the commands block one problem at
// a time.
func TestValidateCommands(t *testing.T) {
	const footer = `
steps:
  - id: s
    run:
      argv: ["echo", "hi"]
`

	cases := []struct {
		name     string
		commands string
		wantErr  string
		wantWarn string
		checkOK  bool
	}{
		{
			name: "valid commands",
			commands: `
secrets:
  token: { from: env, key: TOKEN }
commands:
  test:
    argv: ["go", "test", "./..."]
    timeout: 5m
    readonly: true
  lint:
    argv: ["lint", "{{ .args.path }}"]
    args: { path: { pattern: '^[\w/.-]+$', default: "./..." } }
    parse: json
    max_calls: 5
    env: { TOKEN: { secret: token } }
`,
			checkOK: true,
		},
		{
			name: "name not an identifier",
			commands: `
commands:
  Run-Tests: { argv: ["go", "test"] }
`,
			wantErr: `commands.Run-Tests: "Run-Tests" must match`,
		},
		{
			name: "empty argv",
			commands: `
commands:
  test: { argv: [] }
`,
			wantErr: "commands.test.argv: must not be empty",
		},
		{
			name: "argument without a pattern",
			commands: `
commands:
  lint:
    argv: ["lint", "{{ .args.path }}"]
    args: { path: { default: "./..." } }
`,
			wantErr: "must constrain the argument with a pattern",
		},
		{
			name: "invalid pattern",
			commands: `
commands:
  lint:
    argv: ["lint", "{{ .args.path }}"]
    args: { path: { pattern: '^([' } }
`,
			wantErr: "invalid regular expression",
		},
		{
			name: "default does not match the pattern",
			commands: `
commands:
  lint:
    argv: ["lint", "{{ .args.path }}"]
    args: { path: { pattern: '^\d+$', default: "./..." } }
`,
			wantErr: "does not match the pattern",
		},
		{
			name: "default on a required argument",
			commands: `
commands:
  lint:
    argv: ["lint", "{{ .args.path }}"]
    args: { path: { pattern: '^[\w/.-]+$', default: "./...", required: true } }
`,
			wantWarn: "default is unreachable on a required argument",
		},
		{
			name: "unknown parse mode",
			commands: `
commands:
  test: { argv: ["go", "test"], parse: yaml }
`,
			wantErr: `unknown mode "yaml"`,
		},
		{
			name: "negative max_calls",
			commands: `
commands:
  test: { argv: ["go", "test"], max_calls: -1 }
`,
			wantErr: "commands.test.max_calls: must not be negative",
		},
		{
			name: "undeclared secret in env",
			commands: `
commands:
  test:
    argv: ["go", "test"]
    env: { TOKEN: { secret: nope } }
`,
			wantErr: `undeclared secret "nope"`,
		},
		{
			name: "broken template in argv",
			commands: `
commands:
  lint:
    argv: ["lint", "{{ .args.path"]
`,
			wantErr: "commands.lint.argv[1]",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scn, err := Parse([]byte("version: 1\nname: n\n"+tc.commands+footer), "test.yaml")
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			res := Validate(scn)
			if tc.checkOK {
				if !res.OK() {
					t.Fatalf("OK() = false, Errors = %v", res.Errors)
				}
				if len(res.Warnings) != 0 {
					t.Errorf("Warnings = %v, want none", res.Warnings)
				}
			}
			if tc.wantErr != "" && !diagnosticsContain(res.Errors, tc.wantErr) {
				t.Errorf("Errors = %v, want one mentioning %q", res.Errors, tc.wantErr)
			}
			if tc.wantWarn != "" && !diagnosticsContain(res.Warnings, tc.wantWarn) {
				t.Errorf("Warnings = %v, want one mentioning %q", res.Warnings, tc.wantWarn)
			}
		})
	}
}
