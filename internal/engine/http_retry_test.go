package engine

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/foxzi/baton/internal/runstore"
	"github.com/foxzi/baton/internal/secrets"
)

// headerAuthPack authorises with the header scheme, one readonly GET op
// (section 7.4.3).
const headerAuthPack = `pack: demo
version: 1
config:
  base_url: {}
auth:
  kind: header
  name: PRIVATE-TOKEN
ops:
  whoami:
    get: /whoami
    readonly: true
`

// retryPack has one readonly GET op and one non-readonly POST op, neither
// requiring any argument, so retry behaviour (section 9.4) can be tested
// without an envelope or pagination getting in the way.
const retryPack = `pack: demo
version: 1
config:
  base_url: {}
ops:
  get_thing:
    get: /thing
    readonly: true
  create_thing:
    post: /thing
    readonly: false
`

// pagePack paginates with the page style, mirroring the pack examples of
// internal/httpx/httpx_test.go.
const pagePack = `pack: demo
version: 1
config:
  base_url: {}
ops:
  list_things:
    get: /things
    readonly: true
    paginate: true
    pagination:
      style: page
      param: page
      size_param: per_page
      size: 2
`

// walkForPlaintext fails the test if any file under dir contains plaintext.
func walkForPlaintext(t *testing.T, dir, plaintext string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(data), plaintext) {
			t.Errorf("%s contains the secret plaintext", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
}

// 1. An operation authorised with the header scheme sends the resolved
// secret to the server, and no file in the run directory ever holds its
// plaintext (section 6, 7.4.3, 9.4's neighbour: the redactor covers http
// too).
func TestRun_HTTPOp_HeaderAuth_SecretsNeverOnDisk(t *testing.T) {
	const plaintext = "unlikely-header-secret-77"
	t.Setenv("BATON_HEADER_TOKEN", plaintext)

	var gotToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("PRIVATE-TOKEN")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	yamlText := fmt.Sprintf(`
version: 1
name: header-auth
secrets:
  tok:
    from: env
    key: BATON_HEADER_TOKEN
apis:
  svc:
    pack: demo
    from: ./apis/
    config:
      base_url: %q
    auth:
      secret: tok
steps:
  - id: one
    http:
      op: svc.whoami
`, server.URL)

	eng, store, dir := newTestEngine(t, yamlText, func(o *Options) {
		s, err := secrets.Resolve(o.Scenario.Secrets, filepath.Dir(o.Scenario.Path))
		if err != nil {
			t.Fatalf("secrets.Resolve: %v", err)
		}
		o.Secrets = s
	})
	writePack(t, dir, "demo", headerAuthPack)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want success (error=%+v)", result.Status, result.Error)
	}
	if gotToken != plaintext {
		t.Errorf("server saw PRIVATE-TOKEN = %q, want %q", gotToken, plaintext)
	}

	walkForPlaintext(t, store.Dir(), plaintext)
}

// 2. A step-level auth: override picks a different secret than the apis
// entry declares, while a plain step still uses the entry's own secret
// (section 3.4).
func TestRun_HTTPOp_AuthOverride_SelectsDifferentSecret(t *testing.T) {
	const secretA = "token-alpha-111"
	const secretB = "token-beta-222"
	t.Setenv("BATON_TOKEN_A", secretA)
	t.Setenv("BATON_TOKEN_B", secretB)

	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("PRIVATE-TOKEN"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	yamlText := fmt.Sprintf(`
version: 1
name: auth-override
secrets:
  a:
    from: env
    key: BATON_TOKEN_A
  b:
    from: env
    key: BATON_TOKEN_B
apis:
  svc:
    pack: demo
    from: ./apis/
    config:
      base_url: %q
    auth:
      secret: a
steps:
  - id: plain
    http:
      op: svc.whoami
  - id: override
    http:
      op: svc.whoami
      auth: b
`, server.URL)

	eng, _, dir := newTestEngine(t, yamlText, func(o *Options) {
		s, err := secrets.Resolve(o.Scenario.Secrets, filepath.Dir(o.Scenario.Path))
		if err != nil {
			t.Fatalf("secrets.Resolve: %v", err)
		}
		o.Secrets = s
	})
	writePack(t, dir, "demo", headerAuthPack)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want success (error=%+v)", result.Status, result.Error)
	}
	if len(seen) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(seen))
	}
	if seen[0] != secretA {
		t.Errorf("plain step sent %q, want secret a (%q)", seen[0], secretA)
	}
	if seen[1] != secretB {
		t.Errorf("override step sent %q, want secret b (%q)", seen[1], secretB)
	}
}

// 3. A raw request's input.json records header names but never header
// values, so a literal Authorization header does not leak (section 3.4).
func TestRun_HTTPRaw_InputJSON_HeaderNamesOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	const literalValue = "Bearer literal-static-token-value"
	yamlText := fmt.Sprintf(`
version: 1
name: raw-header-names
steps:
  - id: one
    http:
      url: %q
      headers:
        Authorization: %q
`, server.URL, literalValue)

	eng, store, _ := newTestEngine(t, yamlText, nil)
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want success (error=%+v)", result.Status, result.Error)
	}

	input := readFile(t, filepath.Join(store.Dir(), "steps", "one", "input.json"))
	if !strings.Contains(input, "Authorization") {
		t.Errorf("input.json does not mention the header name Authorization: %s", input)
	}
	if strings.Contains(input, literalValue) || strings.Contains(input, "literal-static-token-value") {
		t.Errorf("input.json leaks the header value: %s", input)
	}
}

// 4. A raw POST sends its method, body and Content-Type as written (section
// 3.4).
func TestRun_HTTPRaw_POST_BodyAndContentType(t *testing.T) {
	var gotMethod, gotBody, gotContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		data, _ := io.ReadAll(r.Body)
		gotBody = string(data)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("created"))
	}))
	defer server.Close()

	yamlText := fmt.Sprintf(`
version: 1
name: raw-post
steps:
  - id: one
    http:
      method: POST
      url: %q
      headers:
        Content-Type: application/json
      body: '{"name":"demo"}'
`, server.URL)

	eng, _, _ := newTestEngine(t, yamlText, nil)
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want success (error=%+v)", result.Status, result.Error)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotBody != `{"name":"demo"}` {
		t.Errorf("body = %q, want %s", gotBody, `{"name":"demo"}`)
	}
}

// 5. expect_status: [200] against a 503 response fails the step with class
// transient (section 9.1). retry is disabled explicitly so the test does not
// pay for the 5s default backoff of section 9.1's transient policy; the 404
// -> command case is already covered by
// TestRun_HTTPRaw_UnexpectedStatusIsCommand in http_test.go.
func TestRun_HTTPRaw_ExpectStatus503IsTransient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	yamlText := fmt.Sprintf(`
version: 1
name: raw-503
steps:
  - id: one
    retry:
      attempts: 0
    http:
      url: %q
      expect_status: [200]
`, server.URL)

	eng, store, _ := newTestEngine(t, yamlText, nil)
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusFailed {
		t.Fatalf("Status = %q, want failed", result.Status)
	}
	if result.Error == nil || result.Error.Class != ClassTransient {
		t.Fatalf("Error = %+v, want class %q", result.Error, ClassTransient)
	}

	out := readStepJSON(t, filepath.Join(store.Dir(), "steps", "one", "output.json"))
	errField, ok := out["error"].(map[string]any)
	if !ok {
		t.Fatalf("output.json error = %#v, want an object", out["error"])
	}
	if errField["class"] != ClassTransient {
		t.Errorf("output.json error.class = %v, want %q", errField["class"], ClassTransient)
	}
}

// 6. A page-style paginated operation over two pages returns the
// concatenated result (section 7.4.4).
func TestRun_HTTPOp_PaginationPageStyleTwoPages(t *testing.T) {
	pages := [][]string{{"a", "b"}, {"c"}}
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idx := int(atomic.AddInt32(&calls, 1)) - 1
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if idx >= len(pages) {
			w.Write([]byte(`[]`))
			return
		}
		encoded := `["` + strings.Join(pages[idx], `","`) + `"]`
		w.Write([]byte(encoded))
	}))
	defer server.Close()

	yamlText := fmt.Sprintf(`
version: 1
name: op-pagination
apis:
  svc:
    pack: demo
    from: ./apis/
    config:
      base_url: %q
steps:
  - id: one
    http:
      op: svc.list_things
`, server.URL)

	eng, store, dir := newTestEngine(t, yamlText, nil)
	writePack(t, dir, "demo", pagePack)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want success (error=%+v)", result.Status, result.Error)
	}

	step := eng.steps["one"]
	list, ok := step.Result.([]any)
	if !ok {
		t.Fatalf("Result is %T, want []any: %#v", step.Result, step.Result)
	}
	want := []any{"a", "b", "c"}
	if len(list) != len(want) {
		t.Fatalf("Result = %#v, want %#v", list, want)
	}
	for i, v := range want {
		if list[i] != v {
			t.Errorf("Result[%d] = %#v, want %#v", i, list[i], v)
		}
	}

	out := readStepJSON(t, filepath.Join(store.Dir(), "steps", "one", "output.json"))
	if out["pages"].(float64) != 2 {
		t.Errorf("output.json pages = %v, want 2", out["pages"])
	}
	if out["truncated"] != false {
		t.Errorf("output.json truncated = %v, want false", out["truncated"])
	}
}

// 7. A readonly operation that fails transiently twice succeeds on the third
// attempt with an explicit retry policy, and the server saw exactly 3
// requests (section 9.4).
func TestRun_HTTPOp_Retry_ReadonlyGET_SucceedsAfterTransientRetries(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	yamlText := fmt.Sprintf(`
version: 1
name: op-retry-readonly
apis:
  svc:
    pack: demo
    from: ./apis/
    config:
      base_url: %q
steps:
  - id: one
    retry:
      on: [transient]
      attempts: 2
      backoff: 1ms
    http:
      op: svc.get_thing
`, server.URL)

	eng, _, dir := newTestEngine(t, yamlText, nil)
	writePack(t, dir, "demo", retryPack)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want success (error=%+v)", result.Status, result.Error)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("server saw %d requests, want 3", got)
	}
}

// 8. A non-readonly operation is not retried even with a retry policy set,
// because it carries no dedupe_key: the server sees exactly one request and
// the run fails with class transient (section 9.4).
func TestRun_HTTPOp_Retry_NonReadonlyPOST_NotRetriedWithoutDedupeKey(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	yamlText := fmt.Sprintf(`
version: 1
name: op-retry-post-no-dedupe
apis:
  svc:
    pack: demo
    from: ./apis/
    config:
      base_url: %q
steps:
  - id: one
    retry:
      on: [transient]
      attempts: 2
      backoff: 1ms
    http:
      op: svc.create_thing
`, server.URL)

	eng, _, dir := newTestEngine(t, yamlText, nil)
	writePack(t, dir, "demo", retryPack)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusFailed {
		t.Fatalf("Status = %q, want failed", result.Status)
	}
	if result.Error == nil || result.Error.Class != ClassTransient {
		t.Fatalf("Error = %+v, want class %q", result.Error, ClassTransient)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("server saw %d requests, want 1 (no retry without dedupe_key)", got)
	}
}

// 9. The same non-readonly operation with dedupe_key set is retried: the
// server sees more than one request (section 9.4).
func TestRun_HTTPOp_Retry_NonReadonlyPOST_RetriedWithDedupeKey(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	yamlText := fmt.Sprintf(`
version: 1
name: op-retry-post-dedupe
apis:
  svc:
    pack: demo
    from: ./apis/
    config:
      base_url: %q
steps:
  - id: one
    dedupe_key: "note-1"
    retry:
      on: [transient]
      attempts: 1
      backoff: 1ms
    http:
      op: svc.create_thing
`, server.URL)

	eng, _, dir := newTestEngine(t, yamlText, nil)
	writePack(t, dir, "demo", retryPack)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusFailed {
		t.Fatalf("Status = %q, want failed", result.Status)
	}
	if result.Error == nil || result.Error.Class != ClassTransient {
		t.Fatalf("Error = %+v, want class %q", result.Error, ClassTransient)
	}
	if got := atomic.LoadInt32(&calls); got <= 1 {
		t.Errorf("server saw %d requests, want more than 1 (dedupe_key allows retry)", got)
	}
}

// 10a. The raw form mirrors case 8: a raw POST without dedupe_key is not
// retried (section 9.4).
func TestRun_HTTPRaw_Retry_POST_NotRetriedWithoutDedupeKey(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	yamlText := fmt.Sprintf(`
version: 1
name: raw-retry-post-no-dedupe
steps:
  - id: one
    retry:
      on: [transient]
      attempts: 2
      backoff: 1ms
    http:
      method: POST
      url: %q
`, server.URL)

	eng, _, _ := newTestEngine(t, yamlText, nil)
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusFailed {
		t.Fatalf("Status = %q, want failed", result.Status)
	}
	if result.Error == nil || result.Error.Class != ClassTransient {
		t.Fatalf("Error = %+v, want class %q", result.Error, ClassTransient)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("server saw %d requests, want 1 (no retry without dedupe_key)", got)
	}
}

// 10b. The raw form mirrors case 7: a raw GET is retried, since a GET is
// readonly by method (section 9.4).
func TestRun_HTTPRaw_Retry_GET_Retried(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	yamlText := fmt.Sprintf(`
version: 1
name: raw-retry-get
steps:
  - id: one
    retry:
      on: [transient]
      attempts: 1
      backoff: 1ms
    http:
      url: %q
`, server.URL)

	eng, _, _ := newTestEngine(t, yamlText, nil)
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusFailed {
		t.Fatalf("Status = %q, want failed", result.Status)
	}
	if result.Error == nil || result.Error.Class != ClassTransient {
		t.Fatalf("Error = %+v, want class %q", result.Error, ClassTransient)
	}
	if got := atomic.LoadInt32(&calls); got <= 1 {
		t.Errorf("server saw %d requests, want more than 1 (GET is retried)", got)
	}
}

// 11. A step with a short timeout against a slow handler fails with class
// timeout (section 7.4.1, 9.1). The handler is bounded so it cannot outlive
// the test: it gives up as soon as the client cancels the request, or after
// 200ms, whichever comes first.
func TestRun_HTTPRaw_Timeout_ClassTimeout(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(200 * time.Millisecond):
		case <-release:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	yamlText := fmt.Sprintf(`
version: 1
name: raw-timeout
steps:
  - id: one
    timeout: 50ms
    http:
      url: %q
`, server.URL)

	eng, store, _ := newTestEngine(t, yamlText, nil)
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusFailed {
		t.Fatalf("Status = %q, want failed", result.Status)
	}
	if result.Error == nil || result.Error.Class != ClassTimeout {
		t.Fatalf("Error = %+v, want class %q", result.Error, ClassTimeout)
	}

	out := readStepJSON(t, filepath.Join(store.Dir(), "steps", "one", "output.json"))
	errField, ok := out["error"].(map[string]any)
	if !ok {
		t.Fatalf("output.json error = %#v, want an object", out["error"])
	}
	if errField["class"] != ClassTimeout {
		t.Errorf("output.json error.class = %v, want %q", errField["class"], ClassTimeout)
	}
}
