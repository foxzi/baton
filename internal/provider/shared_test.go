package provider

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/config"
	"github.com/foxzi/baton/internal/values"
)

func TestSplitModel(t *testing.T) {
	cases := []struct {
		name         string
		ref          string
		wantProvider string
		wantModel    string
		wantErr      bool
	}{
		{"plain", "openai/gpt-4o-mini", "openai", "gpt-4o-mini", false},
		{"model contains a slash", "openrouter/anthropic/claude-3.5-sonnet", "openrouter", "anthropic/claude-3.5-sonnet", false},
		{"no slash", "gpt-4o-mini", "", "", true},
		{"empty", "", "", "", true},
		{"leading slash", "/gpt-4o-mini", "", "", true},
		{"trailing slash", "openai/", "", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotProvider, gotModel, err := SplitModel(tc.ref)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("SplitModel(%q): expected an error", tc.ref)
				}
				if Class(err) != ClassConfig {
					t.Fatalf("class = %q, want %q", Class(err), ClassConfig)
				}
				return
			}
			if err != nil {
				t.Fatalf("SplitModel(%q): %v", tc.ref, err)
			}
			if gotProvider != tc.wantProvider || gotModel != tc.wantModel {
				t.Fatalf("SplitModel(%q) = (%q, %q), want (%q, %q)", tc.ref, gotProvider, gotModel, tc.wantProvider, tc.wantModel)
			}
		})
	}
}

func TestResolveMode(t *testing.T) {
	structuredAndTools := Caps{StructuredOutput: true, Tools: true}
	toolsOnly := Caps{StructuredOutput: false, Tools: true}
	neither := Caps{StructuredOutput: false, Tools: false}

	cases := []struct {
		name string
		want Mode
		caps Caps
		got  Mode
	}{
		{"unset, structured+tools -> native", ModeUnset, structuredAndTools, ModeNative},
		{"unset, tools only -> tool", ModeUnset, toolsOnly, ModeTool},
		{"unset, neither -> prompt", ModeUnset, neither, ModePrompt},

		{"native, structured+tools -> native", ModeNative, structuredAndTools, ModeNative},
		{"native, tools only -> downgrades to tool", ModeNative, toolsOnly, ModeTool},
		{"native, neither -> downgrades to prompt", ModeNative, neither, ModePrompt},

		{"tool, structured+tools -> tool", ModeTool, structuredAndTools, ModeTool},
		{"tool, tools only -> tool", ModeTool, toolsOnly, ModeTool},
		{"tool, neither -> downgrades to prompt", ModeTool, neither, ModePrompt},

		{"prompt, structured+tools -> prompt", ModePrompt, structuredAndTools, ModePrompt},
		{"prompt, tools only -> prompt", ModePrompt, toolsOnly, ModePrompt},
		{"prompt, neither -> prompt", ModePrompt, neither, ModePrompt},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveMode(tc.want, tc.caps)
			if got != tc.got {
				t.Fatalf("ResolveMode(%q, %+v) = %q, want %q", tc.want, tc.caps, got, tc.got)
			}
		})
	}
}

func TestSubmitTool(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"answer":{"type":"integer"}}}`)
	tool := SubmitTool(schema)

	if tool.Name != SubmitToolName {
		t.Fatalf("Name = %q, want %q", tool.Name, SubmitToolName)
	}
	if string(tool.Schema) != string(schema) {
		t.Fatalf("Schema = %s, want %s", tool.Schema, schema)
	}
}

func TestPromptInstruction(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"answer":{"type":"integer"}}}`)
	text := PromptInstruction(schema)

	if !strings.Contains(text, string(schema)) {
		t.Fatalf("PromptInstruction did not carry the schema through: %s", text)
	}
}

func TestResultModeTool(t *testing.T) {
	t.Run("submit call returns its arguments", func(t *testing.T) {
		resp := Response{ToolCalls: []ToolCall{
			{ID: "1", Name: "lookup", Arguments: json.RawMessage(`{"q":"cats"}`)},
			{ID: "2", Name: SubmitToolName, Arguments: json.RawMessage(`{"answer":42}`)},
		}}
		got, err := Result(resp, ModeTool)
		if err != nil {
			t.Fatalf("Result: %v", err)
		}
		if string(got) != `{"answer":42}` {
			t.Fatalf("Result = %s", got)
		}
	})

	t.Run("only some other tool call is a schema error", func(t *testing.T) {
		resp := Response{ToolCalls: []ToolCall{
			{ID: "1", Name: "lookup", Arguments: json.RawMessage(`{"q":"cats"}`)},
		}}
		_, err := Result(resp, ModeTool)
		if err == nil {
			t.Fatal("expected an error")
		}
		if Class(err) != ClassSchema {
			t.Fatalf("class = %q, want %q", Class(err), ClassSchema)
		}
	})

	t.Run("no calls is a schema error", func(t *testing.T) {
		_, err := Result(Response{}, ModeTool)
		if err == nil {
			t.Fatal("expected an error")
		}
		if Class(err) != ClassSchema {
			t.Fatalf("class = %q, want %q", Class(err), ClassSchema)
		}
	})
}

func TestResultText(t *testing.T) {
	cases := []struct {
		name    string
		mode    Mode
		text    string
		want    string
		wantErr bool
	}{
		{"native, plain JSON", ModeNative, `{"answer":42}`, `{"answer":42}`, false},
		{"prompt, plain JSON", ModePrompt, `{"answer":42}`, `{"answer":42}`, false},
		{"bare code fence", ModePrompt, "```\n{\"answer\":42}\n```", `{"answer":42}`, false},
		{"fence names a language", ModePrompt, "```json\n{\"answer\":42}\n```", `{"answer":42}`, false},
		{"prose around the fence", ModePrompt, "Sure, here it is:\n```json\n{\"answer\":42}\n```\nHope that helps.", `{"answer":42}`, false},
		{"empty text is a schema error", ModePrompt, "", "", true},
		{"non-JSON text is a schema error", ModePrompt, "not json at all", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Result(Response{Text: tc.text}, tc.mode)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Result(%q): expected an error", tc.text)
				}
				if Class(err) != ClassSchema {
					t.Fatalf("class = %q, want %q", Class(err), ClassSchema)
				}
				return
			}
			if err != nil {
				t.Fatalf("Result(%q): %v", tc.text, err)
			}
			if string(got) != tc.want {
				t.Fatalf("Result(%q) = %s, want %s", tc.text, got, tc.want)
			}
		})
	}
}

func TestEnsureObjectSchema(t *testing.T) {
	cases := []struct {
		name    string
		schema  string
		wantErr bool
	}{
		{"object schema passes", `{"type":"object","properties":{}}`, false},
		{"non-object root type", `{"type":"array"}`, true},
		{"no type at all", `{"properties":{}}`, true},
		{"not valid JSON", `{not json`, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ensureObjectSchema(json.RawMessage(tc.schema))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ensureObjectSchema(%s): expected an error", tc.schema)
				}
				if Class(err) != ClassConfig {
					t.Fatalf("class = %q, want %q", Class(err), ClassConfig)
				}
				return
			}
			if err != nil {
				t.Fatalf("ensureObjectSchema(%s): %v", tc.schema, err)
			}
		})
	}
}

func TestApplyCaps(t *testing.T) {
	base := Caps{StructuredOutput: true, Tools: true, Vision: true, MaxContext: 100}

	t.Run("nil override changes nothing", func(t *testing.T) {
		got := applyCaps(base, nil)
		if got != base {
			t.Fatalf("applyCaps = %+v, want unchanged %+v", got, base)
		}
	})

	t.Run("sets only structured_output leaves tools alone", func(t *testing.T) {
		yes := true
		got := applyCaps(base, &config.Capabilities{StructuredOutput: &yes})
		if !got.StructuredOutput {
			t.Fatal("StructuredOutput override was not applied")
		}
		if !got.Tools {
			t.Fatal("Tools should be untouched")
		}
	})

	t.Run("false actually turns the field off", func(t *testing.T) {
		no := false
		got := applyCaps(base, &config.Capabilities{Tools: &no})
		if got.Tools {
			t.Fatal("Tools override to false was not applied")
		}
		if !got.StructuredOutput {
			t.Fatal("StructuredOutput should be untouched")
		}
	})
}

func TestRequireKey(t *testing.T) {
	t.Run("present secret returns its plaintext", func(t *testing.T) {
		got, err := requireKey(config.ProviderAnthropic, values.NewSecret("anthropic", "sk-test"))
		if err != nil {
			t.Fatalf("requireKey: %v", err)
		}
		if got != "sk-test" {
			t.Fatalf("requireKey = %q, want %q", got, "sk-test")
		}
	})

	t.Run("zero secret is a config error", func(t *testing.T) {
		_, err := requireKey(config.ProviderAnthropic, values.Secret{})
		if err == nil {
			t.Fatal("expected an error")
		}
		if Class(err) != ClassConfig {
			t.Fatalf("class = %q, want %q", Class(err), ClassConfig)
		}
	})
}

func TestBodyTail(t *testing.T) {
	t.Run("short body comes back trimmed", func(t *testing.T) {
		got := bodyTail([]byte("  boom  "))
		if got != "boom" {
			t.Fatalf("bodyTail = %q, want %q", got, "boom")
		}
	})

	t.Run("long body is truncated with a byte count", func(t *testing.T) {
		long := strings.Repeat("x", maxBodyTail+100)
		got := bodyTail([]byte(long))
		if len(got) <= maxBodyTail {
			t.Fatalf("bodyTail did not truncate: len = %d", len(got))
		}
		wantSuffix := "... (" + strconv.Itoa(len(long)) + " bytes)"
		if !strings.HasSuffix(got, wantSuffix) {
			t.Fatalf("bodyTail = %q, want it to end with %q", got, wantSuffix)
		}
	})
}

func TestClass(t *testing.T) {
	t.Run("returns the class of an *Error", func(t *testing.T) {
		err := errorf(ClassSchema, "boom")
		if Class(err) != ClassSchema {
			t.Fatalf("Class = %q, want %q", Class(err), ClassSchema)
		}
	})

	t.Run("empty string for an error from elsewhere", func(t *testing.T) {
		if got := Class(errors.New("boom")); got != "" {
			t.Fatalf("Class = %q, want empty", got)
		}
	})
}

func TestErrorType(t *testing.T) {
	t.Run("Error includes the cause and the body tail", func(t *testing.T) {
		cause := errors.New("underlying failure")
		err := &Error{Class: ClassCommand, Msg: "request failed", Err: cause, BodyTail: "server said no"}

		got := err.Error()
		if !strings.Contains(got, "request failed") {
			t.Fatalf("Error() = %q, want it to contain the message", got)
		}
		if !strings.Contains(got, "underlying failure") {
			t.Fatalf("Error() = %q, want it to contain the cause", got)
		}
		if !strings.Contains(got, "server said no") {
			t.Fatalf("Error() = %q, want it to contain the body tail", got)
		}
	})

	t.Run("Unwrap exposes the cause to errors.Is", func(t *testing.T) {
		cause := errors.New("underlying failure")
		err := &Error{Class: ClassCommand, Msg: "request failed", Err: cause}

		if !errors.Is(err, cause) {
			t.Fatal("errors.Is did not find the cause through Unwrap")
		}
	})
}

func TestNewFactory(t *testing.T) {
	t.Run("anthropic", func(t *testing.T) {
		p, err := New("main", config.Provider{Kind: config.ProviderAnthropic}, values.NewSecret("anthropic", "sk-test"))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if _, ok := p.(*anthropicProvider); !ok {
			t.Fatalf("provider is %T, want *anthropicProvider", p)
		}
	})

	t.Run("openai", func(t *testing.T) {
		p, err := New("main", config.Provider{Kind: config.ProviderOpenAI}, values.NewSecret("openai", "sk-test"))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if _, ok := p.(*openaiProvider); !ok {
			t.Fatalf("provider is %T, want *openaiProvider", p)
		}
	})

	t.Run("openrouter", func(t *testing.T) {
		p, err := New("main", config.Provider{Kind: config.ProviderOpenRouter}, values.NewSecret("or", "sk-or-test"))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if _, ok := p.(*compatProvider); !ok {
			t.Fatalf("provider is %T, want *compatProvider", p)
		}
	})

	t.Run("openai_compatible", func(t *testing.T) {
		p, err := New("main", config.Provider{Kind: config.ProviderOpenAICompatible, BaseURL: "http://localhost:1234"}, values.Secret{})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if _, ok := p.(*compatProvider); !ok {
			t.Fatalf("provider is %T, want *compatProvider", p)
		}
	})

	t.Run("empty kind is a config error naming the provider", func(t *testing.T) {
		_, err := New("main", config.Provider{}, values.Secret{})
		if err == nil {
			t.Fatal("expected an error")
		}
		if Class(err) != ClassConfig {
			t.Fatalf("class = %q, want %q", Class(err), ClassConfig)
		}
		if !strings.Contains(err.Error(), "main") {
			t.Fatalf("error = %q, want it to name the provider %q", err.Error(), "main")
		}
	})

	t.Run("unknown kind is a config error naming the provider and the kind", func(t *testing.T) {
		_, err := New("main", config.Provider{Kind: "carrier-pigeon"}, values.Secret{})
		if err == nil {
			t.Fatal("expected an error")
		}
		if Class(err) != ClassConfig {
			t.Fatalf("class = %q, want %q", Class(err), ClassConfig)
		}
		if !strings.Contains(err.Error(), "main") || !strings.Contains(err.Error(), "carrier-pigeon") {
			t.Fatalf("error = %q, want it to name both the provider %q and the kind %q", err.Error(), "main", "carrier-pigeon")
		}
	})
}
