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

// newTestAnthropic points a provider at a local server instead of the real
// API, so tests never depend on network access or a real key.
func newTestAnthropic(t *testing.T, handler http.HandlerFunc) Provider {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	p, err := newAnthropic(config.Provider{
		Kind:    config.ProviderAnthropic,
		BaseURL: server.URL,
	}, values.NewSecret("anthropic", "sk-test"))
	if err != nil {
		t.Fatalf("newAnthropic: %v", err)
	}
	return p
}

func TestNewAnthropicRequiresKey(t *testing.T) {
	_, err := newAnthropic(config.Provider{Kind: config.ProviderAnthropic}, values.Secret{})
	if err == nil {
		t.Fatal("expected an error for a missing api_key")
	}
	if Class(err) != ClassConfig {
		t.Fatalf("class = %q, want %q", Class(err), ClassConfig)
	}
}

func TestAnthropicCapabilities(t *testing.T) {
	p := newTestAnthropic(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no request expected")
	})

	caps := p.Capabilities()
	if !caps.StructuredOutput || !caps.Tools || !caps.Vision {
		t.Fatalf("caps = %+v, want all true", caps)
	}
}

func TestAnthropicCapabilitiesOverride(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no request expected")
	}))
	defer server.Close()

	no := false
	p, err := newAnthropic(config.Provider{
		Kind:         config.ProviderAnthropic,
		BaseURL:      server.URL,
		Capabilities: &config.Capabilities{Tools: &no},
	}, values.NewSecret("anthropic", "sk-test"))
	if err != nil {
		t.Fatalf("newAnthropic: %v", err)
	}

	caps := p.Capabilities()
	if caps.Tools {
		t.Fatal("Tools override was not applied")
	}
	if !caps.StructuredOutput {
		t.Fatal("StructuredOutput should keep its default")
	}
}

// writeMessage answers a Messages.New call with a minimal, well-formed
// response body.
func writeMessage(t *testing.T, w http.ResponseWriter, content string, usage string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	_, err := w.Write([]byte(`{
		"id": "msg_1",
		"type": "message",
		"role": "assistant",
		"model": "claude-3-5-sonnet-20241022",
		"content": ` + content + `,
		"stop_reason": "end_turn",
		"stop_sequence": null,
		"usage": ` + usage + `
	}`))
	if err != nil {
		t.Fatalf("write response: %v", err)
	}
}

const testUsage = `{"input_tokens": 12, "output_tokens": 34, "cache_read_input_tokens": 5, "cache_creation_input_tokens": 0}`

func TestAnthropicCompleteText(t *testing.T) {
	var gotBody map[string]any
	p := newTestAnthropic(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("X-Api-Key"); got != "sk-test" {
			t.Fatalf("X-Api-Key = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeMessage(t, w, `[{"type": "text", "text": "hello there"}]`, testUsage)
	})

	resp, err := p.Complete(context.Background(), Request{
		Model:    "claude-3-5-sonnet-20241022",
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
	if resp.FinishReason != "end_turn" {
		t.Fatalf("FinishReason = %q", resp.FinishReason)
	}
	if resp.Model != "claude-3-5-sonnet-20241022" {
		t.Fatalf("Model = %q", resp.Model)
	}

	if gotBody["model"] != "claude-3-5-sonnet-20241022" {
		t.Fatalf("request model = %v", gotBody["model"])
	}
	if gotBody["max_tokens"].(float64) != anthropicDefaultMaxTokens {
		t.Fatalf("request max_tokens = %v, want default", gotBody["max_tokens"])
	}
	system, _ := gotBody["system"].([]any)
	if len(system) != 1 {
		t.Fatalf("request system = %v", gotBody["system"])
	}
}

func TestAnthropicCompleteMaxTokens(t *testing.T) {
	var gotBody map[string]any
	p := newTestAnthropic(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		writeMessage(t, w, `[{"type": "text", "text": "ok"}]`, testUsage)
	})

	_, err := p.Complete(context.Background(), Request{
		Model:     "claude-3-5-sonnet-20241022",
		Messages:  []Message{{Role: RoleUser, Text: "hi"}},
		MaxTokens: 256,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if gotBody["max_tokens"].(float64) != 256 {
		t.Fatalf("request max_tokens = %v, want 256", gotBody["max_tokens"])
	}
}

func TestAnthropicCompleteToolCall(t *testing.T) {
	p := newTestAnthropic(t, func(w http.ResponseWriter, r *http.Request) {
		writeMessage(t, w, `[{"type": "tool_use", "id": "toolu_1", "name": "lookup", "input": {"q": "cats"}}]`, testUsage)
	})

	resp, err := p.Complete(context.Background(), Request{
		Model:    "claude-3-5-sonnet-20241022",
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
	if call.ID != "toolu_1" || call.Name != "lookup" {
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

func TestAnthropicCompleteConversationRoundtrip(t *testing.T) {
	var gotBody struct {
		Messages []map[string]any `json:"messages"`
	}
	p := newTestAnthropic(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeMessage(t, w, `[{"type": "text", "text": "done"}]`, testUsage)
	})

	_, err := p.Complete(context.Background(), Request{
		Model: "claude-3-5-sonnet-20241022",
		Messages: []Message{
			{Role: RoleUser, Text: "call lookup"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{
				ID:        "toolu_1",
				Name:      "lookup",
				Arguments: json.RawMessage(`{"q":"cats"}`),
			}}},
			{Role: RoleTool, ToolCallID: "toolu_1", Text: "cats are great"},
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
	if gotBody.Messages[2]["role"] != "user" {
		t.Fatalf("tool-result message role = %v, want user", gotBody.Messages[2]["role"])
	}
}

func TestAnthropicCompleteModeToolForcesSubmit(t *testing.T) {
	var gotBody map[string]any
	p := newTestAnthropic(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		writeMessage(t, w, `[{"type": "tool_use", "id": "toolu_1", "name": "submit", "input": {"answer": 42}}]`, testUsage)
	})

	resp, err := p.Complete(context.Background(), Request{
		Model:    "claude-3-5-sonnet-20241022",
		Messages: []Message{{Role: RoleUser, Text: "hi"}},
		Schema:   json.RawMessage(`{"type":"object","properties":{"answer":{"type":"integer"}}}`),
		Mode:     ModeTool,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	choice, _ := gotBody["tool_choice"].(map[string]any)
	if choice["type"] != "tool" || choice["name"] != SubmitToolName {
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

func TestAnthropicCompleteModeNativeSendsSchema(t *testing.T) {
	var gotBody map[string]any
	p := newTestAnthropic(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		writeMessage(t, w, `[{"type": "text", "text": "{\"answer\":42}"}]`, testUsage)
	})

	_, err := p.Complete(context.Background(), Request{
		Model:    "claude-3-5-sonnet-20241022",
		Messages: []Message{{Role: RoleUser, Text: "hi"}},
		Schema:   json.RawMessage(`{"type":"object","properties":{"answer":{"type":"integer"}}}`),
		Mode:     ModeNative,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	outputConfig, _ := gotBody["output_config"].(map[string]any)
	format, _ := outputConfig["format"].(map[string]any)
	if format["type"] != "json_schema" {
		t.Fatalf("output_config.format = %v", outputConfig["format"])
	}
	schema, _ := format["schema"].(map[string]any)
	if schema["type"] != "object" {
		t.Fatalf("output_config.format.schema = %v", format["schema"])
	}
}

func TestAnthropicCompleteModeNativeRejectsNonObjectSchema(t *testing.T) {
	p := newTestAnthropic(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no request expected")
	})

	_, err := p.Complete(context.Background(), Request{
		Model:    "claude-3-5-sonnet-20241022",
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

func TestAnthropicCompleteErrorClassification(t *testing.T) {
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
			p := newTestAnthropic(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				w.Write([]byte(`{"type":"error","error":{"type":"some_error","message":"boom"}}`))
			})

			_, err := p.Complete(context.Background(), Request{
				Model:    "claude-3-5-sonnet-20241022",
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

func TestAnthropicCompleteTimeout(t *testing.T) {
	p := newTestAnthropic(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := p.Complete(ctx, Request{
		Model:    "claude-3-5-sonnet-20241022",
		Messages: []Message{{Role: RoleUser, Text: "hi"}},
	})
	if err == nil {
		t.Fatal("expected an error for a cancelled context")
	}
	if Class(err) != ClassTimeout {
		t.Fatalf("class = %q, want %q", Class(err), ClassTimeout)
	}
}
