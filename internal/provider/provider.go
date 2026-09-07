// Package provider talks to language model providers behind one interface.
//
// The specification describes it in section 8.3: one Provider interface with
// implementations for Anthropic, OpenAI and any OpenAI-compatible endpoint.
// Messages, tools and the desired result schema are normalized here, so the
// runner's tool loop and its structured-output strategy are written once.
package provider

import (
	"context"
	"encoding/json"
)

// Role of a normalized message.
type Role string

// Roles a normalized conversation is made of. A tool result is a message of
// its own; providers place it where their own wire format wants it.
const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one normalized turn. A user or tool message carries Text; an
// assistant message carries Text, ToolCalls or both.
type Message struct {
	Role Role
	Text string

	// ToolCalls are the calls an assistant turn asked for.
	ToolCalls []ToolCall

	// ToolCallID identifies the call a tool message answers.
	ToolCallID string
}

// ToolCall is a request from the model to run one tool.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// Tool is one tool offered to the model.
type Tool struct {
	Name        string
	Description string

	// Schema is the JSON Schema of the tool's arguments.
	Schema json.RawMessage
}

// Request is a normalized completion request.
type Request struct {
	// Model is the provider-side model name, without the provider prefix
	// the scenario spells (spec section 3.5).
	Model string

	System   string
	Messages []Message
	Tools    []Tool

	// Schema is the JSON Schema the result must validate against, and Mode
	// how the provider is asked to honour it. In ModePrompt the provider
	// ignores both: the instruction is already part of the prompt.
	Schema json.RawMessage
	Mode   Mode

	MaxTokens   int
	Temperature *float64
}

// Response is a normalized completion response.
type Response struct {
	Text      string
	ToolCalls []ToolCall

	Usage Usage

	// CostUSD is set only when the provider reported a cost itself.
	// Otherwise the caller prices the usage from the pricing table (spec
	// section 8.3).
	CostUSD *float64

	FinishReason string

	// Model is the model the provider says answered, which may be more
	// specific than the one that was asked for.
	Model string
}

// Usage counts the tokens one response cost.
type Usage struct {
	InputTokens  int
	OutputTokens int
	CachedTokens int
}

// Caps is what a provider can do. The runner picks the structured-output
// strategy from it (spec section 8.3).
type Caps struct {
	StructuredOutput bool
	Tools            bool
	Vision           bool
	MaxContext       int
}

// Provider is one language model backend.
type Provider interface {
	Complete(ctx context.Context, req Request) (Response, error)
	Capabilities() Caps
}
