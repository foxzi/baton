package codex

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/foxzi/baton/internal/agent"
	"github.com/foxzi/baton/internal/scenario"
)

// stub writes a shell script that stands in for the CLI: it answers
// --version and exec --help like the real one, records how it was called,
// and prints whatever event stream the test asked for.
type stub struct {
	// version is what --version prints.
	version string

	// flags are the extra flags exec --help advertises; the required ones
	// are added.
	flags []string

	// stdout is printed on stdout, normally an event stream.
	stdout string

	// stderr is printed on stderr.
	stderr string

	// exitCode is the status the script exits with.
	exitCode int

	// sleepSeconds makes the script hang instead of answering, for the test
	// that stops a run.
	sleepSeconds int
}

// call is what the stub recorded about the way it was run.
type call struct {
	Args   []string
	Env    map[string]string
	Dir    string
	Prompt string
}

// build writes the script and returns its path and the directory the
// record lands in.
func (s stub) build(t *testing.T) (binary, record string) {
	t.Helper()

	dir := t.TempDir()
	binary = filepath.Join(dir, "codex")
	record = filepath.Join(dir, "record")
	if err := os.Mkdir(record, 0o755); err != nil {
		t.Fatalf("create the record directory: %v", err)
	}

	version := s.version
	if version == "" {
		version = "codex-cli 0.152.1"
	}
	flags := append([]string{
		"--json", "--sandbox", "--cd", "--skip-git-repo-check",
	}, s.flags...)

	// One file per thing the test asks about, so the script needs nothing
	// but a shell to write the record. exec is the only subcommand this
	// adapter drives, so the stub only special-cases --version and its own
	// --help.
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then echo ` + quote(version) + `; exit 0; fi
if [ "$1" = "exec" ] && [ "$2" = "--help" ]; then echo ` + quote(strings.Join(flags, " ")) + `; exit 0; fi
` + hangLine(s.sleepSeconds) + `
rec=` + quote(record) + `
for arg in "$@"; do printf '%s\n' "$arg"; done > "$rec/args"
printf '%s' "$PWD" > "$rec/pwd"
env > "$rec/env"
cat > "$rec/prompt"
` + printLine(s.stderr, " >&2") + `
` + printLine(s.stdout, "") + `
exit ` + strconv.Itoa(s.exitCode) + `
`
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatalf("write the stub: %v", err)
	}
	return binary, record
}

// hangLine replaces the script's body with a wait, as the process itself so
// that a signal reaches it directly.
func hangLine(seconds int) string {
	if seconds == 0 {
		return ""
	}
	return "exec sleep " + strconv.Itoa(seconds)
}

func printLine(text, redirect string) string {
	if text == "" {
		return ""
	}
	return "printf '%s' " + quote(text) + redirect
}

func quote(text string) string {
	return "'" + strings.ReplaceAll(text, "'", `'\''`) + "'"
}

// readCall reads what the stub recorded.
func readCall(t *testing.T, record string) call {
	t.Helper()

	got := call{
		Args:   readLines(t, filepath.Join(record, "args")),
		Dir:    readFile(t, filepath.Join(record, "pwd")),
		Prompt: readFile(t, filepath.Join(record, "prompt")),
		Env:    make(map[string]string),
	}
	for _, line := range readLines(t, filepath.Join(record, "env")) {
		name, value, ok := strings.Cut(line, "=")
		if ok {
			got.Env[name] = value
		}
	}
	return got
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the record: %v", err)
	}
	return string(data)
}

func readLines(t *testing.T, path string) []string {
	t.Helper()

	text := strings.TrimSuffix(readFile(t, path), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// flagValue returns the value that follows a flag.
func (c call) flagValue(flag string) string {
	for i, arg := range c.Args {
		if arg == flag && i+1 < len(c.Args) {
			return c.Args[i+1]
		}
	}
	return ""
}

// has says whether the flag was passed at all.
func (c call) has(flag string) bool {
	for _, arg := range c.Args {
		if arg == flag {
			return true
		}
	}
	return false
}

const oneTurn = `{"type":"thread.started","thread_id":"t1"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"pong"}}
{"type":"turn.completed","usage":{"input_tokens":12948,"cached_input_tokens":10496,"cache_write_input_tokens":0,"output_tokens":5,"reasoning_output_tokens":0}}
`

const twoTurns = `{"type":"thread.started","thread_id":"t1"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"first"}}
{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":10,"cache_write_input_tokens":0,"output_tokens":20,"reasoning_output_tokens":0}}
{"type":"some.future.event","unexpected":{"nested":true}}
not even json
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"second"}}
{"type":"turn.completed","usage":{"input_tokens":200,"cached_input_tokens":30,"cache_write_input_tokens":0,"output_tokens":40,"reasoning_output_tokens":0}}
`

func fixture(t *testing.T, s stub) (*Engine, agent.Request, string) {
	t.Helper()

	binary, record := s.build(t)
	engine, err := New(context.Background(), Options{Binary: binary})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := agent.Request{
		Prompt:         "review the diff",
		Workspace:      t.TempDir(),
		Gateway:        agent.GatewayInfo{URL: "http://127.0.0.1:8731/steps/review/mcp", Token: "run-token"},
		Tools:          agent.ResolvePolicy(&scenario.AgentStep{Profile: scenario.ProfileReview}),
		TranscriptPath: filepath.Join(t.TempDir(), "transcript", "agent.jsonl"),
	}
	return engine, req, record
}

func TestNewRejectsUnsupportedMajor(t *testing.T) {
	binary, _ := stub{version: "codex-cli 1.0.0"}.build(t)

	if _, err := New(context.Background(), Options{Binary: binary}); err == nil {
		t.Fatal("expected a refusal for an unsupported major version")
	}
}

func TestNewRejectsUnreadableVersion(t *testing.T) {
	binary, _ := stub{version: "unreleased"}.build(t)

	if _, err := New(context.Background(), Options{Binary: binary}); err == nil {
		t.Fatal("expected a refusal when the version cannot be read")
	}
}

func TestNewRejectsMissingBinary(t *testing.T) {
	if _, err := New(context.Background(), Options{Binary: filepath.Join(t.TempDir(), "absent")}); err == nil {
		t.Fatal("expected a refusal for a missing binary")
	}
}

func TestNewAcceptsInstalledVersion(t *testing.T) {
	binary, _ := stub{version: "codex-cli 0.152.1"}.build(t)

	if _, err := New(context.Background(), Options{Binary: binary}); err != nil {
		t.Fatalf("New: %v", err)
	}
}

func TestNewRejectsMissingFlag(t *testing.T) {
	// exec --help without --sandbox belongs to a CLI this adapter cannot
	// keep inside a step's policy.
	binary, _ := stub{}.build(t)
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatalf("read the stub: %v", err)
	}
	script := strings.ReplaceAll(string(data), "--sandbox", "--sandbo")
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatalf("write the stub: %v", err)
	}

	_, err = New(context.Background(), Options{Binary: binary})
	if err == nil {
		t.Fatal("expected a refusal when a required flag is missing")
	}
	if !strings.Contains(err.Error(), "--sandbox") {
		t.Errorf("error does not name the missing flag: %v", err)
	}
}

func TestRunPassesPromptWorkspaceAndFlags(t *testing.T) {
	engine, req, record := fixture(t, stub{stdout: oneTurn})

	if _, err := engine.Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := readCall(t, record)
	if got.Prompt != req.Prompt {
		t.Errorf("prompt = %q, want %q", got.Prompt, req.Prompt)
	}
	want, err := filepath.EvalSymlinks(req.Workspace)
	if err != nil {
		t.Fatalf("resolve the workspace: %v", err)
	}
	if dir, _ := filepath.EvalSymlinks(got.Dir); dir != want {
		t.Errorf("working directory = %q, want %q", got.Dir, want)
	}
	if !got.has("exec") || !got.has("--json") || !got.has("--skip-git-repo-check") {
		t.Errorf("args = %q, want a headless JSON exec run", got.Args)
	}
	if value := got.flagValue("--cd"); value != req.Workspace {
		t.Errorf("--cd = %q, want %q", value, req.Workspace)
	}
	if value := got.flagValue("--sandbox"); value != "read-only" {
		t.Errorf("--sandbox = %q, want read-only for a review profile", value)
	}
}

func TestRunPrependsSystemPromptOnStdin(t *testing.T) {
	engine, req, record := fixture(t, stub{stdout: oneTurn})
	req.System = "be brief"

	if _, err := engine.Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := readCall(t, record)
	if !strings.Contains(got.Prompt, "be brief") || !strings.Contains(got.Prompt, req.Prompt) {
		t.Errorf("prompt = %q, want the system prompt and the prompt together", got.Prompt)
	}
	if strings.Index(got.Prompt, "be brief") > strings.Index(got.Prompt, req.Prompt) {
		t.Errorf("prompt = %q, want the system prompt first", got.Prompt)
	}
}

func TestRunUsesWorkspaceSandboxForFixProfile(t *testing.T) {
	engine, req, record := fixture(t, stub{stdout: oneTurn})
	req.Tools = agent.ResolvePolicy(&scenario.AgentStep{Profile: scenario.ProfileFix})

	if _, err := engine.Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := readCall(t, record)
	if value := got.flagValue("--sandbox"); value != "workspace-write" {
		t.Errorf("--sandbox = %q, want workspace-write for a fix profile", value)
	}
}

func TestRunSkipsUnsupportedFlags(t *testing.T) {
	// The installed CLI has none of the optional flags, so none of them are
	// passed: an unknown flag would fail the whole step.
	engine, req, record := fixture(t, stub{stdout: oneTurn})
	req.Model = "gpt-5-codex"

	if _, err := engine.Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := readCall(t, record)
	for _, flag := range []string{"--ephemeral", "--ignore-rules", "--color", "--model"} {
		if got.has(flag) {
			t.Errorf("args = %q, want no %s", got.Args, flag)
		}
	}
}

func TestRunPassesSupportedFlags(t *testing.T) {
	engine, req, record := fixture(t, stub{
		flags:  []string{"--ephemeral", "--ignore-user-config", "--ignore-rules", "--color", "--model"},
		stdout: oneTurn,
	})
	req.Model = "gpt-5-codex"

	if _, err := engine.Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := readCall(t, record)
	for _, flag := range []string{"--ephemeral", "--ignore-rules"} {
		if !got.has(flag) {
			t.Errorf("args = %q, want %s", got.Args, flag)
		}
	}
	// Even an installation that has it: the flag would drop the config.toml
	// written for this run, and with it the gateway the step works through.
	if got.has("--ignore-user-config") {
		t.Errorf("args = %q, want no --ignore-user-config", got.Args)
	}
	if value := got.flagValue("--color"); value != "never" {
		t.Errorf("--color = %q, want never", value)
	}
	if value := got.flagValue("--model"); value != "gpt-5-codex" {
		t.Errorf("--model = %q, want the step's model", value)
	}
}

func TestConfigTOMLShape(t *testing.T) {
	dir := t.TempDir()
	req := agent.Request{
		Workspace: "/work/step",
		Gateway:   agent.GatewayInfo{URL: "http://127.0.0.1:8731/steps/fix/mcp", Token: "run-token"},
		Tools:     agent.ResolvePolicy(&scenario.AgentStep{Profile: scenario.ProfileFix}),
	}

	if err := writeConfig(dir, req); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}

	path := filepath.Join(dir, "config.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	text := string(data)

	for _, want := range []string{
		`approval_policy = "never"`,
		`sandbox_mode = "workspace-write"`,
		`writable_roots = ["/work/step"]`,
		`web_search = false`,
		`[mcp_servers.baton]`,
		`url = "http://127.0.0.1:8731/steps/fix/mcp"`,
		`bearer_token_env_var = "BATON_GATEWAY_TOKEN"`,
		`default_tools_approval_mode = "approve"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("config.toml = %s, want %q", text, want)
		}
	}
	if strings.Contains(text, "run-token") {
		t.Errorf("config.toml = %s, want no bearer token in the file", text)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config.toml: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestConfigTOMLReadOnlySandbox(t *testing.T) {
	dir := t.TempDir()
	req := agent.Request{
		Workspace: "/work/step",
		Gateway:   agent.GatewayInfo{URL: "http://127.0.0.1:8731/steps/review/mcp"},
		Tools:     agent.ResolvePolicy(&scenario.AgentStep{Profile: scenario.ProfileReview}),
	}

	if err := writeConfig(dir, req); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	text := string(data)

	if !strings.Contains(text, `sandbox_mode = "read-only"`) {
		t.Errorf("config.toml = %s, want read-only sandbox_mode", text)
	}
	if strings.Contains(text, "writable_roots") {
		t.Errorf("config.toml = %s, want no writable_roots for a read-only step", text)
	}
	if strings.Contains(text, "bearer_token_env_var") {
		t.Errorf("config.toml = %s, want no bearer_token_env_var without a token", text)
	}
}

func TestRunSetsGatewayEnvironment(t *testing.T) {
	t.Setenv("SECRET_FROM_RUNNER", "leaked")
	t.Setenv("PATH", os.Getenv("PATH"))

	engine, req, record := fixture(t, stub{stdout: oneTurn})
	req.Env = map[string]string{"OPENAI_API_KEY": "sk-test"}

	if _, err := engine.Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := readCall(t, record)
	if _, ok := got.Env["SECRET_FROM_RUNNER"]; ok {
		t.Error("the runner's own environment reached the agent")
	}
	if got.Env["OPENAI_API_KEY"] != "sk-test" {
		t.Errorf("the step's variables did not reach the agent: %v", got.Env["OPENAI_API_KEY"])
	}
	if got.Env["PATH"] == "" {
		t.Error("PATH did not reach the agent")
	}
	if got.Env["BATON_GATEWAY_TOKEN"] != "run-token" {
		t.Errorf("BATON_GATEWAY_TOKEN = %q, want the run's token", got.Env["BATON_GATEWAY_TOKEN"])
	}

	home := got.Env["HOME"]
	if home == "" || home == os.Getenv("HOME") {
		t.Errorf("HOME = %q, want a home directory of the run's own", home)
	}
	if got.Env["CODEX_HOME"] != home {
		t.Errorf("CODEX_HOME = %q, want it to match HOME (%q)", got.Env["CODEX_HOME"], home)
	}
	if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the temporary home outlived the run: %v", err)
	}
}

func TestRunReportsUsageSummedOverTurns(t *testing.T) {
	engine, req, _ := fixture(t, stub{stdout: twoTurns})

	result, err := engine.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.Usage.InputTokens != 300 {
		t.Errorf("input tokens = %d, want 300", result.Usage.InputTokens)
	}
	if result.Usage.OutputTokens != 60 {
		t.Errorf("output tokens = %d, want 60", result.Usage.OutputTokens)
	}
	if result.Usage.CachedTokens != 40 {
		t.Errorf("cached tokens = %d, want 40", result.Usage.CachedTokens)
	}
	if result.Turns != 2 {
		t.Errorf("turns = %d, want 2", result.Turns)
	}
	if result.CostUSD != 0 {
		t.Errorf("cost = %v, want 0: the CLI reports no cost", result.CostUSD)
	}
	if result.Transcript != req.TranscriptPath {
		t.Errorf("transcript = %q, want %q", result.Transcript, req.TranscriptPath)
	}
	// The engine cannot see the submission: the gateway holds it.
	if result.Submitted || result.Result != nil {
		t.Errorf("result = %+v, want no submission from the engine", result)
	}
}

func TestRunWritesTranscript(t *testing.T) {
	engine, req, _ := fixture(t, stub{stdout: oneTurn})

	if _, err := engine.Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}

	data, err := os.ReadFile(req.TranscriptPath)
	if err != nil {
		t.Fatalf("read the transcript: %v", err)
	}
	if !bytes.Contains(data, []byte(`"type":"turn.completed"`)) {
		t.Errorf("transcript = %s, want the CLI's output", data)
	}
}

func TestRunRejectsSkills(t *testing.T) {
	engine, req, record := fixture(t, stub{stdout: oneTurn})
	req.Skills = []string{t.TempDir()}

	_, err := engine.Run(context.Background(), req)
	if err == nil {
		t.Fatal("expected an error when skills are requested")
	}
	if !strings.Contains(err.Error(), "skills are not supported") {
		t.Errorf("error = %v, want it to say skills are unsupported", err)
	}
	if _, statErr := os.Stat(filepath.Join(record, "args")); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("the CLI was run despite the unsupported skills")
	}
}

func TestRunReportsExitFailure(t *testing.T) {
	engine, req, _ := fixture(t, stub{
		stdout:   oneTurn,
		stderr:   "gateway unreachable",
		exitCode: 1,
	})

	_, err := engine.Run(context.Background(), req)
	if err == nil {
		t.Fatal("expected an error for a non-zero exit")
	}
	if !strings.Contains(err.Error(), "gateway unreachable") {
		t.Errorf("error does not carry the CLI's diagnostics: %v", err)
	}
}

func TestRunReportsNoCompletedTurn(t *testing.T) {
	engine, req, _ := fixture(t, stub{stdout: `{"type":"thread.started","thread_id":"t1"}` + "\n"})

	_, err := engine.Run(context.Background(), req)
	if err == nil {
		t.Fatal("expected an error when no turn completed")
	}
	if !strings.Contains(err.Error(), "no completed turn") {
		t.Errorf("error = %v, want it to say no turn completed", err)
	}
}

func TestRunReportsEmptyOutput(t *testing.T) {
	engine, req, _ := fixture(t, stub{})

	if _, err := engine.Run(context.Background(), req); err == nil {
		t.Fatal("expected an error when the CLI printed nothing")
	}
}

func TestRunHonoursCancellation(t *testing.T) {
	engine, req, _ := fixture(t, stub{stdout: oneTurn, sleepSeconds: 30})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := engine.Run(ctx, req)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want the deadline", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the run took %v, want a prompt stop", elapsed)
	}
}
