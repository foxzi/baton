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

// newTestOpenAI points a provider at a local server instead of the real
// API, so tests never depend on network access or a real key.
func newTestOpenAI(t *testing.T, handler http.HandlerFunc) Provider {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	p, err := newOpenAI(config.Provider{
		Kind:    config.ProviderOpenAI,
		BaseURL: server.URL,
	}, values.NewSecret("openai", "sk-test"))
	if err != nil {
		t.Fatalf("newOpenAI: %v", err)
	}
	return p
}

func TestNewOpenAIRequiresKey(t *testing.T) {
	_, err := newOpenAI(config.Provider{Kind: config.ProviderOpenAI}, values.Secret{})
	if err == nil {
		t.Fatal("expected an error for a missing api_key")
	}
	if Class(err) != ClassConfig {
		t.Fatalf("class = %q, want %q", Class(err), ClassConfig)
	}
}

func TestOpenAICapabilities(t *testing.T) {
	p := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no request expected")
	})

	caps := p.Capabilities()
	if !caps.StructuredOutput || !caps.Tools || !caps.Vision {
		t.Fatalf("caps = %+v, want all true", caps)
	}
}

func TestOpenAICapabilitiesOverride(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no request expected")
	}))
	defer server.Close()

	no := false
	p, err := newOpenAI(config.Provider{
		Kind:         config.ProviderOpenAI,
		BaseURL:      server.URL,
		Capabilities: &config.Capabilities{Tools: &no},
	}, values.NewSecret("openai", "sk-test"))
	if err != nil {
		t.Fatalf("newOpenAI: %v", err)
	}

	caps := p.Capabilities()
	if caps.Tools {
		t.Fatal("Tools override was not applied")
	}
	if !caps.StructuredOutput {
		t.Fatal("StructuredOutput should keep its default")
	}
}

// writeCompletion answers a Chat Completions call with a minimal,
// well-formed response body.
func writeCompletion(t *testing.T, w http.ResponseWriter, message string, finishReason string, usage string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	_, err := w.Write([]byte(`{
		"id": "chatcmpl_1",
		"object": "chat.completion",
		"created": 1,
		"model": "gpt-4o-mini",
		"choices": [{
			"index": 0,
			"message": ` + message + `,
			"finish_reason": "` + finishReason + `"
		}],
		"usage": ` + usage + `
	}`))
	if err != nil {
		t.Fatalf("write response: %v", err)
	}
}

const testOpenAIUsage = `{"prompt_tokens": 12, "completion_tokens": 34, "total_tokens": 46, "prompt_tokens_details": {"cached_tokens": 5}}`

func TestOpenAICompleteText(t *testing.T) {
	var gotBody map[string]any
	p := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Fatalf("Authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeCompletion(t, w, `{"role":"assistant","content":"hello there"}`, "stop", testOpenAIUsage)
	})

	resp, err := p.Complete(context.Background(), Request{
		Model:    "gpt-4o-mini",
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
	if resp.Model != "gpt-4o-mini" {
		t.Fatalf("Model = %q", resp.Model)
	}

	if gotBody["model"] != "gpt-4o-mini" {
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

func TestOpenAICompleteMaxTokens(t *testing.T) {
	var gotBody map[string]any
	p := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		writeCompletion(t, w, `{"role":"assistant","content":"ok"}`, "stop", testOpenAIUsage)
	})

	_, err := p.Complete(context.Background(), Request{
		Model:     "gpt-4o-mini",
		Messages:  []Message{{Role: RoleUser, Text: "hi"}},
		MaxTokens: 256,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if gotBody["max_completion_tokens"].(float64) != 256 {
		t.Fatalf("request max_completion_tokens = %v, want 256", gotBody["max_completion_tokens"])
	}
}

func TestOpenAICompleteToolCall(t *testing.T) {
	p := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		writeCompletion(t, w, `{
			"role": "assistant",
			"content": null,
			"tool_calls": [{
				"id": "call_1",
				"type": "function",
				"function": {"name": "lookup", "arguments": "{\"q\":\"cats\"}"}
			}]
		}`, "tool_calls", testOpenAIUsage)
	})

	resp, err := p.Complete(context.Background(), Request{
		Model:    "gpt-4o-mini",
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

func TestOpenAICompleteConversationRoundtrip(t *testing.T) {
	var gotBody struct {
		Messages []map[string]any `json:"messages"`
	}
	p := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeCompletion(t, w, `{"role":"assistant","content":"done"}`, "stop", testOpenAIUsage)
	})

	_, err := p.Complete(context.Background(), Request{
		Model: "gpt-4o-mini",
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
	if gotBody.Messages[2]["role"] != "tool" {
		t.Fatalf("tool-result message role = %v, want tool", gotBody.Messages[2]["role"])
	}
	if gotBody.Messages[2]["tool_call_id"] != "call_1" {
		t.Fatalf("tool-result message tool_call_id = %v", gotBody.Messages[2]["tool_call_id"])
	}
}

func TestOpenAICompleteModeToolForcesSubmit(t *testing.T) {
	var gotBody map[string]any
	p := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		writeCompletion(t, w, `{
			"role": "assistant",
			"content": null,
			"tool_calls": [{
				"id": "call_1",
				"type": "function",
				"function": {"name": "submit", "arguments": "{\"answer\":42}"}
			}]
		}`, "tool_calls", testOpenAIUsage)
	})

	resp, err := p.Complete(context.Background(), Request{
		Model:    "gpt-4o-mini",
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

func TestOpenAICompleteModeNativeSendsSchema(t *testing.T) {
	var gotBody map[string]any
	p := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		writeCompletion(t, w, `{"role":"assistant","content":"{\"answer\":42}"}`, "stop", testOpenAIUsage)
	})

	_, err := p.Complete(context.Background(), Request{
		Model:    "gpt-4o-mini",
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

func TestOpenAICompleteModeNativeRejectsNonObjectSchema(t *testing.T) {
	p := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no request expected")
	})

	_, err := p.Complete(context.Background(), Request{
		Model:    "gpt-4o-mini",
		Messages: []Message{{Role: RoleUser, Text: "hi"}},
		Schema:   json.RawMessage(`{"type":"array"}`),
		Mode:     ModeNative,
	})
	if err == nil {
		t.Fatal("expected an error for a non-object schema")
	}
	if Class(err) != ClassConfig {
		t.Fatalf("class = %q, want %q", Class(err), ClassConfig)
	}
}

func TestOpenAICompleteErrorClassification(t *testing.T) {
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
			p := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				w.Write([]byte(`{"error":{"type":"some_error","message":"boom"}}`))
			})

			_, err := p.Complete(context.Background(), Request{
				Model:    "gpt-4o-mini",
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

func TestOpenAICompleteTimeout(t *testing.T) {
	p := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := p.Complete(ctx, Request{
		Model:    "gpt-4o-mini",
		Messages: []Message{{Role: RoleUser, Text: "hi"}},
	})
	if err == nil {
		t.Fatal("expected an error for a cancelled context")
	}
	if Class(err) != ClassTimeout {
		t.Fatalf("class = %q, want %q", Class(err), ClassTimeout)
	}
}

func TestOpenAICompleteRefusalIsSchemaError(t *testing.T) {
	p := newTestOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id": "chatcmpl-1",
			"model": "gpt-4.1-mini",
			"choices": [{
				"index": 0,
				"finish_reason": "stop",
				"message": {"role": "assistant", "content": "", "refusal": "I cannot comply"}
			}]
		}`))
	})

	_, err := p.Complete(context.Background(), Request{Model: "gpt-4.1-mini", Mode: ModeNative})
	if err == nil {
		t.Fatal("expected an error for a refusal")
	}
	if Class(err) != ClassSchema {
		t.Fatalf("class = %q, want %q", Class(err), ClassSchema)
	}
	if !strings.Contains(err.Error(), "I cannot comply") {
		t.Fatalf("error = %q, want the refusal text in it", err.Error())
	}
}
