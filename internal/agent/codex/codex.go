// Package codex runs an agent step with the OpenAI Codex CLI (spec section
// 8.2). Unlike the Claude Code adapter, this CLI's built-in shell cannot be
// turned off, so the step's policy is enforced by the sandbox mode and by
// what the gateway itself allows, not by disabling tools.
package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/foxzi/baton/internal/agent"
	"github.com/foxzi/baton/internal/scenario"
)

// Options configure the engine.
type Options struct {
	// Binary is the CLI to run; the default is codex from PATH.
	Binary string
}

// Engine runs steps with one CLI installation.
type Engine struct {
	binary string

	// flags is what this installation's `codex exec --help` advertises. The
	// CLI's flag set moves between releases, so the adapter asks instead of
	// assuming (spec section 8.2).
	flags map[string]bool
}

// supportedMajors are the CLI major versions this adapter knows. A 1.x CLI
// is refused rather than driven blindly, for the same reason the
// claude-code adapter refuses an unknown major: the flags that keep the
// agent inside its policy are the ones most likely to have moved.
var supportedMajors = map[int]bool{0: true}

// requiredFlags are the ones the adapter cannot do its job without.
var requiredFlags = []string{
	"--json",
	"--sandbox",
	"--cd",
	"--skip-git-repo-check",
}

// New checks the installation and returns an engine for it.
func New(ctx context.Context, opts Options) (*Engine, error) {
	binary := opts.Binary
	if binary == "" {
		binary = "codex"
	}

	major, err := majorVersion(ctx, binary)
	if err != nil {
		return nil, err
	}
	if !supportedMajors[major] {
		return nil, fmt.Errorf("codex: major version %d is not supported by this adapter", major)
	}

	flags, err := helpFlags(ctx, binary)
	if err != nil {
		return nil, err
	}
	for _, flag := range requiredFlags {
		if !flags[flag] {
			return nil, fmt.Errorf("codex: the installed CLI has no %s flag", flag)
		}
	}
	return &Engine{binary: binary, flags: flags}, nil
}

// majorVersion asks the CLI for its version and takes the major from it.
func majorVersion(ctx context.Context, binary string) (int, error) {
	out, err := exec.CommandContext(ctx, binary, "--version").Output()
	if err != nil {
		return 0, fmt.Errorf("codex: run %s --version: %w", binary, err)
	}

	match := versionPattern.FindSubmatch(out)
	if match == nil {
		return 0, fmt.Errorf("codex: cannot read a version from %q", strings.TrimSpace(string(out)))
	}
	major, err := strconv.Atoi(string(match[1]))
	if err != nil {
		return 0, fmt.Errorf("codex: cannot read a version from %q", strings.TrimSpace(string(out)))
	}
	return major, nil
}

var versionPattern = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// helpFlags collects the long flags of `codex exec --help`.
func helpFlags(ctx context.Context, binary string) (map[string]bool, error) {
	out, err := exec.CommandContext(ctx, binary, "exec", "--help").Output()
	if err != nil {
		return nil, fmt.Errorf("codex: run %s exec --help: %w", binary, err)
	}

	flags := make(map[string]bool)
	for _, match := range flagPattern.FindAll(out, -1) {
		flags[string(match)] = true
	}
	if len(flags) == 0 {
		return nil, fmt.Errorf("codex: %s exec --help lists no flags", binary)
	}
	return flags, nil
}

var flagPattern = regexp.MustCompile(`--[A-Za-z][A-Za-z0-9-]*`)

// Run hands the prompt to the CLI and reports what it spent.
//
// Submitted and Result stay unset: the CLI submits through the gateway, so
// the session is the authority on the step's result, not this engine.
func (e *Engine) Run(ctx context.Context, req agent.Request) (agent.Result, error) {
	if len(req.Skills) > 0 {
		return agent.Result{}, fmt.Errorf("codex: skills are not supported by this engine")
	}

	home, err := os.MkdirTemp("", "baton-codex-*")
	if err != nil {
		return agent.Result{}, fmt.Errorf("codex: create the home directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(home) }()

	if err := writeConfig(home, req); err != nil {
		return agent.Result{}, err
	}

	transcript, err := createTranscript(req.TranscriptPath)
	if err != nil {
		return agent.Result{}, err
	}
	defer func() { _ = transcript.Close() }()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, e.binary, e.argv(req)...)
	cmd.Dir = req.Workspace
	cmd.Env = environ(home, req.Gateway, req.Env)
	cmd.Stdin = strings.NewReader(prompt(req))
	cmd.Stdout = io.MultiWriter(&stdout, transcript)
	cmd.Stderr = &stderr

	// The CLI gets a chance to shut down cleanly, then it is killed: on
	// cancellation, on the step's timeout, and when the runner cancels
	// because the result is already in (spec section 8.2).
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second

	runErr := cmd.Run()
	result, parseErr := parseResult(stdout.Bytes())
	result.Transcript = req.TranscriptPath

	switch {
	case ctx.Err() != nil:
		return result, ctx.Err()
	case runErr != nil:
		// The events may still be there: the CLI prints them before it
		// exits with an error code, and the tokens were spent either way.
		return result, fmt.Errorf("codex: %w%s", runErr, tail(stderr.String()))
	case parseErr != nil:
		return result, parseErr
	}
	return result, nil
}

// prompt is what goes on stdin: the system prompt, when there is one,
// prepended as a clearly separated block, since this CLI has no
// system-prompt flag.
func prompt(req agent.Request) string {
	if req.System == "" {
		return req.Prompt
	}
	return "System instructions:\n" + req.System + "\n\n---\n\n" + req.Prompt
}

// argv assembles the command line, using only flags the installation has.
// The prompt itself never appears here: it arrives on stdin, which keeps a
// long prompt out of the process table.
func (e *Engine) argv(req agent.Request) []string {
	argv := []string{
		"exec",
		"--json",
		"--skip-git-repo-check",
		"--cd", req.Workspace,
		"--sandbox", sandboxMode(req.Tools.FSWrite),
	}

	// Nothing persisted between runs and no local execpolicy rules the step
	// did not ask for. --ignore-user-config is deliberately not passed: it
	// would drop $CODEX_HOME/config.toml, which is where this run's own
	// configuration and its gateway live. The user's configuration is out
	// of reach anyway, because CODEX_HOME is a directory of our own.
	argv = e.appendIfSupported(argv, "--ephemeral")
	argv = e.appendIfSupported(argv, "--ignore-rules")
	argv = e.appendIfSupported(argv, "--color", "never")

	if req.Model != "" {
		argv = e.appendIfSupported(argv, "--model", req.Model)
	}

	// Neither req.MaxTurns nor req.BudgetUSD has a flag or a config key to
	// enforce them: the CLI is not bounded here, but the gateway's
	// max_tool_calls still bounds the step.
	return argv
}

// sandboxMode picks the sandbox that keeps the step inside its policy. The
// built-in shell tool cannot be removed the way claude-code's Bash tool can,
// so the sandbox is what stands between the agent and the rest of the
// filesystem.
func sandboxMode(write scenario.FSWrite) string {
	if write == scenario.FSWriteWorkspace {
		return "workspace-write"
	}
	return "read-only"
}

// appendIfSupported adds a flag and its values when the installation has it.
func (e *Engine) appendIfSupported(argv []string, flag string, values ...string) []string {
	if !e.flags[flag] {
		return argv
	}
	return append(append(argv, flag), values...)
}

// event is one line of the CLI's --json event stream. Only the fields this
// adapter needs are named; everything else is left for json.Unmarshal to
// discard.
type event struct {
	Type  string `json:"type"`
	Usage struct {
		InputTokens       int `json:"input_tokens"`
		CachedInputTokens int `json:"cached_input_tokens"`
		OutputTokens      int `json:"output_tokens"`
	} `json:"usage"`
}

// parseResult reads the CLI's event stream. It sums usage over every
// turn.completed event and counts them as turns; the CLI reports no cost, so
// CostUSD is left at zero. Unknown event types and unparsable lines are
// skipped, since the stream is meant to be read forward compatibly.
func parseResult(out []byte) (agent.Result, error) {
	var result agent.Result
	turns := 0

	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var ev event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Type != "turn.completed" {
			continue
		}

		turns++
		result.Usage.InputTokens += ev.Usage.InputTokens
		result.Usage.OutputTokens += ev.Usage.OutputTokens
		result.Usage.CachedTokens += ev.Usage.CachedInputTokens
	}
	result.Turns = turns

	if turns == 0 {
		return result, fmt.Errorf("codex: the CLI reported no completed turn")
	}
	return result, nil
}

// createTranscript opens the file the CLI's output is copied into.
func createTranscript(path string) (io.WriteCloser, error) {
	if path == "" {
		return nopCloser{io.Discard}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("codex: create the transcript directory: %w", err)
	}
	file, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("codex: create the transcript: %w", err)
	}
	return file, nil
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// tail returns the end of the CLI's diagnostics, enough to see the reason
// without pasting a whole log into the run's error.
func tail(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	const max = 2000
	if len(text) > max {
		text = "…" + text[len(text)-max:]
	}
	return ": " + text
}
