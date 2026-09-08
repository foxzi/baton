package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/foxzi/baton/internal/jsonschema"
	"github.com/foxzi/baton/internal/secrets"
	"github.com/foxzi/baton/internal/values"
)

// resultSchema is the schema most tests submit against: one required string.
const resultSchema = `{
	"type": "object",
	"properties": {"answer": {"type": "string"}},
	"required": ["answer"],
	"additionalProperties": false
}`

func compileSchema(t *testing.T, text string) *jsonschema.Schema {
	t.Helper()
	schema, err := jsonschema.Compile("result", []byte(text))
	if err != nil {
		t.Fatalf("compile the schema: %v", err)
	}
	return schema
}

// start brings up a gateway and closes it with the test.
func start(t *testing.T, opts Options) *Gateway {
	t.Helper()
	g, err := New(opts)
	if err != nil {
		t.Fatalf("start the gateway: %v", err)
	}
	t.Cleanup(func() { _ = g.Close() })
	return g
}

// open registers a step and closes its session with the test.
func open(t *testing.T, g *Gateway, opts StepOptions) *Session {
	t.Helper()
	if opts.StepID == "" {
		opts.StepID = "step"
	}
	if opts.Result == nil {
		opts.Result = compileSchema(t, resultSchema)
	}
	session, err := g.Open(opts)
	if err != nil {
		t.Fatalf("open the step: %v", err)
	}
	t.Cleanup(session.Close)
	return session
}

// connect opens a client session against a step and closes it with the test.
func connect(t *testing.T, session *Session) *Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := Connect(ctx, session.Info())
	if err != nil {
		t.Fatalf("connect to the step: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// call is the short form of one tool call with JSON arguments.
func call(t *testing.T, client *Client, name, args string) CallResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := client.Call(ctx, name, json.RawMessage(args))
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return res
}

// echoTool answers with the arguments it was given.
func echoTool() Tool {
	return Tool{
		Name:        "echo",
		Description: "Echo the argument back",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`),
		Handler: func(_ context.Context, args json.RawMessage) (any, error) {
			var in struct{ Text string }
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, err
			}
			return in.Text, nil
		},
	}
}

func TestBaseURLIsLoopback(t *testing.T) {
	g := start(t, Options{})
	if !strings.HasPrefix(g.BaseURL(), "http://127.0.0.1:") {
		t.Errorf("base url %q is not on the loopback interface", g.BaseURL())
	}
}

func TestNewGeneratesAToken(t *testing.T) {
	g := start(t, Options{})
	if len(g.Token()) != 64 {
		t.Errorf("token %q is not 32 random bytes in hex", g.Token())
	}
	other := start(t, Options{})
	if g.Token() == other.Token() {
		t.Error("two gateways got the same token")
	}
}

func TestNewKeepsTheGivenToken(t *testing.T) {
	g := start(t, Options{Token: "fixed"})
	if g.Token() != "fixed" {
		t.Errorf("token = %q, want fixed", g.Token())
	}
}

func TestOpenRequiresAStepID(t *testing.T) {
	g := start(t, Options{})
	if _, err := g.Open(StepOptions{Result: compileSchema(t, resultSchema)}); err == nil {
		t.Fatal("a step without an id was accepted")
	}
}

func TestOpenRequiresAResultSchema(t *testing.T) {
	g := start(t, Options{})
	if _, err := g.Open(StepOptions{StepID: "step"}); err == nil {
		t.Fatal("a step without a result schema was accepted")
	}
}

func TestOpenRejectsADuplicateStep(t *testing.T) {
	g := start(t, Options{})
	open(t, g, StepOptions{StepID: "step"})

	_, err := g.Open(StepOptions{StepID: "step", Result: compileSchema(t, resultSchema)})
	if err == nil {
		t.Fatal("the same step id was opened twice")
	}
	if !strings.Contains(err.Error(), "already open") {
		t.Errorf("error = %v, want it to mention that the step is already open", err)
	}
}

func TestOpenRejectsAToolWithoutAName(t *testing.T) {
	g := start(t, Options{})
	_, err := g.Open(StepOptions{
		StepID: "step",
		Result: compileSchema(t, resultSchema),
		Tools:  []Tool{{Description: "nameless"}},
	})
	if err == nil {
		t.Fatal("a tool without a name was accepted")
	}
}

func TestOpenRejectsAnInvalidInputSchema(t *testing.T) {
	g := start(t, Options{})
	_, err := g.Open(StepOptions{
		StepID: "step",
		Result: compileSchema(t, resultSchema),
		Tools:  []Tool{{Name: "broken", InputSchema: json.RawMessage(`{`)}},
	})
	if err == nil {
		t.Fatal("a tool with an unparseable input schema was accepted")
	}
}

func TestToolsListHoldsTheStepToolsAndSubmit(t *testing.T) {
	g := start(t, Options{})
	session := open(t, g, StepOptions{Tools: []Tool{echoTool()}})
	client := connect(t, session)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tools, err := client.Tools(ctx)
	if err != nil {
		t.Fatalf("list the tools: %v", err)
	}

	names := make(map[string]ToolInfo, len(tools))
	for _, tool := range tools {
		names[tool.Name] = tool
	}
	if len(names) != 2 {
		t.Fatalf("tools = %v, want echo and submit_result", names)
	}
	if _, ok := names["submit_result"]; !ok {
		t.Error("submit_result is missing from the tool list")
	}
	echo, ok := names["echo"]
	if !ok {
		t.Fatal("echo is missing from the tool list")
	}
	if echo.Description != "Echo the argument back" {
		t.Errorf("description = %q, want the tool's own", echo.Description)
	}
	if !strings.Contains(string(echo.InputSchema), `"text"`) {
		t.Errorf("input schema = %s, want the tool's own", echo.InputSchema)
	}
}

func TestToolWithoutASchemaAdvertisesAnEmptyObject(t *testing.T) {
	g := start(t, Options{})
	session := open(t, g, StepOptions{Tools: []Tool{{
		Name:    "now",
		Handler: func(context.Context, json.RawMessage) (any, error) { return "answer", nil },
	}}})
	client := connect(t, session)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tools, err := client.Tools(ctx)
	if err != nil {
		t.Fatalf("list the tools: %v", err)
	}
	for _, tool := range tools {
		if tool.Name != "now" {
			continue
		}
		var schema struct {
			Type       string         `json:"type"`
			Properties map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatalf("decode the input schema: %v", err)
		}
		if schema.Type != "object" || len(schema.Properties) != 0 {
			t.Errorf("input schema = %s, want an empty object", tool.InputSchema)
		}
		return
	}
	t.Fatal("the tool is missing from the tool list")
}

func TestCallReachesTheHandler(t *testing.T) {
	g := start(t, Options{})
	session := open(t, g, StepOptions{Tools: []Tool{echoTool()}})
	client := connect(t, session)

	res := call(t, client, "echo", `{"text":"hello"}`)
	if res.IsError {
		t.Fatalf("call answered with an error: %q", res.Text)
	}
	if res.Text != "hello" {
		t.Errorf("text = %q, want hello", res.Text)
	}
}

func TestCallEncodesAStructuredAnswerAsJSON(t *testing.T) {
	g := start(t, Options{})
	session := open(t, g, StepOptions{Tools: []Tool{{
		Name: "stat",
		Handler: func(context.Context, json.RawMessage) (any, error) {
			return map[string]any{"size": 12}, nil
		},
	}}})
	client := connect(t, session)

	res := call(t, client, "stat", `{}`)
	if res.Text != `{"size":12}` {
		t.Errorf("text = %q, want the answer as JSON", res.Text)
	}
}

func TestCallReportsAHandlerErrorAsData(t *testing.T) {
	g := start(t, Options{})
	session := open(t, g, StepOptions{Tools: []Tool{{
		Name: "broken",
		Handler: func(context.Context, json.RawMessage) (any, error) {
			return nil, fmt.Errorf("no such file")
		},
	}}})
	client := connect(t, session)

	res := call(t, client, "broken", `{}`)
	if !res.IsError {
		t.Error("a handler error did not come back as a tool error")
	}
	if !strings.Contains(res.Text, "no such file") {
		t.Errorf("text = %q, want the handler's message", res.Text)
	}
}

func TestCallReportsAnUnencodableAnswer(t *testing.T) {
	g := start(t, Options{})
	session := open(t, g, StepOptions{Tools: []Tool{{
		Name: "cycle",
		Handler: func(context.Context, json.RawMessage) (any, error) {
			return func() {}, nil
		},
	}}})
	client := connect(t, session)

	res := call(t, client, "cycle", `{}`)
	if !res.IsError {
		t.Error("an answer that cannot be encoded did not come back as a tool error")
	}
}

func TestCallCountsAgainstTheStepBudget(t *testing.T) {
	g := start(t, Options{})
	session := open(t, g, StepOptions{Tools: []Tool{echoTool()}, MaxToolCalls: 2})
	client := connect(t, session)

	call(t, client, "echo", `{"text":"one"}`)
	call(t, client, "echo", `{"text":"two"}`)

	res := call(t, client, "echo", `{"text":"three"}`)
	if !res.IsError {
		t.Fatal("the third call was allowed past a budget of two")
	}
	if !strings.Contains(res.Text, "budget") {
		t.Errorf("text = %q, want it to mention the budget", res.Text)
	}
	if session.Calls() != 2 {
		t.Errorf("calls = %d, want the denied call not counted", session.Calls())
	}
}

func TestCallCountsAgainstThePerToolBudget(t *testing.T) {
	g := start(t, Options{})
	tool := echoTool()
	tool.MaxCalls = 1
	session := open(t, g, StepOptions{Tools: []Tool{tool}, MaxToolCalls: 10})
	client := connect(t, session)

	call(t, client, "echo", `{"text":"one"}`)
	res := call(t, client, "echo", `{"text":"two"}`)
	if !res.IsError {
		t.Fatal("the second call was allowed past a per-tool budget of one")
	}
	if !strings.Contains(res.Text, "echo") {
		t.Errorf("text = %q, want it to name the spent tool", res.Text)
	}
}

func TestCallTruncatesALongAnswer(t *testing.T) {
	g := start(t, Options{})
	session := open(t, g, StepOptions{
		Tools: []Tool{{
			Name: "big",
			Handler: func(context.Context, json.RawMessage) (any, error) {
				return strings.Repeat("x", 100), nil
			},
		}},
		MaxResultBytes: 10,
	})
	client := connect(t, session)

	res := call(t, client, "big", `{}`)
	if !strings.HasPrefix(res.Text, strings.Repeat("x", 10)) {
		t.Errorf("text = %q, want it to start with the first 10 bytes", res.Text)
	}
	if !strings.Contains(res.Text, "truncated") {
		t.Errorf("text = %q, want a truncation marker", res.Text)
	}
}

func TestSubmitResultAcceptsAValidResult(t *testing.T) {
	g := start(t, Options{})
	session := open(t, g, StepOptions{})
	client := connect(t, session)

	if _, ok := session.Result(); ok {
		t.Fatal("the step has a result before anything was submitted")
	}

	res := call(t, client, "submit_result", `{"answer":"done"}`)
	if res.IsError {
		t.Fatalf("a valid result was rejected: %q", res.Text)
	}

	result, ok := session.Result()
	if !ok {
		t.Fatal("the step has no result after a valid submit")
	}
	if string(result) != `{"answer":"done"}` {
		t.Errorf("result = %s, want the submitted object", result)
	}
	select {
	case <-session.Done():
	default:
		t.Error("Done was not closed after a valid submit")
	}
}

func TestSubmitResultRejectsAResultAgainstTheSchema(t *testing.T) {
	g := start(t, Options{})
	session := open(t, g, StepOptions{})
	client := connect(t, session)

	res := call(t, client, "submit_result", `{"answer":42}`)
	if !res.IsError {
		t.Fatal("a result of the wrong type was accepted")
	}
	if !strings.Contains(res.Text, "schema") {
		t.Errorf("text = %q, want it to mention the schema", res.Text)
	}
	if _, ok := session.Result(); ok {
		t.Error("an invalid result was kept")
	}
	select {
	case <-session.Done():
		t.Error("Done was closed by an invalid result")
	default:
	}
}

func TestSubmitResultCanBeCorrectedAndSubmittedAgain(t *testing.T) {
	g := start(t, Options{})
	session := open(t, g, StepOptions{})
	client := connect(t, session)

	call(t, client, "submit_result", `{"answer":42}`)
	res := call(t, client, "submit_result", `{"answer":"second try"}`)
	if res.IsError {
		t.Fatalf("the corrected result was rejected: %q", res.Text)
	}

	result, _ := session.Result()
	if string(result) != `{"answer":"second try"}` {
		t.Errorf("result = %s, want the corrected object", result)
	}
}

func TestSubmitResultTwiceKeepsTheLastResult(t *testing.T) {
	g := start(t, Options{})
	session := open(t, g, StepOptions{})
	client := connect(t, session)

	call(t, client, "submit_result", `{"answer":"first"}`)
	call(t, client, "submit_result", `{"answer":"second"}`)

	result, _ := session.Result()
	if string(result) != `{"answer":"second"}` {
		t.Errorf("result = %s, want the last submitted object", result)
	}
}

func TestUnknownToolIsNotAvailable(t *testing.T) {
	g := start(t, Options{})
	session := open(t, g, StepOptions{Tools: []Tool{echoTool()}})
	client := connect(t, session)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := client.Call(ctx, "read_file", nil)
	if err == nil && !res.IsError {
		t.Fatal("a tool the step does not have answered successfully")
	}
	if session.Calls() != 0 {
		t.Errorf("calls = %d, want an unknown tool not to be counted", session.Calls())
	}
	if session.PolicyViolation() == "" {
		t.Error("PolicyViolation() = \"\", want the refused call remembered")
	}
}

func TestRequestWithoutTheTokenIsRejected(t *testing.T) {
	g := start(t, Options{})
	session := open(t, g, StepOptions{})

	res, err := http.Post(session.Info().URL, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("post to the endpoint: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", res.StatusCode)
	}
}

func TestRequestWithTheWrongTokenIsRejected(t *testing.T) {
	g := start(t, Options{})
	session := open(t, g, StepOptions{})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	info := session.Info()
	info.Token = "wrong"
	if _, err := Connect(ctx, info); err == nil {
		t.Fatal("a client with the wrong token connected")
	}
}

func TestRequestForAnotherStepIsRejected(t *testing.T) {
	g := start(t, Options{})
	session := open(t, g, StepOptions{StepID: "first"})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	info := session.Info()
	info.URL = strings.Replace(info.URL, "/steps/first/", "/steps/second/", 1)
	if _, err := Connect(ctx, info); err == nil {
		t.Fatal("a client reached a step that is not open")
	}
}

func TestClosedStepStopsAnswering(t *testing.T) {
	g := start(t, Options{})
	session := open(t, g, StepOptions{Tools: []Tool{echoTool()}})
	info := session.Info()
	session.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := Connect(ctx, info); err == nil {
		t.Fatal("a client connected to a closed step")
	}
}

func TestAuditRecordsEveryCall(t *testing.T) {
	var log bytes.Buffer
	g := start(t, Options{})
	session := open(t, g, StepOptions{
		Tools:        []Tool{echoTool()},
		MaxToolCalls: 1,
		Audit:        &log,
	})
	client := connect(t, session)

	call(t, client, "echo", `{"text":"hello"}`)
	call(t, client, "echo", `{"text":"denied"}`)

	lines := auditLines(t, &log)
	if len(lines) != 2 {
		t.Fatalf("audit has %d lines, want one per call", len(lines))
	}

	first := lines[0]
	if first.Tool != "echo" || first.Status != statusOK {
		t.Errorf("first line = %+v, want an ok echo call", first)
	}
	if first.Time == "" {
		t.Error("first line has no timestamp")
	}
	if _, err := time.Parse(time.RFC3339Nano, first.Time); err != nil {
		t.Errorf("timestamp %q is not RFC 3339: %v", first.Time, err)
	}
	if !strings.Contains(string(first.Args), "hello") {
		t.Errorf("args = %s, want the call's arguments", first.Args)
	}
	if first.ResultBytes != len("hello") {
		t.Errorf("result_bytes = %d, want %d", first.ResultBytes, len("hello"))
	}

	if lines[1].Status != statusDenied {
		t.Errorf("second line status = %q, want %q", lines[1].Status, statusDenied)
	}
	if lines[1].Error == "" {
		t.Error("a denied call has no reason in the audit trail")
	}
}

func TestAuditRecordsAHandlerError(t *testing.T) {
	var log bytes.Buffer
	g := start(t, Options{})
	session := open(t, g, StepOptions{
		Tools: []Tool{{
			Name:    "broken",
			Handler: func(context.Context, json.RawMessage) (any, error) { return nil, fmt.Errorf("boom") },
		}},
		Audit: &log,
	})
	client := connect(t, session)

	call(t, client, "broken", `{}`)

	lines := auditLines(t, &log)
	if len(lines) != 1 {
		t.Fatalf("audit has %d lines, want one", len(lines))
	}
	if lines[0].Status != statusError || lines[0].Error != "boom" {
		t.Errorf("line = %+v, want an error line carrying the handler's message", lines[0])
	}
}

func TestAuditMarksATruncatedAnswer(t *testing.T) {
	var log bytes.Buffer
	g := start(t, Options{})
	session := open(t, g, StepOptions{
		Tools: []Tool{{
			Name: "big",
			Handler: func(context.Context, json.RawMessage) (any, error) {
				return strings.Repeat("x", 100), nil
			},
		}},
		MaxResultBytes: 10,
		Audit:          &log,
	})
	client := connect(t, session)

	call(t, client, "big", `{}`)

	lines := auditLines(t, &log)
	if len(lines) != 1 {
		t.Fatalf("audit has %d lines, want one", len(lines))
	}
	if !lines[0].Truncated {
		t.Error("a truncated answer is not marked in the audit trail")
	}
}

func TestAuditRedactsASecretInTheArguments(t *testing.T) {
	var log bytes.Buffer
	secret := values.NewSecret("token", "s3cr3t")
	g := start(t, Options{Redactor: secrets.NewRedactor(secret)})
	session := open(t, g, StepOptions{Tools: []Tool{echoTool()}, Audit: &log})
	client := connect(t, session)

	call(t, client, "echo", `{"text":"s3cr3t"}`)

	if strings.Contains(log.String(), "s3cr3t") {
		t.Errorf("audit trail carries the secret: %s", log.String())
	}
	if !strings.Contains(log.String(), values.Redacted) {
		t.Errorf("audit trail has no redaction marker: %s", log.String())
	}
}

func TestAuditWithoutAWriterIsNotAnError(t *testing.T) {
	g := start(t, Options{})
	session := open(t, g, StepOptions{Tools: []Tool{echoTool()}})
	client := connect(t, session)

	res := call(t, client, "echo", `{"text":"hello"}`)
	if res.IsError {
		t.Fatalf("a call without an audit writer failed: %q", res.Text)
	}
}

// auditLines decodes the audit trail written so far.
func auditLines(t *testing.T, log *bytes.Buffer) []auditLine {
	t.Helper()

	var lines []auditLine
	scanner := bufio.NewScanner(bytes.NewReader(log.Bytes()))
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var line auditLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("decode the audit line %q: %v", scanner.Text(), err)
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read the audit trail: %v", err)
	}
	return lines
}
