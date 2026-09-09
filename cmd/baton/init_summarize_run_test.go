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
	"testing"

	"github.com/foxzi/baton/internal/exitcode"
)

// TestInitCmd_SummarizeRunsAgainstStub runs the summarize template end to
// end against a stub of an OpenAI-compatible provider (the same pattern as
// TestLLMSmokeExample), so the template's llm step, its schema and its
// report file step are all exercised without a real provider, an API key or
// the network. It also covers overriding the template's own scenario
// inputs (text, out) with `-i`, the mechanism the template's own comments
// point to for feeding in the caller's data.
func TestInitCmd_SummarizeRunsAgainstStub(t *testing.T) {
	const providerToken = "sk-summarize-test"
	const wantText = "The migration finished ahead of schedule."
	const wantOut = "result.json"

	answer := map[string]any{
		"summary":  "The migration finished early.",
		"keywords": []any{"migration", "schedule"},
	}
	encodedAnswer, err := json.Marshal(answer)
	if err != nil {
		t.Fatalf("marshal stub answer: %v", err)
	}

	var sawPrompt string
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bearer "+providerToken; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read provider body: %v", err)
		}
		sawPrompt = string(body)

		message, _ := json.Marshal(map[string]any{"role": "assistant", "content": string(encodedAnswer)})
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{
			"id": "chatcmpl_summarize_test",
			"model": "openai/gpt-4.1-nano",
			"choices": [{"index": 0, "message": %s, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 40, "completion_tokens": 15}
		}`, message)
	}))
	defer llm.Close()

	dir := t.TempDir()
	var initCode int
	_, initErr := captureOutput(t, func() {
		initCode = run([]string{"init", dir, "--template", "summarize", "--provider", "openrouter"})
	})
	if initCode != exitcode.OK {
		t.Fatalf("init exit code = %d, want %d, stderr: %s", initCode, exitcode.OK, initErr)
	}
	scenarioPath := filepath.Join(dir, "summarize.yaml")

	// The generated baton.yaml has no base_url (the provider is unknown at
	// init time); point a separate config at the stub instead of touching
	// the file init wrote.
	configPath := filepath.Join(dir, "stub-config.yaml")
	config := fmt.Sprintf(`providers:
  openrouter:
    kind: openrouter
    base_url: %s
    api_key: { from: env, key: OPENROUTER_API_KEY }
`, llm.URL)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("OPENROUTER_API_KEY", providerToken)

	runsDir := filepath.Join(dir, "runs")
	var runCode int
	_, runErr := captureOutput(t, func() {
		runCode = run([]string{"run", scenarioPath,
			"--config", configPath,
			"--runs-dir", runsDir,
			"--run-id", "summarize-test",
			"-i", "text=" + wantText,
			"-i", "out=" + wantOut,
		})
	})
	if runCode != exitcode.OK {
		t.Fatalf("run exit code = %d, want %d, stderr: %s", runCode, exitcode.OK, runErr)
	}
	if !strings.Contains(sawPrompt, wantText) {
		t.Errorf("the provider request does not carry the overridden text input:\n%s", sawPrompt)
	}

	resultPath := filepath.Join(dir, wantOut)
	data, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read %s: %v", resultPath, err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("the report step's output is not valid JSON: %v\n%s", err, data)
	}
	if got["summary"] != answer["summary"] {
		t.Errorf("summary = %v, want %v", got["summary"], answer["summary"])
	}
	keywords, ok := got["keywords"].([]any)
	if !ok || len(keywords) != 2 || keywords[0] != "migration" || keywords[1] != "schedule" {
		t.Errorf("keywords = %v, want [migration schedule]", got["keywords"])
	}

	assertNoTokenOnDisk(t, runsDir, providerToken)
}
