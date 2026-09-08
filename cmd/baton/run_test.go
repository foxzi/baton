package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/exitcode"
)

// captureOutput redirects os.Stdout and os.Stderr to pipes for the duration
// of fn, and returns everything written to each as a string.
func captureOutput(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()

	origStdout, origStderr := os.Stdout, os.Stderr

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() stdout error = %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() stderr error = %v", err)
	}

	os.Stdout = outW
	os.Stderr = errW
	defer func() {
		os.Stdout = origStdout
		os.Stderr = origStderr
	}()

	outCh := make(chan string, 1)
	errCh := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		io.Copy(&buf, outR)
		outCh <- buf.String()
	}()
	go func() {
		var buf bytes.Buffer
		io.Copy(&buf, errR)
		errCh <- buf.String()
	}()

	fn()

	outW.Close()
	errW.Close()
	stdout = <-outCh
	stderr = <-errCh
	return stdout, stderr
}

// writeScenario writes text as a scenario file inside dir and returns its
// path.
func writeScenario(t *testing.T, dir, name, text string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
	return path
}

const okScenario = `
version: 1
name: ok-scenario
inputs:
  n:
    type: int
    default: 1
steps:
  - id: greet
    run:
      argv: ["echo", "hello"]
      readonly: true
      parse: text
`

// 1. A successful scenario writes runs/<run-id>/run.json and exits 0.
func TestRunCmd_Success(t *testing.T) {
	dir := t.TempDir()
	scenarioPath := writeScenario(t, dir, "s.yaml", okScenario)
	runsDir := filepath.Join(dir, "runs")

	var code int
	captureOutput(t, func() {
		code = runCmd([]string{scenarioPath, "--runs-dir", runsDir, "--run-id", "r1"})
	})

	if code != exitcode.OK {
		t.Fatalf("runCmd() = %d, want %d", code, exitcode.OK)
	}
	runJSON := filepath.Join(runsDir, "r1", "run.json")
	if _, err := os.Stat(runJSON); err != nil {
		t.Fatalf("stat %s: %v", runJSON, err)
	}
}

// 2. Flags may come before or after the scenario path.
func TestRunCmd_FlagsEitherSideOfPath(t *testing.T) {
	dir := t.TempDir()
	scenarioPath := writeScenario(t, dir, "s.yaml", okScenario)
	runsDir := filepath.Join(dir, "runs")

	var code1, code2 int
	captureOutput(t, func() {
		code1 = runCmd([]string{scenarioPath, "-i", "n=1", "--runs-dir", runsDir, "--run-id", "r1"})
	})
	if code1 != exitcode.OK {
		t.Fatalf("flags after path: runCmd() = %d, want %d", code1, exitcode.OK)
	}

	captureOutput(t, func() {
		code2 = runCmd([]string{"-i", "n=1", "--runs-dir", runsDir, "--run-id", "r2", scenarioPath})
	})
	if code2 != exitcode.OK {
		t.Fatalf("flags before path: runCmd() = %d, want %d", code2, exitcode.OK)
	}
}

// 3. A scenario that fails validation (template referencing secrets) returns
// exitcode.Config and creates no run directory.
func TestRunCmd_ValidationFailure(t *testing.T) {
	dir := t.TempDir()
	scenarioText := `
version: 1
name: bad-scenario
steps:
  - id: greet
    run:
      argv: ["echo", "{{ .secrets.tok }}"]
      readonly: true
`
	scenarioPath := writeScenario(t, dir, "s.yaml", scenarioText)
	runsDir := filepath.Join(dir, "runs")

	var code int
	_, stderr := captureOutput(t, func() {
		code = runCmd([]string{scenarioPath, "--runs-dir", runsDir, "--run-id", "r1"})
	})

	if code != exitcode.Config {
		t.Fatalf("runCmd() = %d, want %d; stderr:\n%s", code, exitcode.Config, stderr)
	}
	if _, err := os.Stat(runsDir); !os.IsNotExist(err) {
		t.Fatalf("runs dir %s exists (err=%v), want none", runsDir, err)
	}
}

// 4. A missing scenario file returns exitcode.Config.
func TestRunCmd_MissingScenarioFile(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist.yaml")

	var code int
	captureOutput(t, func() {
		code = runCmd([]string{missing})
	})
	if code != exitcode.Config {
		t.Fatalf("runCmd() = %d, want %d", code, exitcode.Config)
	}
}

// 5. Wrong number of positional arguments prints usage and returns
// exitcode.Config.
func TestRunCmd_WrongArgCount(t *testing.T) {
	dir := t.TempDir()
	scenarioPath := writeScenario(t, dir, "s.yaml", okScenario)

	cases := []struct {
		name string
		args []string
	}{
		{"zero paths", nil},
		{"two paths", []string{scenarioPath, scenarioPath}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var code int
			_, stderr := captureOutput(t, func() {
				code = runCmd(tc.args)
			})
			if code != exitcode.Config {
				t.Fatalf("runCmd(%v) = %d, want %d", tc.args, code, exitcode.Config)
			}
			if !strings.Contains(stderr, "Usage: baton run") {
				t.Fatalf("stderr = %q, want it to contain usage text", stderr)
			}
		})
	}
}

// 6. A required input that is not supplied returns exitcode.Config and
// mentions the input name on stderr.
func TestRunCmd_RequiredInputMissing(t *testing.T) {
	dir := t.TempDir()
	scenarioText := `
version: 1
name: needs-input
inputs:
  who:
    type: string
    required: true
steps:
  - id: greet
    run:
      argv: ["echo", "hi"]
      readonly: true
`
	scenarioPath := writeScenario(t, dir, "s.yaml", scenarioText)

	var code int
	_, stderr := captureOutput(t, func() {
		code = runCmd([]string{scenarioPath, "--runs-dir", filepath.Join(dir, "runs")})
	})
	if code != exitcode.Config {
		t.Fatalf("runCmd() = %d, want %d", code, exitcode.Config)
	}
	if !strings.Contains(stderr, "who") {
		t.Fatalf("stderr = %q, want it to mention input %q", stderr, "who")
	}
}

// 7. -i input coercion failure returns exitcode.Config.
func TestRunCmd_InputCoercionFailure(t *testing.T) {
	dir := t.TempDir()
	scenarioText := `
version: 1
name: int-input
inputs:
  n:
    type: int
steps:
  - id: greet
    run:
      argv: ["echo", "hi"]
      readonly: true
`
	scenarioPath := writeScenario(t, dir, "s.yaml", scenarioText)

	var code int
	captureOutput(t, func() {
		code = runCmd([]string{scenarioPath, "-i", "n=abc", "--runs-dir", filepath.Join(dir, "runs")})
	})
	if code != exitcode.Config {
		t.Fatalf("runCmd() = %d, want %d", code, exitcode.Config)
	}
}

// 8. --input-file supplies an input that reaches the command, and a
// malformed JSON input file returns exitcode.Config.
func TestRunCmd_InputFile(t *testing.T) {
	dir := t.TempDir()
	scenarioText := `
version: 1
name: file-input
inputs:
  who:
    type: string
    required: true
steps:
  - id: greet
    run:
      argv: ["echo", "{{ .inputs.who }}"]
      readonly: true
      parse: text
`
	scenarioPath := writeScenario(t, dir, "s.yaml", scenarioText)
	runsDir := filepath.Join(dir, "runs")

	inputFile := filepath.Join(dir, "inputs.json")
	if err := os.WriteFile(inputFile, []byte(`{"who": "alice"}`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	var code int
	_, stderr := captureOutput(t, func() {
		code = runCmd([]string{scenarioPath, "--input-file", inputFile, "--runs-dir", runsDir, "--run-id", "r1"})
	})
	if code != exitcode.OK {
		t.Fatalf("runCmd() = %d, want %d; stderr:\n%s", code, exitcode.OK, stderr)
	}
	stdoutLog := filepath.Join(runsDir, "r1", "steps", "greet", "stdout.log")
	data, err := os.ReadFile(stdoutLog)
	if err != nil {
		t.Fatalf("read %s: %v", stdoutLog, err)
	}
	if !strings.Contains(string(data), "alice") {
		t.Fatalf("stdout.log = %q, want it to contain %q", data, "alice")
	}

	// Malformed JSON input file.
	badFile := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(badFile, []byte(`{not json`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	var badCode int
	captureOutput(t, func() {
		badCode = runCmd([]string{scenarioPath, "--input-file", badFile, "--runs-dir", runsDir, "--run-id", "r2"})
	})
	if badCode != exitcode.Config {
		t.Fatalf("runCmd() with malformed input file = %d, want %d", badCode, exitcode.Config)
	}
}

// 9. A failing command step returns exitcode.Failure.
func TestRunCmd_StepFailure(t *testing.T) {
	dir := t.TempDir()
	scenarioText := `
version: 1
name: failing-step
steps:
  - id: boom
    run:
      argv: ["false"]
`
	scenarioPath := writeScenario(t, dir, "s.yaml", scenarioText)

	var code int
	captureOutput(t, func() {
		code = runCmd([]string{scenarioPath, "--runs-dir", filepath.Join(dir, "runs"), "--run-id", "r1"})
	})
	if code != exitcode.Failure {
		t.Fatalf("runCmd() = %d, want %d", code, exitcode.Failure)
	}
}

// 10. A false assert returns exitcode.Assert.
func TestRunCmd_AssertFailure(t *testing.T) {
	dir := t.TempDir()
	scenarioText := `
version: 1
name: assert-fails
steps:
  - id: check
    assert:
      condition: "1 == 2"
`
	scenarioPath := writeScenario(t, dir, "s.yaml", scenarioText)

	var code int
	captureOutput(t, func() {
		code = runCmd([]string{scenarioPath, "--runs-dir", filepath.Join(dir, "runs"), "--run-id", "r1"})
	})
	if code != exitcode.Assert {
		t.Fatalf("runCmd() = %d, want %d", code, exitcode.Assert)
	}
}

// 11. A missing secret returns exitcode.Config and creates no run directory.
func TestRunCmd_MissingSecret(t *testing.T) {
	t.Setenv("BATON_TEST_MISSING", "")
	os.Unsetenv("BATON_TEST_MISSING")

	dir := t.TempDir()
	scenarioText := `
version: 1
name: needs-secret
secrets:
  tok:
    from: env
    key: BATON_TEST_MISSING
steps:
  - id: greet
    run:
      argv: ["echo", "hi"]
      readonly: true
      env:
        TOKEN:
          secret: tok
`
	scenarioPath := writeScenario(t, dir, "s.yaml", scenarioText)
	runsDir := filepath.Join(dir, "runs")

	var code int
	captureOutput(t, func() {
		code = runCmd([]string{scenarioPath, "--runs-dir", runsDir, "--run-id", "r1"})
	})
	if code != exitcode.Config {
		t.Fatalf("runCmd() = %d, want %d", code, exitcode.Config)
	}
	if _, err := os.Stat(runsDir); !os.IsNotExist(err) {
		t.Fatalf("runs dir %s exists (err=%v), want none", runsDir, err)
	}
}

// 12. --dry-run prints the plan and creates no run directory.
func TestRunCmd_DryRun(t *testing.T) {
	dir := t.TempDir()
	scenarioText := `
version: 1
name: dry-run-scenario
inputs:
  who:
    type: string
    default: world
steps:
  - id: greet
    run:
      argv: ["echo", "{{ .inputs.who }}"]
      readonly: true
  - id: check
    needs: [greet]
    assert:
      condition: "steps.greet.exit_code == 0"
`
	scenarioPath := writeScenario(t, dir, "s.yaml", scenarioText)
	runsDir := filepath.Join(dir, "runs")

	var code int
	stdout, _ := captureOutput(t, func() {
		code = runCmd([]string{scenarioPath, "--dry-run", "--runs-dir", runsDir})
	})
	if code != exitcode.OK {
		t.Fatalf("runCmd() = %d, want %d", code, exitcode.OK)
	}
	if !strings.Contains(stdout, "dry-run-scenario") {
		t.Fatalf("stdout = %q, want it to mention the scenario name", stdout)
	}
	if !strings.Contains(stdout, "who") {
		t.Fatalf("stdout = %q, want it to mention input %q", stdout, "who")
	}
	for _, id := range []string{"greet", "check"} {
		if !strings.Contains(stdout, id) {
			t.Fatalf("stdout = %q, want it to mention step id %q", stdout, id)
		}
	}
	if _, err := os.Stat(runsDir); !os.IsNotExist(err) {
		t.Fatalf("runs dir %s exists (err=%v), want none", runsDir, err)
	}
}

// 13. --json prints JSONL events on stdout, the human log stays on stderr.
func TestRunCmd_JSONOutput(t *testing.T) {
	dir := t.TempDir()
	scenarioPath := writeScenario(t, dir, "s.yaml", okScenario)
	runsDir := filepath.Join(dir, "runs")

	var code int
	stdout, stderr := captureOutput(t, func() {
		code = runCmd([]string{scenarioPath, "--json", "--runs-dir", runsDir, "--run-id", "r1"})
	})
	if code != exitcode.OK {
		t.Fatalf("runCmd() = %d, want %d", code, exitcode.OK)
	}

	seenStarted, seenFinished := false, false
	scanner := bufio.NewScanner(strings.NewReader(stdout))
	lines := 0
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines++
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("stdout line %q is not valid JSON: %v", line, err)
		}
		typ, ok := event["type"].(string)
		if !ok {
			t.Fatalf("event %v has no string \"type\" field", event)
		}
		switch typ {
		case "run_started":
			seenStarted = true
		case "run_finished":
			seenFinished = true
		}
	}
	if lines == 0 {
		t.Fatalf("stdout produced no JSONL lines")
	}
	if !seenStarted {
		t.Fatalf("stdout JSONL never had a run_started event; stdout:\n%s", stdout)
	}
	if !seenFinished {
		t.Fatalf("stdout JSONL never had a run_finished event; stdout:\n%s", stdout)
	}
	if strings.Contains(stderr, "{") {
		t.Fatalf("stderr looks like it contains JSON: %q", stderr)
	}
}

// 14. BATON_RUNS_DIR is honoured when --runs-dir is absent.
func TestRunCmd_RunsDirFromEnv(t *testing.T) {
	dir := t.TempDir()
	scenarioPath := writeScenario(t, dir, "s.yaml", okScenario)
	envRunsDir := t.TempDir()
	t.Setenv("BATON_RUNS_DIR", envRunsDir)

	var code int
	captureOutput(t, func() {
		code = runCmd([]string{scenarioPath, "--run-id", "r1"})
	})
	if code != exitcode.OK {
		t.Fatalf("runCmd() = %d, want %d", code, exitcode.OK)
	}
	runJSON := filepath.Join(envRunsDir, "r1", "run.json")
	if _, err := os.Stat(runJSON); err != nil {
		t.Fatalf("stat %s: %v", runJSON, err)
	}
}

// 15. Without --runs-dir and without the environment variable, the run
// directory is created next to the scenario file.
func TestRunCmd_RunsDirDefault(t *testing.T) {
	t.Setenv("BATON_RUNS_DIR", "")
	os.Unsetenv("BATON_RUNS_DIR")

	dir := t.TempDir()
	scenarioPath := writeScenario(t, dir, "s.yaml", okScenario)

	var code int
	captureOutput(t, func() {
		code = runCmd([]string{scenarioPath, "--run-id", "r1"})
	})
	if code != exitcode.OK {
		t.Fatalf("runCmd() = %d, want %d", code, exitcode.OK)
	}
	runJSON := filepath.Join(dir, "runs", "r1", "run.json")
	if _, err := os.Stat(runJSON); err != nil {
		t.Fatalf("stat %s: %v", runJSON, err)
	}
}

// readStepOutput decodes runs/<id>/steps/<step>/output.json.
func readStepOutput(t *testing.T, runsDir, runID, step string) map[string]any {
	t.Helper()
	path := filepath.Join(runsDir, runID, "steps", step, "output.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal(%s) error = %v", path, err)
	}
	return out
}

// 16. A repeated run replays the readonly step from the cache, which by
// default lives next to the runs directory.
func TestRunCmd_CacheReplay(t *testing.T) {
	dir := t.TempDir()
	scenarioPath := writeScenario(t, dir, "s.yaml", okScenario)
	runsDir := filepath.Join(dir, "runs")

	for _, runID := range []string{"r1", "r2"} {
		var code int
		captureOutput(t, func() {
			code = runCmd([]string{scenarioPath, "--runs-dir", runsDir, "--run-id", runID})
		})
		if code != exitcode.OK {
			t.Fatalf("run %s: runCmd() = %d, want %d", runID, code, exitcode.OK)
		}
	}

	if _, err := os.Stat(filepath.Join(dir, "cache")); err != nil {
		t.Errorf("default cache directory: %v", err)
	}
	if cached := readStepOutput(t, runsDir, "r1", "greet")["cached"]; cached == true {
		t.Errorf("first run: cached = %v, want no replay", cached)
	}
	second := readStepOutput(t, runsDir, "r2", "greet")
	if second["cached"] != true {
		t.Errorf("second run: cached = %v, want true", second["cached"])
	}
	if result, _ := second["result"].(string); !strings.Contains(result, "hello") {
		t.Errorf("replayed result = %q, want it to contain hello", result)
	}
	stdout, err := os.ReadFile(filepath.Join(runsDir, "r2", "steps", "greet", "stdout.log"))
	if err != nil || !strings.Contains(string(stdout), "hello") {
		t.Errorf("replayed stdout.log = %q, err = %v", stdout, err)
	}
}

// 17. --no-cache ignores an existing entry but still stores the fresh result.
func TestRunCmd_NoCache(t *testing.T) {
	dir := t.TempDir()
	scenarioPath := writeScenario(t, dir, "s.yaml", okScenario)
	runsDir := filepath.Join(dir, "runs")
	cacheDir := filepath.Join(dir, "c")

	run := func(runID string, args ...string) {
		t.Helper()
		argv := append([]string{scenarioPath, "--runs-dir", runsDir, "--cache-dir", cacheDir, "--run-id", runID}, args...)
		var code int
		captureOutput(t, func() { code = runCmd(argv) })
		if code != exitcode.OK {
			t.Fatalf("run %s: runCmd() = %d, want %d", runID, code, exitcode.OK)
		}
	}

	run("r1")
	run("r2", "--no-cache")
	run("r3")

	if cached := readStepOutput(t, runsDir, "r2", "greet")["cached"]; cached == true {
		t.Errorf("--no-cache run: cached = %v, want no replay", cached)
	}
	if cached := readStepOutput(t, runsDir, "r3", "greet")["cached"]; cached != true {
		t.Errorf("run after --no-cache: cached = %v, want true", cached)
	}
}
