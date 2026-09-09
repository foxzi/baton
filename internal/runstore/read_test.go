package runstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foxzi/baton/internal/secrets"
	"github.com/foxzi/baton/internal/values"
)

// writeRun creates a run directory under root and writes run.json for it,
// the way a finished run leaves it behind for the reading side.
func writeRun(t *testing.T, root string, state *RunState) {
	t.Helper()
	store, err := Create(root, state.ID, nil)
	if err != nil {
		t.Fatalf("Create(%s): %v", state.ID, err)
	}
	defer store.Close()
	if err := store.WriteRun(state); err != nil {
		t.Fatalf("WriteRun(%s): %v", state.ID, err)
	}
}

func TestReadRun(t *testing.T) {
	root := t.TempDir()
	started := time.Date(2024, 5, 1, 9, 0, 0, 0, time.UTC)
	writeRun(t, root, &RunState{
		SchemaVersion: SchemaVersion,
		ID:            "20240501-090000-aaaa",
		Name:          "demo",
		Status:        StatusFailed,
		StartedAt:     started,
		FailedStep:    "build",
		Error:         &RunError{Class: "tool", Message: "exit 1"},
		Steps:         map[string]*StepState{"build": {Status: StatusFailed, StartedAt: started}},
	})

	got, err := ReadRun(root, "20240501-090000-aaaa")
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	if got.Name != "demo" || got.Status != StatusFailed || got.FailedStep != "build" {
		t.Errorf("ReadRun() = %+v, want the run that was written", got)
	}
	if got.Error == nil || got.Error.Class != "tool" {
		t.Errorf("ReadRun().Error = %+v, want the tool failure", got.Error)
	}
	if !got.StartedAt.Equal(started) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, started)
	}
}

func TestReadRunMissing(t *testing.T) {
	if _, err := ReadRun(t.TempDir(), "nope"); err == nil {
		t.Fatal("ReadRun() error = nil, want error for a run that does not exist")
	}
}

func TestReadRunBrokenJSON(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "run-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ReadRun(root, "run-1")
	if err == nil {
		t.Fatal("ReadRun() error = nil, want error for truncated run.json")
	}
	if !strings.Contains(err.Error(), "run.json") {
		t.Errorf("ReadRun() error = %q, want the path in the message", err)
	}
}

func TestWriteAndReadCost(t *testing.T) {
	root := t.TempDir()
	store, err := Create(root, "run-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	cost := 0.25
	report := &CostReport{
		SchemaVersion:     SchemaVersion,
		TotalUSD:          &cost,
		TotalInputTokens:  100,
		TotalOutputTokens: 20,
		Steps: []CostEntry{
			{Step: "digest", Model: "claude", InputTokens: 100, OutputTokens: 20, CostUSD: &cost, Duration: "1s"},
			// A model missing from the pricing table has no known cost,
			// which must survive the round trip as null, not as zero.
			{Step: "extra", Model: "unknown", Duration: "2s"},
		},
	}
	if err := store.WriteCost(report); err != nil {
		t.Fatalf("WriteCost: %v", err)
	}

	got, err := ReadCost(root, "run-1")
	if err != nil {
		t.Fatalf("ReadCost: %v", err)
	}
	if got == nil {
		t.Fatal("ReadCost() = nil, want the report that was written")
	}
	if got.TotalUSD == nil || *got.TotalUSD != cost {
		t.Errorf("TotalUSD = %v, want %v", got.TotalUSD, cost)
	}
	if len(got.Steps) != 2 {
		t.Fatalf("Steps = %d, want 2", len(got.Steps))
	}
	if got.Steps[1].CostUSD != nil {
		t.Errorf("Steps[1].CostUSD = %v, want null for a model without pricing", *got.Steps[1].CostUSD)
	}
}

func TestWriteCostWithoutStepsIsAnEmptyList(t *testing.T) {
	root := t.TempDir()
	store, err := Create(root, "run-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := store.WriteCost(&CostReport{SchemaVersion: SchemaVersion}); err != nil {
		t.Fatalf("WriteCost: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(store.Dir(), "cost.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("cost.json is not valid JSON: %v", err)
	}
	if string(raw["steps"]) != "[]" {
		t.Errorf("steps = %s, want [] rather than null", raw["steps"])
	}
}

func TestWriteCostRedactsSecrets(t *testing.T) {
	root := t.TempDir()
	redactor := secrets.NewRedactor(values.NewSecret("token", "s3cr3t"))
	store, err := Create(root, "run-1", redactor)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := store.WriteCost(&CostReport{
		SchemaVersion: SchemaVersion,
		Steps:         []CostEntry{{Step: "digest", Model: "model-s3cr3t"}},
	}); err != nil {
		t.Fatalf("WriteCost: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(store.Dir(), "cost.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "s3cr3t") {
		t.Errorf("cost.json carries the secret in plaintext:\n%s", data)
	}
}

func TestReadCostWithoutFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "run-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	report, err := ReadCost(root, "run-1")
	if err != nil {
		t.Fatalf("ReadCost() error = %v, want nil for a run without llm steps", err)
	}
	if report != nil {
		t.Errorf("ReadCost() = %+v, want nil", report)
	}
}

func TestListNewestFirst(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{
		"20240501-090000-aaaa",
		"20240502-090000-bbbb",
		"20240501-090000-aaaa-r1",
	} {
		writeRun(t, root, &RunState{SchemaVersion: SchemaVersion, ID: id, Status: StatusSuccess})
	}
	// A directory without run.json, and a plain file: neither is a run.
	if err := os.MkdirAll(filepath.Join(root, "20240503-000000-cccc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	runs, err := List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := make([]string, len(runs))
	for i, run := range runs {
		got[i] = run.ID
	}
	want := []string{"20240502-090000-bbbb", "20240501-090000-aaaa-r1", "20240501-090000-aaaa"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("List() = %v, want %v (newest first, a resume right after its original)", got, want)
	}
}

func TestListWithoutRunsDir(t *testing.T) {
	runs, err := List(filepath.Join(t.TempDir(), "runs"))
	if err != nil {
		t.Fatalf("List() error = %v, want nil before the first run", err)
	}
	if runs != nil {
		t.Errorf("List() = %v, want nil", runs)
	}
}

func TestStepIDsOrder(t *testing.T) {
	base := time.Date(2024, 5, 1, 9, 0, 0, 0, time.UTC)
	state := &RunState{Steps: map[string]*StepState{
		"third":  {StartedAt: base.Add(2 * time.Second)},
		"first":  {StartedAt: base},
		"second": {StartedAt: base.Add(time.Second)},
		// Same start time as "first": the id breaks the tie, so the order
		// stays stable between runs of the same state.
		"also-first": {StartedAt: base},
	}}
	got := strings.Join(state.StepIDs(), ",")
	want := "also-first,first,second,third"
	if got != want {
		t.Errorf("StepIDs() = %s, want %s", got, want)
	}
}

func TestStepIDsWithoutSteps(t *testing.T) {
	if ids := (&RunState{}).StepIDs(); len(ids) != 0 {
		t.Errorf("StepIDs() = %v, want empty", ids)
	}
}
