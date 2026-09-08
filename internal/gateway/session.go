package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/foxzi/baton/internal/agent"
	"github.com/foxzi/baton/internal/jsonschema"
)

// Tool is one tool of a step. Handlers come from the runner: the gateway
// owns the protocol, the counting and the audit trail, not the work.
type Tool struct {
	Name string

	// Description is what the model picks the tool by, so a tool without one
	// is close to unusable.
	Description string

	// InputSchema is the JSON Schema of the arguments, as raw JSON. Empty
	// advertises a tool without arguments.
	InputSchema json.RawMessage

	// MaxCalls caps this tool's calls within the step; zero means only the
	// step's own cap applies.
	MaxCalls int

	Handler Handler
}

// Handler does the work of one call. The value it returns is what the agent
// sees: a string goes back as text, anything else as JSON.
//
// An error is data, not a failure: the agent gets its text back and can try
// something else. A handler that cannot be retried says so in its message.
type Handler func(ctx context.Context, args json.RawMessage) (any, error)

// Session is the tool set of one step, and the place its result lands.
type Session struct {
	gateway *Gateway
	stepID  string
	info    agent.GatewayInfo
	server  *mcp.Server
	result  *jsonschema.Schema
	audit   *auditor

	maxCalls int
	maxBytes int64

	mu        sync.Mutex
	calls     int
	perTool   map[string]int
	submitted json.RawMessage
	done      chan struct{}
}

// Info is what an engine needs to reach this step's tools.
func (s *Session) Info() agent.GatewayInfo { return s.info }

// Calls reports how many tool calls the step spent. A call denied by a budget
// is not one of them: it did no work, and counting it would push the number
// in run.json past the budget it was measured against.
func (s *Session) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// Result returns the submitted result, if the agent submitted one.
func (s *Session) Result() (json.RawMessage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.submitted, s.submitted != nil
}

// Done is closed when the agent submits a valid result, so that an engine can
// stop its process without waiting for the model to finish its turn.
func (s *Session) Done() <-chan struct{} { return s.done }

// Close unregisters the step. Later requests to its endpoint get a 404.
func (s *Session) Close() {
	s.gateway.mu.Lock()
	delete(s.gateway.sessions, s.stepID)
	s.gateway.mu.Unlock()
}

// call runs one tool call: it checks the caps, hands the arguments to the
// handler, caps the answer and writes the audit line.
func (s *Session) call(ctx context.Context, tool Tool, args json.RawMessage) *mcp.CallToolResult {
	started := time.Now()
	if err := s.charge(tool); err != nil {
		s.audit.write(auditLine{
			Tool:       tool.Name,
			Args:       args,
			Status:     statusDenied,
			Error:      err.Error(),
			DurationMS: since(started),
		})
		return errorResult(err.Error())
	}

	value, err := tool.Handler(ctx, args)
	if err != nil {
		s.audit.write(auditLine{
			Tool:       tool.Name,
			Args:       args,
			Status:     statusError,
			Error:      err.Error(),
			DurationMS: since(started),
		})
		return errorResult(err.Error())
	}

	text, err := renderValue(value)
	if err != nil {
		// The handler answered with something that is not JSON. That is a
		// bug in the tool, and the agent can do nothing about it, but it
		// still hears about it rather than hanging.
		s.audit.write(auditLine{
			Tool:       tool.Name,
			Args:       args,
			Status:     statusError,
			Error:      err.Error(),
			DurationMS: since(started),
		})
		return errorResult(err.Error())
	}

	text, truncated := capText(text, s.maxBytes)
	s.audit.write(auditLine{
		Tool:        tool.Name,
		Args:        args,
		Status:      statusOK,
		ResultBytes: len(text),
		Truncated:   truncated,
		DurationMS:  since(started),
	})
	return textResult(text)
}

// charge counts a call against the step's caps and the tool's own.
func (s *Session) charge(tool Tool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.calls >= s.maxCalls {
		return fmt.Errorf("tool call budget spent: the step allows %d calls", s.maxCalls)
	}
	if tool.MaxCalls > 0 && s.perTool[tool.Name] >= tool.MaxCalls {
		return fmt.Errorf("%s is spent: it allows %d calls per step", tool.Name, tool.MaxCalls)
	}

	s.calls++
	s.perTool[tool.Name]++
	return nil
}

// submitTool is how a step ends. Nothing else does: an agent that stops
// talking without calling it has not delivered a result (spec section 3.6).
func (s *Session) submitTool() Tool {
	return Tool{
		Name: "submit_result",
		Description: "Submit the step's result. The argument must be the result object itself, " +
			"matching the step's schema. On a schema error the tool answers with the error and " +
			"the step continues, so the result can be corrected and submitted again.",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(_ context.Context, args json.RawMessage) (any, error) {
			if len(args) == 0 {
				return nil, fmt.Errorf("result is missing")
			}
			if _, err := s.result.ValidateJSON(args); err != nil {
				return nil, fmt.Errorf("result does not match the schema: %w", err)
			}

			s.mu.Lock()
			first := s.submitted == nil
			s.submitted = append(json.RawMessage(nil), args...)
			s.mu.Unlock()

			if first {
				close(s.done)
			}
			return "accepted", nil
		},
	}
}

// renderValue turns a handler's answer into the text the agent sees.
func renderValue(value any) (string, error) {
	switch v := value.(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	case json.RawMessage:
		return string(v), nil
	case []byte:
		return string(v), nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("tool answer cannot be encoded: %w", err)
	}
	return string(data), nil
}

// capText enforces max_result_bytes. Cutting an answer is better than
// spending a context window on it, and the marker tells the agent what
// happened so it can narrow its request.
func capText(text string, max int64) (string, bool) {
	if max <= 0 || int64(len(text)) <= max {
		return text, false
	}
	const marker = "\n… truncated at the step's max_result_bytes"
	cut := max
	if cut > int64(len(text)) {
		cut = int64(len(text))
	}
	return text[:cut] + marker, true
}

// textResult is a successful answer, errorResult one the agent should react
// to. Both are tool-level results: a transport error would hide the reason
// from the model (spec section 7.5, where a non-zero exit code is data).
func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func errorResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}
}

// since returns whole milliseconds, the unit the audit log uses.
func since(started time.Time) int64 { return time.Since(started).Milliseconds() }
