package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/foxzi/baton/internal/gateway"
	"github.com/foxzi/baton/internal/httpx"
	"github.com/foxzi/baton/internal/scenario"
)

// ToolInfo is one tool an agent step is given, as `baton tools` reports it
// (spec section 11).
type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`

	// MaxCalls is the tool's own cap within the step; zero means only the
	// step's cap applies.
	MaxCalls int `json:"max_calls,omitempty"`
}

// StepTools answers what the agent of one step would see: the same tool set
// a run builds, plus the submit_result the gateway adds, and without opening
// the gateway or starting the agent. A step with proxied MCP servers starts
// them to ask what they offer, and stops them again before returning.
//
// Everything the step leaves to the runner is resolved on the way, so a step
// naming a command it cannot run or an operation no pack has is reported here
// rather than at the first tool call.
func (e *Engine) StepTools(ctx context.Context, stepID string) ([]ToolInfo, error) {
	step := findStep(e.opts.Scenario.Steps, stepID)
	if step == nil {
		return nil, fmt.Errorf("no step %q in %s", stepID, e.opts.Scenario.Name)
	}
	if step.Agent == nil {
		return nil, fmt.Errorf("step %s is a %s step: only an agent step has tools", stepID, step.Kind())
	}

	call, stepErr := e.prepareAgentPolicy(step)
	if stepErr != nil {
		return nil, stepErr
	}
	// The workspace is taken as configured rather than prepared: listing
	// tools must not rewrite a repository's git configuration (section 7.1).
	set, closeTools, stepErr := e.agentToolSet(ctx, step, call, e.opts.Workspace)
	if stepErr != nil {
		return nil, stepErr
	}
	defer closeTools()

	infos := make([]ToolInfo, 0, len(set)+1)
	for _, tool := range set {
		infos = append(infos, ToolInfo{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.InputSchema,
			MaxCalls:    tool.MaxCalls,
		})
	}
	return append(infos, ToolInfo{
		Name:        gateway.SubmitToolName,
		Description: gateway.SubmitToolDescription,
		InputSchema: json.RawMessage(gateway.SubmitToolSchema),
	}), nil
}

// findStep looks a step up by id, descending into foreach bodies: a step
// inside a foreach is still a step someone can ask about.
func findStep(steps []scenario.Step, id string) *scenario.Step {
	for i := range steps {
		if found := findStepIn(&steps[i], id); found != nil {
			return found
		}
	}
	return nil
}

func findStepIn(step *scenario.Step, id string) *scenario.Step {
	if step.ID == id {
		return step
	}
	if step.Foreach != nil && step.Foreach.Step != nil {
		return findStepIn(step.Foreach.Step, id)
	}
	if step.Until != nil && step.Until.Step != nil {
		return findStepIn(step.Until.Step, id)
	}
	return nil
}

// CallOp calls one operation of one apis entry directly, as `baton apis
// call` does: it lets a pack author try an operation against the real
// service without writing a scenario step to hold it.
func (e *Engine) CallOp(ctx context.Context, apiName, opName string, args map[string]any) (*httpx.OpResult, error) {
	api, stepErr := e.api(apiName, "")
	if stepErr != nil {
		return nil, stepErr
	}
	callCtx, cancel := apiContext(ctx, api)
	defer cancel()
	return e.httpClient().Op(callCtx, api, opName, args)
}
