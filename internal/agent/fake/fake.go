// Package fake is the agent engine tests run on: it replays a scripted
// sequence of tool calls against the gateway instead of talking to a model
// (spec section 8.4). It exercises the runner, the gateway, the budgets and
// on_failure end to end at no cost.
package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/foxzi/baton/internal/agent"
	"github.com/foxzi/baton/internal/gateway"
	"github.com/foxzi/baton/internal/provider"
	"github.com/foxzi/baton/internal/units"
)

// Script is the behaviour of one fake agent step.
type Script struct {
	// Calls are made in order, through the gateway, exactly as a real agent
	// would. A script without a submit_result call leaves the step
	// unsubmitted, which is how the "result not submitted" path is tested.
	Calls []Call `yaml:"calls"`

	// Usage and CostUSD are what the engine reports, so that budget
	// accounting can be tested without a model.
	Usage   Usage   `yaml:"usage"`
	CostUSD float64 `yaml:"cost_usd"`
}

// Call is one tool call.
type Call struct {
	Tool string `yaml:"tool"`

	// Args are the call's arguments, passed to the tool as JSON.
	Args map[string]any `yaml:"args"`

	// Delay is how long the agent "thinks" before the call, for tests about
	// timeouts and cancellation.
	Delay units.Duration `yaml:"delay"`

	// StopOnError ends the script when the tool answers with an error,
	// instead of moving on to the next call.
	StopOnError bool `yaml:"stop_on_error"`
}

// Usage is the token count a script claims.
type Usage struct {
	InputTokens  int `yaml:"input_tokens"`
	OutputTokens int `yaml:"output_tokens"`
}

// Engine replays one script.
type Engine struct {
	script Script
}

// New loads a script. A broken script fails here, before the step starts and
// before the gateway is up.
func New(path string) (*Engine, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read the fake script: %w", err)
	}

	var script Script
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&script); err != nil {
		return nil, fmt.Errorf("parse the fake script %s: %w", path, err)
	}
	for i, call := range script.Calls {
		if call.Tool == "" {
			return nil, fmt.Errorf("parse the fake script %s: call %d has no tool", path, i+1)
		}
	}
	return &Engine{script: script}, nil
}

// NewFromScript builds an engine from a script already in memory, which is
// what Go tests use instead of writing a file.
func NewFromScript(script Script) *Engine { return &Engine{script: script} }

// Run replays the script against the step's gateway.
func (e *Engine) Run(ctx context.Context, req agent.Request) (agent.Result, error) {
	client, err := gateway.Connect(ctx, req.Gateway)
	if err != nil {
		return agent.Result{}, err
	}
	defer func() { _ = client.Close() }()

	transcript, err := newTranscript(req.TranscriptPath)
	if err != nil {
		return agent.Result{}, err
	}
	defer transcript.close()

	result := agent.Result{
		Usage: provider.Usage{
			InputTokens:  e.script.Usage.InputTokens,
			OutputTokens: e.script.Usage.OutputTokens,
		},
		CostUSD:    e.script.CostUSD,
		Transcript: req.TranscriptPath,
	}

	for _, call := range e.script.Calls {
		if req.MaxTurns > 0 && result.Turns >= req.MaxTurns {
			// A real agent that runs out of turns stops mid-task, and the
			// runner decides what an unsubmitted step means.
			break
		}
		if err := wait(ctx, time.Duration(call.Delay)); err != nil {
			return result, err
		}

		args, err := json.Marshal(orEmpty(call.Args))
		if err != nil {
			return result, fmt.Errorf("encode the arguments of %s: %w", call.Tool, err)
		}

		answer, err := client.Call(ctx, call.Tool, args)
		if err != nil {
			return result, err
		}
		result.Turns++
		transcript.write(call.Tool, args, answer)

		if answer.IsError {
			if call.StopOnError {
				break
			}
			continue
		}
		if call.Tool == submitTool {
			result.Submitted = true
			result.Result = args
		}
	}
	return result, nil
}

// submitTool is the one call that ends a step (spec section 7.6).
const submitTool = "submit_result"

// wait sleeps, unless the context ends first.
func wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// orEmpty keeps an argument-less call from sending a JSON null, which a tool
// schema expecting an object would reject.
func orEmpty(args map[string]any) map[string]any {
	if args == nil {
		return map[string]any{}
	}
	return args
}

// transcript records what the agent did, in the same place a real engine puts
// its transcript.
type transcript struct {
	file *os.File
}

func newTranscript(path string) (*transcript, error) {
	if path == "" {
		return &transcript{}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create the transcript directory: %w", err)
	}
	file, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create the transcript: %w", err)
	}
	return &transcript{file: file}, nil
}

// write appends one line. The transcript is a test artefact: a failure to
// write it must not fail the step.
func (t *transcript) write(tool string, args json.RawMessage, answer gateway.CallResult) {
	if t.file == nil {
		return
	}
	line, err := json.Marshal(struct {
		Tool    string          `json:"tool"`
		Args    json.RawMessage `json:"args"`
		Result  string          `json:"result"`
		IsError bool            `json:"is_error,omitempty"`
	}{Tool: tool, Args: args, Result: answer.Text, IsError: answer.IsError})
	if err != nil {
		return
	}
	_, _ = t.file.Write(append(line, '\n'))
}

func (t *transcript) close() {
	if t.file != nil {
		_ = t.file.Close()
	}
}
