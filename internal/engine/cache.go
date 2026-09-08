package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/foxzi/baton/internal/cache"
	"github.com/foxzi/baton/internal/expr"
	"github.com/foxzi/baton/internal/scenario"
	"github.com/foxzi/baton/internal/version"
)

// cacheEntry is what a cached step looks like on disk (section 10.3).
type cacheEntry struct {
	Key       string       `json:"key"`
	Step      string       `json:"step"`
	Kind      string       `json:"kind"`
	CreatedAt time.Time    `json:"created_at"`
	Output    cachedOutput `json:"output"`
}

// cachedOutput is the step result in the shape of section 5.1, which is also
// the output.json a cache hit writes into the run directory.
type cachedOutput struct {
	Status     string            `json:"status"`
	Result     any               `json:"result,omitempty"`
	Stdout     string            `json:"stdout,omitempty"`
	Stderr     string            `json:"stderr,omitempty"`
	ExitCode   int               `json:"exit_code,omitempty"`
	Items      []any             `json:"items,omitempty"`
	HTTPStatus int               `json:"http_status,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       any               `json:"body,omitempty"`
}

func newCachedOutput(out expr.Step) cachedOutput {
	return cachedOutput{
		Status:     expr.StatusSuccess,
		Result:     out.Result,
		Stdout:     out.Stdout,
		Stderr:     out.Stderr,
		ExitCode:   out.ExitCode,
		Items:      out.Items,
		HTTPStatus: out.HTTPStatus,
		Headers:    out.Headers,
		Body:       out.Body,
	}
}

func (c cachedOutput) step() expr.Step {
	return expr.Step{
		Status:     expr.StatusSuccess,
		Result:     c.Result,
		Stdout:     c.Stdout,
		Stderr:     c.Stderr,
		ExitCode:   c.ExitCode,
		Items:      c.Items,
		HTTPStatus: c.HTTPStatus,
		Headers:    c.Headers,
		Body:       c.Body,
	}
}

// cacheable reports whether the result of a step may be cached. The step may
// say so explicitly; otherwise llm steps and readonly run and http steps are
// cached and everything else is not (section 10.3).
func (e *Engine) cacheable(step *scenario.Step) bool {
	if e.opts.Cache == nil {
		return false
	}
	if step.Cache != nil {
		return *step.Cache
	}
	switch step.Kind() {
	case scenario.KindLLM:
		return true
	case scenario.KindRun:
		return step.Run.Readonly
	case scenario.KindHTTP:
		return e.httpReadonly(step.HTTP)
	default:
		return false
	}
}

// cacheKey hashes everything that can change the result of the step: its
// definition without id and when, its rendered inputs, the content of the
// files it reads and the version of baton (section 10.3). An empty key means
// the step is not cached.
func (e *Engine) cacheKey(step *scenario.Step, input any, files map[string]string) string {
	if !e.cacheable(step) {
		return ""
	}
	normalized := *step
	normalized.ID = ""
	normalized.When = ""
	definition, err := yaml.Marshal(&normalized)
	if err != nil {
		return ""
	}
	key, err := cache.Key(map[string]any{
		"version":    version.Version,
		"kind":       string(step.Kind()),
		"definition": string(definition),
		"input":      input,
		"files":      files,
	})
	if err != nil {
		return ""
	}
	return key
}

// cacheGet looks up a step result. It returns the key to store the result
// under, which is empty when this step is not cached at all. On a hit it also
// writes output.json into the step directory, so that a replayed run
// directory looks like a real one.
func (e *Engine) cacheGet(step *scenario.Step, path string, input any, files map[string]string) (string, expr.Step, bool) {
	key := e.cacheKey(step, input, files)
	if key == "" || e.opts.NoCache {
		return key, expr.Step{}, false
	}
	data, ok := e.opts.Cache.Get(key)
	if !ok {
		return key, expr.Step{}, false
	}
	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return key, expr.Step{}, false
	}

	e.replayStepFiles(path, entry.Kind, "cached", entry.Output)
	e.markCacheHit(path)
	e.emit(Event{Type: "cache_hit", Step: step.ID, Fields: map[string]any{"key": key}})
	return key, entry.Output.step(), true
}

// replayStepFiles recreates the files a step would have written, so that a
// replayed run directory looks like a real one. Only the fields the step kind
// actually produced are written, and output.json carries the marker flag
// (cached or resumed) to make the replay visible.
func (e *Engine) replayStepFiles(path, kind, marker string, out cachedOutput) {
	output := map[string]any{"status": out.Status, marker: true}
	if out.Result != nil {
		output["result"] = out.Result
	}
	if out.Items != nil {
		output["items"] = out.Items
	}
	if out.HTTPStatus != 0 {
		output["http_status"] = out.HTTPStatus
	}
	if out.Headers != nil {
		output["headers"] = out.Headers
	}
	if out.Body != nil {
		output["body"] = out.Body
	}
	if kind == string(scenario.KindRun) {
		output["exit_code"] = out.ExitCode
		e.writeStepFile(path, "stdout.log", []byte(out.Stdout))
		e.writeStepFile(path, "stderr.log", []byte(out.Stderr))
	}
	e.writeStepJSON(path, "output.json", output)
}

// cachePut stores a successful step result. A cache that cannot be written is
// reported and forgotten: it must never fail a run that already succeeded.
func (e *Engine) cachePut(key string, step *scenario.Step, out expr.Step) {
	if key == "" || e.opts.Cache == nil {
		return
	}
	entry := cacheEntry{
		Key:       key,
		Step:      step.ID,
		Kind:      string(step.Kind()),
		CreatedAt: e.opts.Now(),
		Output:    newCachedOutput(out),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	if err := e.opts.Cache.Put(key, data); err != nil {
		e.emit(Event{Type: "warning", Step: step.ID, Message: err.Error()})
	}
}

// markCacheHit remembers that a step path was served from the cache, so that
// runStep can flag it in run.json.
func (e *Engine) markCacheHit(path string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.hits[path] = true
}

// takeCacheHit reports and clears the cache-hit flag of a step path.
func (e *Engine) takeCacheHit(path string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	hit := e.hits[path]
	delete(e.hits, path)
	return hit
}

// hashBytes is the content hash of a file that a step reads.
func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
