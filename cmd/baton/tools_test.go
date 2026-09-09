package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/engine"
	"github.com/foxzi/baton/internal/exitcode"
)

// toolsScenario is an agent step backed by the fake engine, with one command
// so the listing has more than just the fs/git/state tools and submit_result.
const toolsScenario = `
version: 1
name: inspect-tools
commands:
  hello:
    argv: ["echo", "hi"]
    description: Say hi
    readonly: true
    max_calls: 2
steps:
  - id: review
    run: "echo unrelated"
  - id: build
    agent:
      engine: fake
      script: script.yaml
      prompt: work
      profile: fix
      result: result.json
`

// writeToolsScenario writes toolsScenario plus the result schema and fake
// behaviour script the "build" step needs next to it, and returns its path.
func writeToolsScenario(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "s.yaml")
	if err := os.WriteFile(path, []byte(toolsScenario), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "result.json"), []byte(`{"type":"object","required":["verdict"],"properties":{"verdict":{"type":"string"}}}`), 0o644); err != nil {
		t.Fatalf("write result schema: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "script.yaml"), []byte("calls: []"), 0o644); err != nil {
		t.Fatalf("write fake script: %v", err)
	}
	return path
}

// 1. The human readable listing names every tool the step would get, plus
// its description and call cap, plus submit_result last.
func TestToolsCmdHumanReadable(t *testing.T) {
	dir := t.TempDir()
	path := writeToolsScenario(t, dir)

	var code int
	stdout, stderr := captureOutput(t, func() {
		code = toolsCmd([]string{path, "--step", "build"})
	})
	if code != exitcode.OK {
		t.Fatalf("exit code = %d, want %d, stderr: %s", code, exitcode.OK, stderr)
	}
	if !strings.Contains(stdout, "step build:") {
		t.Fatalf("stdout missing the step header: %q", stdout)
	}
	if !strings.Contains(stdout, "hello") || !strings.Contains(stdout, "Say hi") {
		t.Errorf("stdout missing the hello command: %q", stdout)
	}
	if !strings.Contains(stdout, "(up to 2 calls)") {
		t.Errorf("stdout missing hello's call cap: %q", stdout)
	}
	if !strings.Contains(stdout, "submit_result") {
		t.Errorf("stdout missing submit_result: %q", stdout)
	}
	// submit_result is the last tool the gateway adds, so it must come
	// after every other tool name in the listing.
	if strings.LastIndex(stdout, "hello") > strings.LastIndex(stdout, "submit_result") {
		t.Errorf("submit_result is not last: %q", stdout)
	}
}

// 2. --json prints the same tool set as parseable JSON instead of the human
// readable listing, which is what indentJSON exists to avoid doing there.
func TestToolsCmdJSON(t *testing.T) {
	dir := t.TempDir()
	path := writeToolsScenario(t, dir)

	var code int
	stdout, stderr := captureOutput(t, func() {
		code = toolsCmd([]string{path, "--step", "build", "--json"})
	})
	if code != exitcode.OK {
		t.Fatalf("exit code = %d, want %d, stderr: %s", code, exitcode.OK, stderr)
	}
	var list []engine.ToolInfo
	if err := json.Unmarshal([]byte(stdout), &list); err != nil {
		t.Fatalf("json.Unmarshal(%q): %v", stdout, err)
	}
	names := make([]string, len(list))
	for i, tool := range list {
		names[i] = tool.Name
	}
	if !strings.Contains(strings.Join(names, ","), "hello") {
		t.Errorf("names = %v, want hello among them", names)
	}
	if names[len(names)-1] != "submit_result" {
		t.Errorf("last tool = %q, want submit_result", names[len(names)-1])
	}
	// The human readable header line must not leak into JSON mode.
	if strings.Contains(stdout, "step build:") {
		t.Errorf("stdout has the human header in JSON mode: %q", stdout)
	}
}

// 3. No --step is a usage error, not a panic on an empty flag.
func TestToolsCmdMissingStepFlag(t *testing.T) {
	dir := t.TempDir()
	path := writeToolsScenario(t, dir)

	var code int
	_, stderr := captureOutput(t, func() {
		code = toolsCmd([]string{path})
	})
	if code != exitcode.Config {
		t.Fatalf("exit code = %d, want %d", code, exitcode.Config)
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Fatalf("stderr does not report usage: %q", stderr)
	}
}

// 4. More than one positional argument is rejected the same way as none.
func TestToolsCmdTooManyPositionalArgs(t *testing.T) {
	dir := t.TempDir()
	path := writeToolsScenario(t, dir)

	var code int
	_, stderr := captureOutput(t, func() {
		code = toolsCmd([]string{path, path, "--step", "build"})
	})
	if code != exitcode.Config {
		t.Fatalf("exit code = %d, want %d", code, exitcode.Config)
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Fatalf("stderr does not report usage: %q", stderr)
	}
}

// 5. A scenario file that is not there is a configuration error, reported
// with the underlying load error rather than a stack trace.
func TestToolsCmdMissingScenarioFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.yaml")

	var code int
	_, stderr := captureOutput(t, func() {
		code = toolsCmd([]string{path, "--step", "build"})
	})
	if code != exitcode.Config {
		t.Fatalf("exit code = %d, want %d", code, exitcode.Config)
	}
	if !strings.Contains(stderr, "baton:") {
		t.Fatalf("stderr does not report the load error: %q", stderr)
	}
}

// 6. A scenario that parses but fails validation (here: a bad version) is
// rejected before any step is inspected, with every validation error named.
func TestToolsCmdInvalidScenario(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.yaml")
	invalid := `
version: 2
name: bad-version
steps:
  - id: build
    run: "echo hi"
`
	if err := os.WriteFile(path, []byte(invalid), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}

	var code int
	_, stderr := captureOutput(t, func() {
		code = toolsCmd([]string{path, "--step", "build"})
	})
	if code != exitcode.Config {
		t.Fatalf("exit code = %d, want %d", code, exitcode.Config)
	}
	if !strings.Contains(stderr, "error:") {
		t.Fatalf("stderr does not list the validation error: %q", stderr)
	}
}

// 7. A step id the scenario does not have surfaces the engine's error, not a
// silent empty listing.
func TestToolsCmdUnknownStep(t *testing.T) {
	dir := t.TempDir()
	path := writeToolsScenario(t, dir)

	var code int
	_, stderr := captureOutput(t, func() {
		code = toolsCmd([]string{path, "--step", "nope"})
	})
	if code != exitcode.Config {
		t.Fatalf("exit code = %d, want %d", code, exitcode.Config)
	}
	if !strings.Contains(stderr, "nope") {
		t.Fatalf("stderr does not name the unknown step: %q", stderr)
	}
}

// 8. A step that is not an agent step has no tools to list.
func TestToolsCmdStepWithoutAgent(t *testing.T) {
	dir := t.TempDir()
	path := writeToolsScenario(t, dir)

	var code int
	_, stderr := captureOutput(t, func() {
		code = toolsCmd([]string{path, "--step", "review"})
	})
	if code != exitcode.Config {
		t.Fatalf("exit code = %d, want %d", code, exitcode.Config)
	}
	if !strings.Contains(stderr, "only an agent step has tools") {
		t.Fatalf("stderr does not explain why: %q", stderr)
	}
}

// 9. A JSON input file that does not parse is a configuration error, named
// with the file path.
func TestToolsCmdUnreadableInputFile(t *testing.T) {
	dir := t.TempDir()
	path := writeToolsScenario(t, dir)
	inputFile := filepath.Join(dir, "inputs.json")
	if err := os.WriteFile(inputFile, []byte("not json"), 0o644); err != nil {
		t.Fatalf("write input file: %v", err)
	}

	var code int
	_, stderr := captureOutput(t, func() {
		code = toolsCmd([]string{path, "--step", "build", "--input-file", inputFile})
	})
	if code != exitcode.Config {
		t.Fatalf("exit code = %d, want %d", code, exitcode.Config)
	}
	if !strings.Contains(stderr, inputFile) {
		t.Fatalf("stderr does not name the input file: %q", stderr)
	}
}

// 10. A required input the caller never supplies is rejected before the
// engine is even built, same as `baton run` would.
func TestToolsCmdMissingRequiredInput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.yaml")
	scn := `
version: 1
name: inspect-required-input
inputs:
  thing:
    type: string
    required: true
steps:
  - id: build
    agent:
      engine: fake
      script: script.yaml
      prompt: "work on {{ .thing }}"
      result: result.json
`
	if err := os.WriteFile(path, []byte(scn), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "result.json"), []byte(`{"type":"object"}`), 0o644); err != nil {
		t.Fatalf("write result schema: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "script.yaml"), []byte("calls: []"), 0o644); err != nil {
		t.Fatalf("write fake script: %v", err)
	}

	var code int
	_, stderr := captureOutput(t, func() {
		code = toolsCmd([]string{path, "--step", "build"})
	})
	if code != exitcode.Config {
		t.Fatalf("exit code = %d, want %d", code, exitcode.Config)
	}
	if !strings.Contains(stderr, "thing") {
		t.Fatalf("stderr does not name the missing input: %q", stderr)
	}
}

// 11. -i supplies the required input above, and the listing succeeds: the
// two paths share the same bound-inputs plumbing.
func TestToolsCmdRequiredInputSupplied(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.yaml")
	scn := `
version: 1
name: inspect-required-input
inputs:
  thing:
    type: string
    required: true
steps:
  - id: build
    agent:
      engine: fake
      script: script.yaml
      prompt: "work on {{ .thing }}"
      result: result.json
`
	if err := os.WriteFile(path, []byte(scn), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "result.json"), []byte(`{"type":"object"}`), 0o644); err != nil {
		t.Fatalf("write result schema: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "script.yaml"), []byte("calls: []"), 0o644); err != nil {
		t.Fatalf("write fake script: %v", err)
	}

	var code int
	stdout, stderr := captureOutput(t, func() {
		code = toolsCmd([]string{path, "--step", "build", "-i", "thing=widgets"})
	})
	if code != exitcode.OK {
		t.Fatalf("exit code = %d, want %d, stderr: %s", code, exitcode.OK, stderr)
	}
	if !strings.Contains(stdout, "step build:") {
		t.Fatalf("stdout missing the step header: %q", stdout)
	}
}

// 13. A declared secret whose environment variable is not set is a
// configuration error: secrets are resolved before any step is inspected
// (section 6), same as `baton run` would reject it.
func TestToolsCmdSecretMissingEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.yaml")
	scn := `
version: 1
name: inspect-secret
secrets:
  token:
    from: env
    key: BATON_TOOLS_TEST_UNSET_VAR
steps:
  - id: build
    agent:
      engine: fake
      script: script.yaml
      prompt: work
      result: result.json
`
	if err := os.WriteFile(path, []byte(scn), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "result.json"), []byte(`{"type":"object"}`), 0o644); err != nil {
		t.Fatalf("write result schema: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "script.yaml"), []byte("calls: []"), 0o644); err != nil {
		t.Fatalf("write fake script: %v", err)
	}
	os.Unsetenv("BATON_TOOLS_TEST_UNSET_VAR")

	var code int
	_, stderr := captureOutput(t, func() {
		code = toolsCmd([]string{path, "--step", "build"})
	})
	if code != exitcode.Config {
		t.Fatalf("exit code = %d, want %d", code, exitcode.Config)
	}
	if !strings.Contains(stderr, "BATON_TOOLS_TEST_UNSET_VAR") {
		t.Fatalf("stderr does not name the unset variable: %q", stderr)
	}
}

// 14. --config pointing at a file config.Parse rejects (here: an unknown
// top-level field, since the config decoder rejects those) is a
// configuration error surfaced before the scenario's own secrets are
// touched.
func TestToolsCmdBadConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := writeToolsScenario(t, dir)
	configPath := filepath.Join(dir, "baton.yaml")
	if err := os.WriteFile(configPath, []byte("not_a_real_field: true\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var code int
	_, stderr := captureOutput(t, func() {
		code = toolsCmd([]string{path, "--step", "build", "--config", configPath})
	})
	if code != exitcode.Config {
		t.Fatalf("exit code = %d, want %d", code, exitcode.Config)
	}
	if !strings.Contains(stderr, "baton:") {
		t.Fatalf("stderr does not report the config error: %q", stderr)
	}
}

// 15. A global api entry whose auth.secret names a secret the config never
// declares is rejected while resolving api secrets, before the engine is
// built at all.
func TestToolsCmdConfigUndeclaredAPISecret(t *testing.T) {
	dir := t.TempDir()
	path := writeToolsScenario(t, dir)
	configPath := filepath.Join(dir, "baton.yaml")
	config := `
apis:
  demo:
    pack: demo
    from: ./nowhere
    auth:
      secret: missing_secret
`
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var code int
	_, stderr := captureOutput(t, func() {
		code = toolsCmd([]string{path, "--step", "build", "--config", configPath})
	})
	if code != exitcode.Config {
		t.Fatalf("exit code = %d, want %d", code, exitcode.Config)
	}
	if !strings.Contains(stderr, "missing_secret") {
		t.Fatalf("stderr does not name the undeclared secret: %q", stderr)
	}
}

// 16. --workspace is accepted and passed through: it must not make the
// engine reject an otherwise valid step.
func TestToolsCmdWorkspaceFlag(t *testing.T) {
	dir := t.TempDir()
	path := writeToolsScenario(t, dir)
	workspace := t.TempDir()

	var code int
	stdout, stderr := captureOutput(t, func() {
		code = toolsCmd([]string{path, "--step", "build", "--workspace", workspace})
	})
	if code != exitcode.OK {
		t.Fatalf("exit code = %d, want %d, stderr: %s", code, exitcode.OK, stderr)
	}
	if !strings.Contains(stdout, "step build:") {
		t.Fatalf("stdout missing the step header: %q", stdout)
	}
}
