package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/runstore"
	"github.com/foxzi/baton/internal/secrets"
)

// writeAgentFiles puts the result schema and a fake behaviour script next to
// the scenario, which is where an agent step looks for them.
func writeAgentFiles(t *testing.T, dir, schema, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "result.json"), []byte(schema), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "script.yaml"), []byte(script), 0o644); err != nil {
		t.Fatalf("write script: %v", err)
	}
}

const agentSchema = `{
  "type": "object",
  "required": ["verdict"],
  "properties": {"verdict": {"type": "string"}},
  "additionalProperties": true
}`

// 1. An agent step that runs a command tool and submits a valid result.
func TestAgent_CommandAndSubmit(t *testing.T) {
	yamlText := `
version: 1
name: agent-success
commands:
  hello:
    argv: ["echo", "hi"]
    description: Say hi
    readonly: true
steps:
  - id: review
    timeout: 30s
    agent:
      engine: fake
      script: script.yaml
      prompt: "Review {{ .thing }}"
      with: { thing: "the code" }
      profile: fix
      result: result.json
`
	script := `
usage: { input_tokens: 100, output_tokens: 20 }
cost_usd: 0.25
calls:
  - tool: hello
  - tool: submit_result
    args: { verdict: "ok" }
`
	eng, store, dir := newTestEngine(t, yamlText, nil)
	writeAgentFiles(t, dir, agentSchema, script)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q (%v), want %q", result.Status, result.Error, runstore.StatusSuccess)
	}

	stepDir := filepath.Join(store.Dir(), "steps", "review")
	out := readStepJSON(t, filepath.Join(stepDir, "output.json"))
	value, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("result is %T, want an object: %#v", out["result"], out["result"])
	}
	if value["verdict"] != "ok" {
		t.Fatalf("result.verdict = %#v, want \"ok\"", value["verdict"])
	}
	if out["tool_calls"] != float64(2) {
		t.Fatalf("tool_calls = %#v, want 2", out["tool_calls"])
	}
	if out["cost_usd"] != 0.25 {
		t.Fatalf("cost_usd = %#v, want 0.25", out["cost_usd"])
	}

	// The prompt is rendered with the with: variables.
	in := readStepJSON(t, filepath.Join(stepDir, "input.json"))
	if in["prompt"] != "Review the code" {
		t.Fatalf("prompt = %#v, want \"Review the code\"", in["prompt"])
	}

	// Every call is audited and the transcript is kept (section 7.7).
	audit := readFile(t, filepath.Join(stepDir, "tool-calls.jsonl"))
	if lines := strings.Count(strings.TrimSpace(audit), "\n") + 1; lines != 2 {
		t.Fatalf("tool-calls.jsonl has %d lines, want 2:\n%s", lines, audit)
	}
	if !strings.Contains(audit, `"tool":"hello"`) {
		t.Fatalf("the command call is not in the audit log:\n%s", audit)
	}
	if _, err := os.Stat(filepath.Join(stepDir, "transcript.jsonl")); err != nil {
		t.Fatalf("transcript: %v", err)
	}

	cost, err := runstore.ReadCost(filepath.Dir(store.Dir()), store.ID())
	if err != nil {
		t.Fatalf("ReadCost: %v", err)
	}
	if len(cost.Steps) != 1 || cost.Steps[0].Step != "review" {
		t.Fatalf("cost.json steps = %#v, want one entry for review", cost.Steps)
	}
	if cost.Steps[0].InputTokens != 100 || cost.Steps[0].OutputTokens != 20 {
		t.Fatalf("cost entry tokens = %d/%d, want 100/20", cost.Steps[0].InputTokens, cost.Steps[0].OutputTokens)
	}
	if cost.TotalUSD == nil || *cost.TotalUSD != 0.25 {
		t.Fatalf("total_usd = %#v, want 0.25", cost.TotalUSD)
	}
}

// 2. A step whose agent stops without submit_result fails with the schema
// class (section 3.6).
func TestAgent_NoSubmitIsSchemaError(t *testing.T) {
	yamlText := `
version: 1
name: agent-no-submit
commands:
  hello:
    argv: ["echo", "hi"]
    readonly: true
steps:
  - id: review
    agent:
      engine: fake
      script: script.yaml
      prompt: work
      profile: fix
      result: result.json
`
	script := "calls:\n  - tool: hello\n"
	eng, store, dir := newTestEngine(t, yamlText, nil)
	writeAgentFiles(t, dir, agentSchema, script)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusFailed {
		t.Fatalf("Status = %q, want %q", result.Status, runstore.StatusFailed)
	}
	if result.Error == nil || result.Error.Class != ClassSchema {
		t.Fatalf("error = %#v, want class %s", result.Error, ClassSchema)
	}
	if !strings.Contains(result.Error.Message, "submit_result") {
		t.Fatalf("message = %q, want it to mention submit_result", result.Error.Message)
	}

	out := readStepJSON(t, filepath.Join(store.Dir(), "steps", "review", "output.json"))
	if out["status"] != "failed" {
		t.Fatalf("output.json status = %#v, want failed", out["status"])
	}
}

// 3. A result that does not match the schema is refused by the gateway, so
// the step ends unsubmitted.
func TestAgent_InvalidResultRefused(t *testing.T) {
	yamlText := `
version: 1
name: agent-bad-result
steps:
  - id: review
    agent:
      engine: fake
      script: script.yaml
      prompt: work
      result: result.json
`
	script := "calls:\n  - tool: submit_result\n    args: { other: 1 }\n"
	eng, store, dir := newTestEngine(t, yamlText, nil)
	writeAgentFiles(t, dir, agentSchema, script)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Error == nil || result.Error.Class != ClassSchema {
		t.Fatalf("error = %#v, want class %s", result.Error, ClassSchema)
	}

	// The refusal is audited as an error, not as a successful call.
	audit := readFile(t, filepath.Join(store.Dir(), "steps", "review", "tool-calls.jsonl"))
	if !strings.Contains(audit, "does not match the schema") {
		t.Fatalf("the refusal is not in the audit log:\n%s", audit)
	}
}

// 4. Only the commands the step's policy names reach the agent (section
// 7.3).
func TestAgent_ToolsNarrowedByPolicy(t *testing.T) {
	yamlText := `
version: 1
name: agent-policy
commands:
  allowed:
    argv: ["echo", "yes"]
    readonly: true
  denied:
    argv: ["echo", "no"]
    readonly: true
steps:
  - id: review
    agent:
      engine: fake
      script: script.yaml
      prompt: work
      profile: fix
      tools:
        exec: { mode: commands, commands: [allowed] }
      result: result.json
`
	allowedScript := `
calls:
  - tool: allowed
  - tool: submit_result
    args: { verdict: "ok" }
`
	var tools any
	eng, store, dir := newTestEngine(t, yamlText, func(o *Options) {
		o.Observer = func(ev Event) {
			if ev.Type == "agent_started" {
				tools = ev.Fields["tools"]
			}
		}
	})
	writeAgentFiles(t, dir, agentSchema, allowedScript)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q (%v), want success", result.Status, result.Error)
	}
	// One of the two declared commands is offered, the allowed one.
	if tools != 1 {
		t.Fatalf("agent_started tools = %#v, want 1", tools)
	}
	audit := readFile(t, filepath.Join(store.Dir(), "steps", "review", "tool-calls.jsonl"))
	if !strings.Contains(audit, `"tool":"allowed"`) {
		t.Fatalf("the allowed command was not called:\n%s", audit)
	}

	// The command the policy left out is not on the gateway at all, so
	// calling it is a protocol error for the agent.
	deniedScript := "calls:\n  - tool: denied\n"
	eng, _, dir = newTestEngine(t, yamlText, nil)
	writeAgentFiles(t, dir, agentSchema, deniedScript)

	result, err = eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Error == nil || result.Error.Class != ClassCommand {
		t.Fatalf("error = %#v, want class %s", result.Error, ClassCommand)
	}
}

// 5. The step's budget_usd is enforced against what the engine reports.
func TestAgent_StepBudgetExceeded(t *testing.T) {
	yamlText := `
version: 1
name: agent-budget
steps:
  - id: review
    agent:
      engine: fake
      script: script.yaml
      prompt: work
      budget_usd: 0.10
      result: result.json
`
	script := `
cost_usd: 0.5
calls:
  - tool: submit_result
    args: { verdict: "ok" }
`
	eng, _, dir := newTestEngine(t, yamlText, nil)
	writeAgentFiles(t, dir, agentSchema, script)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Error == nil || result.Error.Class != ClassBudget {
		t.Fatalf("error = %#v, want class %s", result.Error, ClassBudget)
	}
	if code := result.ExitCode(); code != 4 {
		t.Fatalf("ExitCode = %d, want 4", code)
	}
}

// 6. An unknown engine, a missing schema and a skill directory without a
// SKILL.md are all config errors, before the gateway is even started.
func TestAgent_ConfigErrors(t *testing.T) {
	cases := []struct {
		name  string
		step  string
		files func(t *testing.T, dir string)
		want  string
	}{
		{
			name: "unknown engine",
			step: `
      engine: codex
      prompt: work
      result: result.json
`,
			want: "unknown agent engine",
		},
		{
			name: "missing schema",
			step: `
      engine: fake
      script: script.yaml
      prompt: work
      result: missing.json
`,
			want: "agent.result",
		},
		{
			name: "skill without SKILL.md",
			step: `
      engine: fake
      script: script.yaml
      prompt: work
      skills: [./skills/go]
      result: result.json
`,
			want: "agent.skills",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			yamlText := "version: 1\nname: agent-config\nsteps:\n  - id: review\n    agent:" + tc.step
			eng, _, dir := newTestEngine(t, yamlText, nil)
			writeAgentFiles(t, dir, agentSchema, "calls: []\n")

			result, err := eng.Run(context.Background())
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if result.Error == nil || result.Error.Class != ClassConfig {
				t.Fatalf("error = %#v, want class %s", result.Error, ClassConfig)
			}
			if !strings.Contains(result.Error.Message, tc.want) {
				t.Fatalf("message = %q, want it to mention %q", result.Error.Message, tc.want)
			}
		})
	}
}

// 7. A step's env reaches the engine with its secrets resolved, and the
// secret never lands in the run directory (section 13).
func TestAgent_EnvSecretStaysOutOfRunDir(t *testing.T) {
	yamlText := `
version: 1
name: agent-env
secrets:
  token: { from: env, key: BATON_TEST_AGENT_TOKEN }
steps:
  - id: review
    agent:
      engine: fake
      script: script.yaml
      prompt: work
      env:
        TOKEN: { secret: token }
      result: result.json
`
	script := `
calls:
  - tool: submit_result
    args: { verdict: "ok" }
`
	const plaintext = "s3cret-agent-value"
	t.Setenv("BATON_TEST_AGENT_TOKEN", plaintext)
	eng, store, dir := newTestEngine(t, yamlText, func(o *Options) {
		s, err := secrets.Resolve(o.Scenario.Secrets, filepath.Dir(o.Scenario.Path))
		if err != nil {
			t.Fatalf("secrets.Resolve: %v", err)
		}
		o.Secrets = s
	})
	writeAgentFiles(t, dir, agentSchema, script)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q (%v), want success", result.Status, result.Error)
	}

	// Nothing in the run directory may carry the plaintext (section 13).
	err = filepath.WalkDir(store.Dir(), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(data), plaintext) {
			t.Errorf("%s contains the secret plaintext", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk run directory: %v", err)
	}
}

// authPack mirrors gitlabLikePack but declares header authorisation, so the
// runner has a credential to inject on the agent's behalf.
const authPack = `pack: gitlab
version: 1
auth: { kind: header, name: PRIVATE-TOKEN }
config:
  base_url: {}
ops:
  get_project:
    get: /projects/{id}
    readonly: true
    params:
      id: { pattern: '^\d+$' }
  create_note:
    post: /projects/{id}/notes
    params:
      id: { pattern: '^\d+$' }
      body: {}
    encode: json
`

// 8. The readonly pack operations the step's policy names reach the agent as
// tools, and the runner performs the call with the credential the agent
// never sees (sections 7.4.1 and 7.4.8).
func TestAgent_APIToolCall(t *testing.T) {
	var gotPath, gotToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.Header.Get("PRIVATE-TOKEN")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":7,"name":"demo"}`))
	}))
	defer server.Close()

	yamlText := fmt.Sprintf(`
version: 1
name: agent-apis
secrets:
  forge: { from: env, key: BATON_TEST_FORGE }
apis:
  gitlab:
    pack: gitlab
    from: ./apis/
    auth: { secret: forge }
    config:
      base_url: %q
steps:
  - id: review
    agent:
      engine: fake
      script: script.yaml
      prompt: work
      tools:
        apis: [gitlab.get_project]
      result: result.json
`, server.URL)
	script := `
calls:
  - tool: gitlab.get_project
    args: { id: "7" }
  - tool: submit_result
    args: { verdict: "ok" }
`
	t.Setenv("BATON_TEST_FORGE", "s3cr3t")
	eng, store, dir := newTestEngine(t, yamlText, func(o *Options) {
		s, err := secrets.Resolve(o.Scenario.Secrets, filepath.Dir(o.Scenario.Path))
		if err != nil {
			t.Fatalf("secrets.Resolve: %v", err)
		}
		o.Secrets = s
	})
	writeAgentFiles(t, dir, agentSchema, script)
	writePack(t, dir, "gitlab", authPack)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q (%v), want success", result.Status, result.Error)
	}
	if gotPath != "/projects/7" {
		t.Errorf("server saw path %q, want /projects/7", gotPath)
	}
	if gotToken != "s3cr3t" {
		t.Errorf("server saw PRIVATE-TOKEN %q, want the runner to inject the credential", gotToken)
	}

	audit := readFile(t, filepath.Join(store.Dir(), "steps", "review", "tool-calls.jsonl"))
	if !strings.Contains(audit, `"tool":"gitlab.get_project"`) {
		t.Errorf("the api call is not in the audit log:\n%s", audit)
	}
	if strings.Contains(audit, "s3cr3t") {
		t.Errorf("the audit log leaks the credential:\n%s", audit)
	}
}

// 9. A step that names an operation the pack marks as writing is a
// configuration error: only readonly operations may become tools.
func TestAgent_APIToolMustBeReadonly(t *testing.T) {
	yamlText := `
version: 1
name: agent-apis-write
apis:
  gitlab:
    pack: gitlab
    from: ./apis/
    config:
      base_url: "http://example.invalid"
steps:
  - id: review
    agent:
      engine: fake
      script: script.yaml
      prompt: work
      tools:
        apis: [gitlab.create_note]
      result: result.json
`
	eng, _, dir := newTestEngine(t, yamlText, nil)
	writeAgentFiles(t, dir, agentSchema, "calls: []\n")
	writePack(t, dir, "gitlab", gitlabLikePack)

	result, _ := eng.Run(context.Background())
	if result.Status != runstore.StatusFailed {
		t.Fatalf("Status = %q, want failed", result.Status)
	}
	if result.Error == nil || result.Error.Class != ClassConfig {
		t.Fatalf("error = %+v, want class %q", result.Error, ClassConfig)
	}
	if !strings.Contains(result.Error.Message, "agent.tools") {
		t.Errorf("message = %q, want it to point at agent.tools", result.Error.Message)
	}
}
