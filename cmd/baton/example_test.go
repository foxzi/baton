package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/exitcode"
)

// TestExample_MRComment runs examples/mr-comment.yaml against a stub of the
// GitLab API. It is the acceptance scenario of milestone M1: run -> http op
// -> assert, with the token never reaching the run directory.
func TestExample_MRComment(t *testing.T) {
	const token = "glpat-example-token"

	var (
		changesPath string
		notePath    string
		noteBody    map[string]any
		seenToken   string
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/acme%2Fweb/merge_requests/42/changes", func(w http.ResponseWriter, r *http.Request) {
		changesPath = r.URL.EscapedPath()
		seenToken = r.Header.Get("PRIVATE-TOKEN")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"iid": 42,
			"title": "Add the runner",
			"description": "",
			"author": {"username": "vorontsov"},
			"diff_refs": {"base_sha": "aaa", "head_sha": "bbb"},
			"changes": [
				{"new_path": "cmd/baton/main.go", "diff": "@@", "deleted_file": false},
				{"new_path": "README.md", "diff": "@@", "deleted_file": false}
			]
		}`))
	})
	mux.HandleFunc("/api/v4/projects/acme%2Fweb/merge_requests/42/notes", func(w http.ResponseWriter, r *http.Request) {
		notePath = r.URL.EscapedPath()
		json.NewDecoder(r.Body).Decode(&noteBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id": 7, "web_url": "https://gitlab.example/acme/web/-/merge_requests/42#note_7"}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	runsDir := t.TempDir()
	t.Setenv("GITLAB_TOKEN", token)

	var code int
	_, stderr := captureOutput(t, func() {
		code = run([]string{"run", "../../examples/mr-comment.yaml",
			"-i", "project=acme/web",
			"-i", "mr=42",
			"-i", "api_url=" + server.URL + "/api/v4",
			"--run-id", "example",
			"--runs-dir", runsDir,
		})
	})
	if code != exitcode.OK {
		t.Fatalf("run() = %d, want %d; stderr:\n%s", code, exitcode.OK, stderr)
	}

	// The pack sends the project as one path segment, slash and all.
	if want := "/api/v4/projects/acme%2Fweb/merge_requests/42/changes"; changesPath != want {
		t.Errorf("changes path = %q, want %q", changesPath, want)
	}
	if want := "/api/v4/projects/acme%2Fweb/merge_requests/42/notes"; notePath != want {
		t.Errorf("note path = %q, want %q", notePath, want)
	}
	if seenToken != token {
		t.Errorf("PRIVATE-TOKEN = %q, want %q", seenToken, token)
	}

	// The comment carries the file count produced by the run step.
	body, _ := noteBody["body"].(string)
	if !strings.Contains(body, "baton saw 2 changed files") {
		t.Errorf("note body = %q, want it to report 2 changed files", body)
	}
	if !strings.Contains(body, "bbb") {
		t.Errorf("note body = %q, want the head sha from the changes step", body)
	}

	assertNoTokenOnDisk(t, filepath.Join(runsDir, "example"), token)
}

// assertNoTokenOnDisk walks dir and fails if any file contains secret.
func assertNoTokenOnDisk(t *testing.T, dir, secret string) {
	t.Helper()
	filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), secret) {
			t.Errorf("%s contains the secret", path)
		}
		return nil
	})
}
