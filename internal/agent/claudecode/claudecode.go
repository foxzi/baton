// Package claudecode runs an agent step with the Claude Code CLI (spec
// section 8.2). The CLI gets the step's tools through the gateway's MCP
// endpoint; its own built-in tools are narrowed to what the step's profile
// allows, and its process is kept on a short leash: an empty environment, a
// throwaway home directory and no shell.
package claudecode

import (
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
	"github.com/foxzi/baton/internal/provider"
	"github.com/foxzi/baton/internal/scenario"
)

// Options configure the engine.
type Options struct {
	// Binary is the CLI to run; the default is claude from PATH.
	Binary string
}

// Engine runs steps with one CLI installation.
type Engine struct {
	binary string

	// flags is what this installation's help page advertises. The CLI's flag
	// set moves between releases, so the adapter asks instead of assuming
	// (spec section 8.2).
	flags map[string]bool
}

// supportedMajors are the CLI major versions this adapter knows. A newer one
// is refused rather than driven blindly: the flags that keep the agent inside
// its policy are the ones most likely to have moved.
var supportedMajors = map[int]bool{1: true, 2: true}

// requiredFlags are the ones the adapter cannot do its job without.
var requiredFlags = []string{
	"--print",
	"--output-format",
	"--mcp-config",
	"--settings",
	"--allowedTools",
	"--disallowedTools",
}

// New checks the installation and returns an engine for it.
func New(ctx context.Context, opts Options) (*Engine, error) {
	binary := opts.Binary
	if binary == "" {
		binary = "claude"
	}

	major, err := majorVersion(ctx, binary)
	if err != nil {
		return nil, err
	}
	if !supportedMajors[major] {
		return nil, fmt.Errorf("claude-code: major version %d is not supported by this adapter", major)
	}

	flags, err := helpFlags(ctx, binary)
	if err != nil {
		return nil, err
	}
	for _, flag := range requiredFlags {
		if !flags[flag] {
			return nil, fmt.Errorf("claude-code: the installed CLI has no %s flag", flag)
		}
	}
	return &Engine{binary: binary, flags: flags}, nil
}

// majorVersion asks the CLI for its version and takes the major from it.
func majorVersion(ctx context.Context, binary string) (int, error) {
	out, err := exec.CommandContext(ctx, binary, "--version").Output()
	if err != nil {
		return 0, fmt.Errorf("claude-code: run %s --version: %w", binary, err)
	}

	match := versionPattern.FindSubmatch(out)
	if match == nil {
		return 0, fmt.Errorf("claude-code: cannot read a version from %q", strings.TrimSpace(string(out)))
	}
	major, err := strconv.Atoi(string(match[1]))
	if err != nil {
		return 0, fmt.Errorf("claude-code: cannot read a version from %q", strings.TrimSpace(string(out)))
	}
	return major, nil
}

var versionPattern = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// helpFlags collects the long flags of the installed CLI.
func helpFlags(ctx context.Context, binary string) (map[string]bool, error) {
	out, err := exec.CommandContext(ctx, binary, "--help").Output()
	if err != nil {
		return nil, fmt.Errorf("claude-code: run %s --help: %w", binary, err)
	}

	flags := make(map[string]bool)
	for _, match := range flagPattern.FindAll(out, -1) {
		flags[string(match)] = true
	}
	if len(flags) == 0 {
		return nil, fmt.Errorf("claude-code: %s --help lists no flags", binary)
	}
	return flags, nil
}

var flagPattern = regexp.MustCompile(`--[A-Za-z][A-Za-z0-9-]*`)

// Run hands the prompt to the CLI and reports what it spent.
//
// Submitted and Result stay unset: the CLI submits through the gateway, so
// the session is the authority on the step's result, not this engine.
func (e *Engine) Run(ctx context.Context, req agent.Request) (agent.Result, error) {
	home, err := os.MkdirTemp("", "baton-claude-*")
	if err != nil {
		return agent.Result{}, fmt.Errorf("claude-code: create the home directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(home) }()

	mcpPath, err := writeMCPConfig(home, req.Gateway)
	if err != nil {
		return agent.Result{}, err
	}
	settingsPath, err := writeSettings(home, req.Tools)
	if err != nil {
		return agent.Result{}, err
	}
	skills, err := linkSkills(req.Workspace, req.Skills)
	if err != nil {
		return agent.Result{}, err
	}
	defer skills.remove()

	transcript, err := createTranscript(req.TranscriptPath)
	if err != nil {
		return agent.Result{}, err
	}
	defer func() { _ = transcript.Close() }()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, e.binary, e.argv(req, mcpPath, settingsPath)...)
	cmd.Dir = req.Workspace
	cmd.Env = environ(home, req.Env)
	cmd.Stdin = strings.NewReader(req.Prompt)
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
		// The report may still be there: the CLI prints it before it exits
		// with an error code, and the tokens were spent either way.
		return result, fmt.Errorf("claude-code: %w%s", runErr, tail(stderr.String()))
	case parseErr != nil:
		return result, parseErr
	}
	return result, nil
}

// argv assembles the command line, using only flags the installation has.
func (e *Engine) argv(req agent.Request, mcpPath, settingsPath string) []string {
	// The prompt arrives on stdin, which keeps a long prompt out of the
	// process table.
	argv := []string{
		"--print",
		"--output-format", "json",
		"--mcp-config", mcpPath,
		"--settings", settingsPath,
		"--allowedTools", commaList(allowedTools(req.Tools)),
		"--disallowedTools", commaList(toolDenials(req.Tools)),
	}

	// Nothing but the gateway and the CLI's own tools: a server from the
	// user's own configuration would be a hole in the step's policy.
	argv = e.appendIfSupported(argv, "--strict-mcp-config")

	// Restricted mode drops the tools that run code, confines the file tools
	// to the working directory and ignores the user's and the repository's
	// settings files, which is the same bargain the step's policy makes.
	argv = e.appendIfSupported(argv, "--restricted")

	// Bare mode drops what would make two runs of the same step differ:
	// hooks, plugins, auto-discovered CLAUDE.md files and keychain reads.
	argv = e.appendIfSupported(argv, "--bare")

	// The built-in set the step is allowed, so that a tool outside the
	// policy is not merely unapproved but absent.
	if tools := builtinTools(req.Tools); len(tools) > 0 {
		argv = e.appendIfSupported(argv, "--tools", commaList(tools))
	}

	// A prompt the agent cannot answer must fail, not wait for a human.
	argv = e.appendIfSupported(argv, "--permission-prompts", "none")

	// Only the one mode that is spelled the same in every version this
	// adapter supports, and only where it changes anything.
	if req.Tools.FSWrite == scenario.FSWriteWorkspace {
		argv = e.appendIfSupported(argv, "--permission-mode", "acceptEdits")
	}

	// Without the flag the turn cap is not enforced here; the gateway's
	// max_tool_calls still bounds the step.
	if req.MaxTurns > 0 {
		argv = e.appendIfSupported(argv, "--max-turns", strconv.Itoa(req.MaxTurns))
	}
	if req.BudgetUSD > 0 {
		argv = e.appendIfSupported(argv, "--max-budget-usd", strconv.FormatFloat(req.BudgetUSD, 'f', -1, 64))
	}

	if req.Model != "" {
		argv = e.appendIfSupported(argv, "--model", req.Model)
	}
	if req.System != "" {
		// Appended, not replaced: the CLI's own instructions are what make
		// its built-in tools work.
		argv = e.appendIfSupported(argv, "--append-system-prompt", req.System)
	}
	return argv
}

// appendIfSupported adds a flag and its values when the installation has it.
func (e *Engine) appendIfSupported(argv []string, flag string, values ...string) []string {
	if !e.flags[flag] {
		return argv
	}
	return append(append(argv, flag), values...)
}

// report is the JSON the CLI prints with --output-format json.
type report struct {
	IsError  bool     `json:"is_error"`
	Subtype  string   `json:"subtype"`
	NumTurns int      `json:"num_turns"`
	Cost     float64  `json:"total_cost_usd"`
	Errors   []string `json:"errors"`
	Usage    struct {
		InputTokens     int `json:"input_tokens"`
		OutputTokens    int `json:"output_tokens"`
		CacheReadTokens int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

// parseResult reads the CLI's report. An empty or unreadable report is an
// error, but the caller still gets whatever could be read.
func parseResult(out []byte) (agent.Result, error) {
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return agent.Result{}, fmt.Errorf("claude-code: the CLI printed no report")
	}

	var rep report
	if err := json.Unmarshal(out, &rep); err != nil {
		return agent.Result{}, fmt.Errorf("claude-code: cannot read the report: %w", err)
	}

	result := agent.Result{
		Usage: provider.Usage{
			InputTokens:  rep.Usage.InputTokens,
			OutputTokens: rep.Usage.OutputTokens,
			CachedTokens: rep.Usage.CacheReadTokens,
		},
		CostUSD: rep.Cost,
		Turns:   rep.NumTurns,
	}
	if rep.IsError {
		return result, fmt.Errorf("claude-code: the agent ended with %s: %s",
			orUnknown(rep.Subtype), strings.Join(rep.Errors, "; "))
	}
	return result, nil
}

func orUnknown(subtype string) string {
	if subtype == "" {
		return "an error"
	}
	return subtype
}

// createTranscript opens the file the CLI's output is copied into.
func createTranscript(path string) (io.WriteCloser, error) {
	if path == "" {
		return nopCloser{io.Discard}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("claude-code: create the transcript directory: %w", err)
	}
	file, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("claude-code: create the transcript: %w", err)
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
