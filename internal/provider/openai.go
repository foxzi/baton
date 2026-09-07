package provider

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/shared"

	"github.com/foxzi/baton/internal/config"
	"github.com/foxzi/baton/internal/values"
)

// openaiProvider implements Provider on top of the official Go SDK, using
// the Chat Completions API (spec section 8.3: "Chat Completions ...
// официальный Go SDK").
type openaiProvider struct {
	client openai.Client
	caps   Caps
}

// newOpenAI builds the OpenAI backend. Retries are turned off on the SDK
// client for the same reason as the Anthropic backend: the engine already
// retries transient failures with its own backoff (spec section 9.1).
func newOpenAI(cfg config.Provider, apiKey values.Secret) (Provider, error) {
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

	return &openaiProvider{
		client: openai.NewClient(opts...),
		caps:   caps,
	}, nil
}

// Capabilities reports what this backend can do. StructuredOutput is true:
// the Chat Completions API takes a native `response_format: json_schema`
// with `strict: true` (spec section 8.3). Tools and Vision are the API's
// own function-calling and image-input support.
func (p *openaiProvider) Capabilities() Caps { return p.caps }

// Complete sends one normalized request through the Chat Completions API.
func (p *openaiProvider) Complete(ctx context.Context, req Request) (Response, error) {
	params := openai.ChatCompletionNewParams{
		Model:    req.Model,
		Messages: openaiMessages(req.System, req.Messages),
	}
	if req.MaxTokens > 0 {
		params.MaxCompletionTokens = param.NewOpt(int64(req.MaxTokens))
	}
	if req.Temperature != nil {
		params.Temperature = param.NewOpt(*req.Temperature)
	}

	tools, err := openaiTools(req.Tools)
	if err != nil {
		return Response{}, err
	}

	switch req.Mode {
	case ModeTool:
		if len(req.Schema) > 0 {
			if err := ensureObjectSchema(req.Schema); err != nil {
				return Response{}, err
			}
			tool, err := openaiTool(SubmitTool(req.Schema))
			if err != nil {
				return Response{}, err
			}
			tools = append(tools, tool)
			params.ToolChoice = openai.ToolChoiceOptionFunctionToolChoice(
				openai.ChatCompletionNamedToolChoiceFunctionParam{Name: SubmitToolName},
			)
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
			params.ResponseFormat = openai.ChatCompletionNewParamsResponseFormatUnion{
				OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{
					JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{
						Name:   schemaName,
						Schema: schema,
						Strict: param.NewOpt(true),
					},
				},
			}
		}
	case ModePrompt, ModeUnset:
		// The instruction is already part of the prompt (see
		// PromptInstruction); nothing schema-specific to add here.
	}
	if len(tools) > 0 {
		params.Tools = tools
	}

	completion, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return Response{}, openaiClassify(err)
	}

	// A model may refuse a strict json_schema request instead of answering
	// it (spec section 8.3). The refusal is not a result, and it is not
	// transient either: it is a schema failure, so the engine retries the
	// same model with the validation message in context.
	if len(completion.Choices) > 0 {
		if refusal := completion.Choices[0].Message.Refusal; refusal != "" {
			return Response{}, errorf(ClassSchema, "the model refused to answer: %s", refusal)
		}
	}

	return openaiToResponse(completion), nil
}

// openaiMessages translates a system prompt and normalized messages into the
// SDK's wire shape. A tool result has no role of its own to normalize:
// OpenAI already models it as a dedicated "tool" message, matching RoleTool.
func openaiMessages(system string, messages []Message) []openai.ChatCompletionMessageParamUnion {
	out := make([]openai.ChatCompletionMessageParamUnion, 0, len(messages)+1)
	if system != "" {
		out = append(out, openai.SystemMessage(system))
	}
	for _, m := range messages {
		switch m.Role {
		case RoleUser:
			out = append(out, openai.UserMessage(m.Text))
		case RoleAssistant:
			out = append(out, openaiAssistantMessage(m))
		case RoleTool:
			out = append(out, openai.ToolMessage(m.Text, m.ToolCallID))
		}
	}
	return out
}

// openaiAssistantMessage builds an assistant turn, whose tool calls (unlike
// text) have no constructor helper in the SDK.
func openaiAssistantMessage(m Message) openai.ChatCompletionMessageParamUnion {
	var assistant openai.ChatCompletionAssistantMessageParam
	if m.Text != "" {
		assistant.Content.OfString = param.NewOpt(m.Text)
	}
	for _, call := range m.ToolCalls {
		assistant.ToolCalls = append(assistant.ToolCalls, openai.ChatCompletionMessageToolCallUnionParam{
			OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
				ID: call.ID,
				Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
					Name:      call.Name,
					Arguments: string(call.Arguments),
				},
			},
		})
	}
	return openai.ChatCompletionMessageParamUnion{OfAssistant: &assistant}
}

// openaiTools translates normalized tools into SDK function-tool
// definitions.
func openaiTools(tools []Tool) ([]openai.ChatCompletionToolUnionParam, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	out := make([]openai.ChatCompletionToolUnionParam, 0, len(tools))
	for _, t := range tools {
		tool, err := openaiTool(t)
		if err != nil {
			return nil, err
		}
		out = append(out, tool)
	}
	return out, nil
}

// openaiTool converts one normalized tool into the SDK's function-tool
// param, whose "parameters" field takes the tool's JSON Schema directly.
func openaiTool(t Tool) (openai.ChatCompletionToolUnionParam, error) {
	function := shared.FunctionDefinitionParam{Name: t.Name}
	if t.Description != "" {
		function.Description = param.NewOpt(t.Description)
	}
	if len(t.Schema) > 0 {
		var params shared.FunctionParameters
		if err := json.Unmarshal(t.Schema, &params); err != nil {
			return openai.ChatCompletionToolUnionParam{}, wrapf(ClassConfig, err, "tool schema is not valid JSON")
		}
		function.Parameters = params
	}
	return openai.ChatCompletionToolUnionParam{
		OfFunction: &openai.ChatCompletionFunctionToolParam{Function: function},
	}, nil
}

// openaiToResponse maps a Chat Completions answer back into the normalized
// Response. CostUSD is left nil: OpenAI does not report a cost on this API,
// so the caller prices it from the pricing table (spec section 8.3).
func openaiToResponse(completion *openai.ChatCompletion) Response {
	if len(completion.Choices) == 0 {
		return Response{
			Usage: openaiUsage(completion.Usage),
			Model: completion.Model,
		}
	}

	choice := completion.Choices[0]
	var calls []ToolCall
	for _, call := range choice.Message.ToolCalls {
		calls = append(calls, ToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: json.RawMessage(call.Function.Arguments),
		})
	}

	return Response{
		Text:         choice.Message.Content,
		ToolCalls:    calls,
		Usage:        openaiUsage(completion.Usage),
		FinishReason: choice.FinishReason,
		Model:        completion.Model,
	}
}

// openaiUsage normalizes the Chat Completions usage block. CachedTokens
// comes from prompt_tokens_details.cached_tokens, the only cache accounting
// this API reports.
func openaiUsage(usage openai.CompletionUsage) Usage {
	return Usage{
		InputTokens:  int(usage.PromptTokens),
		OutputTokens: int(usage.CompletionTokens),
		CachedTokens: int(usage.PromptTokensDetails.CachedTokens),
	}
}

// openaiClassify turns a Chat Completions failure into a classified *Error.
// A context error surfaces bare, like the Anthropic backend; an
// *openai.Error carries the HTTP status and body of a failed request.
func openaiClassify(err error) *Error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return wrapf(ClassTimeout, err, "openai request timed out")
	}

	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		classified := wrapf(classForStatus(apiErr.StatusCode), err, "openai request failed")
		classified.Status = apiErr.StatusCode
		classified.BodyTail = bodyTail([]byte(apiErr.RawJSON()))
		return classified
	}

	return wrapf(ClassTransient, err, "openai request failed")
}
