package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/config"
	"github.com/foxzi/baton/internal/values"
)

// newTestCompat points a compat provider at a local server instead of a real
// endpoint. key == "" builds a zero values.Secret, so the provider sees no
// api_key at all.
func newTestCompat(t *testing.T, kind config.ProviderKind, key string, handler http.HandlerFunc) Provider {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	var secret values.Secret
	if key != "" {
		secret = values.NewSecret("test", key)
	}
	p, err := newCompat(config.Provider{
		Kind:    kind,
		BaseURL: server.URL,
		AppName: "baton-test",
	}, secret)
	if err != nil {
		t.Fatalf("newCompat: %v", err)
	}
	return p
}

// writeCompatCompletion answers a Chat Completions call with a minimal,
// well-formed response body.
func writeCompatCompletion(t *testing.T, w http.ResponseWriter, message, finishReason, usage string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	body := `{
		"id": "chatcmpl_1",
		"object": "chat.completion",
		"model": "test-model",
		"choices": [{
			"index": 0,
			"message": ` + message + `,
			"finish_reason": "` + finishReason + `"
		}]`
	if usage != "" {
		body += `, "usage": ` + usage
	}
	body += `}`
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatalf("write response: %v", err)
	}
}

const testCompatUsage = `{"prompt_tokens": 12, "completion_tokens": 34, "prompt_tokens_details": {"cached_tokens": 5}}`

func TestNewCompatOpenAICompatibleRequiresBaseURL(t *testing.T) {
	_, err := newCompat(config.Provider{Kind: config.ProviderOpenAICompatible}, values.Secret{})
	if err == nil {
		t.Fatal("expected an error for a missing base_url")
	}
	if Class(err) != ClassConfig {
		t.Fatalf("class = %q, want %q", Class(err), ClassConfig)
	}
}

func TestNewCompatOpenRouterRequiresKey(t *testing.T) {
	_, err := newCompat(config.Provider{Kind: config.ProviderOpenRouter}, values.Secret{})
	if err == nil {
		t.Fatal("expected an error for a missing api_key")
	}
	if Class(err) != ClassConfig {
		t.Fatalf("class = %q, want %q", Class(err), ClassConfig)
	}
}

func TestNewCompatOpenRouterDefaultBaseURL(t *testing.T) {
	p, err := newCompat(config.Provider{Kind: config.ProviderOpenRouter}, values.NewSecret("or", "sk-or-test"))
	if err != nil {
		t.Fatalf("newCompat: %v", err)
	}
	cp, ok := p.(*compatProvider)
	if !ok {
		t.Fatalf("provider is %T, want *compatProvider", p)
	}
	if cp.baseURL != compatDefaultOpenRouterBaseURL {
		t.Fatalf("baseURL = %q, want %q", cp.baseURL, compatDefaultOpenRouterBaseURL)
	}
}

func TestNewCompatRejectsBadBaseURL(t *testing.T) {
	_, err := newCompat(config.Provider{
		Kind:    config.ProviderOpenAICompatible,
		BaseURL: "ftp://example.com",
	}, values.Secret{})
	if err == nil {
		t.Fatal("expected an error for a non-http(s) base_url")
	}
	if Class(err) != ClassConfig {
		t.Fatalf("class = %q, want %q", Class(err), ClassConfig)
	}
}

func TestCompatOpenAICompatibleNoKeySendsNoAuthorization(t *testing.T) {
	p := newTestCompat(t, config.ProviderOpenAICompatible, "", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("Authorization = %q, want none", got)
		}
		writeCompatCompletion(t, w, `{"role":"assistant","content":"ok"}`, "stop", testCompatUsage)
	})

	_, err := p.Complete(context.Background(), Request{
		Model:    "local-model",
		Messages: []Message{{Role: RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
}

func TestCompatOpenAICompatibleKeySendsBearer(t *testing.T) {
	p := newTestCompat(t, config.ProviderOpenAICompatible, "sk-local", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-local" {
			t.Fatalf("Authorization = %q", got)
		}
		writeCompatCompletion(t, w, `{"role":"assistant","content":"ok"}`, "stop", testCompatUsage)
	})

	_, err := p.Complete(context.Background(), Request{
		Model:    "local-model",
		Messages: []Message{{Role: RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
}

func TestCompatOpenRouterHeadersAndUsageInclude(t *testing.T) {
	var gotBody map[string]any
	p := newTestCompat(t, config.ProviderOpenRouter, "sk-or-test", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-or-test" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("HTTP-Referer"); got == "" {
			t.Fatal("HTTP-Referer header missing")
		}
		if got := r.Header.Get("X-Title"); got != "baton-test" {
			t.Fatalf("X-Title = %q, want app_name", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeCompatCompletion(t, w, `{"role":"assistant","content":"ok"}`, "stop",
			`{"prompt_tokens":12,"completion_tokens":34,"cost":0.00042}`)
	})

	resp, err := p.Complete(context.Background(), Request{
		Model:    "openrouter-model",
		Messages: []Message{{Role: RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	usage, _ := gotBody["usage"].(map[string]any)
	if usage["include"] != true {
		t.Fatalf("request usage = %v, want {include: true}", gotBody["usage"])
	}

	if resp.CostUSD == nil || *resp.CostUSD != 0.00042 {
		t.Fatalf("CostUSD = %v, want 0.00042", resp.CostUSD)
	}
}

func TestCompatCompleteText(t *testing.T) {
	var gotBody map[string]any
	p := newTestCompat(t, config.ProviderOpenAICompatible, "sk-test", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeCompatCompletion(t, w, `{"role":"assistant","content":"hello there"}`, "stop", testCompatUsage)
	})

	resp, err := p.Complete(context.Background(), Request{
		Model:    "local-model",
		System:   "be terse",
		Messages: []Message{{Role: RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if resp.Text != "hello there" {
		t.Fatalf("Text = %q", resp.Text)
	}
	if resp.Usage.InputTokens != 12 || resp.Usage.OutputTokens != 34 || resp.Usage.CachedTokens != 5 {
		t.Fatalf("Usage = %+v", resp.Usage)
	}
	if resp.FinishReason != "stop" {
		t.Fatalf("FinishReason = %q", resp.FinishReason)
	}
	if resp.Model != "test-model" {
		t.Fatalf("Model = %q", resp.Model)
	}
	// openai_compatible never reports a cost of its own (spec 8.3): the
	// caller prices the usage from the pricing table instead.
	if resp.CostUSD != nil {
		t.Fatalf("CostUSD = %v, want nil for openai_compatible", *resp.CostUSD)
	}

	if gotBody["model"] != "local-model" {
		t.Fatalf("request model = %v", gotBody["model"])
	}
	messages, _ := gotBody["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("request messages = %v", gotBody["messages"])
	}
	first, _ := messages[0].(map[string]any)
	if first["role"] != "system" {
		t.Fatalf("first message role = %v, want system", first["role"])
	}
}

func TestCompatCompleteToolCall(t *testing.T) {
	p := newTestCompat(t, config.ProviderOpenAICompatible, "sk-test", func(w http.ResponseWriter, r *http.Request) {
		writeCompatCompletion(t, w, `{
			"role": "assistant",
			"content": null,
			"tool_calls": [{
				"id": "call_1",
				"type": "function",
				"function": {"name": "lookup", "arguments": "{\"q\":\"cats\"}"}
			}]
		}`, "tool_calls", testCompatUsage)
	})

	resp, err := p.Complete(context.Background(), Request{
		Model:    "local-model",
		Messages: []Message{{Role: RoleUser, Text: "hi"}},
		Tools: []Tool{{
			Name:        "lookup",
			Description: "looks things up",
			Schema:      json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`),
		}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %+v", resp.ToolCalls)
	}
	call := resp.ToolCalls[0]
	if call.ID != "call_1" || call.Name != "lookup" {
		t.Fatalf("call = %+v", call)
	}
	var args map[string]any
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		t.Fatalf("call.Arguments not JSON: %v", err)
	}
	if args["q"] != "cats" {
		t.Fatalf("call.Arguments = %s", call.Arguments)
	}
}

func TestCompatCompleteConversationRoundtrip(t *testing.T) {
	var gotBody struct {
		Messages []map[string]any `json:"messages"`
	}
	p := newTestCompat(t, config.ProviderOpenAICompatible, "sk-test", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeCompatCompletion(t, w, `{"role":"assistant","content":"done"}`, "stop", testCompatUsage)
	})

	_, err := p.Complete(context.Background(), Request{
		Model: "local-model",
		Messages: []Message{
			{Role: RoleUser, Text: "call lookup"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{
				ID:        "call_1",
				Name:      "lookup",
				Arguments: json.RawMessage(`{"q":"cats"}`),
			}}},
			{Role: RoleTool, ToolCallID: "call_1", Text: "cats are great"},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if len(gotBody.Messages) != 3 {
		t.Fatalf("messages sent = %d, want 3", len(gotBody.Messages))
	}
	if gotBody.Messages[1]["role"] != "assistant" {
		t.Fatalf("second message role = %v", gotBody.Messages[1]["role"])
	}
	toolCalls, _ := gotBody.Messages[1]["tool_calls"].([]any)
	if len(toolCalls) != 1 {
		t.Fatalf("second message tool_calls = %v", gotBody.Messages[1]["tool_calls"])
	}
	if gotBody.Messages[2]["role"] != "tool" {
		t.Fatalf("tool-result message role = %v, want tool", gotBody.Messages[2]["role"])
	}
	if gotBody.Messages[2]["tool_call_id"] != "call_1" {
		t.Fatalf("tool-result message tool_call_id = %v", gotBody.Messages[2]["tool_call_id"])
	}
}

func TestCompatCompleteModeNativeSendsSchema(t *testing.T) {
	var gotBody map[string]any
	p := newTestCompat(t, config.ProviderOpenAICompatible, "sk-test", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeCompatCompletion(t, w, `{"role":"assistant","content":"{\"answer\":42}"}`, "stop", testCompatUsage)
	})

	_, err := p.Complete(context.Background(), Request{
		Model:    "local-model",
		Messages: []Message{{Role: RoleUser, Text: "hi"}},
		Schema:   json.RawMessage(`{"type":"object","properties":{"answer":{"type":"integer"}}}`),
		Mode:     ModeNative,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	responseFormat, _ := gotBody["response_format"].(map[string]any)
	if responseFormat["type"] != "json_schema" {
		t.Fatalf("response_format = %v", gotBody["response_format"])
	}
	jsonSchema, _ := responseFormat["json_schema"].(map[string]any)
	schema, _ := jsonSchema["schema"].(map[string]any)
	if schema["type"] != "object" {
		t.Fatalf("response_format.json_schema.schema = %v", jsonSchema["schema"])
	}
	if jsonSchema["strict"] != true {
		t.Fatalf("response_format.json_schema.strict = %v", jsonSchema["strict"])
	}
}

func TestCompatCompleteModeToolForcesSubmit(t *testing.T) {
	var gotBody map[string]any
	p := newTestCompat(t, config.ProviderOpenAICompatible, "sk-test", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeCompatCompletion(t, w, `{
			"role": "assistant",
			"content": null,
			"tool_calls": [{
				"id": "call_1",
				"type": "function",
				"function": {"name": "submit", "arguments": "{\"answer\":42}"}
			}]
		}`, "tool_calls", testCompatUsage)
	})

	resp, err := p.Complete(context.Background(), Request{
		Model:    "local-model",
		Messages: []Message{{Role: RoleUser, Text: "hi"}},
		Schema:   json.RawMessage(`{"type":"object","properties":{"answer":{"type":"integer"}}}`),
		Mode:     ModeTool,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	choice, _ := gotBody["tool_choice"].(map[string]any)
	function, _ := choice["function"].(map[string]any)
	if choice["type"] != "function" || function["name"] != SubmitToolName {
		t.Fatalf("tool_choice = %v", gotBody["tool_choice"])
	}

	result, err := Result(resp, ModeTool)
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("Result not JSON: %v", err)
	}
	if parsed["answer"] != float64(42) {
		t.Fatalf("Result = %s", result)
	}
}

func TestCompatCompleteMissingUsageNotError(t *testing.T) {
	p := newTestCompat(t, config.ProviderOpenAICompatible, "sk-test", func(w http.ResponseWriter, r *http.Request) {
		writeCompatCompletion(t, w, `{"role":"assistant","content":"ok"}`, "stop", "")
	})

	resp, err := p.Complete(context.Background(), Request{
		Model:    "local-model",
		Messages: []Message{{Role: RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Text != "ok" {
		t.Fatalf("Text = %q", resp.Text)
	}
	if resp.Usage != (Usage{}) {
		t.Fatalf("Usage = %+v, want zero value", resp.Usage)
	}
}

func TestCompatCompleteErrorClassification(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   string
	}{
		{"rate limited", http.StatusTooManyRequests, ClassTransient},
		{"server error", http.StatusInternalServerError, ClassTransient},
		{"unauthorized", http.StatusUnauthorized, ClassConfig},
		{"bad request", http.StatusBadRequest, ClassCommand},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestCompat(t, config.ProviderOpenAICompatible, "sk-test", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				w.Write([]byte(`{"error":{"type":"some_error","message":"boom"}}`))
			})

			_, err := p.Complete(context.Background(), Request{
				Model:    "local-model",
				Messages: []Message{{Role: RoleUser, Text: "hi"}},
			})
			if err == nil {
				t.Fatal("expected an error")
			}
			if Class(err) != tc.want {
				t.Fatalf("class = %q, want %q", Class(err), tc.want)
			}
			if !strings.Contains(err.Error(), "boom") {
				t.Fatalf("error message = %q, want it to contain the body", err.Error())
			}
		})
	}
}

func TestCompatCompleteNonJSONBody(t *testing.T) {
	p := newTestCompat(t, config.ProviderOpenAICompatible, "sk-test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not json"))
	})

	_, err := p.Complete(context.Background(), Request{
		Model:    "local-model",
		Messages: []Message{{Role: RoleUser, Text: "hi"}},
	})
	if err == nil {
		t.Fatal("expected an error for a non-JSON body")
	}
	if Class(err) != ClassCommand {
		t.Fatalf("class = %q, want %q", Class(err), ClassCommand)
	}
}

func TestCompatCompleteTimeout(t *testing.T) {
	p := newTestCompat(t, config.ProviderOpenAICompatible, "sk-test", func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := p.Complete(ctx, Request{
		Model:    "local-model",
		Messages: []Message{{Role: RoleUser, Text: "hi"}},
	})
	if err == nil {
		t.Fatal("expected an error for a cancelled context")
	}
	if Class(err) != ClassTimeout {
		t.Fatalf("class = %q, want %q", Class(err), ClassTimeout)
	}
}

func TestCompatCapabilitiesOpenRouter(t *testing.T) {
	p := newTestCompat(t, config.ProviderOpenRouter, "sk-or-test", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no request expected")
	})

	caps := p.Capabilities()
	if !caps.StructuredOutput || !caps.Tools {
		t.Fatalf("caps = %+v, want both true", caps)
	}
}

func TestCompatCapabilitiesOpenAICompatibleDefault(t *testing.T) {
	p := newTestCompat(t, config.ProviderOpenAICompatible, "", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no request expected")
	})

	caps := p.Capabilities()
	if caps.StructuredOutput {
		t.Fatal("StructuredOutput default should be false for openai_compatible")
	}
	if !caps.Tools {
		t.Fatal("Tools default should be true for openai_compatible")
	}
}

func TestCompatCapabilitiesOpenAICompatibleOverride(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no request expected")
	}))
	defer server.Close()

	yes := true
	p, err := newCompat(config.Provider{
		Kind:         config.ProviderOpenAICompatible,
		BaseURL:      server.URL,
		Capabilities: &config.Capabilities{StructuredOutput: &yes},
	}, values.Secret{})
	if err != nil {
		t.Fatalf("newCompat: %v", err)
	}

	caps := p.Capabilities()
	if !caps.StructuredOutput {
		t.Fatal("StructuredOutput override was not applied")
	}
	if !caps.Tools {
		t.Fatal("Tools should keep its default of true")
	}
}
