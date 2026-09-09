package runstore

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/foxzi/baton/internal/secrets"
	"github.com/foxzi/baton/internal/values"
)

var idPattern = regexp.MustCompile(`^\d{8}-\d{6}-[0-9a-f]{4}$`)

func TestNewID(t *testing.T) {
	now := time.Date(2024, 3, 5, 13, 4, 7, 0, time.UTC)
	id := NewID(now)
	if !idPattern.MatchString(id) {
		t.Fatalf("id %q does not match %s", id, idPattern)
	}
	if !strings.HasPrefix(id, "20240305-130407-") {
		t.Fatalf("id %q does not encode %v", id, now)
	}

	id2 := NewID(now)
	if id == id2 {
		t.Fatalf("two ids for the same time were equal: %q", id)
	}
}

func TestValidateID(t *testing.T) {
	valid := []string{
		NewID(time.Date(2024, 3, 5, 13, 4, 7, 0, time.UTC)),
		"20240305-130407-ab12-r1",
		"20240305-130407-ab12-r12",
		"r1",
		"base",
		"run-1",
	}
	for _, id := range valid {
		if err := ValidateID(id); err != nil {
			t.Errorf("ValidateID(%q) = %v, want nil", id, err)
		}
	}

	invalid := []string{
		"../evil",
		"a/b",
		"",
		"20250101-120000-xyz!",
		"/etc/passwd",
		"..",
	}
	for _, id := range invalid {
		if err := ValidateID(id); err == nil {
			t.Errorf("ValidateID(%q) = nil, want error", id)
		}
	}
}

func TestCreateRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Dir(root)

	for _, id := range []string{"../escape", "../../escape", "/escape"} {
		if _, err := Create(root, id, nil); err == nil {
			t.Errorf("Create(%q) = nil error, want error", id)
		}
	}

	if _, err := os.Stat(filepath.Join(parent, "escape")); !os.IsNotExist(err) {
		t.Fatalf("Create escaped runsDir: %v", err)
	}
}

func TestCreate(t *testing.T) {
	root := t.TempDir()

	store, err := Create(root, "run-1", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer store.Close()

	wantDir := filepath.Join(root, "run-1")
	if store.Dir() != wantDir {
		t.Errorf("Dir() = %q, want %q", store.Dir(), wantDir)
	}
	if store.ID() != "run-1" {
		t.Errorf("ID() = %q, want run-1", store.ID())
	}
	if info, err := os.Stat(wantDir); err != nil || !info.IsDir() {
		t.Errorf("run directory was not created: %v", err)
	}

	if err := store.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestCreate_NonEmptyDirFails(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "run-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "leftover.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Create(root, "run-1", nil); err == nil {
		t.Fatal("Create over a non-empty run directory succeeded, want error")
	}
}

func TestEvent(t *testing.T) {
	root := t.TempDir()
	store, err := Create(root, "run-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := store.Event("run_started", map[string]any{"name": "demo"}); err != nil {
		t.Fatalf("Event: %v", err)
	}
	if err := store.Event("step_started", map[string]any{"step": "a"}); err != nil {
		t.Fatalf("Event: %v", err)
	}

	lines := readLines(t, filepath.Join(store.Dir(), "events.jsonl"))
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	for _, line := range lines {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("line %q is not valid JSON: %v", line, err)
		}
		if _, ok := decoded["ts"]; !ok {
			t.Errorf("line %q missing ts", line)
		}
		if _, ok := decoded["type"]; !ok {
			t.Errorf("line %q missing type", line)
		}
	}
}

func TestWriteRun(t *testing.T) {
	root := t.TempDir()
	store, err := Create(root, "run-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	started := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	state := &RunState{
		SchemaVersion: SchemaVersion,
		ID:            "run-1",
		Name:          "demo",
		Status:        StatusRunning,
		StartedAt:     started,
		Inputs:        map[string]any{"env": "prod"},
		Steps: map[string]*StepState{
			"a": {Status: StatusSuccess, StartedAt: started},
		},
	}
	if err := store.WriteRun(state); err != nil {
		t.Fatalf("WriteRun: %v", err)
	}

	runPath := filepath.Join(store.Dir(), "run.json")
	data, err := os.ReadFile(runPath)
	if err != nil {
		t.Fatal(err)
	}
	var got RunState
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("run.json is not valid JSON: %v", err)
	}
	if got.ID != state.ID || got.Name != state.Name || got.Status != state.Status {
		t.Errorf("got %+v, want %+v", got, state)
	}
	if !got.StartedAt.Equal(state.StartedAt) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, state.StartedAt)
	}

	// Second write: exactly one run.json, updated content, no leftover temp files.
	finished := started.Add(time.Minute)
	state.Status = StatusSuccess
	state.FinishedAt = &finished
	if err := store.WriteRun(state); err != nil {
		t.Fatalf("WriteRun (second): %v", err)
	}

	entries, err := os.ReadDir(store.Dir())
	if err != nil {
		t.Fatal(err)
	}
	var runFiles, tmpFiles int
	for _, e := range entries {
		switch {
		case e.Name() == "run.json":
			runFiles++
		case strings.HasPrefix(e.Name(), ".tmp-"):
			tmpFiles++
		}
	}
	if runFiles != 1 {
		t.Errorf("found %d run.json files, want 1", runFiles)
	}
	if tmpFiles != 0 {
		t.Errorf("found %d leftover temp files, want 0", tmpFiles)
	}

	data, err = os.ReadFile(runPath)
	if err != nil {
		t.Fatal(err)
	}
	var got2 RunState
	if err := json.Unmarshal(data, &got2); err != nil {
		t.Fatal(err)
	}
	if got2.Status != StatusSuccess || got2.FinishedAt == nil {
		t.Errorf("second write not reflected: %+v", got2)
	}
}

func TestStepDir(t *testing.T) {
	root := t.TempDir()
	store, err := Create(root, "run-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	dir, err := store.StepDir("review/3")
	if err != nil {
		t.Fatalf("StepDir: %v", err)
	}
	want := filepath.Join(store.Dir(), "steps", "review", "3")
	if dir != want {
		t.Errorf("StepDir = %q, want %q", dir, want)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Errorf("nested step dir was not created: %v", err)
	}
}

func TestWriteStepFileAndJSON(t *testing.T) {
	root := t.TempDir()
	store, err := Create(root, "run-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := store.WriteStepFile("build", "stdout.log", []byte("hello\n")); err != nil {
		t.Fatalf("WriteStepFile: %v", err)
	}
	stdoutPath := filepath.Join(store.Dir(), "steps", "build", "stdout.log")
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello\n" {
		t.Errorf("stdout.log = %q, want %q", data, "hello\n")
	}

	type output struct {
		Status string `json:"status"`
	}
	if err := store.WriteStepJSON("build", "output.json", output{Status: "success"}); err != nil {
		t.Fatalf("WriteStepJSON: %v", err)
	}
	outputPath := filepath.Join(store.Dir(), "steps", "build", "output.json")
	data, err = os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	var got output
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("output.json is not valid JSON: %v", err)
	}
	if got.Status != "success" {
		t.Errorf("Status = %q, want success", got.Status)
	}
}

func TestEffects(t *testing.T) {
	root := t.TempDir()
	store, err := Create(root, "run-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if store.EffectDone("post:/orders") {
		t.Error("EffectDone true before MarkEffect")
	}
	if err := store.MarkEffect("post:/orders"); err != nil {
		t.Fatalf("MarkEffect: %v", err)
	}
	if !store.EffectDone("post:/orders") {
		t.Error("EffectDone false after MarkEffect")
	}

	data, err := os.ReadFile(filepath.Join(store.Dir(), "effects.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "post:/orders") {
		t.Errorf("effects.json does not contain the key: %s", data)
	}

	// Reading back into a fresh Store is not supported: Create refuses a
	// non-empty run directory, and no loader is implemented since the task
	// did not ask for one. Reflection is verified by reading the file
	// directly, above.
	var decoded effectsFile
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, k := range decoded.Keys {
		if k == "post:/orders" {
			found = true
		}
	}
	if !found {
		t.Errorf("decoded effects.json does not contain the key: %+v", decoded)
	}
}

// TestSecurity_NoPlaintextLeak is the most important test in this package: no
// file written anywhere under the run directory may contain a secret's
// plaintext, regardless of which method wrote it.
func TestSecurity_NoPlaintextLeak(t *testing.T) {
	const plaintext = "s3cr3t-plaintext"
	redactor := secrets.NewRedactor(values.NewSecret("tok", plaintext))

	root := t.TempDir()
	store, err := Create(root, "run-1", redactor)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := store.Event("http_request", map[string]any{
		"url": "https://api.example.com/?token=" + plaintext,
	}); err != nil {
		t.Fatalf("Event: %v", err)
	}

	started := time.Now().UTC()
	if err := store.WriteRun(&RunState{
		SchemaVersion: SchemaVersion,
		ID:            "run-1",
		Name:          "demo",
		Status:        StatusFailed,
		StartedAt:     started,
		FailedStep:    "call-api",
		Error: &RunError{
			Class:   "command",
			Message: "request failed with body: " + plaintext,
		},
	}); err != nil {
		t.Fatalf("WriteRun: %v", err)
	}

	if err := store.WriteStepFile("call-api", "stderr.log", []byte("auth failed: "+plaintext+"\n")); err != nil {
		t.Fatalf("WriteStepFile: %v", err)
	}

	type input struct {
		Header string `json:"header"`
	}
	if err := store.WriteStepJSON("call-api", "input.json", input{Header: "Bearer " + plaintext}); err != nil {
		t.Fatalf("WriteStepJSON: %v", err)
	}

	if err := store.MarkEffect("post:" + plaintext); err != nil {
		t.Fatalf("MarkEffect: %v", err)
	}

	sawRedacted := false
	err = filepath.Walk(store.Dir(), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(plaintext)) {
			t.Errorf("file %s contains the secret plaintext", path)
		}
		if bytes.Contains(data, []byte(values.Redacted)) {
			sawRedacted = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", store.Dir(), err)
	}
	if !sawRedacted {
		t.Error("no file contained the redacted marker \"***\"; redaction may not have run")
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}
