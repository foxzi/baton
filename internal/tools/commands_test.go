package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foxzi/baton/internal/agent"
	"github.com/foxzi/baton/internal/gateway"
	"github.com/foxzi/baton/internal/scenario"
	"github.com/foxzi/baton/internal/values"
)

// stubSecrets is the secret store of a test.
type stubSecrets map[string]string

func (s stubSecrets) Lookup(name string) (values.Secret, bool) {
	value, ok := s[name]
	if !ok {
		return values.Secret{}, false
	}
	return values.NewSecret(name, value), true
}

// script writes an executable shell script into dir and returns its path.
func script(t *testing.T, dir, name, body string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}

// call runs one command tool and returns what the agent would see.
func call(t *testing.T, c *Commands, name string, args string) (Response, error) {
	t.Helper()

	var handler = func() gateway.Handler {
		for _, tool := range c.Tools() {
			if tool.Name == name {
				return tool.Handler
			}
		}
		t.Fatalf("no tool named %q", name)
		return nil
	}()

	value, err := handler(context.Background(), json.RawMessage(args))
	if err != nil {
		return Response{}, err
	}
	response, ok := value.(Response)
	if !ok {
		t.Fatalf("handler returned %T, want Response", value)
	}
	return response, nil
}

func TestNewCommandsRejectsMissingWorkspace(t *testing.T) {
	if _, err := NewCommands(CommandOptions{}); err == nil {
		t.Fatalf("NewCommands without a workspace: error = nil, want an error")
	}
}

func TestNewCommandsExecModeNoneGivesNoTools(t *testing.T) {
	c, err := NewCommands(CommandOptions{
		Workspace: t.TempDir(),
		Declared:  map[string]scenario.Command{"test": {Argv: []string{"true"}}},
		Policy:    agent.Policy{ExecMode: scenario.ExecModeNone},
	})
	if err != nil {
		t.Fatalf("NewCommands: %v", err)
	}
	if tools := c.Tools(); len(tools) != 0 {
		t.Fatalf("Tools() = %d tools, want 0", len(tools))
	}
}

func TestNewCommandsEmptyListMeansEveryCommand(t *testing.T) {
	c, err := NewCommands(CommandOptions{
		Workspace: t.TempDir(),
		Declared: map[string]scenario.Command{
			"test": {Argv: []string{"true"}},
			"lint": {Argv: []string{"true"}},
		},
		Policy: agent.Policy{ExecMode: scenario.ExecModeCommands},
	})
	if err != nil {
		t.Fatalf("NewCommands: %v", err)
	}

	tools := c.Tools()
	if len(tools) != 2 {
		t.Fatalf("Tools() = %d tools, want 2", len(tools))
	}
	// Sorted, so that the same step always advertises the same list.
	if tools[0].Name != "lint" || tools[1].Name != "test" {
		t.Errorf("Tools() names = %q, %q, want lint, test", tools[0].Name, tools[1].Name)
	}
}

func TestNewCommandsNarrowsToTheAllowedSubset(t *testing.T) {
	c, err := NewCommands(CommandOptions{
		Workspace: t.TempDir(),
		Declared: map[string]scenario.Command{
			"test":   {Argv: []string{"true"}},
			"deploy": {Argv: []string{"true"}},
		},
		Policy: agent.Policy{ExecMode: scenario.ExecModeCommands, ExecCommands: []string{"test"}},
	})
	if err != nil {
		t.Fatalf("NewCommands: %v", err)
	}

	tools := c.Tools()
	if len(tools) != 1 || tools[0].Name != "test" {
		t.Fatalf("Tools() = %+v, want the single tool test", tools)
	}
}

func TestNewCommandsRejectsAnUndeclaredCommand(t *testing.T) {
	_, err := NewCommands(CommandOptions{
		Workspace: t.TempDir(),
		Declared:  map[string]scenario.Command{"test": {Argv: []string{"true"}}},
		Policy:    agent.Policy{ExecMode: scenario.ExecModeCommands, ExecCommands: []string{"nope"}},
	})
	if err == nil {
		t.Fatalf("NewCommands with an undeclared command: error = nil, want an error")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error = %q, want it to name the command", err)
	}
}

func TestToolsDescribeTheArguments(t *testing.T) {
	c := newCommands(t, t.TempDir(), map[string]scenario.Command{
		"test": {
			Argv:        []string{"go", "test", "{{ .args.path }}"},
			Description: "run the tests",
			MaxCalls:    3,
			Args: map[string]scenario.CommandArg{
				"path":  {Pattern: `[\w./-]+`, Default: "./..."},
				"focus": {Pattern: `\w+`, Required: true},
			},
		},
	})

	tools := c.Tools()
	if len(tools) != 1 {
		t.Fatalf("Tools() = %d tools, want 1", len(tools))
	}
	tool := tools[0]
	if tool.Description != "run the tests" {
		t.Errorf("Description = %q, want the scenario's description", tool.Description)
	}
	if tool.MaxCalls != 3 {
		t.Errorf("MaxCalls = %d, want 3", tool.MaxCalls)
	}

	var schema struct {
		Type       string `json:"type"`
		Required   []string
		Properties map[string]struct {
			Type    string `json:"type"`
			Pattern string `json:"pattern"`
			Default string `json:"default"`
		} `json:"properties"`
		AdditionalProperties bool `json:"additionalProperties"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("unmarshal InputSchema: %v", err)
	}
	if schema.Type != "object" {
		t.Errorf("schema type = %q, want object", schema.Type)
	}
	if schema.AdditionalProperties {
		t.Error("additionalProperties = true, want false: unknown arguments are rejected")
	}
	if len(schema.Required) != 1 || schema.Required[0] != "focus" {
		t.Errorf("required = %v, want [focus]", schema.Required)
	}
	if got := schema.Properties["path"]; got.Type != "string" || got.Pattern != `[\w./-]+` || got.Default != "./..." {
		t.Errorf("path property = %+v, want the declared type, pattern and default", got)
	}
}

func TestCommandRunsInTheWorkspace(t *testing.T) {
	workspace := t.TempDir()
	bin := script(t, t.TempDir(), "pwd.sh", "pwd")

	c := newCommands(t, workspace, map[string]scenario.Command{
		"where": {Argv: []string{bin}},
	})

	response, err := call(t, c, "where", `{}`)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if response.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", response.ExitCode)
	}
	// The workspace path can be a symlink on macOS, so compare what the
	// shell reports about both.
	if got := strings.TrimSpace(response.Stdout); got != workspace {
		resolved, _ := filepath.EvalSymlinks(workspace)
		if got != resolved {
			t.Errorf("cwd = %q, want the workspace %q", got, workspace)
		}
	}
}

func TestCommandArgumentsBecomeSingleArgvEntries(t *testing.T) {
	bin := script(t, t.TempDir(), "argv.sh", `for a in "$@"; do echo "[$a]"; done`)

	c := newCommands(t, t.TempDir(), map[string]scenario.Command{
		"echo": {
			Argv: []string{bin, "--focus={{ .args.focus }}", "{{ .args.path }}"},
			Args: map[string]scenario.CommandArg{
				"focus": {Pattern: `[\w ]+`},
				"path":  {Pattern: `[\w./-]+`, Default: "./..."},
			},
		},
	})

	response, err := call(t, c, "echo", `{"focus":"two words"}`)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	want := "[--focus=two words]\n[./...]\n"
	if response.Stdout != want {
		t.Errorf("stdout = %q, want %q", response.Stdout, want)
	}
}

func TestCommandExitCodeIsData(t *testing.T) {
	bin := script(t, t.TempDir(), "fail.sh", "echo out; echo err >&2; exit 7")

	c := newCommands(t, t.TempDir(), map[string]scenario.Command{
		"fail": {Argv: []string{bin}},
	})

	response, err := call(t, c, "fail", `{}`)
	if err != nil {
		t.Fatalf("call: a failing command must be an answer, not an error: %v", err)
	}
	if response.ExitCode != 7 {
		t.Errorf("ExitCode = %d, want 7", response.ExitCode)
	}
	if strings.TrimSpace(response.Stdout) != "out" || strings.TrimSpace(response.Stderr) != "err" {
		t.Errorf("stdout = %q, stderr = %q, want out and err", response.Stdout, response.Stderr)
	}
}

func TestCommandParsesItsOutput(t *testing.T) {
	dir := t.TempDir()
	c := newCommands(t, t.TempDir(), map[string]scenario.Command{
		"json":  {Argv: []string{script(t, dir, "json.sh", `echo '{"ok":true}'`)}, Parse: scenario.ParseJSON},
		"lines": {Argv: []string{script(t, dir, "lines.sh", "printf 'a\\nb\\n'")}, Parse: scenario.ParseLines},
		"bad":   {Argv: []string{script(t, dir, "bad.sh", "echo not-json")}, Parse: scenario.ParseJSON},
	})

	response, err := call(t, c, "json", `{}`)
	if err != nil {
		t.Fatalf("call json: %v", err)
	}
	result, ok := response.Result.(map[string]any)
	if !ok || result["ok"] != true {
		t.Errorf("Result = %#v, want the decoded object", response.Result)
	}

	response, err = call(t, c, "lines", `{}`)
	if err != nil {
		t.Fatalf("call lines: %v", err)
	}
	lines, ok := response.Result.([]any)
	if !ok || len(lines) != 2 || lines[0] != "a" || lines[1] != "b" {
		t.Errorf("Result = %#v, want the two lines", response.Result)
	}

	if _, err = call(t, c, "bad", `{}`); err == nil {
		t.Error("call bad: error = nil, want the parse failure reported to the agent")
	}
}

func TestCommandCapsItsOutput(t *testing.T) {
	bin := script(t, t.TempDir(), "loud.sh", "printf '0123456789'")

	c, err := NewCommands(CommandOptions{
		Workspace:      t.TempDir(),
		Declared:       map[string]scenario.Command{"loud": {Argv: []string{bin}}},
		Policy:         agent.Policy{ExecMode: scenario.ExecModeCommands},
		MaxOutputBytes: 4,
	})
	if err != nil {
		t.Fatalf("NewCommands: %v", err)
	}

	response, callErr := call(t, c, "loud", `{}`)
	if callErr != nil {
		t.Fatalf("call: %v", callErr)
	}
	if !response.Truncated {
		t.Error("Truncated = false, want true")
	}
	if !strings.HasPrefix(response.Stdout, "0123") {
		t.Errorf("stdout = %q, want it to start with the first four bytes", response.Stdout)
	}
	if !strings.Contains(response.Stdout, "truncated") {
		t.Errorf("stdout = %q, want it to say the output was cut", response.Stdout)
	}
}

func TestCommandEnvIsMinimal(t *testing.T) {
	t.Setenv("BATON_TEST_LEAK", "leaked")

	bin := script(t, t.TempDir(), "env.sh", "env")
	c := newCommands(t, t.TempDir(), map[string]scenario.Command{
		"env": {
			Argv: []string{bin},
			Env: map[string]scenario.EnvValue{
				"PLAIN": {Value: "value"},
				"TOKEN": {Secret: "api_token"},
			},
		},
	})
	c.secrets = stubSecrets{"api_token": "s3cret"}

	response, err := call(t, c, "env", `{}`)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if strings.Contains(response.Stdout, "BATON_TEST_LEAK") {
		t.Error("the runner's own environment reached the command")
	}
	for _, want := range []string{"PLAIN=value", "TOKEN=s3cret"} {
		if !strings.Contains(response.Stdout, want) {
			t.Errorf("env is missing %q:\n%s", want, response.Stdout)
		}
	}
	if !strings.Contains(response.Stdout, "PATH=") {
		t.Error("env has no PATH, so nothing would be findable")
	}
}

func TestCommandUnknownSecretIsRefused(t *testing.T) {
	c := newCommands(t, t.TempDir(), map[string]scenario.Command{
		"env": {Argv: []string{"true"}, Env: map[string]scenario.EnvValue{"TOKEN": {Secret: "missing"}}},
	})
	c.secrets = stubSecrets{}

	if _, err := call(t, c, "env", `{}`); err == nil {
		t.Fatal("call: error = nil, want the unknown secret refused")
	}
}

func TestCommandMissingExecutableIsRefused(t *testing.T) {
	c := newCommands(t, t.TempDir(), map[string]scenario.Command{
		"nope": {Argv: []string{filepath.Join(t.TempDir(), "not-there")}},
	})

	_, err := call(t, c, "nope", `{}`)
	if err == nil {
		t.Fatal("call: error = nil, want the missing executable refused")
	}
	if !strings.Contains(err.Error(), "cannot be run") {
		t.Errorf("error = %q, want it to say the command never ran", err)
	}
}

func TestCommandTimeoutIsReported(t *testing.T) {
	bin := script(t, t.TempDir(), "slow.sh", "sleep 5")

	c := newCommands(t, t.TempDir(), map[string]scenario.Command{
		"slow": {Argv: []string{bin}, Timeout: scenario.Duration(50 * time.Millisecond)},
	})

	started := time.Now()
	_, err := call(t, c, "slow", `{}`)
	if err == nil {
		t.Fatal("call: error = nil, want the timeout reported")
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Errorf("the call took %s, so the timeout did not apply", elapsed)
	}
}

func TestCommandsShareTheStepBudget(t *testing.T) {
	bin := script(t, t.TempDir(), "slow.sh", "sleep 5")

	c, err := NewCommands(CommandOptions{
		Workspace: t.TempDir(),
		Declared: map[string]scenario.Command{
			"slow": {Argv: []string{bin}, Timeout: scenario.Duration(time.Minute)},
		},
		Policy: agent.Policy{ExecMode: scenario.ExecModeCommands},
		Budget: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewCommands: %v", err)
	}

	// The first call is cut down to the budget rather than to its own
	// generous timeout, and it spends the budget doing so.
	if _, err := call(t, c, "slow", `{}`); err == nil {
		t.Fatal("first call: error = nil, want the budget to cut it short")
	}
	if remaining := c.Remaining(); remaining > 0 {
		t.Errorf("Remaining() = %s, want the budget spent", remaining)
	}

	_, err = call(t, c, "slow", `{}`)
	if err == nil {
		t.Fatal("second call: error = nil, want the exhausted budget refused")
	}
	if !strings.Contains(err.Error(), "no command time left") {
		t.Errorf("error = %q, want it to name the budget", err)
	}
}

func TestCommandBudgetKeepsWhatAQuickCallDidNotUse(t *testing.T) {
	bin := script(t, t.TempDir(), "quick.sh", "true")

	c, err := NewCommands(CommandOptions{
		Workspace: t.TempDir(),
		Declared: map[string]scenario.Command{
			"quick": {Argv: []string{bin}, Timeout: scenario.Duration(10 * time.Second)},
		},
		Policy: agent.Policy{ExecMode: scenario.ExecModeCommands},
		Budget: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewCommands: %v", err)
	}

	if _, err := call(t, c, "quick", `{}`); err != nil {
		t.Fatalf("call: %v", err)
	}
	if remaining := c.Remaining(); remaining < 25*time.Second {
		t.Errorf("Remaining() = %s, want nearly the whole budget back", remaining)
	}
}

// The pattern is what keeps a shell metacharacter out of argv in the first
// place: an argument the scenario describes as a path does not match once a
// semicolon is in it (spec section 13).
func TestCommandArgumentWithShellMetacharactersIsRefused(t *testing.T) {
	bin := script(t, t.TempDir(), "argv.sh", `for a in "$@"; do echo "[$a]"; done`)

	c := newCommands(t, t.TempDir(), map[string]scenario.Command{
		"echo": {
			Argv: []string{bin, "{{ .args.path }}"},
			Args: map[string]scenario.CommandArg{"path": {Pattern: `[\w./-]+`}},
		},
	})

	if _, err := call(t, c, "echo", `{"path":"; echo pwned"}`); err == nil {
		t.Fatalf("call with %q: error = nil, want the pattern to refuse it", "; echo pwned")
	}
}

// And where the pattern does allow such a value, it reaches the program as
// one argv entry: there is no shell to expand it (spec sections 7.5 and 13).
func TestCommandArgumentIsNotInterpretedByAShell(t *testing.T) {
	bin := script(t, t.TempDir(), "argv.sh", `for a in "$@"; do echo "[$a]"; done`)

	c := newCommands(t, t.TempDir(), map[string]scenario.Command{
		"echo": {
			Argv: []string{bin, "{{ .args.free }}"},
			Args: map[string]scenario.CommandArg{"free": {Pattern: `.+`}},
		},
	})

	response, err := call(t, c, "echo", `{"free":"$(id)"}`)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if response.Stdout != "[$(id)]\n" {
		t.Errorf("stdout = %q, want the argument passed literally", response.Stdout)
	}
}

// A path that leaves the workspace is refused before the pattern is even
// consulted, so a loose pattern is not a way out (spec sections 7.5 and 13).
func TestCommandArgumentRejectsPathsOutsideTheWorkspace(t *testing.T) {
	bin := script(t, t.TempDir(), "argv.sh", `for a in "$@"; do echo "[$a]"; done`)

	c := newCommands(t, t.TempDir(), map[string]scenario.Command{
		"echo": {
			Argv: []string{bin, "{{ .args.path }}"},
			Args: map[string]scenario.CommandArg{"path": {Pattern: `.+`}},
		},
	})

	for _, value := range []string{"/etc/passwd", "../etc/passwd", "src/../../etc/passwd"} {
		if _, err := call(t, c, "echo", `{"path":`+quoteJSON(value)+`}`); err == nil {
			t.Errorf("call with %q: error = nil, want it refused", value)
		}
	}
}

// quoteJSON renders a value as a JSON string.
func quoteJSON(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// newCommands builds a command set of every declared command.
func newCommands(t *testing.T, workspace string, declared map[string]scenario.Command) *Commands {
	t.Helper()

	c, err := NewCommands(CommandOptions{
		Workspace: workspace,
		Declared:  declared,
		Policy:    agent.Policy{ExecMode: scenario.ExecModeCommands},
	})
	if err != nil {
		t.Fatalf("NewCommands: %v", err)
	}
	return c
}
