package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/foxzi/baton/internal/exitcode"
)

// TestExample_WeeklyReport runs examples/weekly-report.yaml against stubs of
// the GitLab API and of an OpenAI-compatible provider. It is the acceptance
// scenario of milestone M2: foreach over several projects -> llm with a
// schema -> notify, and a second run of the same week that spends no tokens
// because both the http items and the llm answer are cached.
func TestExample_WeeklyReport(t *testing.T) {
	const (
		gitlabToken   = "glpat-weekly-token"
		providerToken = "sk-weekly-token"
	)

	var (
		forgeCalls    atomic.Int64
		providerCalls atomic.Int64
		mu            sync.Mutex // guards seenProjects/seenQuery: foreach fans out over the handler on many goroutines
		seenProjects  = map[string]int{}
		seenQuery     string
	)

	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != gitlabToken {
			t.Errorf("PRIVATE-TOKEN = %q, want %q", r.Header.Get("PRIVATE-TOKEN"), gitlabToken)
		}
		path := r.URL.EscapedPath()
		if !strings.HasSuffix(path, "/merge_requests") {
			t.Errorf("unexpected path %q", path)
			http.NotFound(w, r)
			return
		}
		forgeCalls.Add(1)
		project := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v4/projects/"), "/merge_requests")
		mu.Lock()
		seenProjects[project]++
		seenQuery = r.URL.RawQuery
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		// One merge request per project, enough for the digest to name.
		fmt.Fprintf(w, `[{
			"iid": 7,
			"title": "Speed up %s",
			"author": {"username": "vorontsov"},
			"web_url": "https://gitlab.example/%s/-/merge_requests/7",
			"merged_at": "2026-01-05T10:00:00Z",
			"updated_at": "2026-01-05T10:00:00Z"
		}]`, project, project)
	}))
	defer forge.Close()

	answer := map[string]any{
		"headline": "Two projects merged one change each.",
		"projects": []any{
			map[string]any{"project": "acme/web", "merged": 1, "highlight": "Speed up the web app"},
			map[string]any{"project": "acme/api", "merged": 1, "highlight": "Speed up the api"},
		},
	}
	encodedAnswer, err := json.Marshal(answer)
	if err != nil {
		t.Fatalf("marshal answer: %v", err)
	}

	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if want := "/chat/completions"; r.URL.Path != want {
			t.Errorf("provider path = %q, want %q", r.URL.Path, want)
		}
		if got, want := r.Header.Get("Authorization"), "Bearer "+providerToken; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		message, _ := json.Marshal(map[string]any{"role": "assistant", "content": string(encodedAnswer)})
		fmt.Fprintf(w, `{
			"id": "chatcmpl_weekly",
			"model": "anthropic/claude-3.5-sonnet",
			"choices": [{"index": 0, "message": %s, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 900, "completion_tokens": 120}
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
pricing:
  openrouter/anthropic/claude-3.5-sonnet:
    input_per_mtok: 3
    output_per_mtok: 15
`, llm.URL)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	runsDir := filepath.Join(dir, "runs")
	cacheDir := filepath.Join(dir, "cache")
	t.Setenv("GITLAB_TOKEN", gitlabToken)
	t.Setenv("OPENROUTER_API_KEY", providerToken)

	weekly := func(runID string) (string, string, int) {
		var code int
		stdout, stderr := captureOutput(t, func() {
			code = run([]string{"run", "../../examples/weekly-report.yaml",
				"-i", `projects=["acme/web", "acme/api"]`,
				"-i", "since=2026-01-01",
				"-i", "api_url=" + forge.URL + "/api/v4",
				"--config", configPath,
				"--cache-dir", cacheDir,
				"--runs-dir", runsDir,
				"--run-id", runID,
			})
		})
		return stdout, stderr, code
	}

	stdout, stderr, code := weekly("first")
	if code != exitcode.OK {
		t.Fatalf("run() = %d, want %d; stderr:\n%s", code, exitcode.OK, stderr)
	}

	if got := forgeCalls.Load(); got != 2 {
		t.Errorf("forge calls = %d, want 2 (one per project)", got)
	}
	for _, project := range []string{"acme%2Fweb", "acme%2Fapi"} {
		mu.Lock()
		count := seenProjects[project]
		mu.Unlock()
		if count != 1 {
			t.Errorf("project %s was requested %d times, want 1", project, count)
		}
	}
	mu.Lock()
	query := seenQuery
	mu.Unlock()
	if !strings.Contains(query, "state=merged") || !strings.Contains(query, "updated_after=2026-01-01") {
		t.Errorf("query = %q, want state and updated_after", query)
	}
	if got := providerCalls.Load(); got != 1 {
		t.Errorf("provider calls = %d, want 1", got)
	}

	// The notify channel is stdout, so the report itself is on stdout.
	if !strings.Contains(stdout, "Two projects merged one change each.") {
		t.Errorf("stdout does not carry the digest:\n%s", stdout)
	}
	if !strings.Contains(stdout, "acme/web: 1 merged") {
		t.Errorf("stdout does not carry the project lines:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Weekly report since 2026-01-01") {
		t.Errorf("stdout does not carry the report title:\n%s", stdout)
	}

	// The pricing table covers the model, so the run warns about nothing.
	if strings.Contains(stderr, "pricing") {
		t.Errorf("stderr mentions pricing:\n%s", stderr)
	}

	// A repeated run of the same week is served from the cache: neither the
	// forge nor the provider is called again, and the report is still sent.
	stdout, stderr, code = weekly("second")
	if code != exitcode.OK {
		t.Fatalf("second run() = %d, want %d; stderr:\n%s", code, exitcode.OK, stderr)
	}
	if got := forgeCalls.Load(); got != 2 {
		t.Errorf("forge calls after the second run = %d, want 2", got)
	}
	if got := providerCalls.Load(); got != 1 {
		t.Errorf("provider calls after the second run = %d, want 1: the llm step was not cached", got)
	}
	if !strings.Contains(stdout, "Two projects merged one change each.") {
		t.Errorf("the cached run sent no report:\n%s", stdout)
	}

	assertNoTokenOnDisk(t, runsDir, gitlabToken)
	assertNoTokenOnDisk(t, runsDir, providerToken)
	assertNoTokenOnDisk(t, cacheDir, gitlabToken)
	assertNoTokenOnDisk(t, cacheDir, providerToken)
}
