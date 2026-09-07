package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/foxzi/baton/internal/config"
	"github.com/foxzi/baton/internal/values"
)

// compatDefaultOpenRouterBaseURL is used when an openrouter provider does
// not set base_url of its own (spec section 8.3).
const compatDefaultOpenRouterBaseURL = "https://openrouter.ai/api/v1"

// compatDefaultAppName names the caller in HTTP-Referer/X-Title when a
// provider config leaves app_name empty. OpenRouter asks for these headers
// so it can attribute traffic on its own dashboard; something generic beats
// sending nothing.
const compatDefaultAppName = "baton"

// compatMaxBodyBytes caps how much of a response body is read, mirroring
// httpx.DefaultMaxBytes. A chat completion answer is always far smaller.
const compatMaxBodyBytes = 8 << 20

// compatProvider implements Provider on net/http rather than an SDK (spec
// section 8.3: "тот же клиент, что openrouter, произвольный base_url").
// One implementation serves both config kinds; newCompat is the only place
// that tells them apart.
type compatProvider struct {
	client  *http.Client
	baseURL string

	// apiKey is empty when the provider was given none. That is a
	// configuration error for openrouter (checked in newCompat) but valid
	// for openai_compatible, whose local servers often need no key at all.
	apiKey string

	kind config.ProviderKind

	// headers are sent on every request. Only openrouter populates them
	// (HTTP-Referer/X-Title, spec section 8.3).
	headers map[string]string

	caps Caps
}

// newCompat builds the shared backend for both config kinds.
func newCompat(cfg config.Provider, apiKey values.Secret) (Provider, error) {
	baseURL := cfg.BaseURL
	headers := map[string]string{}
	var caps Caps
	var key string

	switch cfg.Kind {
	case config.ProviderOpenRouter:
		if baseURL == "" {
			baseURL = compatDefaultOpenRouterBaseURL
		}
		resolved, err := requireKey(cfg.Kind, apiKey)
		if err != nil {
			return nil, err
		}
		key = resolved

		appName := cfg.AppName
		if appName == "" {
			appName = compatDefaultAppName
		}
		// The config carries nothing more specific than app_name (no real
		// site URL), so both attribution headers are derived from it.
		headers["HTTP-Referer"] = "https://" + appName
		headers["X-Title"] = appName

		// Whether a given model actually honours json_schema is only
		// knowable per-model through OpenRouter's /models endpoint (spec
		// section 8.3); that per-model check is out of scope here, so both
		// capabilities are reported true and ResolveMode is left to
		// downgrade at the caller if a request fails downstream.
		caps = Caps{StructuredOutput: true, Tools: true}

	case config.ProviderOpenAICompatible:
		if baseURL == "" {
			return nil, errorf(ClassConfig, "provider kind %s needs a base_url", cfg.Kind)
		}
		// A local server such as Ollama needs no key; sending none is
		// valid here, unlike the hard requirement of the other kinds.
		if !apiKey.IsZero() {
			key = apiKey.Reveal()
		}
		// An arbitrary endpoint advertises nothing about itself; the
		// honest default is "can call tools, cannot be trusted with a
		// native JSON Schema mode" until capabilities: overrides it (spec
		// section 8.3: "по флагу capabilities в конфиге").
		caps = Caps{StructuredOutput: false, Tools: true}

	default:
		return nil, errorf(ClassConfig, "newCompat: unsupported provider kind %s", cfg.Kind)
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, wrapf(ClassConfig, err, "provider base_url %q is invalid", baseURL)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errorf(ClassConfig, "provider base_url %q must be http or https", baseURL)
	}

	return &compatProvider{
		client:  &http.Client{},
		baseURL: baseURL,
		apiKey:  key,
		kind:    cfg.Kind,
		headers: headers,
		caps:    applyCaps(caps, cfg.Capabilities),
	}, nil
}

// Capabilities reports what this backend can do, after the config's own
// override (see newCompat for the per-kind default).
func (p *compatProvider) Capabilities() Caps { return p.caps }

// compatRequestBody is the Chat Completions request shape hand-written
// rather than taken from a SDK type, per spec section 8.3.
type compatRequestBody struct {
	Model          string          `json:"model"`
	Messages       []compatMessage `json:"messages"`
	Tools          []compatTool    `json:"tools,omitempty"`
	ToolChoice     any             `json:"tool_choice,omitempty"`
	ResponseFormat any             `json:"response_format,omitempty"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	Temperature    *float64        `json:"temperature,omitempty"`

	// Usage asks OpenRouter to include usage.cost in the response (spec
	// section 8.3: "запрашивать usage: { include: true }"). Left unset for
	// openai_compatible: an arbitrary endpoint may reject an unknown field.
	Usage any `json:"usage,omitempty"`
}

// compatMessage is one Chat Completions message. Content is a pointer so an
// assistant turn that only carries tool_calls can omit it instead of
// sending an empty string.
type compatMessage struct {
	Role       string           `json:"role"`
	Content    *string          `json:"content,omitempty"`
	ToolCalls  []compatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type compatToolCall struct {
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Function compatToolCallFunction `json:"function"`
}

type compatToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type compatTool struct {
	Type     string             `json:"type"`
	Function compatToolFunction `json:"function"`
}

type compatToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// compatResponseBody is the Chat Completions response shape, decoded
// leniently: an OpenAI-compatible server is trusted to answer with a choice
// but not with anything else a real OpenAI response would also carry.
type compatResponseBody struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content   *string          `json:"content"`
			ToolCalls []compatToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		Cost *float64 `json:"cost"`
	} `json:"usage"`
}

// Complete sends one normalized request through the Chat Completions
// endpoint of base_url.
func (p *compatProvider) Complete(ctx context.Context, req Request) (Response, error) {
	body := compatRequestBody{
		Model:    req.Model,
		Messages: compatMessages(req.System, req.Messages),
	}
	if req.MaxTokens > 0 {
		body.MaxTokens = req.MaxTokens
	}
	if req.Temperature != nil {
		body.Temperature = req.Temperature
	}

	tools := compatTools(req.Tools)

	switch req.Mode {
	case ModeTool:
		if len(req.Schema) > 0 {
			if err := ensureObjectSchema(req.Schema); err != nil {
				return Response{}, err
			}
			submit := SubmitTool(req.Schema)
			tools = append(tools, compatTool{
				Type: "function",
				Function: compatToolFunction{
					Name:        submit.Name,
					Description: submit.Description,
					Parameters:  submit.Schema,
				},
			})
			body.ToolChoice = map[string]any{
				"type":     "function",
				"function": map[string]any{"name": SubmitToolName},
			}
		}
	case ModeNative:
		if len(req.Schema) > 0 {
			if err := ensureObjectSchema(req.Schema); err != nil {
				return Response{}, err
			}
			var schema map[string]any
			if err := json.Unmarshal(req.Schema, &schema); err != nil {
				return Response{}, wrapf(ClassConfig, err, "schema is not valid JSON")
			}
			body.ResponseFormat = map[string]any{
				"type": "json_schema",
				"json_schema": map[string]any{
					"name":   schemaName,
					"schema": schema,
					"strict": true,
				},
			}
		}
	case ModePrompt, ModeUnset:
		// The instruction is already part of the prompt (see
		// PromptInstruction); nothing schema-specific to add here.
	}
	if len(tools) > 0 {
		body.Tools = tools
	}

	if p.kind == config.ProviderOpenRouter {
		body.Usage = map[string]any{"include": true}
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return Response{}, wrapf(ClassConfig, err, "compat: cannot encode the request")
	}

	target := strings.TrimSuffix(p.baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return Response{}, wrapf(ClassConfig, err, "compat: cannot build the request")
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	for name, value := range p.headers {
		httpReq.Header.Set(name, value)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return Response{}, wrapf(ClassTimeout, err, "compat request timed out")
		}
		return Response{}, wrapf(ClassTransient, err, "compat request failed")
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, compatMaxBodyBytes+1))
	if err != nil {
		return Response{}, wrapf(ClassTransient, err, "compat: reading the response failed")
	}
	if int64(len(data)) > compatMaxBodyBytes {
		data = data[:compatMaxBodyBytes]
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		classified := errorf(classForStatus(resp.StatusCode), "compat request failed")
		classified.Status = resp.StatusCode
		classified.BodyTail = bodyTail(data)
		return Response{}, classified
	}

	var out compatResponseBody
	if err := json.Unmarshal(data, &out); err != nil {
		return Response{}, wrapf(ClassCommand, err, "compat: response is not valid JSON")
	}

	return compatToResponse(p.kind, out), nil
}

// compatMessages translates a system prompt and normalized messages into
// the wire shape. A tool result has no role of its own to normalize: this
// API already models it as a dedicated "tool" message, matching RoleTool.
func compatMessages(system string, messages []Message) []compatMessage {
	out := make([]compatMessage, 0, len(messages)+1)
	if system != "" {
		text := system
		out = append(out, compatMessage{Role: "system", Content: &text})
	}
	for _, m := range messages {
		switch m.Role {
		case RoleUser:
			text := m.Text
			out = append(out, compatMessage{Role: "user", Content: &text})
		case RoleAssistant:
			msg := compatMessage{Role: "assistant"}
			if m.Text != "" {
				text := m.Text
				msg.Content = &text
			}
			for _, call := range m.ToolCalls {
				msg.ToolCalls = append(msg.ToolCalls, compatToolCall{
					ID:   call.ID,
					Type: "function",
					Function: compatToolCallFunction{
						Name:      call.Name,
						Arguments: string(call.Arguments),
					},
				})
			}
			out = append(out, msg)
		case RoleTool:
			text := m.Text
			out = append(out, compatMessage{Role: "tool", Content: &text, ToolCallID: m.ToolCallID})
		}
	}
	return out
}

// compatTools translates normalized tools into the wire shape.
func compatTools(tools []Tool) []compatTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]compatTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, compatTool{
			Type: "function",
			Function: compatToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Schema,
			},
		})
	}
	return out
}

// compatToResponse maps a Chat Completions answer back into the normalized
// Response. CostUSD is set only for openrouter and only when the endpoint
// actually reported usage.cost (it does so only when the request asked for
// usage.include); openai_compatible never sets it, so the caller prices the
// usage from the pricing table (spec section 8.3).
func compatToResponse(kind config.ProviderKind, body compatResponseBody) Response {
	resp := Response{Model: body.Model}

	if body.Usage != nil {
		resp.Usage = Usage{
			InputTokens:  body.Usage.PromptTokens,
			OutputTokens: body.Usage.CompletionTokens,
		}
		if body.Usage.PromptTokensDetails != nil {
			resp.Usage.CachedTokens = body.Usage.PromptTokensDetails.CachedTokens
		}
		if kind == config.ProviderOpenRouter && body.Usage.Cost != nil {
			resp.CostUSD = body.Usage.Cost
		}
	}

	if len(body.Choices) == 0 {
		return resp
	}

	choice := body.Choices[0]
	if choice.Message.Content != nil {
		resp.Text = *choice.Message.Content
	}
	for _, call := range choice.Message.ToolCalls {
		resp.ToolCalls = append(resp.ToolCalls, ToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: json.RawMessage(call.Function.Arguments),
		})
	}
	resp.FinishReason = choice.FinishReason
	return resp
}
