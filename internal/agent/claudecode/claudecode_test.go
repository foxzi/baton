package claudecode

import (
	"context"
	"encoding/json"
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
// --version and --help like the real one, records how it was called, and
// prints whatever report the test asked for.
type stub struct {
	// version is what --version prints.
	version string

	// flags are the flags --help advertises; the required ones are added.
	flags []string

	// report is printed on stdout.
	report string

	// stderr is printed on stderr.
	stderr string

	// exitCode is the status the script exits with.
	exitCode int

	// sleepSeconds makes the script hang instead of answering, for the tests
	// that stop a run.
	sleepSeconds int
}

// call is what the stub recorded about the way it was run.
type call struct {
	Args   []string
	Env    map[string]string
	Dir    string
	Prompt string
}

// build writes the script and returns its path and the directory the record
// lands in.
func (s stub) build(t *testing.T) (binary, record string) {
	t.Helper()

	dir := t.TempDir()
	binary = filepath.Join(dir, "claude")
	record = filepath.Join(dir, "record")
	if err := os.Mkdir(record, 0o755); err != nil {
		t.Fatalf("create the record directory: %v", err)
	}

	version := s.version
	if version == "" {
		version = "2.1.263 (Claude Code)"
	}
	flags := append([]string{
		"--print", "--output-format", "--mcp-config", "--settings",
		"--allowedTools", "--disallowedTools",
	}, s.flags...)

	// One file per thing the test asks about, so the script needs nothing
	// but a shell to write the record.
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then echo ` + quote(version) + `; exit 0; fi
if [ "$1" = "--help" ]; then echo ` + quote(strings.Join(flags, " ")) + `; exit 0; fi
` + hangLine(s.sleepSeconds) + `
rec=` + quote(record) + `
for arg in "$@"; do printf '%s\n' "$arg"; done > "$rec/args"
printf '%s' "$PWD" > "$rec/pwd"
env > "$rec/env"
cat > "$rec/prompt"
` + printLine(s.stderr, " >&2") + `
` + printLine(s.report, "") + `
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

const successReport = `{"type":"result","subtype":"success","is_error":false,"num_turns":4,` +
	`"result":"done","total_cost_usd":0.0125,` +
	`"usage":{"input_tokens":1200,"output_tokens":340,"cache_read_input_tokens":800}}`

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

func TestNewRejectsUnknownMajor(t *testing.T) {
	binary, _ := stub{version: "3.0.1 (Claude Code)"}.build(t)

	if _, err := New(context.Background(), Options{Binary: binary}); err == nil {
		t.Fatal("expected a refusal for an unknown major version")
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

func TestNewRejectsMissingFlag(t *testing.T) {
	// A help page without --mcp-config belongs to a CLI this adapter cannot
	// hand the gateway to.
	binary, _ := stub{}.build(t)
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatalf("read the stub: %v", err)
	}
	script := strings.ReplaceAll(string(data), "--mcp-config", "--mcp-conf")
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatalf("write the stub: %v", err)
	}

	_, err = New(context.Background(), Options{Binary: binary})
	if err == nil {
		t.Fatal("expected a refusal when a required flag is missing")
	}
	if !strings.Contains(err.Error(), "--mcp-config") {
		t.Errorf("error does not name the missing flag: %v", err)
	}
}

func TestRunPassesPromptAndWorkspace(t *testing.T) {
	engine, req, record := fixture(t, stub{report: successReport})

	if _, err := engine.Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := readCall(t, record)
	if got.Prompt != req.Prompt {
		t.Errorf("prompt = %q, want %q", got.Prompt, req.Prompt)
	}
	// The workspace may be reached through a symlink on macOS temp dirs, so
	// compare what the shell reports with the same resolution.
	want, err := filepath.EvalSymlinks(req.Workspace)
	if err != nil {
		t.Fatalf("resolve the workspace: %v", err)
	}
	if dir, _ := filepath.EvalSymlinks(got.Dir); dir != want {
		t.Errorf("working directory = %q, want %q", got.Dir, want)
	}
	if !got.has("--print") || got.flagValue("--output-format") != "json" {
		t.Errorf("args = %q, want a headless JSON run", got.Args)
	}
}

func TestRunReportsUsage(t *testing.T) {
	engine, req, _ := fixture(t, stub{report: successReport})

	result, err := engine.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.Usage.InputTokens != 1200 || result.Usage.OutputTokens != 340 {
		t.Errorf("usage = %+v, want the report's tokens", result.Usage)
	}
	if result.Usage.CachedTokens != 800 {
		t.Errorf("cached tokens = %d, want 800", result.Usage.CachedTokens)
	}
	if result.CostUSD != 0.0125 {
		t.Errorf("cost = %v, want 0.0125", result.CostUSD)
	}
	if result.Turns != 4 {
		t.Errorf("turns = %d, want 4", result.Turns)
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
	engine, req, _ := fixture(t, stub{report: successReport})

	if _, err := engine.Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}

	data, err := os.ReadFile(req.TranscriptPath)
	if err != nil {
		t.Fatalf("read the transcript: %v", err)
	}
	if !strings.Contains(string(data), `"num_turns":4`) {
		t.Errorf("transcript = %s, want the CLI's output", data)
	}
}

func TestRunGivesGatewayThroughMCPConfig(t *testing.T) {
	engine, req, record := fixture(t, stub{flags: []string{"--strict-mcp-config"}, report: successReport})

	if _, err := engine.Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := readCall(t, record)
	if !got.has("--strict-mcp-config") {
		t.Errorf("args = %q, want --strict-mcp-config", got.Args)
	}

	// The stub is gone by now along with its temporary home, so the
	// configuration is read from what the stub recorded.
	path := got.flagValue("--mcp-config")
	if path == "" {
		t.Fatalf("args = %q, want --mcp-config", got.Args)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the MCP configuration outlived the run: %v", err)
	}
}

// TestMCPConfigShape checks the file the CLI is handed, since the run deletes
// it before a test could read it.
func TestMCPConfigShape(t *testing.T) {
	dir := t.TempDir()
	path, err := writeMCPConfig(dir, agent.GatewayInfo{
		URL:   "http://127.0.0.1:8731/steps/review/mcp",
		Token: "run-token",
	})
	if err != nil {
		t.Fatalf("writeMCPConfig: %v", err)
	}

	var config struct {
		Servers map[string]struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the configuration: %v", err)
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("read the configuration %s: %v", data, err)
	}

	server, ok := config.Servers[serverName]
	if !ok {
		t.Fatalf("configuration = %s, want a %q server", data, serverName)
	}
	if server.Type != "http" {
		t.Errorf("transport = %q, want http", server.Type)
	}
	if server.URL != "http://127.0.0.1:8731/steps/review/mcp" {
		t.Errorf("url = %q, want the step's endpoint", server.URL)
	}
	if server.Headers["Authorization"] != "Bearer run-token" {
		t.Errorf("headers = %v, want the run's token", server.Headers)
	}

	// The file carries a token, so it must not be readable by others.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat the configuration: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestSettingsDenyPaths(t *testing.T) {
	policy := agent.ResolvePolicy(&scenario.AgentStep{
		Profile: scenario.ProfileReview,
		Tools:   &scenario.Tools{FS: &scenario.FSTools{Deny: []string{"secrets/**"}}},
	})

	path, err := writeSettings(t.TempDir(), policy)
	if err != nil {
		t.Fatalf("writeSettings: %v", err)
	}

	var settings struct {
		Permissions struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the settings: %v", err)
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("read the settings %s: %v", data, err)
	}

	deny := strings.Join(settings.Permissions.Deny, " ")
	for _, want := range []string{
		"Read(.git/**)", "Edit(.git/**)", "Read(.env*)", "Read(**/*.pem)", "Read(**/id_*)",
		"Read(secrets/**)", "Edit(secrets/**)",
		"Bash", "WebFetch", "WebSearch", "Task",
		// A review profile does not write.
		"Edit", "Write",
	} {
		if !contains(settings.Permissions.Deny, want) {
			t.Errorf("deny = %q, want %q", deny, want)
		}
	}
}

func TestSettingsKeepWriteToolsForFixProfile(t *testing.T) {
	policy := agent.ResolvePolicy(&scenario.AgentStep{Profile: scenario.ProfileFix})

	path, err := writeSettings(t.TempDir(), policy)
	if err != nil {
		t.Fatalf("writeSettings: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the settings: %v", err)
	}

	var settings struct {
		Permissions struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("read the settings %s: %v", data, err)
	}
	if contains(settings.Permissions.Deny, "Write") {
		t.Errorf("deny = %v, want the write tools left open", settings.Permissions.Deny)
	}
	if !contains(settings.Permissions.Deny, "Bash") {
		t.Errorf("deny = %v, want Bash denied even for the fix profile", settings.Permissions.Deny)
	}
}

func TestRunNarrowsToolsByProfile(t *testing.T) {
	flags := []string{"--tools", "--permission-mode", "--permission-prompts", "--restricted", "--bare"}

	t.Run("review", func(t *testing.T) {
		engine, req, record := fixture(t, stub{flags: flags, report: successReport})

		if _, err := engine.Run(context.Background(), req); err != nil {
			t.Fatalf("Run: %v", err)
		}

		got := readCall(t, record)
		if tools := got.flagValue("--tools"); tools != "Read,Glob,Grep" {
			t.Errorf("--tools = %q, want the read-only set", tools)
		}
		if allowed := got.flagValue("--allowedTools"); !strings.Contains(allowed, "mcp__baton") {
			t.Errorf("--allowedTools = %q, want the gateway's tools", allowed)
		}
		for _, want := range []string{"Bash", "WebFetch", "Write"} {
			if denied := got.flagValue("--disallowedTools"); !strings.Contains(denied, want) {
				t.Errorf("--disallowedTools = %q, want %q", denied, want)
			}
		}
		if got.has("--permission-mode") {
			t.Errorf("args = %q, want no permission mode for a read-only step", got.Args)
		}
		if got.flagValue("--permission-prompts") != "none" {
			t.Errorf("args = %q, want prompts answered by nobody", got.Args)
		}
		if !got.has("--restricted") || !got.has("--bare") {
			t.Errorf("args = %q, want restricted and bare mode", got.Args)
		}
	})

	t.Run("fix", func(t *testing.T) {
		engine, req, record := fixture(t, stub{flags: flags, report: successReport})
		req.Tools = agent.ResolvePolicy(&scenario.AgentStep{Profile: scenario.ProfileFix})

		if _, err := engine.Run(context.Background(), req); err != nil {
			t.Fatalf("Run: %v", err)
		}

		got := readCall(t, record)
		if tools := got.flagValue("--tools"); !strings.Contains(tools, "Write") {
			t.Errorf("--tools = %q, want the write tools", tools)
		}
		if mode := got.flagValue("--permission-mode"); mode != "acceptEdits" {
			t.Errorf("--permission-mode = %q, want acceptEdits", mode)
		}
		if denied := got.flagValue("--disallowedTools"); strings.Contains(denied, "Write") {
			t.Errorf("--disallowedTools = %q, want the write tools left open", denied)
		}
	})
}

func TestRunSkipsUnsupportedFlags(t *testing.T) {
	// The installed CLI has no --max-turns, so the cap is simply not passed:
	// an unknown flag would fail the whole step.
	engine, req, record := fixture(t, stub{report: successReport})
	req.MaxTurns = 8
	req.BudgetUSD = 1.5
	req.Model = "claude-sonnet-4-5"
	req.System = "be brief"

	if _, err := engine.Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := readCall(t, record)
	for _, flag := range []string{"--max-turns", "--max-budget-usd", "--model", "--append-system-prompt", "--restricted"} {
		if got.has(flag) {
			t.Errorf("args = %q, want no %s", got.Args, flag)
		}
	}
}

func TestRunPassesSupportedLimits(t *testing.T) {
	engine, req, record := fixture(t, stub{
		flags:  []string{"--max-turns", "--max-budget-usd", "--model", "--append-system-prompt"},
		report: successReport,
	})
	req.MaxTurns = 8
	req.BudgetUSD = 1.5
	req.Model = "claude-sonnet-4-5"
	req.System = "be brief"

	if _, err := engine.Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := readCall(t, record)
	if value := got.flagValue("--max-turns"); value != "8" {
		t.Errorf("--max-turns = %q, want 8", value)
	}
	if value := got.flagValue("--max-budget-usd"); value != "1.5" {
		t.Errorf("--max-budget-usd = %q, want 1.5", value)
	}
	if value := got.flagValue("--model"); value != "claude-sonnet-4-5" {
		t.Errorf("--model = %q, want the step's model", value)
	}
	if value := got.flagValue("--append-system-prompt"); value != "be brief" {
		t.Errorf("--append-system-prompt = %q, want the step's system prompt", value)
	}
}

func TestRunKeepsEnvironmentSmall(t *testing.T) {
	t.Setenv("SECRET_FROM_RUNNER", "leaked")
	t.Setenv("PATH", os.Getenv("PATH"))

	engine, req, record := fixture(t, stub{report: successReport})
	req.Env = map[string]string{"ANTHROPIC_API_KEY": "sk-test"}

	if _, err := engine.Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := readCall(t, record)
	if _, ok := got.Env["SECRET_FROM_RUNNER"]; ok {
		t.Error("the runner's own environment reached the agent")
	}
	if got.Env["ANTHROPIC_API_KEY"] != "sk-test" {
		t.Errorf("the step's variables did not reach the agent: %v", got.Env["ANTHROPIC_API_KEY"])
	}
	if got.Env["PATH"] == "" {
		t.Error("PATH did not reach the agent")
	}
	home := got.Env["HOME"]
	if home == "" || home == os.Getenv("HOME") {
		t.Errorf("HOME = %q, want a home directory of the run's own", home)
	}
	if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the temporary home outlived the run: %v", err)
	}
}

func TestRunLinksSkillsAndCleansUp(t *testing.T) {
	skill := t.TempDir()
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("# skill"), 0o644); err != nil {
		t.Fatalf("write the skill: %v", err)
	}

	engine, req, _ := fixture(t, stub{report: successReport})
	req.Skills = []string{skill}
	link := filepath.Join(req.Workspace, ".claude", "skills", filepath.Base(skill))

	if _, err := engine.Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if _, err := os.Lstat(link); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the skill link outlived the run: %v", err)
	}
	if _, err := os.ReadFile(filepath.Join(skill, "SKILL.md")); err != nil {
		t.Errorf("the skill itself was touched: %v", err)
	}
}

func TestLinkSkillsKeepsWorkspaceCopy(t *testing.T) {
	workspace := t.TempDir()
	skill := filepath.Join(t.TempDir(), "review")
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatalf("create the skill: %v", err)
	}

	// A skill of the same name already in the workspace stays as it is.
	own := filepath.Join(workspace, ".claude", "skills", "review")
	if err := os.MkdirAll(own, 0o755); err != nil {
		t.Fatalf("create the workspace skill: %v", err)
	}

	linked, err := linkSkills(workspace, []string{skill})
	if err != nil {
		t.Fatalf("linkSkills: %v", err)
	}
	if len(linked) != 0 {
		t.Errorf("linked = %v, want the workspace copy left alone", linked)
	}

	linked.remove()
	if _, err := os.Stat(own); err != nil {
		t.Errorf("the workspace skill was removed: %v", err)
	}
}

func TestRunReportsExitFailure(t *testing.T) {
	engine, req, _ := fixture(t, stub{
		report:   successReport,
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

func TestRunReportsAgentError(t *testing.T) {
	engine, req, _ := fixture(t, stub{report: `{"type":"result","subtype":"error_max_turns",` +
		`"is_error":true,"num_turns":9,"total_cost_usd":0.5,"errors":["turn limit reached"],` +
		`"usage":{"input_tokens":10,"output_tokens":2}}`})

	result, err := engine.Run(context.Background(), req)
	if err == nil {
		t.Fatal("expected an error when the agent ended badly")
	}
	if !strings.Contains(err.Error(), "error_max_turns") || !strings.Contains(err.Error(), "turn limit reached") {
		t.Errorf("error = %v, want the report's reason", err)
	}
	// The tokens were spent, so the runner has to hear about them.
	if result.CostUSD != 0.5 || result.Turns != 9 {
		t.Errorf("result = %+v, want the spending reported", result)
	}
}

func TestRunReportsEmptyOutput(t *testing.T) {
	engine, req, _ := fixture(t, stub{})

	if _, err := engine.Run(context.Background(), req); err == nil {
		t.Fatal("expected an error when the CLI printed nothing")
	}
}

func TestRunHonoursCancellation(t *testing.T) {
	engine, req, _ := fixture(t, stub{report: successReport, sleepSeconds: 30})

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

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
