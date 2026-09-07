package engine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/config"
	"github.com/foxzi/baton/internal/runstore"
	"github.com/foxzi/baton/internal/secrets"
	"github.com/foxzi/baton/internal/values"
)

// llmSchema is the schema every llm test writes next to the scenario: an
// object with one required string field, small enough that a rejection is
// obviously about the schema and not about the fixture.
const llmSchema = `{
  "type": "object",
  "properties": {"answer": {"type": "string"}},
  "required": ["answer"],
  "additionalProperties": false
}`

// llmServer starts a fake Chat Completions endpoint. Each call takes the
// next handler of the list, so a test spells out a conversation as a
// sequence, and the number of calls actually made is checked at cleanup.
type llmServer struct {
	url      string
	requests []map[string]any
	calls    int
	handlers []http.HandlerFunc
}

func newLLMServer(t *testing.T, handlers ...http.HandlerFunc) *llmServer {
	t.Helper()
	fake := &llmServer{handlers: handlers}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		fake.requests = append(fake.requests, body)
		index := fake.calls
		fake.calls++
		if index >= len(fake.handlers) {
			t.Errorf("unexpected provider call %d", index+1)
			http.Error(w, "unexpected call", http.StatusInternalServerError)
			return
		}
		fake.handlers[index](w, r)
	}))
	t.Cleanup(func() {
		server.Close()
		if fake.calls != len(fake.handlers) {
			t.Errorf("provider calls = %d, want %d", fake.calls, len(fake.handlers))
		}
	})
	fake.url = server.URL
	return fake
}

// submitCall answers with a submit tool call carrying arguments verbatim,
// which is what ModeTool expects. usage may be empty.
func submitCall(t *testing.T, arguments, usage string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{
			"id":    "chatcmpl_1",
			"model": "test-model",
			"choices": []any{map[string]any{
				"index": 0,
				"message": map[string]any{
					"role": "assistant",
					"tool_calls": []any{map[string]any{
						"id":       "call_1",
						"type":     "function",
						"function": map[string]any{"name": "submit", "arguments": arguments},
					}},
				},
				"finish_reason": "tool_calls",
			}},
		}
		if usage != "" {
			var parsed any
			if err := json.Unmarshal([]byte(usage), &parsed); err != nil {
				t.Fatalf("usage fixture: %v", err)
			}
			body["usage"] = parsed
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Errorf("write response: %v", err)
		}
	}
}

// textAnswer answers with plain assistant text, which is what ModePrompt
// expects.
func textAnswer(t *testing.T, text string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{
			"id":    "chatcmpl_1",
			"model": "test-model",
			"choices": []any{map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": text},
				"finish_reason": "stop",
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Errorf("write response: %v", err)
		}
	}
}

// status answers with an HTTP error, for the transient and command classes.
func status(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", code)
	}
}

// llmConfig is a configuration with one openai_compatible provider per url.
// That kind reports no structured output and no api key requirement, so the
// default mode is the submit tool.
func llmConfig(urls map[string]string) *config.Config {
	cfg := &config.Config{Providers: map[string]config.Provider{}}
	for name, url := range urls {
		cfg.Providers[name] = config.Provider{Kind: config.ProviderOpenAICompatible, BaseURL: url}
	}
	return cfg
}

// writeSchema puts the schema file next to the scenario.
func writeSchema(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// 1. A successful llm step: the result is the parsed answer, and output.json
// records the model and the usage.
func TestRun_LLMSuccess(t *testing.T) {
	fake := newLLMServer(t, submitCall(t, `{"answer":"yes"}`, `{"prompt_tokens":10,"completion_tokens":4}`))
	eng, store, dir := newTestEngine(t, `
name: llm
steps:
  - id: ask
    llm:
      model: local/test-model
      prompt: "Say yes"
      schema: schemas/answer.json
`, func(opts *Options) {
		opts.Config = llmConfig(map[string]string{"local": fake.url})
	})
	writeSchema(t, dir, "schemas/answer.json", llmSchema)

	result, err := eng.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("status = %s, error = %+v", result.Status, result.Error)
	}

	out := readStepJSON(t, filepath.Join(store.Dir(), "steps", "ask", "output.json"))
	if got := out["model_used"]; got != "local/test-model" {
		t.Errorf("model_used = %v", got)
	}
	if got := out["mode"]; got != "tool" {
		t.Errorf("mode = %v, want tool", got)
	}
	answer, _ := out["result"].(map[string]any)
	if answer["answer"] != "yes" {
		t.Errorf("result = %v", out["result"])
	}
	usage, _ := out["usage"].(map[string]any)
	if usage["input_tokens"] != float64(10) || usage["output_tokens"] != float64(4) {
		t.Errorf("usage = %v", out["usage"])
	}
	if out["cost_usd"] != nil {
		t.Errorf("cost_usd = %v, want null without a pricing entry", out["cost_usd"])
	}

	// The prompt reached the provider, and the request asked for the submit
	// tool because the provider advertises no structured output.
	request := fake.requests[0]
	messages, _ := request["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages = %v", request["messages"])
	}
	first, _ := messages[0].(map[string]any)
	if first["content"] != "Say yes" {
		t.Errorf("prompt = %v", first["content"])
	}
	if request["tools"] == nil {
		t.Errorf("request carries no tools: %v", request)
	}
}

// 2. The result of an llm step is available to later steps.
func TestRun_LLMResultFeedsNextStep(t *testing.T) {
	fake := newLLMServer(t, submitCall(t, `{"answer":"hello"}`, ""))
	eng, _, dir := newTestEngine(t, `
name: llm
steps:
  - id: ask
    llm:
      model: local/test-model
      prompt: "hi"
      schema: schemas/answer.json
  - id: echo
    run:
      argv: [sh, -c, "echo {{ .steps.ask.result.answer }}"]
`, func(opts *Options) {
		opts.Config = llmConfig(map[string]string{"local": fake.url})
	})
	writeSchema(t, dir, "schemas/answer.json", llmSchema)

	result, err := eng.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("status = %s, error = %+v", result.Status, result.Error)
	}
	if got := eng.steps["echo"].Stdout; got != "hello\n" {
		t.Errorf("stdout = %q", got)
	}
}

// 3. An answer that does not validate is retried on the same model with the
// validation message in the conversation.
func TestRun_LLMSchemaRetry(t *testing.T) {
	fake := newLLMServer(t,
		submitCall(t, `{"answer":42}`, ""),
		submitCall(t, `{"answer":"fixed"}`, ""),
	)
	eng, store, dir := newTestEngine(t, `
name: llm
steps:
  - id: ask
    llm:
      model: local/test-model
      prompt: "hi"
      schema: schemas/answer.json
`, func(opts *Options) {
		opts.Config = llmConfig(map[string]string{"local": fake.url})
	})
	writeSchema(t, dir, "schemas/answer.json", llmSchema)

	result, err := eng.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("status = %s, error = %+v", result.Status, result.Error)
	}

	// The second request carries the rejected tool call and its correction.
	messages, _ := fake.requests[1]["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages = %v", fake.requests[1]["messages"])
	}
	correction, _ := messages[2].(map[string]any)
	if correction["role"] != "tool" {
		t.Errorf("correction role = %v, want tool", correction["role"])
	}
	text, _ := correction["content"].(string)
	if text == "" || !containsAll(text, "did not validate", "/answer") {
		t.Errorf("correction = %q", text)
	}

	state := readRunState(t, store.Dir())
	if got := state.Steps["ask"].Attempts; got != 1 {
		t.Errorf("engine attempts = %d, want 1: the step retries internally", got)
	}
}

// 4. A schema failure that survives every retry fails the step.
func TestRun_LLMSchemaFailure(t *testing.T) {
	fake := newLLMServer(t,
		submitCall(t, `{"answer":1}`, ""),
		submitCall(t, `{"answer":2}`, ""),
	)
	eng, store, dir := newTestEngine(t, `
name: llm
steps:
  - id: ask
    retry: { attempts: 1, on: [schema] }
    llm:
      model: local/test-model
      prompt: "hi"
      schema: schemas/answer.json
`, func(opts *Options) {
		opts.Config = llmConfig(map[string]string{"local": fake.url})
	})
	writeSchema(t, dir, "schemas/answer.json", llmSchema)

	result, err := eng.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusFailed {
		t.Fatalf("status = %s", result.Status)
	}
	if result.Error.Class != ClassSchema {
		t.Errorf("class = %s, want %s", result.Error.Class, ClassSchema)
	}
	out := readStepJSON(t, filepath.Join(store.Dir(), "steps", "ask", "output.json"))
	if out["status"] != "failed" {
		t.Errorf("output.json status = %v", out["status"])
	}
}

// 5. A transient provider failure moves the step to the next fallback model.
func TestRun_LLMFallbackModel(t *testing.T) {
	broken := newLLMServer(t, status(http.StatusInternalServerError))
	good := newLLMServer(t, submitCall(t, `{"answer":"ok"}`, ""))
	eng, store, dir := newTestEngine(t, `
name: llm
steps:
  - id: ask
    retry: { attempts: 0 }
    llm:
      model: first/test-model
      fallback_models: [second/test-model]
      prompt: "hi"
      schema: schemas/answer.json
`, func(opts *Options) {
		opts.Config = llmConfig(map[string]string{"first": broken.url, "second": good.url})
	})
	writeSchema(t, dir, "schemas/answer.json", llmSchema)

	result, err := eng.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("status = %s, error = %+v", result.Status, result.Error)
	}
	out := readStepJSON(t, filepath.Join(store.Dir(), "steps", "ask", "output.json"))
	if got := out["model_used"]; got != "second/test-model" {
		t.Errorf("model_used = %v, want the fallback", got)
	}
}

// 6. A schema failure stays on the same model: the chain is for transient
// failures only (section 3.5).
func TestRun_LLMSchemaDoesNotFallback(t *testing.T) {
	first := newLLMServer(t,
		submitCall(t, `{"answer":1}`, ""),
		submitCall(t, `{"answer":2}`, ""),
	)
	second := newLLMServer(t)
	eng, _, dir := newTestEngine(t, `
name: llm
steps:
  - id: ask
    retry: { attempts: 1, on: [schema] }
    llm:
      model: first/test-model
      fallback_models: [second/test-model]
      prompt: "hi"
      schema: schemas/answer.json
`, func(opts *Options) {
		opts.Config = llmConfig(map[string]string{"first": first.url, "second": second.url})
	})
	writeSchema(t, dir, "schemas/answer.json", llmSchema)

	result, err := eng.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Error == nil || result.Error.Class != ClassSchema {
		t.Fatalf("error = %+v, want schema", result.Error)
	}
}

// 7. structured_mode: prompt sends the schema in the prompt and accepts the
// answer as text, including one wrapped in a code fence.
func TestRun_LLMPromptMode(t *testing.T) {
	fake := newLLMServer(t, textAnswer(t, "```json\n{\"answer\":\"fenced\"}\n```"))
	eng, store, dir := newTestEngine(t, `
name: llm
steps:
  - id: ask
    llm:
      model: local/test-model
      prompt: "hi"
      structured_mode: prompt
      schema: schemas/answer.json
`, func(opts *Options) {
		opts.Config = llmConfig(map[string]string{"local": fake.url})
	})
	writeSchema(t, dir, "schemas/answer.json", llmSchema)

	result, err := eng.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("status = %s, error = %+v", result.Status, result.Error)
	}
	if fake.requests[0]["tools"] != nil {
		t.Errorf("prompt mode sent tools: %v", fake.requests[0])
	}
	messages, _ := fake.requests[0]["messages"].([]any)
	first, _ := messages[0].(map[string]any)
	text, _ := first["content"].(string)
	if !containsAll(text, "hi", "JSON Schema") {
		t.Errorf("prompt = %q", text)
	}
	out := readStepJSON(t, filepath.Join(store.Dir(), "steps", "ask", "output.json"))
	answer, _ := out["result"].(map[string]any)
	if answer["answer"] != "fenced" {
		t.Errorf("result = %v", out["result"])
	}
}

// 8. with: variables and the system prompt are rendered from files.
func TestRun_LLMPromptFilesAndWith(t *testing.T) {
	fake := newLLMServer(t, submitCall(t, `{"answer":"ok"}`, ""))
	eng, store, dir := newTestEngine(t, `
name: llm
inputs:
  topic: { type: string, default: goats }
steps:
  - id: seed
    run:
      argv: [echo, context-line]
  - id: ask
    llm:
      model: local/test-model
      system: prompts/system.md
      prompt: prompts/ask.md
      with:
        note: "{{ .steps.seed.stdout }}"
      schema: schemas/answer.json
`, func(opts *Options) {
		opts.Config = llmConfig(map[string]string{"local": fake.url})
		opts.Inputs = map[string]any{"topic": "goats"}
	})
	writeSchema(t, dir, "schemas/answer.json", llmSchema)
	writeSchema(t, dir, "prompts/system.md", "You answer about {{ .inputs.topic }}.")
	writeSchema(t, dir, "prompts/ask.md", "Note: {{ .note }}")

	result, err := eng.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("status = %s, error = %+v", result.Status, result.Error)
	}

	messages, _ := fake.requests[0]["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("messages = %v", fake.requests[0]["messages"])
	}
	system, _ := messages[0].(map[string]any)
	if system["content"] != "You answer about goats." {
		t.Errorf("system = %v", system["content"])
	}
	user, _ := messages[1].(map[string]any)
	if user["content"] != "Note: context-line\n" {
		t.Errorf("prompt = %v", user["content"])
	}

	input := readStepJSON(t, filepath.Join(store.Dir(), "steps", "ask", "input.json"))
	if input["prompt"] != "Note: context-line\n" {
		t.Errorf("input.json prompt = %v", input["prompt"])
	}
}

// 9. Cost comes from the pricing table when the provider reports none, and
// lands in cost.json and run.json.
func TestRun_LLMCostFromPricing(t *testing.T) {
	fake := newLLMServer(t, submitCall(t, `{"answer":"ok"}`, `{"prompt_tokens":1000000,"completion_tokens":500000}`))
	eng, store, dir := newTestEngine(t, `
name: llm
steps:
  - id: ask
    llm:
      model: local/test-model
      prompt: "hi"
      schema: schemas/answer.json
`, func(opts *Options) {
		cfg := llmConfig(map[string]string{"local": fake.url})
		cfg.Pricing = map[string]config.Price{
			"local/test-model": {InputPerMTok: 3, OutputPerMTok: 15},
		}
		opts.Config = cfg
	})
	writeSchema(t, dir, "schemas/answer.json", llmSchema)

	if _, err := eng.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var report runstore.CostReport
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(store.Dir(), "cost.json"))), &report); err != nil {
		t.Fatalf("cost.json: %v", err)
	}
	if len(report.Steps) != 1 {
		t.Fatalf("cost.json steps = %+v", report.Steps)
	}
	entry := report.Steps[0]
	if entry.Step != "ask" || entry.Model != "local/test-model" {
		t.Errorf("entry = %+v", entry)
	}
	// 1M input at $3 plus 0.5M output at $15.
	if entry.CostUSD == nil || *entry.CostUSD != 10.5 {
		t.Fatalf("cost_usd = %v, want 10.5", entry.CostUSD)
	}
	if report.TotalUSD == nil || *report.TotalUSD != 10.5 {
		t.Errorf("total_usd = %v", report.TotalUSD)
	}

	state := readRunState(t, store.Dir())
	if state.CostUSD == nil || *state.CostUSD != 10.5 {
		t.Errorf("run.json cost_usd = %v", state.CostUSD)
	}
}

// 10. A run that goes over budget.usd fails with the budget class, which is
// never retried and never falls back to another model.
func TestRun_LLMRunBudget(t *testing.T) {
	fake := newLLMServer(t, submitCall(t, `{"answer":"ok"}`, `{"prompt_tokens":1000000,"completion_tokens":0}`))
	unused := newLLMServer(t)
	eng, store, dir := newTestEngine(t, `
name: llm
budget:
  usd: 1
steps:
  - id: ask
    llm:
      model: local/test-model
      fallback_models: [other/test-model]
      prompt: "hi"
      schema: schemas/answer.json
`, func(opts *Options) {
		cfg := llmConfig(map[string]string{"local": fake.url, "other": unused.url})
		cfg.Pricing = map[string]config.Price{"local/test-model": {InputPerMTok: 3}}
		opts.Config = cfg
	})
	writeSchema(t, dir, "schemas/answer.json", llmSchema)

	result, err := eng.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusFailed || result.Error.Class != ClassBudget {
		t.Fatalf("status = %s, error = %+v", result.Status, result.Error)
	}
	if got := readRunState(t, store.Dir()).Steps["ask"].Attempts; got != 1 {
		t.Errorf("attempts = %d, want 1: budget is never retried", got)
	}
}

// 11. An unknown provider, a missing schema file and an empty prompt are all
// configuration errors, reported before the provider is called.
func TestRun_LLMConfigErrors(t *testing.T) {
	cases := []struct {
		name    string
		step    string
		schema  bool
		message string
	}{
		{
			name:    "unknown provider",
			step:    "model: nope/test-model\n      prompt: hi\n      schema: schemas/answer.json",
			schema:  true,
			message: "unknown provider",
		},
		{
			name:    "missing schema file",
			step:    "model: local/test-model\n      prompt: hi\n      schema: schemas/absent.json",
			message: "llm.schema",
		},
		{
			name:    "empty prompt",
			step:    "model: local/test-model\n      prompt: \"   \"\n      schema: schemas/answer.json",
			schema:  true,
			message: "llm.prompt is empty",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			unused := newLLMServer(t)
			body := fmt.Sprintf(`
name: llm
steps:
  - id: ask
    llm:
      %s
`, testCase.step)
			eng, _, dir := newTestEngine(t, body, func(opts *Options) {
				opts.Config = llmConfig(map[string]string{"local": unused.url})
			})
			if testCase.schema {
				writeSchema(t, dir, "schemas/answer.json", llmSchema)
			}

			result, err := eng.Run(t.Context())
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if result.Error == nil || result.Error.Class != ClassConfig {
				t.Fatalf("error = %+v, want config", result.Error)
			}
			if !containsAll(result.Error.Message, testCase.message) {
				t.Errorf("message = %q, want it to mention %q", result.Error.Message, testCase.message)
			}
		})
	}
}

// 12. The api key of a provider is masked in the run directory even though a
// scenario cannot read it (section 13).
func TestRun_LLMProviderKeyRedacted(t *testing.T) {
	fake := newLLMServer(t, submitCall(t, `{"answer":"topsecret"}`, ""))
	key := values.NewSecret("providers.local.api_key", "topsecret")
	eng, store, dir := newTestEngine(t, `
name: llm
steps:
  - id: ask
    llm:
      model: local/test-model
      prompt: "hi"
      schema: schemas/answer.json
`, func(opts *Options) {
		opts.Config = llmConfig(map[string]string{"local": fake.url})
		opts.ProviderKeys = map[string]values.Secret{"local": key}
		store, err := secrets.Resolve(nil, ".")
		if err != nil {
			t.Fatalf("secrets.Resolve: %v", err)
		}
		opts.Secrets = store.WithHidden(key)
	})
	writeSchema(t, dir, "schemas/answer.json", llmSchema)

	if _, err := eng.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The provider echoed the key back as its answer; the run directory must
	// not carry it in plaintext.
	body := readFile(t, filepath.Join(store.Dir(), "steps", "ask", "output.json"))
	if containsAll(body, "topsecret") {
		t.Errorf("output.json leaks the api key:\n%s", body)
	}

	// It did reach the provider, as the Authorization header.
	if fake.requests[0] == nil {
		t.Fatal("no request recorded")
	}
}

// containsAll reports whether text contains every substring.
func containsAll(text string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(text, part) {
			return false
		}
	}
	return true
}
