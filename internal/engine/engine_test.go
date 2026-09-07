package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/foxzi/baton/internal/runstore"
	"github.com/foxzi/baton/internal/scenario"
	"github.com/foxzi/baton/internal/secrets"
)

// newTestEngine parses yamlText as a scenario file inside a fresh temp
// directory, creates a run store next to it and builds an Engine. mutate, if
// given, may set Inputs, Secrets, Observer and Now before the store and
// engine are built; when it sets Secrets, the store is created with that
// store's redactor, mirroring how the CLI wires the two together.
func newTestEngine(t *testing.T, yamlText string, mutate func(*Options)) (*Engine, *runstore.Store, string) {
	t.Helper()
	dir := t.TempDir()
	scenarioPath := filepath.Join(dir, "s.yaml")
	if err := os.WriteFile(scenarioPath, []byte(yamlText), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}
	scn, err := scenario.Parse([]byte(yamlText), scenarioPath)
	if err != nil {
		t.Fatalf("scenario.Parse: %v", err)
	}

	opts := Options{Scenario: scn}
	if mutate != nil {
		mutate(&opts)
	}

	var redactor *secrets.Redactor
	if opts.Secrets != nil {
		redactor = opts.Secrets.Redactor()
	}

	store, err := runstore.Create(filepath.Join(dir, "runs"), "test-run", redactor)
	if err != nil {
		t.Fatalf("runstore.Create: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	opts.Store = store

	eng, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return eng, store, dir
}

// readRunState reads and decodes run.json from a run directory (store.Dir()).
func readRunState(t *testing.T, runDir string) *runstore.RunState {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(runDir, "run.json"))
	if err != nil {
		t.Fatalf("read run.json: %v", err)
	}
	var state runstore.RunState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("run.json is not valid JSON: %v\n%s", err, data)
	}
	return &state
}

// readFile reads a file and fails the test if it cannot.
func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// readStepJSON reads and decodes a step JSON file (output.json, input.json)
// as a generic map.
func readStepJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(readFile(t, path)), &v); err != nil {
		t.Fatalf("%s is not valid JSON: %v", path, err)
	}
	return v
}

// 1. A successful two-step run.
func TestRun_TwoStepsSuccess(t *testing.T) {
	yamlText := `
version: 1
name: two-steps
steps:
  - id: one
    run:
      argv: ["echo", "hello"]
      parse: text
  - id: two
    run:
      argv: ["printf", "a\nb\nc\n"]
      parse: lines
`
	eng, store, _ := newTestEngine(t, yamlText, nil)
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want %q", result.Status, runstore.StatusSuccess)
	}
	if code := result.ExitCode(); code != 0 {
		t.Fatalf("ExitCode = %d, want 0", code)
	}

	out := readStepJSON(t, filepath.Join(store.Dir(), "steps", "two", "output.json"))
	items, ok := out["result"].([]any)
	if !ok {
		t.Fatalf("result is %T, want []any: %#v", out["result"], out["result"])
	}
	want := []any{"a", "b", "c"}
	if len(items) != len(want) {
		t.Fatalf("result = %#v, want %#v", items, want)
	}
	for i, v := range want {
		if items[i] != v {
			t.Fatalf("result[%d] = %#v, want %#v", i, items[i], v)
		}
	}
}

// 2. run.json content of a successful run.
func TestRun_RunJSONContent(t *testing.T) {
	yamlText := `
version: 1
name: demo-run
steps:
  - id: one
    run:
      argv: ["echo", "hi"]
  - id: two
    run:
      argv: ["true"]
`
	eng, store, _ := newTestEngine(t, yamlText, nil)
	if _, err := eng.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	state := readRunState(t, store.Dir())
	if state.SchemaVersion != runstore.SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", state.SchemaVersion, runstore.SchemaVersion)
	}
	if state.ID != "test-run" {
		t.Errorf("ID = %q, want test-run", state.ID)
	}
	if state.Name != "demo-run" {
		t.Errorf("Name = %q, want demo-run", state.Name)
	}
	if state.Status != runstore.StatusSuccess {
		t.Errorf("Status = %q, want %q", state.Status, runstore.StatusSuccess)
	}
	if state.FinishedAt == nil {
		t.Fatal("FinishedAt is nil")
	}
	for _, id := range []string{"one", "two"} {
		step, ok := state.Steps[id]
		if !ok {
			t.Fatalf("steps[%s] missing", id)
		}
		if step.Status != runstore.StatusSuccess {
			t.Errorf("steps[%s].Status = %q, want %q", id, step.Status, runstore.StatusSuccess)
		}
		if step.ExitCode == nil || *step.ExitCode != 0 {
			t.Errorf("steps[%s].ExitCode = %v, want 0", id, step.ExitCode)
		}
	}
}

// 3. Templates in argv, including a value with spaces staying one argument.
func TestRun_ArgvTemplates(t *testing.T) {
	yamlText := `
version: 1
name: argv-templates
inputs:
  who:
    type: string
  spaced:
    type: string
steps:
  - id: greet
    run:
      argv: ["echo", "{{ .inputs.who }}"]
  - id: spaced
    run:
      argv: ["printf", "%s|", "{{ .inputs.spaced }}"]
`
	eng, store, _ := newTestEngine(t, yamlText, func(o *Options) {
		o.Inputs = map[string]any{"who": "world", "spaced": "a b"}
	})
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want success", result.Status)
	}

	greet := readFile(t, filepath.Join(store.Dir(), "steps", "greet", "stdout.log"))
	if greet != "world\n" {
		t.Errorf("greet stdout = %q, want %q", greet, "world\n")
	}

	spaced := readFile(t, filepath.Join(store.Dir(), "steps", "spaced", "stdout.log"))
	if spaced != "a b|" {
		t.Errorf("spaced stdout = %q, want %q", spaced, "a b|")
	}
}

// 4. Step results feeding the next step through .steps.<id>.stdout.
func TestRun_StepResultFeedsNextStep(t *testing.T) {
	yamlText := `
version: 1
name: chain
steps:
  - id: one
    run:
      argv: ["echo", "hello-from-one"]
  - id: two
    run:
      argv: ["cat"]
      stdin: "{{ .steps.one.stdout }}"
`
	eng, store, _ := newTestEngine(t, yamlText, nil)
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want success", result.Status)
	}

	two := readFile(t, filepath.Join(store.Dir(), "steps", "two", "stdout.log"))
	if !strings.Contains(two, "hello-from-one") {
		t.Errorf("step two stdout = %q, want it to contain %q", two, "hello-from-one")
	}
}

// 5. when: false skips the step without executing anything.
func TestRun_WhenFalseSkipsStep(t *testing.T) {
	yamlText := `
version: 1
name: skip
steps:
  - id: skip-me
    when: "false"
    run:
      argv: ["echo", "should not run"]
`
	eng, store, _ := newTestEngine(t, yamlText, nil)
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want success", result.Status)
	}

	state := readRunState(t, store.Dir())
	step, ok := state.Steps["skip-me"]
	if !ok {
		t.Fatal("steps[skip-me] missing from run.json")
	}
	if step.Status != runstore.StatusSkipped {
		t.Errorf("Status = %q, want %q", step.Status, runstore.StatusSkipped)
	}

	if _, err := os.Stat(filepath.Join(store.Dir(), "steps", "skip-me", "stdout.log")); !os.IsNotExist(err) {
		t.Errorf("stdout.log exists for a skipped step (err=%v)", err)
	}
}

// 6. A failing command with the default on_error stops the run.
func TestRun_FailingCommandStopsRun(t *testing.T) {
	yamlText := `
version: 1
name: fail-default
steps:
  - id: boom
    run:
      argv: ["false"]
  - id: never
    run:
      argv: ["echo", "never"]
`
	eng, store, _ := newTestEngine(t, yamlText, nil)
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusFailed {
		t.Fatalf("Status = %q, want failed", result.Status)
	}
	if code := result.ExitCode(); code != 1 {
		t.Errorf("ExitCode = %d, want 1", code)
	}

	state := readRunState(t, store.Dir())
	if state.FailedStep != "boom" {
		t.Errorf("FailedStep = %q, want boom", state.FailedStep)
	}
	if state.Error == nil || state.Error.Class != ClassCommand {
		t.Errorf("Error = %+v, want class %q", state.Error, ClassCommand)
	}
	if _, ok := state.Steps["never"]; ok {
		t.Error("steps[never] present in run.json, want absent: step after a failing default-on_error step ran")
	}
}

// 7. on_error: continue lets the run proceed past a failed step.
func TestRun_OnErrorContinue(t *testing.T) {
	yamlText := `
version: 1
name: continue
steps:
  - id: boom
    on_error: continue
    run:
      argv: ["false"]
  - id: after
    run:
      argv: ["echo", "ok"]
`
	eng, store, _ := newTestEngine(t, yamlText, nil)
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want success", result.Status)
	}

	state := readRunState(t, store.Dir())
	if state.Steps["boom"].Status != runstore.StatusFailed {
		t.Errorf("boom.Status = %q, want failed", state.Steps["boom"].Status)
	}
	if state.Steps["after"] == nil || state.Steps["after"].Status != runstore.StatusSuccess {
		t.Errorf("after step did not run successfully: %+v", state.Steps["after"])
	}
}

// 8. on_error: fallback recovers a failed step.
func TestRun_OnErrorFallback(t *testing.T) {
	yamlText := `
version: 1
name: fallback
steps:
  - id: boom
    on_error: fallback
    run:
      argv: ["false"]
    fallback:
      run:
        argv: ["echo", "fallback-ran"]
`
	eng, store, _ := newTestEngine(t, yamlText, nil)
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want success", result.Status)
	}

	state := readRunState(t, store.Dir())
	step := state.Steps["boom"]
	if step == nil {
		t.Fatal("steps[boom] missing")
	}
	if !step.FallbackUsed {
		t.Error("FallbackUsed = false, want true")
	}
	if step.Status != runstore.StatusSuccess {
		t.Errorf("Status = %q, want success", step.Status)
	}
}

// 9. allow_exit_codes accepts a non-zero exit code.
func TestRun_AllowExitCodes(t *testing.T) {
	yamlText := `
version: 1
name: allow-exit
steps:
  - id: three
    run:
      argv: ["sh", "-c", "exit 3"]
      allow_exit_codes: [0, 3]
`
	eng, store, _ := newTestEngine(t, yamlText, nil)
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want success", result.Status)
	}

	state := readRunState(t, store.Dir())
	step := state.Steps["three"]
	if step == nil || step.ExitCode == nil || *step.ExitCode != 3 {
		t.Errorf("step three exit code = %v, want 3", step)
	}
}

// 10. parse: json decodes valid JSON and rejects invalid JSON with the
// schema class.
func TestRun_ParseJSON(t *testing.T) {
	yamlText := `
version: 1
name: parse-json
steps:
  - id: good
    run:
      argv: ["printf", "{\"a\":1}"]
      parse: json
  - id: bad
    run:
      argv: ["echo", "not json"]
      parse: json
`
	eng, store, _ := newTestEngine(t, yamlText, nil)
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusFailed {
		t.Fatalf("Status = %q, want failed", result.Status)
	}
	if code := result.ExitCode(); code != 1 {
		t.Errorf("ExitCode = %d, want 1", code)
	}

	out := readStepJSON(t, filepath.Join(store.Dir(), "steps", "good", "output.json"))
	m, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("good result is %T, want map[string]any: %#v", out["result"], out["result"])
	}
	if m["a"] != float64(1) {
		t.Errorf("good result[a] = %#v, want 1", m["a"])
	}

	state := readRunState(t, store.Dir())
	if state.Error == nil || state.Error.Class != ClassSchema {
		t.Errorf("Error = %+v, want class %q", state.Error, ClassSchema)
	}
}

// 11. timeout stops a long-running command quickly.
func TestRun_Timeout(t *testing.T) {
	yamlText := `
version: 1
name: timeout
steps:
  - id: slow
    timeout: 100ms
    run:
      argv: ["sleep", "5"]
`
	eng, store, _ := newTestEngine(t, yamlText, nil)

	started := time.Now()
	result, err := eng.Run(context.Background())
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Run took %s, want under 2s", elapsed)
	}
	if result.Status != runstore.StatusFailed {
		t.Fatalf("Status = %q, want failed", result.Status)
	}

	state := readRunState(t, store.Dir())
	if state.Error == nil || state.Error.Class != ClassTimeout {
		t.Errorf("Error = %+v, want class %q", state.Error, ClassTimeout)
	}
}

// 12. A false assert stops the run without executing on_failure.
func TestRun_AssertTrueSucceeds(t *testing.T) {
	yamlText := `
version: 1
name: assert-true
steps:
  - id: check
    assert:
      condition: "1 == 1"
`
	eng, store, _ := newTestEngine(t, yamlText, nil)
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want success", result.Status)
	}
	_ = store
}

func TestRun_AssertFalseSkipsOnFailure(t *testing.T) {
	markerDir := t.TempDir()
	markerFile := filepath.Join(markerDir, "marker.txt")

	yamlText := fmt.Sprintf(`
version: 1
name: assert-false
steps:
  - id: check
    assert:
      condition: "1 == 2"
on_failure:
  - id: notify
    run:
      argv: ["sh", "-c", "echo bad > %s"]
`, markerFile)

	eng, store, _ := newTestEngine(t, yamlText, nil)
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusFailed {
		t.Fatalf("Status = %q, want failed", result.Status)
	}
	if code := result.ExitCode(); code != 2 {
		t.Errorf("ExitCode = %d, want 2", code)
	}

	state := readRunState(t, store.Dir())
	if state.Error == nil || state.Error.Class != ClassAssert {
		t.Errorf("Error = %+v, want class %q", state.Error, ClassAssert)
	}

	if _, err := os.Stat(markerFile); !os.IsNotExist(err) {
		t.Errorf("on_failure ran after a false assert: marker file err=%v", err)
	}
}

// 13. on_failure runs for a command failure and sees run.failed_step and
// run.error.class.
func TestRun_OnFailureRunsForCommandFailure(t *testing.T) {
	yamlText := `
version: 1
name: on-failure-runs
steps:
  - id: boom
    run:
      argv: ["false"]
on_failure:
  - id: notify
    run:
      argv: ["sh", "-c", "echo {{ .run.failed_step }} {{ .run.error.class }} > result.txt"]
`
	eng, store, dir := newTestEngine(t, yamlText, nil)
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusFailed {
		t.Fatalf("Status = %q, want failed", result.Status)
	}

	content := readFile(t, filepath.Join(dir, "result.txt"))
	if !strings.Contains(content, "boom") {
		t.Errorf("result.txt = %q, want it to contain the failed step id %q", content, "boom")
	}
	if !strings.Contains(content, ClassCommand) {
		t.Errorf("result.txt = %q, want it to contain the error class %q", content, ClassCommand)
	}

	stdout := readFile(t, filepath.Join(store.Dir(), "steps", "on_failure", "notify", "stdout.log"))
	_ = stdout // on_failure output lands under steps/on_failure/<id>/, existence checked by readFile above.
}

// 14. max_output_bytes truncates a large output and marks the truncation.
func TestRun_MaxOutputBytesTruncates(t *testing.T) {
	produced := strings.Repeat("A", 2000)
	yamlText := fmt.Sprintf(`
version: 1
name: truncate
steps:
  - id: big
    run:
      argv: ["printf", %q]
      max_output_bytes: 16
`, produced)

	eng, store, _ := newTestEngine(t, yamlText, nil)
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want success", result.Status)
	}

	stdout := readFile(t, filepath.Join(store.Dir(), "steps", "big", "stdout.log"))
	if !strings.HasPrefix(stdout, strings.Repeat("A", 16)) {
		t.Errorf("stdout.log does not start with the first 16 bytes: %q", stdout)
	}
	if !strings.Contains(stdout, "[baton: truncated") {
		t.Errorf("stdout.log missing truncation marker: %q", stdout)
	}

	outputPath := filepath.Join(store.Dir(), "steps", "big", "output.json")
	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("stat output.json: %v", err)
	}
	if info.Size() >= int64(len(produced)) {
		t.Errorf("output.json size = %d, want well under %d (the produced size)", info.Size(), len(produced))
	}
}

// 15. Secrets reach the child process's environment but never the run
// directory.
func TestRun_SecretsReachChildNotDisk(t *testing.T) {
	const plaintext = "unlikely-plaintext-42"
	t.Setenv("BATON_TEST_TOKEN", plaintext)

	yamlText := `
version: 1
name: secret-env
secrets:
  tok:
    from: env
    key: BATON_TEST_TOKEN
steps:
  - id: check
    run:
      argv: ["sh", "-c", 'test "$TOK" = ` + plaintext + ` && echo match']
      env:
        TOK:
          secret: tok
`
	var secretStore *secrets.Store
	eng, store, _ := newTestEngine(t, yamlText, func(o *Options) {
		s, err := secrets.Resolve(o.Scenario.Secrets, filepath.Dir(o.Scenario.Path))
		if err != nil {
			t.Fatalf("secrets.Resolve: %v", err)
		}
		secretStore = s
		o.Secrets = s
	})
	_ = secretStore

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want success", result.Status)
	}

	stdout := readFile(t, filepath.Join(store.Dir(), "steps", "check", "stdout.log"))
	if !strings.Contains(stdout, "match") {
		t.Fatalf("child did not see the secret value: stdout = %q", stdout)
	}

	input := readFile(t, filepath.Join(store.Dir(), "steps", "check", "input.json"))
	if strings.Contains(input, plaintext) {
		t.Errorf("input.json contains the secret plaintext: %s", input)
	}
	if !strings.Contains(input, "TOK") {
		t.Errorf("input.json does not mention the env var name: %s", input)
	}

	// Walk the entire run directory: nothing may contain the plaintext.
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

// 16. An unknown secret name in run.env fails with the config class.
func TestRun_UnknownSecretFailsConfig(t *testing.T) {
	yamlText := `
version: 1
name: unknown-secret
steps:
  - id: check
    run:
      argv: ["true"]
      env:
        X:
          secret: missing
`
	eng, _, _ := newTestEngine(t, yamlText, nil)
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusFailed {
		t.Fatalf("Status = %q, want failed", result.Status)
	}
	if code := result.ExitCode(); code != 3 {
		t.Errorf("ExitCode = %d, want 3", code)
	}
	if result.Error == nil || result.Error.Class != ClassConfig {
		t.Errorf("Error = %+v, want class %q", result.Error, ClassConfig)
	}
}

// 17. A step kind the runner does not execute yet fails with the config
// class.
func TestRun_UnimplementedKindFailsConfig(t *testing.T) {
	yamlText := `
version: 1
name: unimplemented
steps:
  - id: think
    llm:
      prompt: "hello"
`
	eng, _, _ := newTestEngine(t, yamlText, nil)
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusFailed {
		t.Fatalf("Status = %q, want failed", result.Status)
	}
	if code := result.ExitCode(); code != 3 {
		t.Errorf("ExitCode = %d, want 3", code)
	}
	if result.Error == nil || result.Error.Class != ClassConfig {
		t.Errorf("Error = %+v, want class %q", result.Error, ClassConfig)
	}
}

// 18. events.jsonl and the Observer callback both see the run's events.
func TestRun_Events(t *testing.T) {
	yamlText := `
version: 1
name: events
steps:
  - id: one
    run:
      argv: ["echo", "hi"]
`
	var (
		mu     sync.Mutex
		gotObs []string
	)
	eng, store, _ := newTestEngine(t, yamlText, func(o *Options) {
		o.Observer = func(e Event) {
			mu.Lock()
			defer mu.Unlock()
			gotObs = append(gotObs, e.Type)
		}
	})
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want success", result.Status)
	}

	data := readFile(t, filepath.Join(store.Dir(), "events.jsonl"))
	lines := strings.Split(strings.TrimRight(data, "\n"), "\n")
	var gotFile []string
	for _, line := range lines {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("line %q is not valid JSON: %v", line, err)
		}
		if _, ok := decoded["ts"]; !ok {
			t.Errorf("line %q missing ts", line)
		}
		typ, ok := decoded["type"].(string)
		if !ok {
			t.Fatalf("line %q missing type", line)
		}
		gotFile = append(gotFile, typ)
	}

	for _, want := range []string{"run_started", "step_started", "step_finished", "run_finished"} {
		if !containsClass(gotFile, want) {
			t.Errorf("events.jsonl types = %v, want it to contain %q", gotFile, want)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(gotObs) != len(gotFile) {
		t.Fatalf("observer saw %v, events.jsonl saw %v", gotObs, gotFile)
	}
	for i := range gotObs {
		if gotObs[i] != gotFile[i] {
			t.Errorf("event %d: observer=%q file=%q, want equal", i, gotObs[i], gotFile[i])
		}
	}
}

// 19. Retry attempts a readonly step several times but a step with side
// effects only once.
func TestRun_Retry(t *testing.T) {
	t.Run("readonly is retried", func(t *testing.T) {
		yamlText := `
version: 1
name: retry-readonly
steps:
  - id: boom
    retry:
      on: [command]
      attempts: 2
      backoff: 1ms
    run:
      argv: ["false"]
      readonly: true
`
		eng, store, _ := newTestEngine(t, yamlText, nil)
		result, err := eng.Run(context.Background())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if result.Status != runstore.StatusFailed {
			t.Fatalf("Status = %q, want failed", result.Status)
		}

		state := readRunState(t, store.Dir())
		step := state.Steps["boom"]
		if step == nil {
			t.Fatal("steps[boom] missing")
		}
		if step.Attempts != 3 {
			t.Errorf("Attempts = %d, want 3", step.Attempts)
		}
	})

	t.Run("non-readonly is not retried", func(t *testing.T) {
		yamlText := `
version: 1
name: retry-not-readonly
steps:
  - id: boom
    retry:
      on: [command]
      attempts: 2
      backoff: 1ms
    run:
      argv: ["false"]
`
		eng, store, _ := newTestEngine(t, yamlText, nil)
		result, err := eng.Run(context.Background())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if result.Status != runstore.StatusFailed {
			t.Fatalf("Status = %q, want failed", result.Status)
		}

		state := readRunState(t, store.Dir())
		step := state.Steps["boom"]
		if step == nil {
			t.Fatal("steps[boom] missing")
		}
		if step.Attempts != 1 {
			t.Errorf("Attempts = %d, want 1 (a step with side effects must not be retried without dedupe_key)", step.Attempts)
		}
	})
}

// 20. New rejects a nil scenario and a nil store.
func TestNew_RejectsMissingRequiredFields(t *testing.T) {
	dir := t.TempDir()
	store, err := runstore.Create(filepath.Join(dir, "runs"), "test-run", nil)
	if err != nil {
		t.Fatalf("runstore.Create: %v", err)
	}
	defer store.Close()

	if _, err := New(Options{Scenario: nil, Store: store}); err == nil {
		t.Error("New with a nil scenario succeeded, want error")
	}

	scn, err := scenario.Parse([]byte("version: 1\nname: x\nsteps: []\n"), filepath.Join(dir, "s.yaml"))
	if err != nil {
		t.Fatalf("scenario.Parse: %v", err)
	}
	if _, err := New(Options{Scenario: scn, Store: nil}); err == nil {
		t.Error("New with a nil store succeeded, want error")
	}
}
