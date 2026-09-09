package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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
	// One of the two declared commands is offered, the allowed one, next to
	// the four file tools, the six git tools and the state.get the fix
	// profile grants (section 7.2).
	if tools != 12 {
		t.Fatalf("agent_started tools = %#v, want 12", tools)
	}
	audit := readFile(t, filepath.Join(store.Dir(), "steps", "review", "tool-calls.jsonl"))
	if !strings.Contains(audit, `"tool":"allowed"`) {
		t.Fatalf("the allowed command was not called:\n%s", audit)
	}

	// The command the policy left out is not on the gateway at all, so
	// calling it is refused as a policy violation (section 13).
	deniedScript := "calls:\n  - tool: denied\n"
	eng, store, dir = newTestEngine(t, yamlText, nil)
	writeAgentFiles(t, dir, agentSchema, deniedScript)

	result, err = eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Error == nil || result.Error.Class != ClassPolicy {
		t.Fatalf("error = %#v, want class %s", result.Error, ClassPolicy)
	}
	audit = readFile(t, filepath.Join(store.Dir(), "steps", "review", "tool-calls.jsonl"))
	if !strings.Contains(audit, `"tool":"denied"`) || !strings.Contains(audit, `"status":"denied"`) {
		t.Errorf("the refused call is not in the audit log:\n%s", audit)
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
      engine: no-such-engine
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

// 9a. An agent that asks the gateway for a writing operation of a pack it
// otherwise has access to is refused at run time as well: the operation is
// not one of the step's tools (section 13).
func TestAgent_APIWriteOpIsRefusedAtRuntime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the server was called for %s %s, want the call refused by the gateway", r.Method, r.URL.Path)
	}))
	defer server.Close()

	yamlText := fmt.Sprintf(`
version: 1
name: agent-apis-runtime
apis:
  gitlab:
    pack: gitlab
    from: ./apis/
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
  - tool: gitlab.create_note
    args: { id: "7", body: "hi" }
  - tool: submit_result
    args: { verdict: "ok" }
`
	eng, store, dir := newTestEngine(t, yamlText, nil)
	writeAgentFiles(t, dir, agentSchema, script)
	writePack(t, dir, "gitlab", gitlabLikePack)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Error == nil || result.Error.Class != ClassPolicy {
		t.Fatalf("error = %+v, want class %q", result.Error, ClassPolicy)
	}
	audit := readFile(t, filepath.Join(store.Dir(), "steps", "review", "tool-calls.jsonl"))
	if !strings.Contains(audit, `"status":"denied"`) {
		t.Errorf("the refused call is not in the audit log:\n%s", audit)
	}
}

// 10. A step with read-write state access can store a value with state.set
// and read it back with state.get; the value lands in the scenario's state
// file and both calls are audited (section 7.6).
func TestAgent_StateToolsReadWrite(t *testing.T) {
	yamlText := `
version: 1
name: agent-state
steps:
  - id: review
    agent:
      engine: fake
      script: script.yaml
      prompt: work
      tools:
        state: read-write
      result: result.json
`
	script := `
calls:
  - tool: state.set
    args: { key: "last_run", value: "ok" }
  - tool: state.get
    args: { key: "last_run" }
  - tool: submit_result
    args: { verdict: "ok" }
`
	stateDir := filepath.Join(t.TempDir(), "state")
	eng, store, dir := newTestEngine(t, yamlText, func(o *Options) {
		o.StateDir = stateDir
	})
	writeAgentFiles(t, dir, agentSchema, script)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q (%v), want success", result.Status, result.Error)
	}

	stateFile := filepath.Join(stateDir, "agent-state.json")
	stateData := readFile(t, stateFile)
	var stored map[string]any
	if err := json.Unmarshal([]byte(stateData), &stored); err != nil {
		t.Fatalf("state file is not valid JSON: %v\n%s", err, stateData)
	}
	if stored["last_run"] != "ok" {
		t.Fatalf("state file last_run = %#v, want \"ok\":\n%s", stored["last_run"], stateData)
	}

	audit := readFile(t, filepath.Join(store.Dir(), "steps", "review", "tool-calls.jsonl"))
	if !strings.Contains(audit, `"tool":"state.set"`) {
		t.Errorf("state.set is not in the audit log:\n%s", audit)
	}
	if !strings.Contains(audit, `"tool":"state.get"`) {
		t.Errorf("state.get is not in the audit log:\n%s", audit)
	}
}

// 11. A step whose policy names an allowed host can fetch a page with the
// fetch tool and submit a result from what it read (section 7.6). The call
// is audited as a success, and the extracted text reaches the transcript,
// which is the one place the fake engine's tool answers are observable.
func TestAgent_FetchToolGetsPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><body><p>hello from the page</p></body></html>")
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")

	yamlText := fmt.Sprintf(`
version: 1
name: agent-fetch
steps:
  - id: review
    agent:
      engine: fake
      script: script.yaml
      prompt: work
      tools:
        fetch: { allow: [%q] }
      result: result.json
`, host)
	script := fmt.Sprintf(`
calls:
  - tool: fetch
    args: { url: %q }
  - tool: submit_result
    args: { verdict: "ok" }
`, server.URL+"/page")

	eng, store, dir := newTestEngine(t, yamlText, nil)
	writeAgentFiles(t, dir, agentSchema, script)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q (%v), want success", result.Status, result.Error)
	}

	stepDir := filepath.Join(store.Dir(), "steps", "review")
	audit := readFile(t, filepath.Join(stepDir, "tool-calls.jsonl"))
	if !strings.Contains(audit, `"tool":"fetch"`) {
		t.Fatalf("the fetch call is not in the audit log:\n%s", audit)
	}
	if !strings.Contains(audit, `"status":"ok"`) {
		t.Fatalf("the fetch call is not audited as a success:\n%s", audit)
	}

	transcript := readFile(t, filepath.Join(stepDir, "transcript.jsonl"))
	if !strings.Contains(transcript, "hello from the page") {
		t.Fatalf("the fetched page's text is not in the transcript:\n%s", transcript)
	}
}

// 12. A step whose policy names a different host than the one it tries to
// fetch never reaches the server: the fetch tool refuses the call before any
// request is made. The refusal is a policy one, so the step fails with class
// policy and the audit line says denied, whatever the agent does next
// (section 13).
func TestAgent_FetchToolDeniedHostNeverReached(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
	}))
	defer server.Close()

	yamlText := `
version: 1
name: agent-fetch-denied
steps:
  - id: review
    agent:
      engine: fake
      script: script.yaml
      prompt: work
      tools:
        fetch: { allow: ["example.com"] }
      result: result.json
`
	script := fmt.Sprintf(`
calls:
  - tool: fetch
    args: { url: %q }
    stop_on_error: true
`, server.URL)

	eng, store, dir := newTestEngine(t, yamlText, nil)
	writeAgentFiles(t, dir, agentSchema, script)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if calls != 0 {
		t.Fatalf("server calls = %d, want the host outside the allow list never reached", calls)
	}
	if result.Status != runstore.StatusFailed {
		t.Fatalf("Status = %q, want %q", result.Status, runstore.StatusFailed)
	}
	if result.Error == nil || result.Error.Class != ClassPolicy {
		t.Fatalf("error = %#v, want class %s", result.Error, ClassPolicy)
	}
	if !strings.Contains(result.Error.Message, "allow list") {
		t.Fatalf("message = %q, want it to mention the allow list", result.Error.Message)
	}

	audit := readFile(t, filepath.Join(store.Dir(), "steps", "review", "tool-calls.jsonl"))
	if !strings.Contains(audit, `"tool":"fetch"`) {
		t.Fatalf("the fetch call is not in the audit log:\n%s", audit)
	}
	if !strings.Contains(audit, `"status":"denied"`) {
		t.Fatalf("the fetch call is not audited as denied:\n%s", audit)
	}
	if !strings.Contains(audit, "allow list") {
		t.Fatalf("the audit entry does not mention the allow list refusal:\n%s", audit)
	}
}

// 13. A step with the fix profile can read the repository and commit to it
// with the git tools (section 7.2). The tools run in the prepared workspace,
// so the commit lands in the repository the run was pointed at.
func TestAgent_GitToolsReadAndCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	yamlText := `
version: 1
name: agent-git
steps:
  - id: review
    agent:
      engine: fake
      script: script.yaml
      prompt: work
      profile: fix
      result: result.json
`
	script := `
calls:
  - tool: git.status
  - tool: git.log
    args: { max_count: "1" }
  - tool: git.commit
    args: { message: "chore: agent commit" }
  - tool: submit_result
    args: { verdict: "ok" }
`
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	for _, args := range [][]string{{"add", "a.txt"}, {"commit", "-q", "-m", "first"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatalf("rewrite a.txt: %v", err)
	}

	eng, store, dir := newTestEngine(t, yamlText, func(o *Options) {
		o.Workspace = repo
	})
	writeAgentFiles(t, dir, agentSchema, script)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q (%v), want success", result.Status, result.Error)
	}

	log := exec.Command("git", "log", "--pretty=format:%s")
	log.Dir = repo
	out, err := log.CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "chore: agent commit") {
		t.Fatalf("the agent's commit is not in the history:\n%s", out)
	}

	audit := readFile(t, filepath.Join(store.Dir(), "steps", "review", "tool-calls.jsonl"))
	for _, tool := range []string{"git.status", "git.log", "git.commit"} {
		if !strings.Contains(audit, `"tool":"`+tool+`"`) {
			t.Errorf("%s is not in the audit log:\n%s", tool, audit)
		}
	}
	if strings.Contains(audit, `"status":"error"`) {
		t.Errorf("a git call was audited as an error:\n%s", audit)
	}
}

// A file tool that refuses a path outside the workspace fails the step with
// class policy, even though the agent went on and submitted a result: a step
// that reached past its boundary broke its policy whatever it did afterwards
// (section 13).
func TestAgent_FileToolEscapeIsAPolicyFailure(t *testing.T) {
	yamlText := `
version: 1
name: agent-fs-escape
steps:
  - id: review
    agent:
      engine: fake
      script: script.yaml
      prompt: work
      profile: fix
      result: result.json
`
	script := `
calls:
  - tool: fs.read
    args: { path: "../outside.txt" }
  - tool: submit_result
    args: { verdict: "ok" }
`
	parent := t.TempDir()
	if err := os.WriteFile(filepath.Join(parent, "outside.txt"), []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("write outside.txt: %v", err)
	}
	workspace := filepath.Join(parent, "repo")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("create the workspace: %v", err)
	}

	eng, store, dir := newTestEngine(t, yamlText, func(o *Options) {
		o.Workspace = workspace
	})
	writeAgentFiles(t, dir, agentSchema, script)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusFailed {
		t.Fatalf("Status = %q, want %q", result.Status, runstore.StatusFailed)
	}
	if result.Error == nil || result.Error.Class != ClassPolicy {
		t.Fatalf("error = %#v, want class %s", result.Error, ClassPolicy)
	}

	audit := readFile(t, filepath.Join(store.Dir(), "steps", "review", "tool-calls.jsonl"))
	if !strings.Contains(audit, `"status":"denied"`) {
		t.Errorf("the refused read is not audited as denied:\n%s", audit)
	}
	if strings.Contains(audit, "secret") {
		t.Errorf("the file outside the workspace was read:\n%s", audit)
	}
}

// 21. The file tools of section 7.3 reach an agent whose engine has none of
// its own, and they act on the prepared workspace: what fs.write leaves
// behind is on disk when the run ends.
func TestAgent_FileToolsReadAndWrite(t *testing.T) {
	yamlText := `
version: 1
name: agent-fs
steps:
  - id: review
    agent:
      engine: fake
      script: script.yaml
      prompt: work
      profile: fix
      result: result.json
`
	script := `
calls:
  - tool: fs.glob
    args: { pattern: "*.txt" }
  - tool: fs.grep
    args: { pattern: "needle" }
  - tool: fs.read
    args: { path: "a.txt" }
  - tool: fs.write
    args: { path: "out/note.txt", content: "written by the agent\n" }
  - tool: submit_result
    args: { verdict: "ok" }
`
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "a.txt"), []byte("a needle here\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}

	eng, store, dir := newTestEngine(t, yamlText, func(o *Options) {
		o.Workspace = workspace
	})
	writeAgentFiles(t, dir, agentSchema, script)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q (%v), want success", result.Status, result.Error)
	}

	written, err := os.ReadFile(filepath.Join(workspace, "out", "note.txt"))
	if err != nil {
		t.Fatalf("read what fs.write left: %v", err)
	}
	if string(written) != "written by the agent\n" {
		t.Errorf("note.txt = %q, want what the agent wrote", written)
	}

	audit := readFile(t, filepath.Join(store.Dir(), "steps", "review", "tool-calls.jsonl"))
	for _, tool := range []string{"fs.glob", "fs.grep", "fs.read", "fs.write"} {
		if !strings.Contains(audit, `"tool":"`+tool+`"`) {
			t.Errorf("%s is not in the audit log:\n%s", tool, audit)
		}
	}
	if strings.Contains(audit, `"status":"error"`) {
		t.Errorf("a file call was audited as an error:\n%s", audit)
	}
}
