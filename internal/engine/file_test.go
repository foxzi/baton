package engine

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/foxzi/baton/internal/runstore"
)

// TestRun_FileRead covers the parse modes of a file read: text, json and
// lines.
func TestRun_FileRead(t *testing.T) {
	cases := []struct {
		name    string
		content string
		parse   string
		want    any
	}{
		{
			name:    "text",
			content: "hello world",
			parse:   "text",
			want:    "hello world",
		},
		{
			name:    "json",
			content: `{"a": 1, "b": "two"}`,
			parse:   "json",
			want:    map[string]any{"a": float64(1), "b": "two"},
		},
		{
			name:    "lines",
			content: "a\nb\nc\n",
			parse:   "lines",
			want:    []any{"a", "b", "c"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			yamlText := `
version: 1
name: file-read
steps:
  - id: r
    file:
      read: data.txt
      parse: ` + tc.parse + `
`
			eng, store, dir := newTestEngine(t, yamlText, nil)
			if err := os.WriteFile(filepath.Join(dir, "data.txt"), []byte(tc.content), 0o644); err != nil {
				t.Fatalf("write data.txt: %v", err)
			}

			result, err := eng.Run(context.Background())
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if result.Status != runstore.StatusSuccess {
				t.Fatalf("Status = %q, want success (error=%v)", result.Status, result.Error)
			}

			step := eng.steps["r"]
			if !equalResult(step.Result, tc.want) {
				t.Errorf("result = %#v, want %#v", step.Result, tc.want)
			}

			out := readStepJSON(t, filepath.Join(store.Dir(), "steps", "r", "output.json"))
			if out["status"] != "success" {
				t.Errorf("output.json status = %v, want success", out["status"])
			}
		})
	}
}

// equalResult compares the shapes decoded from JSON-ish step results without
// pulling in reflect.DeepEqual edge cases across map/slice element types.
func equalResult(got, want any) bool {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for k, wv := range w {
			if !equalResult(g[k], wv) {
				return false
			}
		}
		return true
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !equalResult(g[i], w[i]) {
				return false
			}
		}
		return true
	default:
		return got == want
	}
}

// TestRun_FileReadMissing checks that reading a file that does not exist is
// a command-class error.
func TestRun_FileReadMissing(t *testing.T) {
	yamlText := `
version: 1
name: file-read-missing
steps:
  - id: r
    file:
      read: nope.txt
`
	eng, _, _ := newTestEngine(t, yamlText, nil)
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
}

// TestRun_FileReadOverMaxBytes checks that a file larger than max_bytes (or
// the default 1 MiB cap) is refused as a command-class error.
func TestRun_FileReadOverMaxBytes(t *testing.T) {
	cases := []struct {
		name     string
		maxBytes string
		size     int
	}{
		{name: "explicit max_bytes", maxBytes: "max_bytes: 10", size: 11},
		{name: "default cap", maxBytes: "", size: (1 << 20) + 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			yamlText := `
version: 1
name: file-read-too-big
steps:
  - id: r
    file:
      read: big.txt
      ` + tc.maxBytes + `
`
			eng, _, dir := newTestEngine(t, yamlText, nil)
			content := make([]byte, tc.size)
			if err := os.WriteFile(filepath.Join(dir, "big.txt"), content, 0o644); err != nil {
				t.Fatalf("write big.txt: %v", err)
			}

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
		})
	}
}

// TestRun_FileWrite checks that a write creates parent directories, truncates
// the file and reports {path, bytes} as its result, without leaking the
// content into input.json.
func TestRun_FileWrite(t *testing.T) {
	yamlText := `
version: 1
name: file-write
steps:
  - id: w
    file:
      write: sub/dir/out.txt
      content: "hello file"
`
	eng, store, dir := newTestEngine(t, yamlText, nil)
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want success (error=%v)", result.Status, result.Error)
	}

	got := readFile(t, filepath.Join(dir, "sub", "dir", "out.txt"))
	if got != "hello file" {
		t.Errorf("file content = %q, want %q", got, "hello file")
	}

	step := eng.steps["w"]
	res, ok := step.Result.(map[string]any)
	if !ok {
		t.Fatalf("result is %T, want map[string]any: %#v", step.Result, step.Result)
	}
	if res["path"] != "sub/dir/out.txt" {
		t.Errorf("result path = %v, want sub/dir/out.txt", res["path"])
	}
	if res["bytes"] != len("hello file") {
		t.Errorf("result bytes = %v, want %d", res["bytes"], len("hello file"))
	}

	input := readStepJSON(t, filepath.Join(store.Dir(), "steps", "w", "input.json"))
	if _, ok := input["content"]; ok {
		t.Errorf("input.json contains content: %v", input)
	}
	if bytes, ok := input["bytes"].(float64); !ok || int(bytes) != len("hello file") {
		t.Errorf("input.json bytes = %v, want %d", input["bytes"], len("hello file"))
	}
}

// TestRun_FileAppend checks that append adds to an existing file and creates
// one that does not exist yet.
func TestRun_FileAppend(t *testing.T) {
	yamlText := `
version: 1
name: file-append
steps:
  - id: a
    file:
      append: log.txt
      content: "second\n"
`
	eng, _, dir := newTestEngine(t, yamlText, nil)
	if err := os.WriteFile(filepath.Join(dir, "log.txt"), []byte("first\n"), 0o644); err != nil {
		t.Fatalf("write log.txt: %v", err)
	}

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want success (error=%v)", result.Status, result.Error)
	}

	got := readFile(t, filepath.Join(dir, "log.txt"))
	if got != "first\nsecond\n" {
		t.Errorf("file content = %q, want %q", got, "first\nsecond\n")
	}
}

// TestRun_FileAppendCreatesMissing checks that append works on a file that
// does not exist yet.
func TestRun_FileAppendCreatesMissing(t *testing.T) {
	yamlText := `
version: 1
name: file-append-new
steps:
  - id: a
    file:
      append: fresh/log.txt
      content: "only\n"
`
	eng, _, dir := newTestEngine(t, yamlText, nil)
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want success (error=%v)", result.Status, result.Error)
	}

	got := readFile(t, filepath.Join(dir, "fresh", "log.txt"))
	if got != "only\n" {
		t.Errorf("file content = %q, want %q", got, "only\n")
	}
}

// TestRun_FileGlob checks that glob matches with ** across segments and
// returns a sorted list of workspace-relative paths, skipping directories
// and symlinks.
func TestRun_FileGlob(t *testing.T) {
	yamlText := `
version: 1
name: file-glob
steps:
  - id: g
    file:
      glob: "notes/**/*.md"
`
	eng, _, dir := newTestEngine(t, yamlText, nil)

	mustWrite := func(rel, content string) {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	mustWrite("notes/z.md", "z")
	mustWrite("notes/a.md", "a")
	mustWrite("notes/sub/b.md", "b")
	mustWrite("notes/sub/skip.txt", "skip")
	mustWrite("other/c.md", "c")

	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(dir, "notes", "a.md"), filepath.Join(dir, "notes", "link.md")); err != nil {
			t.Fatalf("symlink: %v", err)
		}
	}

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want success (error=%v)", result.Status, result.Error)
	}

	step := eng.steps["g"]
	items, ok := step.Result.([]any)
	if !ok {
		t.Fatalf("result is %T, want []any: %#v", step.Result, step.Result)
	}
	want := []any{"notes/a.md", "notes/sub/b.md", "notes/z.md"}
	if len(items) != len(want) {
		t.Fatalf("result = %#v, want %#v", items, want)
	}
	for i, v := range want {
		if items[i] != v {
			t.Errorf("result[%d] = %#v, want %#v", i, items[i], v)
		}
	}
}

// TestRun_FileAbsolutePathRefused checks that an absolute path is a
// config-class error at run time.
func TestRun_FileAbsolutePathRefused(t *testing.T) {
	yamlText := `
version: 1
name: file-absolute
steps:
  - id: r
    file:
      read: /etc/passwd
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

// TestRun_FileDotDotRefused checks that a path containing .. is a
// config-class error at run time, for both read and write targets.
func TestRun_FileDotDotRefused(t *testing.T) {
	cases := []struct {
		name string
		file string
	}{
		{name: "read", file: `read: "../outside.txt"`},
		{name: "write", file: "write: \"../outside.txt\"\n      content: hi"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			yamlText := `
version: 1
name: file-dotdot
steps:
  - id: s
    file:
      ` + tc.file + `
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
		})
	}
}
