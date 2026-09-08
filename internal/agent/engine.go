// Package agent holds what the runner and the agent engines agree on: the
// Engine interface every engine implements (spec section 8.1) and the tool
// policy a step's profile and tools block resolve to (spec sections 7.2 and
// 7.3).
package agent

import (
	"context"
	"encoding/json"

	"github.com/foxzi/baton/internal/provider"
)

// Engine runs one agent step: it hands the prompt to an agent, lets it work
// through the gateway's tools, and reports what came back.
type Engine interface {
	Run(ctx context.Context, req Request) (Result, error)
}

// Request is everything an engine needs for one step. The engine never sees
// the scenario: the runner has already resolved templates, profiles and
// limits.
type Request struct {
	Prompt string
	System string

	// Model is the engine's own model name, empty when the engine decides.
	Model string

	// Workspace is the directory the agent works in, already prepared by the
	// runner (spec section 7.1).
	Workspace string

	// Skills are directories holding a SKILL.md.
	Skills []string

	// Gateway is where the agent's tools live; the engine passes it to the
	// agent and nothing else.
	Gateway GatewayInfo

	// Tools is what the agent may do, resolved from the step's profile. An
	// engine uses it for its own built-in tools; the gateway enforces the
	// rest.
	Tools Policy

	MaxTurns  int
	BudgetUSD float64

	// Env are the extra environment variables the agent's process gets on
	// top of the engine's minimal allow list. Secrets are already resolved,
	// so this map must never be written to disk.
	Env map[string]string

	// TranscriptPath is where the engine writes the agent's transcript.
	TranscriptPath string
}

// GatewayInfo locates the gateway for one step: its MCP endpoint and the
// bearer token that is good for this run only (spec section 7.7).
type GatewayInfo struct {
	URL   string
	Token string
}

// Result is what an engine reports back about a step.
type Result struct {
	// Submitted says whether the agent called submit_result. A step that
	// ends without it is an error, and the runner, not the engine, says so.
	Submitted  bool
	Result     json.RawMessage
	Usage      provider.Usage
	CostUSD    float64
	Turns      int
	Transcript string
}
