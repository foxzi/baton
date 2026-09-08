package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/cache"
)

// cacheProbe is a scenario whose single step appends a line to a marker file
// every time it actually runs, so a test can count executions.
type cacheProbe struct {
	yaml   string
	marker string
	dir    string
}

// newCacheProbe builds the probe scenario. extra is spliced into the step,
// after the run body, for cache: and readonly: variations.
func newCacheProbe(t *testing.T, stepFields, runFields string) *cacheProbe {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "calls")
	yamlText := fmt.Sprintf(`
version: 1
name: probe
steps:
  - id: probe
%s    run:
      argv: ["sh", "-c", "echo call >> %s; printf hi"]
      parse: text
%s`, stepFields, marker, runFields)
	return &cacheProbe{yaml: yamlText, marker: marker, dir: dir}
}

// calls is how many times the command has run.
func (p *cacheProbe) calls(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(p.marker)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	return len(strings.Split(strings.TrimSpace(string(data)), "\n"))
}

// run executes the probe scenario once against the given cache directory.
func (p *cacheProbe) run(t *testing.T, cacheDir string, noCache bool) (*Result, string) {
	t.Helper()
	eng, store, _ := newTestEngine(t, p.yaml, func(opts *Options) {
		// The workspace is part of the rendered inputs, so it has to be
		// the same directory on every run of the probe.
		opts.Workspace = p.dir
		opts.Cache = cache.Open(cacheDir)
		opts.NoCache = noCache
	})
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return result, store.Dir()
}

// 1. A readonly run step is replayed from the cache instead of executed
// again (section 10.3).
func TestCache_ReadonlyRunIsReplayed(t *testing.T) {
	probe := newCacheProbe(t, "", "      readonly: true\n")
	cacheDir := filepath.Join(t.TempDir(), "cache")

	first, _ := probe.run(t, cacheDir, false)
	if first.Status != "success" {
		t.Fatalf("first run status = %s", first.Status)
	}
	if got := probe.calls(t); got != 1 {
		t.Fatalf("the command ran %d times on the first run, want 1", got)
	}

	second, runDir := probe.run(t, cacheDir, false)
	if second.Status != "success" {
		t.Fatalf("second run status = %s", second.Status)
	}
	if got := probe.calls(t); got != 1 {
		t.Fatalf("the command ran %d times in total, want 1: the cache was not used", got)
	}

	// The replayed step looks like a real one: the result is there and
	// run.json says where it came from.
	out := readStepJSON(t, filepath.Join(runDir, "steps", "probe", "output.json"))
	if out["result"] != "hi" {
		t.Errorf("output.json result = %v, want hi", out["result"])
	}
	if out["cached"] != true {
		t.Errorf("output.json cached = %v, want true", out["cached"])
	}
	state := readRunState(t, runDir)
	if !state.Steps["probe"].CacheHit {
		t.Error("run.json cache_hit = false, want true")
	}
	if state.Steps["probe"].Status != "success" {
		t.Errorf("run.json step status = %s", state.Steps["probe"].Status)
	}
}

// 2. Without a cache directory nothing is cached.
func TestCache_DisabledRunsEveryTime(t *testing.T) {
	probe := newCacheProbe(t, "", "      readonly: true\n")
	probe.run(t, "", false)
	probe.run(t, "", false)
	if got := probe.calls(t); got != 2 {
		t.Fatalf("the command ran %d times, want 2", got)
	}
}

// 3. A run step with side effects is not cached by default (section 10.3).
func TestCache_SideEffectsAreNotCached(t *testing.T) {
	probe := newCacheProbe(t, "", "")
	cacheDir := filepath.Join(t.TempDir(), "cache")
	probe.run(t, cacheDir, false)
	probe.run(t, cacheDir, false)
	if got := probe.calls(t); got != 2 {
		t.Fatalf("the command ran %d times, want 2: a non-readonly step was cached", got)
	}
}

// 4. cache: true caches a step the defaults would not (section 10.3).
func TestCache_ExplicitTrue(t *testing.T) {
	probe := newCacheProbe(t, "    cache: true\n", "")
	cacheDir := filepath.Join(t.TempDir(), "cache")
	probe.run(t, cacheDir, false)
	probe.run(t, cacheDir, false)
	if got := probe.calls(t); got != 1 {
		t.Fatalf("the command ran %d times, want 1", got)
	}
}

// 5. cache: false opts a readonly step out.
func TestCache_ExplicitFalse(t *testing.T) {
	probe := newCacheProbe(t, "    cache: false\n", "      readonly: true\n")
	cacheDir := filepath.Join(t.TempDir(), "cache")
	probe.run(t, cacheDir, false)
	probe.run(t, cacheDir, false)
	if got := probe.calls(t); got != 2 {
		t.Fatalf("the command ran %d times, want 2", got)
	}
}

// 6. --no-cache skips reading but keeps writing, so the run after it hits
// (section 10.3).
func TestCache_NoCacheStillWrites(t *testing.T) {
	probe := newCacheProbe(t, "", "      readonly: true\n")
	cacheDir := filepath.Join(t.TempDir(), "cache")

	probe.run(t, cacheDir, true)
	if got := probe.calls(t); got != 1 {
		t.Fatalf("the command ran %d times, want 1", got)
	}
	probe.run(t, cacheDir, true)
	if got := probe.calls(t); got != 2 {
		t.Fatalf("the command ran %d times with --no-cache, want 2", got)
	}
	probe.run(t, cacheDir, false)
	if got := probe.calls(t); got != 2 {
		t.Fatalf("the command ran %d times, want 2: --no-cache did not store the result", got)
	}
}

// 7. A step whose rendered inputs changed misses the cache.
func TestCache_RenderedInputsChangeTheKey(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	dir := t.TempDir()
	marker := filepath.Join(dir, "calls")
	yamlText := fmt.Sprintf(`
version: 1
name: probe
inputs:
  who:
    type: string
    default: world
steps:
  - id: probe
    run:
      argv: ["sh", "-c", "echo call >> %s; printf '{{ .inputs.who }}'"]
      parse: text
      readonly: true
`, marker)

	call := func(who string) string {
		eng, store, _ := newTestEngine(t, yamlText, func(opts *Options) {
			opts.Inputs = map[string]any{"who": who}
			opts.Workspace = dir
			opts.Cache = cache.Open(cacheDir)
		})
		if _, err := eng.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		out := readStepJSON(t, filepath.Join(store.Dir(), "steps", "probe", "output.json"))
		return fmt.Sprint(out["result"])
	}

	if got := call("world"); got != "world" {
		t.Fatalf("result = %q", got)
	}
	if got := call("mars"); got != "mars" {
		t.Fatalf("result = %q, want mars: the cache answered for another input", got)
	}
	if got := call("world"); got != "world" {
		t.Fatalf("result = %q", got)
	}

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if got := len(strings.Split(strings.TrimSpace(string(data)), "\n")); got != 2 {
		t.Fatalf("the command ran %d times, want 2", got)
	}
}

// 8. A corrupt entry is a miss, not a failure.
func TestCache_CorruptEntryIsAMiss(t *testing.T) {
	probe := newCacheProbe(t, "", "      readonly: true\n")
	cacheDir := filepath.Join(t.TempDir(), "cache")
	probe.run(t, cacheDir, false)

	corrupted := 0
	filepath.WalkDir(cacheDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
			t.Fatalf("corrupt entry: %v", err)
		}
		corrupted++
		return nil
	})
	if corrupted != 1 {
		t.Fatalf("corrupted %d entries, want 1", corrupted)
	}

	result, _ := probe.run(t, cacheDir, false)
	if result.Status != "success" {
		t.Fatalf("status = %s, want success", result.Status)
	}
	if got := probe.calls(t); got != 2 {
		t.Fatalf("the command ran %d times, want 2", got)
	}
}

// 9. The acceptance criterion of the cache: a repeated llm step spends no
// tokens because the provider is not called again (section 10.3).
func TestCache_LLMIsReplayed(t *testing.T) {
	fake := newLLMServer(t, submitCall(t, `{"answer":"yes"}`, `{"prompt_tokens":10,"completion_tokens":4}`))
	cacheDir := filepath.Join(t.TempDir(), "cache")
	yamlText := `
name: llm
steps:
  - id: ask
    llm:
      model: local/test-model
      prompt: "Say yes"
      schema: schemas/answer.json
`
	ask := func(schema string) map[string]any {
		eng, store, dir := newTestEngine(t, yamlText, func(opts *Options) {
			opts.Config = llmConfig(map[string]string{"local": fake.url})
			opts.Cache = cache.Open(cacheDir)
		})
		writeSchema(t, dir, "schemas/answer.json", schema)
		result, err := eng.Run(t.Context())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if result.Status != "success" {
			t.Fatalf("status = %s, error = %+v", result.Status, result.Error)
		}
		return readStepJSON(t, filepath.Join(store.Dir(), "steps", "ask", "output.json"))
	}

	first := ask(llmSchema)
	if answer, _ := first["result"].(map[string]any); answer["answer"] != "yes" {
		t.Fatalf("result = %v", first["result"])
	}

	second := ask(llmSchema)
	if second["cached"] != true {
		t.Errorf("output.json cached = %v, want true", second["cached"])
	}
	if answer, _ := second["result"].(map[string]any); answer["answer"] != "yes" {
		t.Errorf("replayed result = %v", second["result"])
	}
	// The replay spent nothing, so there is no usage to report.
	if second["usage"] != nil {
		t.Errorf("output.json usage = %v, want none on a replay", second["usage"])
	}
	if fake.calls != 1 {
		t.Errorf("provider calls = %d, want 1", fake.calls)
	}
}

// 10. Editing the schema file invalidates the entry, because the key covers
// the content of the files a step reads (section 10.3).
func TestCache_SchemaContentChangesTheKey(t *testing.T) {
	fake := newLLMServer(t,
		submitCall(t, `{"answer":"yes"}`, ""),
		submitCall(t, `{"answer":"yes","note":"more"}`, ""),
	)
	cacheDir := filepath.Join(t.TempDir(), "cache")
	yamlText := `
name: llm
steps:
  - id: ask
    llm:
      model: local/test-model
      prompt: "Say yes"
      schema: schemas/answer.json
`
	relaxed := strings.Replace(llmSchema, `"additionalProperties": false`, `"additionalProperties": true`, 1)

	for _, schema := range []string{llmSchema, relaxed} {
		eng, _, dir := newTestEngine(t, yamlText, func(opts *Options) {
			opts.Config = llmConfig(map[string]string{"local": fake.url})
			opts.Cache = cache.Open(cacheDir)
		})
		writeSchema(t, dir, "schemas/answer.json", schema)
		result, err := eng.Run(t.Context())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if result.Status != "success" {
			t.Fatalf("status = %s, error = %+v", result.Status, result.Error)
		}
	}
	if fake.calls != 2 {
		t.Errorf("provider calls = %d, want 2: the schema edit did not invalidate the entry", fake.calls)
	}
}
