package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/runstore"
)

// writePack writes a pack file into <dir>/apis/<name>.yaml, the layout
// expected by an apis entry with from: ./apis/ (section 7.4.1).
func writePack(t *testing.T, dir, name, body string) {
	t.Helper()
	apisDir := filepath.Join(dir, "apis")
	if err := os.MkdirAll(apisDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", apisDir, err)
	}
	path := filepath.Join(apisDir, name+".yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// 1. A raw GET request without parse: returns the body as text, and records
// status/result in output.json (section 3.4).
func TestRun_HTTPRawGET_TextResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if r.URL.Path != "/ping" {
			t.Errorf("path = %q, want /ping", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("pong"))
	}))
	defer server.Close()

	yamlText := fmt.Sprintf(`
version: 1
name: raw-get
steps:
  - id: one
    http:
      url: %q
`, server.URL+"/ping")

	eng, store, _ := newTestEngine(t, yamlText, nil)
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want success (error=%+v)", result.Status, result.Error)
	}

	step, ok := eng.steps["one"]
	if !ok {
		t.Fatalf("eng.steps has no entry for \"one\"")
	}
	if step.HTTPStatus != http.StatusOK {
		t.Errorf("HTTPStatus = %d, want 200", step.HTTPStatus)
	}
	if step.Result != "pong" {
		t.Errorf("Result = %#v, want %q", step.Result, "pong")
	}
	if step.Body != "pong" {
		t.Errorf("Body = %#v, want %q", step.Body, "pong")
	}

	out := readStepJSON(t, filepath.Join(store.Dir(), "steps", "one", "output.json"))
	if out["http_status"].(float64) != http.StatusOK {
		t.Errorf("output.json http_status = %v, want 200", out["http_status"])
	}
	if out["result"] != "pong" {
		t.Errorf("output.json result = %v, want pong", out["result"])
	}

	body := readFile(t, filepath.Join(store.Dir(), "steps", "one", "response.body"))
	if body != "pong" {
		t.Errorf("response.body = %q, want pong", body)
	}
}

// 2. parse: json decodes the body into a structured result.
func TestRun_HTTPRawGET_ParseJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true,"count":3}`))
	}))
	defer server.Close()

	yamlText := fmt.Sprintf(`
version: 1
name: raw-get-json
steps:
  - id: one
    http:
      url: %q
      parse: json
`, server.URL+"/things")

	eng, store, _ := newTestEngine(t, yamlText, nil)
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want success (error=%+v)", result.Status, result.Error)
	}

	step := eng.steps["one"]
	decoded, ok := step.Result.(map[string]any)
	if !ok {
		t.Fatalf("Result is %T, want map[string]any: %#v", step.Result, step.Result)
	}
	if decoded["ok"] != true {
		t.Errorf("Result[ok] = %#v, want true", decoded["ok"])
	}
	if decoded["count"] != float64(3) {
		t.Errorf("Result[count] = %#v, want 3", decoded["count"])
	}

	out := readStepJSON(t, filepath.Join(store.Dir(), "steps", "one", "output.json"))
	if out["status"] != "success" {
		t.Errorf("output.json status = %v, want success", out["status"])
	}
}

// 3. parse: json on a body that is not valid JSON fails the step with the
// schema class, and the run fails with that class recorded in run.json
// (section 9.1).
func TestRun_HTTPRawGET_ParseJSONInvalid(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not json"))
	}))
	defer server.Close()

	yamlText := fmt.Sprintf(`
version: 1
name: raw-get-bad-json
steps:
  - id: one
    http:
      url: %q
      parse: json
`, server.URL+"/things")

	eng, store, _ := newTestEngine(t, yamlText, nil)
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusFailed {
		t.Fatalf("Status = %q, want failed", result.Status)
	}
	if result.Error == nil || result.Error.Class != ClassSchema {
		t.Fatalf("Error = %+v, want class %q", result.Error, ClassSchema)
	}
	if result.FailedStep != "one" {
		t.Errorf("FailedStep = %q, want one", result.FailedStep)
	}

	state := readRunState(t, store.Dir())
	if state.Steps["one"].Error == nil || state.Steps["one"].Error.Class != ClassSchema {
		t.Fatalf("run.json steps.one.error = %+v, want class %q", state.Steps["one"].Error, ClassSchema)
	}

	out := readStepJSON(t, filepath.Join(store.Dir(), "steps", "one", "output.json"))
	if out["status"] != "failed" {
		t.Errorf("output.json status = %v, want failed", out["status"])
	}
	errField, ok := out["error"].(map[string]any)
	if !ok {
		t.Fatalf("output.json error = %#v, want an object", out["error"])
	}
	if errField["class"] != ClassSchema {
		t.Errorf("output.json error.class = %v, want %q", errField["class"], ClassSchema)
	}

	// response.body is still written: the parse failure happens after the
	// request succeeds.
	body := readFile(t, filepath.Join(store.Dir(), "steps", "one", "response.body"))
	if body != "not json" {
		t.Errorf("response.body = %q, want %q", body, "not json")
	}
}

// 4. parse: lines splits the body on newlines.
func TestRun_HTTPRawGET_ParseLines(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("a\nb\nc\n"))
	}))
	defer server.Close()

	yamlText := fmt.Sprintf(`
version: 1
name: raw-get-lines
steps:
  - id: one
    http:
      url: %q
      parse: lines
`, server.URL)

	eng, _, _ := newTestEngine(t, yamlText, nil)
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want success (error=%+v)", result.Status, result.Error)
	}

	step := eng.steps["one"]
	items, ok := step.Result.([]any)
	if !ok {
		t.Fatalf("Result is %T, want []any: %#v", step.Result, step.Result)
	}
	want := []any{"a", "b", "c"}
	if len(items) != len(want) {
		t.Fatalf("Result = %#v, want %#v", items, want)
	}
	for i, v := range want {
		if items[i] != v {
			t.Fatalf("Result[%d] = %#v, want %#v", i, items[i], v)
		}
	}
}

// 5. Request headers reach the server, rendered from a template, and
// response headers reach the expression context of a later step's assert
// (section 3.4, 5.1).
func TestRun_HTTPRaw_Headers(t *testing.T) {
	var gotHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Request-Id")
		w.Header().Set("X-Reply", "seen")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	yamlText := fmt.Sprintf(`
version: 1
name: raw-headers
inputs:
  reqid:
    type: string
    default: "req-42"
steps:
  - id: one
    http:
      url: %q
      headers:
        X-Request-Id: "{{ .inputs.reqid }}"
  - id: two
    assert:
      condition: 'steps.one.headers["X-Reply"] == "seen"'
`, server.URL)

	eng, _, _ := newTestEngine(t, yamlText, func(opts *Options) {
		opts.Inputs = map[string]any{"reqid": "req-42"}
	})
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want success (error=%+v)", result.Status, result.Error)
	}
	if gotHeader != "req-42" {
		t.Errorf("server saw X-Request-Id = %q, want req-42", gotHeader)
	}
}

// gitlabLikePack is a minimal pack: one readonly GET op and one POST op that
// takes a body argument, both without envelope or pagination so the raw
// decoded JSON is the result.
const gitlabLikePack = `pack: gitlab
version: 1
config:
  base_url: {}
ops:
  get_project:
    get: /projects/{id}
    readonly: true
    params:
      id: { pattern: '^\d+$' }
  create_note:
    post: /projects/{id}/notes
    params:
      id: { pattern: '^\d+$' }
      body: {}
    encode: json
`

// 6. The operation form of an http step calls a pack whose base_url points at
// the test server, and the response comes back as the normalised result
// (section 3.4, 7.4.1).
func TestRun_HTTPOp_GET(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":7,"name":"demo"}`))
	}))
	defer server.Close()

	yamlText := fmt.Sprintf(`
version: 1
name: op-get
apis:
  gitlab:
    pack: gitlab
    from: ./apis/
    config:
      base_url: %q
steps:
  - id: one
    http:
      op: gitlab.get_project
      args:
        id: "7"
`, server.URL)

	eng, store, dir := newTestEngine(t, yamlText, nil)
	writePack(t, dir, "gitlab", gitlabLikePack)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want success (error=%+v)", result.Status, result.Error)
	}
	if gotPath != "/projects/7" {
		t.Errorf("server saw path %q, want /projects/7", gotPath)
	}

	step := eng.steps["one"]
	decoded, ok := step.Result.(map[string]any)
	if !ok {
		t.Fatalf("Result is %T, want map[string]any: %#v", step.Result, step.Result)
	}
	if decoded["name"] != "demo" {
		t.Errorf("Result[name] = %#v, want demo", decoded["name"])
	}
	if step.HTTPStatus != http.StatusOK {
		t.Errorf("HTTPStatus = %d, want 200", step.HTTPStatus)
	}

	out := readStepJSON(t, filepath.Join(store.Dir(), "steps", "one", "output.json"))
	if out["pages"].(float64) != 1 {
		t.Errorf("output.json pages = %v, want 1", out["pages"])
	}
}

// 7. The operation form sends the rendered args as the request body for a
// non-readonly op (section 3.4).
func TestRun_HTTPOp_POST_ArgsAsBody(t *testing.T) {
	var gotBody, gotContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		buf := make([]byte, 256)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"created":true}`))
	}))
	defer server.Close()

	yamlText := fmt.Sprintf(`
version: 1
name: op-post
inputs:
  message:
    type: string
    default: "hello"
apis:
  gitlab:
    pack: gitlab
    from: ./apis/
    config:
      base_url: %q
steps:
  - id: one
    dedupe_key: "note-1"
    http:
      op: gitlab.create_note
      args:
        id: "1"
        body: "{{ .inputs.message }}"
`, server.URL)

	eng, _, dir := newTestEngine(t, yamlText, func(opts *Options) {
		opts.Inputs = map[string]any{"message": "hello"}
	})
	writePack(t, dir, "gitlab", gitlabLikePack)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want success (error=%+v)", result.Status, result.Error)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotBody != `{"body":"hello"}` {
		t.Errorf("request body = %q, want %s", gotBody, `{"body":"hello"}`)
	}

	step := eng.steps["one"]
	decoded, ok := step.Result.(map[string]any)
	if !ok {
		t.Fatalf("Result is %T, want map[string]any: %#v", step.Result, step.Result)
	}
	if decoded["created"] != true {
		t.Errorf("Result[created] = %#v, want true", decoded["created"])
	}
	if step.HTTPStatus != http.StatusCreated {
		t.Errorf("HTTPStatus = %d, want 201", step.HTTPStatus)
	}
}

// 8. A raw request that gets an unexpected status fails the step as a
// command error and records it in run.json (section 9.1).
func TestRun_HTTPRaw_UnexpectedStatusIsCommand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"nope"}`))
	}))
	defer server.Close()

	yamlText := fmt.Sprintf(`
version: 1
name: raw-404
steps:
  - id: one
    http:
      url: %q
`, server.URL+"/missing")

	eng, store, _ := newTestEngine(t, yamlText, nil)
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusFailed {
		t.Fatalf("Status = %q, want failed", result.Status)
	}
	if result.Error == nil || result.Error.Class != ClassCommand {
		t.Fatalf("Error = %+v, want class %q", result.Error, ClassCommand)
	}

	out := readStepJSON(t, filepath.Join(store.Dir(), "steps", "one", "output.json"))
	if out["status"] != "failed" {
		t.Errorf("output.json status = %v, want failed", out["status"])
	}
}

// 9. An operation whose pack cannot be found fails as a config error (section
// 9.1), before any request reaches the network.
func TestRun_HTTPOp_UnknownAPI(t *testing.T) {
	yamlText := `
version: 1
name: op-unknown-api
steps:
  - id: one
    http:
      op: gitlab.get_project
      args:
        id: "1"
`
	eng, _, _ := newTestEngine(t, yamlText, nil)
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusFailed {
		t.Fatalf("Status = %q, want failed", result.Status)
	}
	if result.Error == nil || result.Error.Class != ClassConfig {
		t.Fatalf("Error = %+v, want class %q", result.Error, ClassConfig)
	}
}

// forgeGetChangePack implements only forge/v1.get_change, not the rest of
// the forge/v1 interface (list_files, get_file, post_comment, post_review).
const forgeGetChangePack = `pack: gitlab
version: 1
config:
  base_url: {}
ops:
  get_change:
    get: /projects/{project}/merge_requests/{id}/changes
    readonly: true
    params:
      project: { pattern: '^[\w./-]+$', encode: path }
      id: { pattern: '^\d+$' }
    transform: |
      { id: .iid, title, files: [] }
    implements: forge/v1.get_change
`

// forgeGetChangeExample is the recorded response CheckImplements replays to
// check the transform against forge/v1.get_change's result schema.
const forgeGetChangeExample = `{"iid": 1, "title": "demo"}`

// 10. An apis entry that declares interface: forge/v1 fails to load, as a
// config error, when its pack implements only part of the interface (spec
// section 3.4, 7.4.5).
func TestRun_HTTPOp_InterfaceNotImplemented(t *testing.T) {
	yamlText := `
version: 1
name: op-interface
apis:
  gitlab:
    pack: gitlab
    from: ./apis/
    interface: forge/v1
    config:
      base_url: "http://example.invalid"
steps:
  - id: one
    http:
      op: gitlab.get_change
      args:
        project: "demo"
        id: "1"
`
	var failMessage string
	eng, _, dir := newTestEngine(t, yamlText, func(o *Options) {
		o.Observer = func(e Event) {
			if e.Type == "step_failed" {
				failMessage = e.Message
			}
		}
	})
	writePack(t, dir, "gitlab", forgeGetChangePack)
	examplesDir := filepath.Join(dir, "apis", "examples")
	if err := os.MkdirAll(examplesDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", examplesDir, err)
	}
	if err := os.WriteFile(filepath.Join(examplesDir, "get_change.json"), []byte(forgeGetChangeExample), 0o644); err != nil {
		t.Fatalf("write example: %v", err)
	}

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusFailed {
		t.Fatalf("Status = %q, want failed", result.Status)
	}
	if result.Error == nil || result.Error.Class != ClassConfig {
		t.Fatalf("Error = %+v, want class %q", result.Error, ClassConfig)
	}
	if !strings.Contains(failMessage, "does not implement forge/v1") {
		t.Errorf("step_failed message = %q, want it to mention %q", failMessage, "does not implement forge/v1")
	}
}
