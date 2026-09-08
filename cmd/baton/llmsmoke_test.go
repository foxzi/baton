package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/foxzi/baton/internal/exitcode"
)

// TestLLMSmokeExample runs examples/llm-smoke.yaml against a stub of an
// OpenAI-compatible provider. It covers the shape the example is made of: a
// plain-text llm step, a schema-constrained llm step and a notify step that
// reports both results.
func TestLLMSmokeExample(t *testing.T) {
	const providerToken = "sk-smoke-token"

	var calls atomic.Int64

	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bearer "+providerToken; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read provider body: %v", err)
		}
		prompt := string(body)

		n := calls.Add(1)
		var answer map[string]any
		switch n {
		case 1:
			if !strings.Contains(prompt, "capital of France") {
				t.Errorf("the first call does not carry the ping prompt:\n%s", prompt)
			}
			answer = map[string]any{"answer": "Paris"}
		case 2:
			if !strings.Contains(prompt, "deploy pipeline") {
				t.Errorf("the second call does not carry the input text:\n%s", prompt)
			}
			answer = map[string]any{
				"language":  "en",
				"sentiment": "positive",
				"keywords":  []any{"deploy", "pipeline", "rollback"},
			}
		default:
			t.Errorf("unexpected completion request #%d", n)
			http.NotFound(w, r)
			return
		}

		encoded, err := json.Marshal(answer)
		if err != nil {
			t.Errorf("marshal answer: %v", err)
		}
		message, _ := json.Marshal(map[string]any{"role": "assistant", "content": string(encoded)})
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{
			"id": "chatcmpl_smoke",
			"model": "openai/gpt-4.1-nano",
			"choices": [{"index": 0, "message": %s, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 60, "completion_tokens": 20}
		}`, message)
	}))
	defer llm.Close()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	config := fmt.Sprintf(`providers:
  openrouter:
    kind: openrouter
    base_url: %s
    api_key: { from: env, key: OPENROUTER_API_KEY }
notify:
  report:
    kind: stdout
`, llm.URL)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	runsDir := filepath.Join(dir, "runs")
	cacheDir := filepath.Join(dir, "cache")
	t.Setenv("OPENROUTER_API_KEY", providerToken)

	var code int
	stdout, stderr := captureOutput(t, func() {
		code = run([]string{"run", "../../examples/llm-smoke.yaml",
			"--config", configPath,
			"--cache-dir", cacheDir,
			"--runs-dir", runsDir,
			"--run-id", "smoke",
		})
	})
	if code != exitcode.OK {
		t.Fatalf("run() = %d, want %d; stderr:\n%s", code, exitcode.OK, stderr)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("completion requests = %d, want 2", got)
	}
	if !strings.Contains(stdout, "llm-smoke: Paris") {
		t.Errorf("the report does not carry the ping answer:\n%s", stdout)
	}
	if !strings.Contains(stdout, "language: en") || !strings.Contains(stdout, "sentiment: positive") {
		t.Errorf("the report does not carry the extracted fields:\n%s", stdout)
	}
	if !strings.Contains(stdout, "keywords: deploy, pipeline, rollback") {
		t.Errorf("the report does not carry the keywords:\n%s", stdout)
	}

	assertNoTokenOnDisk(t, runsDir, providerToken)
	assertNoTokenOnDisk(t, cacheDir, providerToken)
}
