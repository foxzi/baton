package fake

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foxzi/baton/internal/agent"
	"github.com/foxzi/baton/internal/gateway"
	"github.com/foxzi/baton/internal/jsonschema"
	"github.com/foxzi/baton/internal/units"
)

// resultSchema is what the test steps submit against.
const resultSchema = `{
	"type": "object",
	"properties": {"verdict": {"type": "string"}},
	"required": ["verdict"],
	"additionalProperties": false
}`

// step brings up a gateway with one step and the given tools, and returns the
// request an engine gets for it.
func step(t *testing.T, tools ...gateway.Tool) (*gateway.Session, agent.Request) {
	t.Helper()

	schema, err := jsonschema.Compile("result", []byte(resultSchema))
	if err != nil {
		t.Fatalf("compile the schema: %v", err)
	}

	g, err := gateway.New(gateway.Options{})
	if err != nil {
		t.Fatalf("start the gateway: %v", err)
	}
	t.Cleanup(func() { _ = g.Close() })

	session, err := g.Open(gateway.StepOptions{StepID: "step", Result: schema, Tools: tools})
	if err != nil {
		t.Fatalf("open the step: %v", err)
	}
	t.Cleanup(session.Close)

	return session, agent.Request{
		Prompt:         "review the change",
		Gateway:        session.Info(),
		TranscriptPath: filepath.Join(t.TempDir(), "transcript.jsonl"),
	}
}

// countingTool records the arguments of every call it gets.
func countingTool(name string, calls *[]string) gateway.Tool {
	return gateway.Tool{
		Name:        name,
		Description: "Record the call",
		Handler: func(_ context.Context, args json.RawMessage) (any, error) {
			*calls = append(*calls, string(args))
			return "done", nil
		},
	}
}

func run(t *testing.T, engine *Engine, req agent.Request) agent.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := engine.Run(ctx, req)
	if err != nil {
		t.Fatalf("run the fake engine: %v", err)
	}
	return result
}

func TestRunSubmitsTheResult(t *testing.T) {
	session, req := step(t)
	engine := NewFromScript(Script{Calls: []Call{{
		Tool: "submit_result",
		Args: map[string]any{"verdict": "ok"},
	}}})

	result := run(t, engine, req)
	if !result.Submitted {
		t.Fatal("the step was not submitted")
	}
	if result.Turns != 1 {
		t.Errorf("turns = %d, want 1", result.Turns)
	}

	var got struct{ Verdict string }
	if err := json.Unmarshal(result.Result, &got); err != nil {
		t.Fatalf("decode the result: %v", err)
	}
	if got.Verdict != "ok" {
		t.Errorf("verdict = %q, want ok", got.Verdict)
	}

	stored, ok := session.Result()
	if !ok {
		t.Fatal("the gateway kept no result")
	}
	if string(stored) != string(result.Result) {
		t.Errorf("gateway result = %s, engine result = %s", stored, result.Result)
	}
}

func TestRunReplaysEveryCallInOrder(t *testing.T) {
	var calls []string
	_, req := step(t, countingTool("test", &calls), countingTool("lint", &calls))
	engine := NewFromScript(Script{Calls: []Call{
		{Tool: "test", Args: map[string]any{"path": "./..."}},
		{Tool: "lint"},
		{Tool: "submit_result", Args: map[string]any{"verdict": "ok"}},
	}})

	result := run(t, engine, req)
	if result.Turns != 3 {
		t.Errorf("turns = %d, want 3", result.Turns)
	}
	if len(calls) != 2 {
		t.Fatalf("the tools saw %d calls, want 2", len(calls))
	}
	if !strings.Contains(calls[0], `"path":"./..."`) {
		t.Errorf("first call args = %s, want the scripted arguments", calls[0])
	}
	if calls[1] != `{}` {
		t.Errorf("second call args = %s, want an empty object", calls[1])
	}
}

func TestRunWithoutSubmitReportsNotSubmitted(t *testing.T) {
	var calls []string
	_, req := step(t, countingTool("test", &calls))
	engine := NewFromScript(Script{Calls: []Call{{Tool: "test"}}})

	result := run(t, engine, req)
	if result.Submitted {
		t.Error("a script that never submits reported a submitted step")
	}
	if result.Result != nil {
		t.Errorf("result = %s, want none", result.Result)
	}
}

func TestRunDoesNotSubmitOnASchemaError(t *testing.T) {
	_, req := step(t)
	engine := NewFromScript(Script{Calls: []Call{{
		Tool: "submit_result",
		Args: map[string]any{"verdict": 42},
	}}})

	result := run(t, engine, req)
	if result.Submitted {
		t.Error("a result rejected by the schema counted as submitted")
	}
	if result.Turns != 1 {
		t.Errorf("turns = %d, want the rejected call counted", result.Turns)
	}
}

func TestRunContinuesAfterAToolError(t *testing.T) {
	var calls []string
	_, req := step(t,
		gateway.Tool{
			Name:    "broken",
			Handler: func(context.Context, json.RawMessage) (any, error) { return nil, fmt.Errorf("boom") },
		},
		countingTool("test", &calls),
	)
	engine := NewFromScript(Script{Calls: []Call{
		{Tool: "broken"},
		{Tool: "test"},
		{Tool: "submit_result", Args: map[string]any{"verdict": "ok"}},
	}})

	result := run(t, engine, req)
	if !result.Submitted {
		t.Error("the script stopped at a tool error it was not told to stop at")
	}
	if len(calls) != 1 {
		t.Errorf("the tool after the error saw %d calls, want 1", len(calls))
	}
}

func TestRunStopsOnErrorWhenAsked(t *testing.T) {
	var calls []string
	_, req := step(t,
		gateway.Tool{
			Name:    "broken",
			Handler: func(context.Context, json.RawMessage) (any, error) { return nil, fmt.Errorf("boom") },
		},
		countingTool("test", &calls),
	)
	engine := NewFromScript(Script{Calls: []Call{
		{Tool: "broken", StopOnError: true},
		{Tool: "test"},
	}})

	result := run(t, engine, req)
	if result.Turns != 1 {
		t.Errorf("turns = %d, want the script to stop at the failing call", result.Turns)
	}
	if len(calls) != 0 {
		t.Errorf("the tool after the error saw %d calls, want none", len(calls))
	}
}

func TestRunStopsAtMaxTurns(t *testing.T) {
	var calls []string
	_, req := step(t, countingTool("test", &calls))
	req.MaxTurns = 2
	engine := NewFromScript(Script{Calls: []Call{
		{Tool: "test"},
		{Tool: "test"},
		{Tool: "submit_result", Args: map[string]any{"verdict": "ok"}},
	}})

	result := run(t, engine, req)
	if result.Turns != 2 {
		t.Errorf("turns = %d, want the turn cap to hold", result.Turns)
	}
	if result.Submitted {
		t.Error("a script cut off by the turn cap reported a submitted step")
	}
}

func TestRunReportsTheScriptedUsageAndCost(t *testing.T) {
	_, req := step(t)
	engine := NewFromScript(Script{
		Calls:   []Call{{Tool: "submit_result", Args: map[string]any{"verdict": "ok"}}},
		Usage:   Usage{InputTokens: 120, OutputTokens: 40},
		CostUSD: 0.02,
	})

	result := run(t, engine, req)
	if result.Usage.InputTokens != 120 || result.Usage.OutputTokens != 40 {
		t.Errorf("usage = %+v, want the scripted tokens", result.Usage)
	}
	if result.CostUSD != 0.02 {
		t.Errorf("cost = %v, want 0.02", result.CostUSD)
	}
}

func TestRunWritesTheTranscript(t *testing.T) {
	var calls []string
	_, req := step(t, countingTool("test", &calls))
	engine := NewFromScript(Script{Calls: []Call{
		{Tool: "test", Args: map[string]any{"path": "./..."}},
		{Tool: "submit_result", Args: map[string]any{"verdict": "ok"}},
	}})

	result := run(t, engine, req)
	if result.Transcript != req.TranscriptPath {
		t.Errorf("transcript = %q, want %q", result.Transcript, req.TranscriptPath)
	}

	data, err := os.ReadFile(req.TranscriptPath)
	if err != nil {
		t.Fatalf("read the transcript: %v", err)
	}

	var lines []transcriptLine
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		var line transcriptLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("decode the transcript line %q: %v", scanner.Text(), err)
		}
		lines = append(lines, line)
	}
	if len(lines) != 2 {
		t.Fatalf("the transcript has %d lines, want one per call", len(lines))
	}
	if lines[0].Tool != "test" || !strings.Contains(string(lines[0].Args), "./...") {
		t.Errorf("first line = %+v, want the test call with its arguments", lines[0])
	}
	if lines[0].Result != "done" {
		t.Errorf("first line result = %q, want the tool's answer", lines[0].Result)
	}
	if lines[1].Tool != "submit_result" {
		t.Errorf("second line = %+v, want the submit call", lines[1])
	}
}

func TestRunWithoutATranscriptPathIsNotAnError(t *testing.T) {
	_, req := step(t)
	req.TranscriptPath = ""
	engine := NewFromScript(Script{Calls: []Call{{
		Tool: "submit_result",
		Args: map[string]any{"verdict": "ok"},
	}}})

	if result := run(t, engine, req); !result.Submitted {
		t.Error("the step was not submitted")
	}
}

func TestRunHonoursTheDelayAndTheContext(t *testing.T) {
	var calls []string
	_, req := step(t, countingTool("test", &calls))
	engine := NewFromScript(Script{Calls: []Call{
		{Tool: "test", Delay: units.Duration(time.Hour)},
	}})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	started := time.Now()
	if _, err := engine.Run(ctx, req); err == nil {
		t.Fatal("a cancelled run reported success")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("the run took %v, want it to end with the context", elapsed)
	}
	if len(calls) != 0 {
		t.Errorf("the tool saw %d calls, want the delay to hold the call back", len(calls))
	}
}

func TestRunFailsWithoutAGateway(t *testing.T) {
	engine := NewFromScript(Script{Calls: []Call{{Tool: "submit_result"}}})
	req := agent.Request{Gateway: agent.GatewayInfo{URL: "http://127.0.0.1:1/mcp", Token: "t"}}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := engine.Run(ctx, req); err == nil {
		t.Fatal("a run without a reachable gateway reported success")
	}
}

func TestNewReadsAScriptFile(t *testing.T) {
	path := writeScript(t, `
calls:
  - tool: test
    args: { path: "./..." }
    delay: 10ms
  - tool: submit_result
    args: { verdict: ok }
usage: { input_tokens: 10, output_tokens: 5 }
cost_usd: 0.001
`)

	engine, err := New(path)
	if err != nil {
		t.Fatalf("load the script: %v", err)
	}

	var calls []string
	_, req := step(t, countingTool("test", &calls))
	result := run(t, engine, req)
	if !result.Submitted {
		t.Error("the step was not submitted")
	}
	if result.Usage.InputTokens != 10 || result.CostUSD != 0.001 {
		t.Errorf("usage = %+v, cost = %v, want the file's numbers", result.Usage, result.CostUSD)
	}
	if len(calls) != 1 {
		t.Errorf("the tool saw %d calls, want 1", len(calls))
	}
}

func TestNewRejectsAMissingFile(t *testing.T) {
	if _, err := New(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatal("a missing script file was accepted")
	}
}

func TestNewRejectsAnUnknownField(t *testing.T) {
	path := writeScript(t, "calls: [{tool: test, arguments: {}}]\n")
	if _, err := New(path); err == nil {
		t.Fatal("a script with an unknown field was accepted")
	}
}

func TestNewRejectsACallWithoutATool(t *testing.T) {
	path := writeScript(t, "calls: [{args: {path: x}}]\n")
	_, err := New(path)
	if err == nil {
		t.Fatal("a call without a tool was accepted")
	}
	if !strings.Contains(err.Error(), "no tool") {
		t.Errorf("error = %v, want it to name the missing tool", err)
	}
}

// transcriptLine is one line of the engine's transcript.
type transcriptLine struct {
	Tool    string          `json:"tool"`
	Args    json.RawMessage `json:"args"`
	Result  string          `json:"result"`
	IsError bool            `json:"is_error"`
}

func writeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "script.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write the script: %v", err)
	}
	return path
}
