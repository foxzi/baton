// Package tools builds the tool set the gateway serves an agent step out of
// what the scenario declares. The commands of spec section 7.5 are here: the
// runner executes them, never the agent's own process, which is what keeps
// the argument vector, the working directory and the environment under the
// scenario's control.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/foxzi/baton/internal/agent"
	"github.com/foxzi/baton/internal/gateway"
	"github.com/foxzi/baton/internal/scenario"
	"github.com/foxzi/baton/internal/tmpl"
	"github.com/foxzi/baton/internal/values"
)

// SecretSource resolves the secrets a command's environment refers to.
type SecretSource interface {
	Lookup(name string) (values.Secret, bool)
}

// CommandOptions is what the runner hands over for one step's commands.
type CommandOptions struct {
	// Workspace is the directory every command runs in.
	Workspace string

	// Declared are the scenario's commands, before the step's policy
	// narrows them.
	Declared map[string]scenario.Command

	// Policy decides whether the step gets command tools at all and which
	// ones (spec section 7.3).
	Policy agent.Policy

	Secrets SecretSource

	// Budget is the wall clock all of the step's command calls share; the
	// runner sets it to half the step's timeout (spec section 7.5).
	Budget time.Duration

	// MaxOutputBytes caps stdout and stderr of a single call.
	MaxOutputBytes int64
}

// Defaults for what a command does not say.
const (
	// DefaultTimeout is a command's own time limit; the step's budget still
	// applies on top of it.
	DefaultTimeout = 2 * time.Minute

	// DefaultMaxOutputBytes keeps a chatty build log from filling the
	// agent's context on its own.
	DefaultMaxOutputBytes = 64 * 1024

	// terminateGrace is how long a stopped command has to end on its own
	// before its output pipes are closed under it.
	terminateGrace = 5 * time.Second
)

// Commands is the command tool set of one step, and the budget those tools
// share.
type Commands struct {
	workspace string
	commands  map[string]scenario.Command
	secrets   SecretSource
	renderer  *tmpl.Renderer
	maxBytes  int64

	mu sync.Mutex
	// bounded tells a step with a spent budget from a step that was never
	// given one.
	bounded   bool
	remaining time.Duration
}

// NewCommands narrows the scenario's commands to what the step's policy
// allows. A policy naming a command the scenario does not declare is a
// configuration error, not an empty tool set: the scenario meant something
// that is not there.
func NewCommands(opts CommandOptions) (*Commands, error) {
	if opts.Workspace == "" {
		return nil, errors.New("commands: no workspace")
	}

	selected := make(map[string]scenario.Command)
	if opts.Policy.ExecMode == scenario.ExecModeCommands {
		if len(opts.Policy.ExecCommands) == 0 {
			// An empty list means every command the scenario declares.
			for id, command := range opts.Declared {
				selected[id] = command
			}
		}
		for _, id := range opts.Policy.ExecCommands {
			command, ok := opts.Declared[id]
			if !ok {
				return nil, fmt.Errorf("commands: the step allows %q, which the scenario does not declare", id)
			}
			selected[id] = command
		}
	}

	maxBytes := opts.MaxOutputBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxOutputBytes
	}
	return &Commands{
		workspace: opts.Workspace,
		commands:  selected,
		secrets:   opts.Secrets,
		renderer:  tmpl.NewRenderer(opts.Workspace),
		maxBytes:  maxBytes,
		bounded:   opts.Budget > 0,
		remaining: opts.Budget,
	}, nil
}

// Tools returns one tool per command the step may run, in a stable order so
// that two runs of the same step advertise the same list.
func (c *Commands) Tools() []gateway.Tool {
	ids := make([]string, 0, len(c.commands))
	for id := range c.commands {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	tools := make([]gateway.Tool, 0, len(ids))
	for _, id := range ids {
		command := c.commands[id]
		tools = append(tools, gateway.Tool{
			Name:        id,
			Description: command.Description,
			InputSchema: argsSchema(command.Args),
			MaxCalls:    command.MaxCalls,
			Handler:     c.handler(id, command),
		})
	}
	return tools
}

// Response is what a command call returns to the agent (spec section 7.5).
// A non-zero exit code is data: a failing test run is an answer, not a
// broken tool.
type Response struct {
	ExitCode   int    `json:"exit_code"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	Truncated  bool   `json:"truncated"`
	DurationMS int64  `json:"duration_ms"`

	// Result is the parsed stdout, present only for a command that asks for
	// parsing.
	Result any `json:"result,omitempty"`
}

// handler runs one command on behalf of the agent.
func (c *Commands) handler(id string, command scenario.Command) gateway.Handler {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		args, err := bindArgs(command.Args, raw)
		if err != nil {
			return nil, err
		}

		argv, err := c.renderArgv(id, command.Argv, args)
		if err != nil {
			return nil, err
		}

		env, err := c.env(command.Env)
		if err != nil {
			return nil, err
		}

		timeout, err := c.take(command.Timeout.Duration())
		if err != nil {
			return nil, err
		}

		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		stdout := &cappedBuffer{limit: c.maxBytes}
		stderr := &cappedBuffer{limit: c.maxBytes}

		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir = c.workspace
		cmd.Env = env
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		// A command that waits for input would sit there until the timeout.
		cmd.Stdin = strings.NewReader("")
		isolate(cmd)
		cmd.Cancel = func() error { return terminate(cmd) }
		// The last resort if the group ignores the signal: the pipes are
		// closed and the wait ends, whatever the children are doing.
		cmd.WaitDelay = terminateGrace

		started := time.Now()
		runErr := cmd.Run()
		spent := time.Since(started)
		c.give(timeout, spent)

		response := Response{
			ExitCode:   exitCodeOf(cmd),
			Stdout:     stdout.String(),
			Stderr:     stderr.String(),
			Truncated:  stdout.truncated() || stderr.truncated(),
			DurationMS: spent.Milliseconds(),
		}

		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return nil, fmt.Errorf("command %s: no answer within %s", id, timeout)
		case ctx.Err() != nil:
			return nil, fmt.Errorf("command %s: %w", id, ctx.Err())
		case runErr != nil && cmd.ProcessState == nil:
			// The command never ran: a missing executable is the scenario's
			// problem, and the agent cannot fix it by trying again.
			return nil, fmt.Errorf("command %s cannot be run: %w", id, runErr)
		}

		if command.Parse != scenario.ParseUnset {
			parsed, err := parseOutput(command.Parse, response.Stdout)
			if err != nil {
				return nil, fmt.Errorf("command %s: %w", id, err)
			}
			response.Result = parsed
		}
		return response, nil
	}
}

// renderArgv fills the argv templates with the call's arguments. The
// arguments are rendered into single argv entries and never split, so a value
// with a space in it stays one argument.
func (c *Commands) renderArgv(id string, argv []string, args map[string]string) ([]string, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("command %s has no argv", id)
	}

	data := map[string]any{"args": args}
	rendered := make([]string, 0, len(argv))
	for i, entry := range argv {
		value, err := c.renderer.Render(fmt.Sprintf("%s.argv[%d]", id, i), entry, data)
		if err != nil {
			return nil, fmt.Errorf("command %s: argv[%d]: %w", id, i, err)
		}
		rendered = append(rendered, value)
	}
	if rendered[0] == "" {
		return nil, fmt.Errorf("command %s has an empty executable", id)
	}
	return rendered, nil
}

// env builds the command's environment out of the runner's allow list and
// what the command declares.
func (c *Commands) env(declared map[string]scenario.EnvValue) ([]string, error) {
	return processEnv(c.secrets, declared)
}

// take reserves time for one call out of the step's shared budget.
func (c *Commands) take(timeout time.Duration) (time.Duration, error) {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// A step without a budget is bounded by the individual timeouts only.
	if !c.bounded {
		return timeout, nil
	}
	if c.remaining <= 0 {
		return 0, errors.New("the step has no command time left")
	}
	if timeout > c.remaining {
		timeout = c.remaining
	}
	c.remaining -= timeout
	return timeout, nil
}

// give returns the reserved time a call did not use, so that a quick command
// does not cost the step its whole timeout.
func (c *Commands) give(reserved, spent time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.bounded {
		return
	}
	if unused := reserved - spent; unused > 0 {
		c.remaining += unused
	}
}

// Remaining reports the command time the step has left, for the run's
// records.
func (c *Commands) Remaining() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.remaining
}

// parseOutput turns stdout into a value, the same way a run step does.
func parseOutput(mode scenario.ParseMode, stdout string) (any, error) {
	switch mode {
	case scenario.ParseText:
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
		var value any
		if err := json.Unmarshal([]byte(stdout), &value); err != nil {
			return nil, fmt.Errorf("parse json: %w", err)
		}
		return value, nil
	default:
		return nil, fmt.Errorf("unknown parse mode %q", mode)
	}
}

// exitCodeOf reports the command's exit code, or -1 when it never ran.
func exitCodeOf(cmd *exec.Cmd) int {
	if cmd.ProcessState == nil {
		return -1
	}
	return cmd.ProcessState.ExitCode()
}

// cappedBuffer collects output up to a limit and remembers that it stopped.
type cappedBuffer struct {
	limit   int64
	data    []byte
	dropped int64
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	room := b.limit - int64(len(b.data))
	if room > 0 {
		if int64(len(p)) <= room {
			b.data = append(b.data, p...)
			return len(p), nil
		}
		b.data = append(b.data, p[:room]...)
		p = p[room:]
	}
	b.dropped += int64(len(p))
	// The command is not told its output is being dropped: a broken pipe
	// would end a build early and lose the part that was kept.
	return len(p), nil
}

func (b *cappedBuffer) String() string {
	if b.dropped == 0 {
		return string(b.data)
	}
	return string(b.data) + fmt.Sprintf("\n[baton: truncated, %d more bytes]\n", b.dropped)
}

func (b *cappedBuffer) truncated() bool { return b.dropped > 0 }

// argsSchema describes a command's arguments to the agent. The pattern goes
// into the schema as well as being enforced, so that the agent can see the
// shape it has to meet instead of guessing at rejections.
func argsSchema(args map[string]scenario.CommandArg) json.RawMessage {
	properties := make(map[string]any, len(args))
	required := make([]string, 0, len(args))

	names := make([]string, 0, len(args))
	for name := range args {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		arg := args[name]
		property := map[string]any{"type": "string"}
		if arg.Pattern != "" {
			property["pattern"] = arg.Pattern
		}
		if arg.Default != "" {
			property["default"] = arg.Default
		}
		properties[name] = property
		if arg.Required {
			required = append(required, name)
		}
	}

	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}

	data, err := json.Marshal(schema)
	if err != nil {
		// The schema is built from strings only, so this cannot fail; a
		// tool without a schema would still be usable.
		return nil
	}
	return data
}

// scalar renders an argument the agent passed as a string. A model that
// answers 3 where a string was asked for means the number three.
func scalar(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case bool:
		return strconv.FormatBool(typed), true
	case json.Number:
		return typed.String(), true
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), true
	default:
		return "", false
	}
}
