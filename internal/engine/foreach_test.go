package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/runstore"
)

// itemStatuses is the status of every entry of steps.<id>.items, in order.
func itemStatuses(t *testing.T, items []any) []string {
	t.Helper()
	out := make([]string, 0, len(items))
	for i, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("item %d is %T, want a map", i, item)
		}
		status, _ := entry["status"].(string)
		out = append(out, status)
	}
	return out
}

func TestRun_ForeachSequential(t *testing.T) {
	eng, store, _ := newTestEngine(t, `
name: foreach
version: 1
inputs:
  names:
    type: list
    default: ["a", "b", "c"]
steps:
  - id: greet
    foreach:
      items: "{{ .inputs.names }}"
      as: name
      step:
        run:
          argv: ["sh", "-c", "printf 'hello {{ .name }}'"]
          parse: text
`, func(o *Options) {
		o.Inputs = map[string]any{"names": []any{"a", "b", "c"}}
	})

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("run failed: %v", result.Error)
	}

	items := eng.steps["greet"].Items
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3", len(items))
	}
	if got := itemStatuses(t, items); strings.Join(got, ",") != "success,success,success" {
		t.Fatalf("statuses = %v", got)
	}

	// Every item has its own directory under the foreach step.
	for i, want := range []string{"hello a", "hello b", "hello c"} {
		path := filepath.Join(store.Dir(), "steps", "greet", strconv.Itoa(i), "stdout.log")
		if got := readFile(t, path); got != want {
			t.Errorf("item %d stdout = %q, want %q", i, got, want)
		}
	}

	// The foreach step itself records the list.
	var output struct {
		Items []map[string]any `json:"items"`
	}
	data, err := os.ReadFile(filepath.Join(store.Dir(), "steps", "greet", "output.json"))
	if err != nil {
		t.Fatalf("read output.json: %v", err)
	}
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatalf("output.json: %v", err)
	}
	if len(output.Items) != 3 {
		t.Fatalf("output.json has %d items, want 3", len(output.Items))
	}
}

// A parsed body result reaches the item entry, and later steps can read the
// list through steps.<id>.items.
func TestRun_ForeachResultsAreReadable(t *testing.T) {
	eng, _, _ := newTestEngine(t, `
name: foreach-results
version: 1
steps:
  - id: work
    foreach:
      items: '["x", "y"]'
      as: item
      step:
        run:
          argv: ["sh", "-c", "printf '{\"got\": \"{{ .item }}\"}'"]
          parse: json
  - id: check
    assert:
      condition: "len(steps.work.items) == 2"
      message: "two items"
`, nil)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("run failed: %v", result.Error)
	}

	items := eng.steps["work"].Items
	for i, want := range []string{"x", "y"} {
		entry := items[i].(map[string]any)
		got, _ := entry["result"].(map[string]any)
		if got == nil || got["got"] != want {
			t.Errorf("item %d result = %v, want got=%q", i, entry["result"], want)
		}
		if entry["error"] != nil {
			t.Errorf("item %d error = %v, want nil", i, entry["error"])
		}
	}
}

// on_item_error: fail stops the run and reports the item's own error class.
func TestRun_ForeachItemErrorFail(t *testing.T) {
	eng, store, _ := newTestEngine(t, `
name: foreach-fail
version: 1
steps:
  - id: work
    foreach:
      items: '["ok", "bad"]'
      as: item
      step:
        run:
          argv: ["sh", "-c", "test {{ .item }} = ok"]
  - id: after
    run:
      argv: ["true"]
`, nil)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status == runstore.StatusSuccess {
		t.Fatal("run succeeded, want failure")
	}
	if result.Error.Class != ClassCommand {
		t.Errorf("class = %q, want command", result.Error.Class)
	}
	if !strings.Contains(result.Error.Message, "foreach") {
		t.Errorf("message = %q, want it to mention foreach", result.Error.Message)
	}

	state := readRunState(t, store.Dir())
	if _, ok := state.Steps["after"]; ok {
		t.Error("the step after a failed foreach ran")
	}
	if got := itemStatuses(t, eng.steps["work"].Items); got[0] != "success" || got[1] != "failed" {
		t.Errorf("statuses = %v", got)
	}
}

// on_item_error: continue keeps going; min_success decides the verdict.
func TestRun_ForeachItemErrorContinue(t *testing.T) {
	body := `
name: foreach-continue
version: 1
steps:
  - id: work
    foreach:
      items: '["ok", "bad", "ok"]'
      as: item
      on_item_error: continue
      MINSUCCESS
      step:
        run:
          argv: ["sh", "-c", "test {{ .item }} = ok"]
`
	cases := []struct {
		name    string
		line    string
		success bool
	}{
		{name: "no threshold", line: "", success: true},
		{name: "threshold met", line: "min_success: 0.6", success: true},
		{name: "threshold missed", line: "min_success: 1.0", success: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, _, _ := newTestEngine(t, strings.Replace(body, "MINSUCCESS", tc.line, 1), nil)
			result, err := eng.Run(context.Background())
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if success := result.Status == runstore.StatusSuccess; success != tc.success {
				t.Fatalf("status = %q, want success = %v (error %v)", result.Status, tc.success, result.Error)
			}
			if !tc.success && !strings.Contains(result.Error.Message, "min_success") {
				t.Errorf("message = %q, want it to mention min_success", result.Error.Message)
			}
			if got := itemStatuses(t, eng.steps["work"].Items); strings.Join(got, ",") != "success,failed,success" {
				t.Errorf("statuses = %v", got)
			}
		})
	}
}

// Items run in parallel when max_parallel allows it, and the results stay in
// input order whatever the completion order was.
func TestRun_ForeachParallel(t *testing.T) {
	dir := t.TempDir()
	marks := filepath.Join(dir, "marks")
	if err := os.MkdirAll(marks, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	eng, _, _ := newTestEngine(t, `
name: foreach-parallel
version: 1
inputs:
  marks:
    type: string
steps:
  - id: work
    foreach:
      items: '["a", "b", "c"]'
      as: item
      max_parallel: 3
      step:
        run:
          argv: ["sh", "-c", "echo x >> {{ .inputs.marks }}/{{ .item }}; while [ $(ls {{ .inputs.marks }} | wc -l) -lt 3 ]; do sleep 0.01; done; printf '{{ .item }}'"]
          parse: text
`, func(o *Options) {
		o.Inputs = map[string]any{"marks": marks}
	})

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Each item waits for the others, so the step can only succeed if all
	// three ran at the same time.
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("run failed: %v", result.Error)
	}

	entries, err := os.ReadDir(marks)
	if err != nil {
		t.Fatalf("read marks: %v", err)
	}
	names := []string{}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "a,b,c" {
		t.Fatalf("marks = %v", names)
	}
}

// items must produce a list; anything else is a config error.
func TestRun_ForeachItemsNotAList(t *testing.T) {
	eng, _, _ := newTestEngine(t, `
name: foreach-bad-items
version: 1
inputs:
  what:
    type: string
    default: "plain"
steps:
  - id: work
    foreach:
      items: "{{ .inputs.what }}"
      as: item
      step:
        run:
          argv: ["true"]
`, func(o *Options) {
		o.Inputs = map[string]any{"what": "plain"}
	})

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status == runstore.StatusSuccess {
		t.Fatal("run succeeded, want failure")
	}
	if result.Error.Class != ClassConfig {
		t.Errorf("class = %q, want config", result.Error.Class)
	}
	if !strings.Contains(result.Error.Message, "not a list") {
		t.Errorf("message = %q", result.Error.Message)
	}
}

// An empty list is not an error: the step succeeds with no items.
func TestRun_ForeachEmptyList(t *testing.T) {
	eng, _, _ := newTestEngine(t, `
name: foreach-empty
version: 1
steps:
  - id: work
    foreach:
      items: '[]'
      as: item
      step:
        run:
          argv: ["false"]
`, nil)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("run failed: %v", result.Error)
	}
	if items := eng.steps["work"].Items; len(items) != 0 {
		t.Fatalf("items = %v, want none", items)
	}
}

// A body with on_error: continue absorbs its own failure: the item is failed
// but the foreach is not.
func TestRun_ForeachBodyOnErrorContinue(t *testing.T) {
	eng, _, _ := newTestEngine(t, `
name: foreach-body-continue
version: 1
steps:
  - id: work
    foreach:
      items: '["bad"]'
      as: item
      step:
        on_error: continue
        run:
          argv: ["false"]
`, nil)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("run failed: %v", result.Error)
	}
	if got := itemStatuses(t, eng.steps["work"].Items); got[0] != "failed" {
		t.Errorf("statuses = %v, want failed", got)
	}
}

// A retry inside the body applies per item.
func TestRun_ForeachBodyRetry(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "count")

	eng, _, _ := newTestEngine(t, `
name: foreach-retry
version: 1
inputs:
  counter:
    type: string
steps:
  - id: work
    foreach:
      items: '["only"]'
      as: item
      step:
        retry:
          attempts: 3
          backoff: "1ms"
        run:
          readonly: true
          argv: ["sh", "-c", "echo x >> {{ .inputs.counter }}; test $(wc -l < {{ .inputs.counter }}) -ge 2"]
`, func(o *Options) {
		o.Inputs = map[string]any{"counter": counter}
	})

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("run failed: %v", result.Error)
	}
	if got := strings.Count(readFile(t, counter), "x"); got != 2 {
		t.Fatalf("body ran %d times, want 2", got)
	}
}

func TestLoneAction(t *testing.T) {
	cases := []struct {
		text  string
		inner string
		ok    bool
	}{
		{text: "{{ .inputs.list }}", inner: ".inputs.list", ok: true},
		{text: "  {{ .inputs.list }}  ", inner: ".inputs.list", ok: true},
		{text: `["a"]`, ok: false},
		{text: "{{ .a }}{{ .b }}", ok: false},
		{text: "{{ .inputs.list | toJSON }}", ok: false},
		{text: "prefix {{ .a }}", ok: false},
	}
	for _, tc := range cases {
		inner, ok := loneAction(tc.text)
		if ok != tc.ok || inner != tc.inner {
			t.Errorf("loneAction(%q) = %q, %v; want %q, %v", tc.text, inner, ok, tc.inner, tc.ok)
		}
	}
}
