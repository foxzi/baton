package provider

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/foxzi/baton/internal/config"
	"github.com/foxzi/baton/internal/values"
)

// anthropicDefaultMaxTokens is used when a step does not set max_tokens: the
// Messages API rejects a request without one, and the specification does not
// prescribe a value (section 8.3).
const anthropicDefaultMaxTokens = 4096

// anthropicProvider implements Provider on top of the official Go SDK
// (spec section 8.3: "Messages API, официальный Go SDK").
type anthropicProvider struct {
	client anthropic.Client
	caps   Caps
}

// newAnthropic builds the Anthropic backend. Retries are turned off on the
// SDK client: the engine already retries transient failures with its own
// backoff (spec section 9.1), and letting the SDK retry underneath it would
// both double the delay and hide the transient class from the engine until
// the SDK's own attempts are exhausted.
func newAnthropic(cfg config.Provider, apiKey values.Secret) (Provider, error) {
	key, err := requireKey(cfg.Kind, apiKey)
	if err != nil {
		return nil, err
	}

	opts := []option.RequestOption{
		option.WithAPIKey(key),
		option.WithMaxRetries(0),
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}

	caps := applyCaps(Caps{
		StructuredOutput: true,
		Tools:            true,
		Vision:           true,
	}, cfg.Capabilities)

	return &anthropicProvider{
		client: anthropic.NewClient(opts...),
		caps:   caps,
	}, nil
}

// Capabilities reports what this backend can do. StructuredOutput is true:
// v1.71.0 exposes a native JSON Schema response format through
// MessageNewParams.OutputConfig.Format (a JSONOutputFormatParam), documented
// at https://platform.claude.com/docs/en/build-with-claude/structured-outputs
// and not gated behind a beta header. Tools and Vision are the Messages
// API's own tool-use and image-input support.
func (p *anthropicProvider) Capabilities() Caps { return p.caps }

// Complete sends one normalized request through the Messages API.
func (p *anthropicProvider) Complete(ctx context.Context, req Request) (Response, error) {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = anthropicDefaultMaxTokens
	}

	params := anthropic.MessageNewParams{
		Model:     req.Model,
		MaxTokens: int64(maxTokens),
		Messages:  anthropicMessages(req.Messages),
	}
	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.System}}
	}
	if req.Temperature != nil {
		params.Temperature = anthropic.Float(*req.Temperature)
	}

	tools, err := anthropicTools(req.Tools)
	if err != nil {
		return Response{}, err
	}

	switch req.Mode {
	case ModeTool:
		if len(req.Schema) > 0 {
			if err := ensureObjectSchema(req.Schema); err != nil {
				return Response{}, err
			}
			submit := SubmitTool(req.Schema)
			schema, err := anthropicToolSchema(submit.Schema)
			if err != nil {
				return Response{}, err
			}
			tool := anthropic.ToolUnionParamOfTool(schema, submit.Name)
			tool.OfTool.Description = anthropic.String(submit.Description)
			tools = append(tools, tool)
			params.ToolChoice = anthropic.ToolChoiceUnionParam{
				OfTool: &anthropic.ToolChoiceToolParam{Name: SubmitToolName},
			}
		}
	case ModeNative:
		if len(req.Schema) > 0 {
			if err := ensureObjectSchema(req.Schema); err != nil {
				return Response{}, err
			}
			var schema map[string]any
			if unmarshalErr := json.Unmarshal(req.Schema, &schema); unmarshalErr != nil {
				return Response{}, wrapf(ClassConfig, unmarshalErr, "schema is not valid JSON")
			}
			params.OutputConfig = anthropic.OutputConfigParam{
				Format: anthropic.JSONOutputFormatParam{Schema: schema},
			}
		}
	case ModePrompt, ModeUnset:
		// The instruction is already part of the prompt (see
		// PromptInstruction); nothing schema-specific to add here.
	}
	if len(tools) > 0 {
		params.Tools = tools
	}

	msg, err := p.client.Messages.New(ctx, params)
	if err != nil {
		return Response{}, anthropicClassify(err)
	}

	return anthropicToResponse(msg), nil
}

// anthropicMessages translates normalized messages into the SDK's wire
// shape. A tool result has no role of its own on the wire: Anthropic expects
// it as a tool_result block inside a user-role message.
func anthropicMessages(messages []Message) []anthropic.MessageParam {
	out := make([]anthropic.MessageParam, 0, len(messages))
	for _, m := range messages {
		switch m.Role {
		case RoleUser:
			out = append(out, anthropic.NewUserMessage(anthropic.NewTextBlock(m.Text)))
		case RoleAssistant:
			blocks := make([]anthropic.ContentBlockParamUnion, 0, len(m.ToolCalls)+1)
			if m.Text != "" {
				blocks = append(blocks, anthropic.NewTextBlock(m.Text))
			}
			for _, call := range m.ToolCalls {
				blocks = append(blocks, anthropic.NewToolUseBlock(call.ID, call.Arguments, call.Name))
			}
			out = append(out, anthropic.NewAssistantMessage(blocks...))
		case RoleTool:
			out = append(out, anthropic.NewUserMessage(anthropic.NewToolResultBlock(m.ToolCallID, m.Text, false)))
		}
	}
	return out
}

// anthropicTools translates normalized tools into SDK tool definitions.
func anthropicTools(tools []Tool) ([]anthropic.ToolUnionParam, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	out := make([]anthropic.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		schema, err := anthropicToolSchema(t.Schema)
		if err != nil {
			return nil, err
		}
		tool := anthropic.ToolUnionParamOfTool(schema, t.Name)
		if t.Description != "" {
			tool.OfTool.Description = anthropic.String(t.Description)
		}
		out = append(out, tool)
	}
	return out, nil
}

// anthropicToolSchema converts a tool's JSON Schema into the SDK's
// ToolInputSchemaParam. Only "properties" and "required" have dedicated
// fields on that type; anything else the schema carries (e.g.
// "additionalProperties") is preserved via ExtraFields rather than dropped.
func anthropicToolSchema(schema json.RawMessage) (anthropic.ToolInputSchemaParam, error) {
	out := anthropic.ToolInputSchemaParam{}
	if len(schema) == 0 {
		return out, nil
	}

	var raw map[string]any
	if err := json.Unmarshal(schema, &raw); err != nil {
		return anthropic.ToolInputSchemaParam{}, wrapf(ClassConfig, err, "tool schema is not valid JSON")
	}

	if props, ok := raw["properties"]; ok {
		out.Properties = props
	}
	if required, ok := raw["required"].([]any); ok {
		for _, r := range required {
			if name, ok := r.(string); ok {
				out.Required = append(out.Required, name)
			}
		}
	}

	extras := make(map[string]any)
	for k, v := range raw {
		switch k {
		case "type", "properties", "required":
			continue
		default:
			extras[k] = v
		}
	}
	if len(extras) > 0 {
		out.ExtraFields = extras
	}
	return out, nil
}

// anthropicToResponse maps a Messages API answer back into the normalized
// Response. CostUSD is left nil: Anthropic does not report a cost, so the
// caller prices it from the pricing table (spec section 8.3).
func anthropicToResponse(msg *anthropic.Message) Response {
	var text strings.Builder
	var calls []ToolCall
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "tool_use":
			calls = append(calls, ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: block.Input,
			})
		}
	}

	return Response{
		Text:      text.String(),
		ToolCalls: calls,
		Usage: Usage{
			InputTokens:  int(msg.Usage.InputTokens),
			OutputTokens: int(msg.Usage.OutputTokens),
			CachedTokens: int(msg.Usage.CacheReadInputTokens),
		},
		FinishReason: string(msg.StopReason),
		Model:        msg.Model,
	}
}

// anthropicClassify turns a Messages.New failure into a classified *Error.
// A context error surfaces bare (see requestconfig.ExecuteNewRequest in the
// SDK, which returns the caller context's error once it is done); an
// *anthropic.Error carries the HTTP status and body of a failed request.
func anthropicClassify(err error) *Error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return wrapf(ClassTimeout, err, "anthropic request timed out")
	}

	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		classified := wrapf(classForStatus(apiErr.StatusCode), err, "anthropic request failed")
		classified.Status = apiErr.StatusCode
		classified.BodyTail = bodyTail([]byte(apiErr.RawJSON()))
		return classified
	}

	return wrapf(ClassTransient, err, "anthropic request failed")
}
