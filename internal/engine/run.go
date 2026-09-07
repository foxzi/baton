package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/foxzi/baton/internal/expr"
	"github.com/foxzi/baton/internal/scenario"
)

// defaultMaxOutputBytes caps stdout and stderr of a command when the step
// does not set max_output_bytes (section 3.3).
const defaultMaxOutputBytes = 1 << 20

// stderrTailBytes is how much of stderr travels into the on_failure context
// (section 9.3).
const stderrTailBytes = 2 << 10

// execRun executes a run step: a command, never a shell (section 3.3).
func (e *Engine) execRun(ctx context.Context, step *scenario.Step, path string) (expr.Step, *Error) {
	body := step.Run

	argv, stepErr := e.renderArgv(step.ID, body.Argv)
	if stepErr != nil {
		return expr.Step{}, stepErr
	}
	cwd, stepErr := e.resolveCwd(step.ID, body.Cwd)
	if stepErr != nil {
		return expr.Step{}, stepErr
	}
	env, envNames, stepErr := e.buildEnv(step.ID, body.Env)
	if stepErr != nil {
		return expr.Step{}, stepErr
	}
	stdin := ""
	if body.Stdin != "" {
		stdin, stepErr = e.render(fmt.Sprintf("%s.run.stdin", step.ID), body.Stdin)
		if stepErr != nil {
			return expr.Step{}, stepErr
		}
	}

	e.writeStepJSON(path, "input.json", map[string]any{
		"argv":     argv,
		"cwd":      cwd,
		"env":      envNames,
		"stdin":    stdin,
		"parse":    string(body.Parse),
		"readonly": body.Readonly,
	})

	limit := body.MaxOutputBytes.Bytes()
	if limit <= 0 {
		limit = defaultMaxOutputBytes
	}
	stdout := &cappedBuffer{limit: limit}
	stderr := &cappedBuffer{limit: limit}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = cwd
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	runErr := cmd.Run()

	e.writeStepFile(path, "stdout.log", stdout.Bytes())
	e.writeStepFile(path, "stderr.log", stderr.Bytes())

	out := expr.Step{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCodeOf(cmd),
	}

	if stepErr := e.classifyRunError(ctx, step, runErr, out); stepErr != nil {
		e.writeStepJSON(path, "output.json", map[string]any{
			"status":    expr.StatusFailed,
			"exit_code": out.ExitCode,
			"error":     map[string]any{"class": stepErr.Class, "message": stepErr.Msg},
		})
		return out, stepErr
	}

	result, stepErr := parseOutput(step.ID, body.Parse, out.Stdout)
	if stepErr != nil {
		e.writeStepJSON(path, "output.json", map[string]any{
			"status":    expr.StatusFailed,
			"exit_code": out.ExitCode,
			"error":     map[string]any{"class": stepErr.Class, "message": stepErr.Msg},
		})
		return out, stepErr
	}
	out.Result = result

	e.writeStepJSON(path, "output.json", map[string]any{
		"status":    expr.StatusSuccess,
		"result":    out.Result,
		"exit_code": out.ExitCode,
	})
	return out, nil
}

// classifyRunError turns the outcome of cmd.Run into an error class
// (section 9.1), or nil when the exit code is allowed.
func (e *Engine) classifyRunError(ctx context.Context, step *scenario.Step, runErr error, out expr.Step) *Error {
	if ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &Error{
			Class:      ClassTimeout,
			Msg:        fmt.Sprintf("step %s: timed out", step.ID),
			Err:        runErr,
			StderrTail: tail(out.Stderr, stderrTailBytes),
		}
	}
	if ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
		return &Error{
			Class:      ClassConfig,
			Msg:        fmt.Sprintf("step %s: cancelled", step.ID),
			Err:        runErr,
			StderrTail: tail(out.Stderr, stderrTailBytes),
		}
	}

	var exitErr *exec.ExitError
	if runErr != nil && !errors.As(runErr, &exitErr) {
		// The command could not be started at all: not found, not
		// executable, bad working directory. Deterministic, so it is a
		// command error and is not retried.
		return &Error{
			Class: ClassCommand,
			Msg:   fmt.Sprintf("step %s: %v", step.ID, runErr),
			Err:   runErr,
		}
	}

	if !allowedExit(step.Run.AllowExitCodes, out.ExitCode) {
		return &Error{
			Class:      ClassCommand,
			Msg:        fmt.Sprintf("step %s: exit code %d", step.ID, out.ExitCode),
			StderrTail: tail(out.Stderr, stderrTailBytes),
		}
	}
	return nil
}

// renderArgv renders every argument. Templates are rendered one argument at a
// time, so a rendered value never splits into several arguments.
func (e *Engine) renderArgv(stepID string, argv []string) ([]string, *Error) {
	if len(argv) == 0 {
		return nil, errorf(ClassConfig, "step %s: run.argv is empty", stepID)
	}
	rendered := make([]string, 0, len(argv))
	for i, arg := range argv {
		value, err := e.render(fmt.Sprintf("%s.run.argv[%d]", stepID, i), arg)
		if err != nil {
			return nil, err
		}
		rendered = append(rendered, value)
	}
	if rendered[0] == "" {
		return nil, errorf(ClassConfig, "step %s: run.argv[0] rendered empty", stepID)
	}
	return rendered, nil
}

// resolveCwd resolves the working directory of a command. The default and the
// literal "workspace" both mean the workspace; anything else is taken
// relative to it (section 3.3).
func (e *Engine) resolveCwd(stepID, cwd string) (string, *Error) {
	workspace := e.opts.Workspace
	if cwd == "" || cwd == "workspace" {
		return workspace, nil
	}
	rendered, err := e.render(fmt.Sprintf("%s.run.cwd", stepID), cwd)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(rendered) {
		return rendered, nil
	}
	return filepath.Join(workspace, rendered), nil
}

// buildEnv builds the child environment and the list of names to record in
// input.json. Secret values reach the child process and nothing else: they
// are never rendered, logged or written to the run directory (section 6).
func (e *Engine) buildEnv(stepID string, declared map[string]scenario.EnvValue) ([]string, []string, *Error) {
	env := os.Environ()
	names := make([]string, 0, len(declared))
	for _, name := range sortedKeys(declared) {
		entry := declared[name]
		if entry.Secret != "" {
			secret, ok := e.opts.Secrets.Lookup(entry.Secret)
			if !ok {
				return nil, nil, errorf(ClassConfig, "step %s: run.env.%s: unknown secret %q", stepID, name, entry.Secret)
			}
			env = append(env, name+"="+secret.Reveal())
			names = append(names, name+" (secret)")
			continue
		}
		value, err := e.render(fmt.Sprintf("%s.run.env.%s", stepID, name), entry.Value)
		if err != nil {
			return nil, nil, err
		}
		env = append(env, name+"="+value)
		names = append(names, name)
	}
	return env, names, nil
}

// parseOutput turns stdout into the step result (section 3.3).
func parseOutput(stepID string, mode scenario.ParseMode, stdout string) (any, *Error) {
	switch mode {
	case scenario.ParseUnset, scenario.ParseText:
		return stdout, nil
	case scenario.ParseLines:
		trimmed := strings.TrimRight(stdout, "\n")
		if trimmed == "" {
			return []any{}, nil
		}
		lines := strings.Split(trimmed, "\n")
		items := make([]any, 0, len(lines))
		for _, line := range lines {
			items = append(items, strings.TrimSuffix(line, "\r"))
		}
		return items, nil
	case scenario.ParseJSON:
		var result any
		if err := json.Unmarshal([]byte(stdout), &result); err != nil {
			return nil, wrapf(ClassSchema, err, "step %s: parse json", stepID)
		}
		return result, nil
	default:
		return nil, errorf(ClassConfig, "step %s: unknown parse mode %q", stepID, mode)
	}
}

// execAssert stops the run when its condition is false (section 3.9).
func (e *Engine) execAssert(step *scenario.Step) (expr.Step, *Error) {
	program, err := expr.CompileBool(step.Assert.Condition)
	if err != nil {
		return expr.Step{}, wrapf(ClassConfig, err, "step %s: assert.condition", step.ID)
	}
	ok, err := program.EvalBool(e.exprContext())
	if err != nil {
		return expr.Step{}, wrapf(ClassConfig, err, "step %s: assert.condition", step.ID)
	}
	if ok {
		return expr.Step{Result: true}, nil
	}

	message := step.Assert.Message
	if message == "" {
		message = fmt.Sprintf("assert failed: %s", step.Assert.Condition)
	} else {
		rendered, renderErr := e.render(fmt.Sprintf("%s.assert.message", step.ID), message)
		if renderErr != nil {
			return expr.Step{}, renderErr
		}
		message = rendered
	}
	return expr.Step{Result: false}, errorf(ClassAssert, "%s", message)
}

// writeStepFile records a step file, reporting store problems as events
// instead of failing the step.
func (e *Engine) writeStepFile(path, name string, data []byte) {
	if err := e.opts.Store.WriteStepFile(path, name, data); err != nil {
		e.emit(Event{Type: "store_error", Step: path, Message: err.Error()})
	}
}

func (e *Engine) writeStepJSON(path, name string, value any) {
	if err := e.opts.Store.WriteStepJSON(path, name, value); err != nil {
		e.emit(Event{Type: "store_error", Step: path, Message: err.Error()})
	}
}

// exitCodeOf reports the exit code of a finished command, or -1 when the
// command never started.
func exitCodeOf(cmd *exec.Cmd) int {
	if cmd.ProcessState == nil {
		return -1
	}
	return cmd.ProcessState.ExitCode()
}

// allowedExit reports whether the exit code is accepted; the default is 0
// only (section 3.3).
func allowedExit(allowed []int, code int) bool {
	if len(allowed) == 0 {
		return code == 0
	}
	for _, item := range allowed {
		if item == code {
			return true
		}
	}
	return false
}

// cappedBuffer collects output up to a limit and counts what it dropped, so a
// runaway command cannot exhaust memory (max_output_bytes, section 3.3).
type cappedBuffer struct {
	limit   int64
	data    []byte
	dropped int64
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	room := b.limit - int64(len(b.data))
	if room <= 0 {
		b.dropped += int64(len(p))
		return len(p), nil
	}
	if int64(len(p)) > room {
		b.data = append(b.data, p[:room]...)
		b.dropped += int64(len(p)) - room
		return len(p), nil
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

// Bytes returns the collected output with a truncation marker when output was
// dropped.
func (b *cappedBuffer) Bytes() []byte {
	if b.dropped == 0 {
		return b.data
	}
	return append(b.data, []byte(fmt.Sprintf("\n[baton: truncated, %d more bytes]\n", b.dropped))...)
}

func (b *cappedBuffer) String() string { return string(b.Bytes()) }

// tail returns the last n bytes of text.
func tail(text string, n int) string {
	if len(text) <= n {
		return text
	}
	return text[len(text)-n:]
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
