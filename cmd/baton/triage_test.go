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

// TestExample_Triage runs examples/triage.yaml against stubs of the GitLab API
// and of an OpenAI-compatible provider. It covers the shape the example is
// made of: raw requests through an api pack, an llm step with a schema, and a
// switch that pages the on-call channel for a critical report only.
func TestExample_Triage(t *testing.T) {
	const (
		gitlabToken   = "glpat-triage-token"
		providerToken = "sk-triage-token"
	)

	var (
		labelCalls   atomic.Int64
		commentCalls atomic.Int64
		seenLabels   string
		seenComment  string
	)

	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != gitlabToken {
			t.Errorf("PRIVATE-TOKEN = %q, want %q", r.Header.Get("PRIVATE-TOKEN"), gitlabToken)
		}
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.EscapedPath()
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/issues/41"):
			fmt.Fprint(w, `{
				"iid": 41,
				"title": "Import loses the last row of a csv",
				"author": {"username": "reporter"},
				"labels": ["needs-triage"],
				"description": "## Steps\n\n1. Upload **rows.csv**\n2. The last row is missing.",
				"web_url": "https://gitlab.example/acme/-/issues/41"
			}`)
		case r.Method == http.MethodPut && strings.HasSuffix(path, "/issues/41"):
			labelCalls.Add(1)
			seenLabels = r.URL.Query().Get("add_labels")
			fmt.Fprint(w, `{"iid": 41, "labels": ["needs-triage", "bug"]}`)
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/issues/41/notes"):
			commentCalls.Add(1)
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read comment body: %v", err)
			}
			var note struct {
				Body string `json:"body"`
			}
			if err := json.Unmarshal(body, &note); err != nil {
				t.Errorf("comment body is not json: %v (%s)", err, body)
			}
			seenComment = note.Body
			fmt.Fprint(w, `{"id": 9001, "web_url": "https://gitlab.example/acme/-/issues/41#note_9001"}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, path)
			http.NotFound(w, r)
		}
	}))
	defer forge.Close()

	// The severity the model answers with; the switch branches on it.
	severity := "critical"
	needsInfo := false

	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bearer "+providerToken; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read provider body: %v", err)
		}
		// md2text turns the markdown body into plain text before it reaches
		// the model: the heading marks and the backticks are gone.
		prompt := string(body)
		if strings.Contains(prompt, "## Steps") || strings.Contains(prompt, "**rows.csv**") {
			t.Errorf("the issue body reached the model as markdown:\n%s", prompt)
		}
		if !strings.Contains(prompt, "Upload rows.csv") {
			t.Errorf("the prompt does not carry the issue body:\n%s", prompt)
		}

		answer := map[string]any{
			"severity":   severity,
			"area":       "csv import",
			"summary":    "The csv importer drops the final row of every file.",
			"labels":     []any{"bug", "data-loss"},
			"needs_info": needsInfo,
		}
		if needsInfo {
			answer["question"] = "Which delimiter does the file use?"
		}
		encoded, err := json.Marshal(answer)
		if err != nil {
			t.Errorf("marshal answer: %v", err)
		}
		message, _ := json.Marshal(map[string]any{"role": "assistant", "content": string(encoded)})
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{
			"id": "chatcmpl_triage",
			"model": "anthropic/claude-3.5-sonnet",
			"choices": [{"index": 0, "message": %s, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 700, "completion_tokens": 90}
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
  alerts:
    kind: stdout
pricing:
  openrouter/anthropic/claude-3.5-sonnet:
    input_per_mtok: 3
    output_per_mtok: 15
`, llm.URL)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	runsDir := filepath.Join(dir, "runs")
	t.Setenv("GITLAB_TOKEN", gitlabToken)
	t.Setenv("OPENROUTER_API_KEY", providerToken)

	// Every run gets its own cache directory: the two runs below differ only
	// in the answer of the model, which a shared cache would replay.
	triage := func(runID, cacheDir string) (string, string, int) {
		var code int
		stdout, stderr := captureOutput(t, func() {
			code = run([]string{"run", "../../examples/triage.yaml",
				"-i", "project=7",
				"-i", "issue=41",
				"-i", "api_url=" + forge.URL + "/api/v4",
				"--config", configPath,
				"--cache-dir", cacheDir,
				"--runs-dir", runsDir,
				"--run-id", runID,
			})
		})
		return stdout, stderr, code
	}

	criticalCache := filepath.Join(dir, "cache-critical")
	stdout, stderr, code := triage("critical", criticalCache)
	if code != exitcode.OK {
		t.Fatalf("run() = %d, want %d; stderr:\n%s", code, exitcode.OK, stderr)
	}
	if got := labelCalls.Load(); got != 1 {
		t.Errorf("label calls = %d, want 1", got)
	}
	if seenLabels != "bug,data-loss" {
		t.Errorf("add_labels = %q, want %q", seenLabels, "bug,data-loss")
	}
	if got := commentCalls.Load(); got != 1 {
		t.Errorf("comment calls = %d, want 1", got)
	}
	if !strings.Contains(seenComment, "**baton triage** — critical / csv import") {
		t.Errorf("the comment does not carry the verdict:\n%s", seenComment)
	}
	if strings.Contains(seenComment, "To move this forward") {
		t.Errorf("the comment asks a question the model did not ask:\n%s", seenComment)
	}
	// The critical branch of the switch pages the channel, which is stdout.
	if !strings.Contains(stdout, "Critical issue #41") {
		t.Errorf("the critical report did not page the channel:\n%s", stdout)
	}

	// A minor report takes the default branch: it is labelled and commented
	// on, but nobody is paged. The question of a report that needs more
	// information is appended to the same comment.
	severity, needsInfo = "minor", true
	minorCache := filepath.Join(dir, "cache-minor")
	stdout, stderr, code = triage("minor", minorCache)
	if code != exitcode.OK {
		t.Fatalf("second run() = %d, want %d; stderr:\n%s", code, exitcode.OK, stderr)
	}
	if got := commentCalls.Load(); got != 2 {
		t.Errorf("comment calls after the second run = %d, want 2", got)
	}
	if !strings.Contains(seenComment, "To move this forward: Which delimiter does the file use?") {
		t.Errorf("the comment does not carry the question:\n%s", seenComment)
	}
	if strings.Contains(stdout, "Critical issue") {
		t.Errorf("a minor report paged the channel:\n%s", stdout)
	}

	assertNoTokenOnDisk(t, runsDir, gitlabToken)
	assertNoTokenOnDisk(t, runsDir, providerToken)
	assertNoTokenOnDisk(t, criticalCache, gitlabToken)
	assertNoTokenOnDisk(t, criticalCache, providerToken)
}
