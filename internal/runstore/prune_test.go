package runstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pruneFixture writes four runs: three finished at one-day intervals, the
// newest of them today, plus one that is still marked running since
// yesterday. It returns the reference time the runs are measured against.
func pruneFixture(t *testing.T, root string) time.Time {
	t.Helper()
	now := time.Date(2024, 5, 10, 12, 0, 0, 0, time.UTC)
	for i, id := range []string{
		"20240510-090000-aaaa",
		"20240509-090000-bbbb",
		"20240508-090000-cccc",
	} {
		finished := now.Add(-time.Duration(i) * 24 * time.Hour)
		writeRun(t, root, &RunState{
			SchemaVersion: SchemaVersion, ID: id, Status: StatusSuccess,
			StartedAt: finished.Add(-time.Minute), FinishedAt: &finished,
		})
	}
	writeRun(t, root, &RunState{
		SchemaVersion: SchemaVersion, ID: "20240509-100000-dddd", Status: StatusRunning,
		StartedAt: now.Add(-24 * time.Hour),
	})
	return now
}

func remaining(t *testing.T, root string) []string {
	t.Helper()
	runs, err := List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	ids := make([]string, len(runs))
	for i, run := range runs {
		ids[i] = run.ID
	}
	return ids
}

func TestPruneKeepsNewestAndRunning(t *testing.T) {
	root := t.TempDir()
	now := pruneFixture(t, root)

	removed, err := Prune(root, PruneOptions{Keep: 1, Now: now})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	// The running run sorts second but is not removed by Keep alone.
	want := "20240509-090000-bbbb,20240508-090000-cccc"
	if got := strings.Join(removed, ","); got != want {
		t.Errorf("removed = %v, want %v", got, want)
	}
	if got := strings.Join(remaining(t, root), ","); got != "20240510-090000-aaaa,20240509-100000-dddd" {
		t.Errorf("remaining = %v", got)
	}
}

func TestPruneOlderThanRemovesStaleRunningRuns(t *testing.T) {
	root := t.TempDir()
	now := pruneFixture(t, root)

	removed, err := Prune(root, PruneOptions{OlderThan: 36 * time.Hour, Now: now})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	// Two days old: past the threshold. The running run started a day ago,
	// which is within it, so it stays.
	if got := strings.Join(removed, ","); got != "20240508-090000-cccc" {
		t.Errorf("removed = %v, want the two-day-old run only", got)
	}

	removed, err = Prune(root, PruneOptions{OlderThan: 12 * time.Hour, Now: now})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if got := strings.Join(removed, ","); got != "20240509-100000-dddd,20240509-090000-bbbb" {
		t.Errorf("removed = %v, want the day-old finished run and the stale running one", got)
	}
	if got := strings.Join(remaining(t, root), ","); got != "20240510-090000-aaaa" {
		t.Errorf("remaining = %v", got)
	}
}

func TestPruneDryRunDeletesNothing(t *testing.T) {
	root := t.TempDir()
	now := pruneFixture(t, root)

	removed, err := Prune(root, PruneOptions{Keep: 1, DryRun: true, Now: now})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(removed) != 2 {
		t.Errorf("removed = %v, want two candidates", removed)
	}
	if got := remaining(t, root); len(got) != 4 {
		t.Errorf("remaining = %v, want all four runs intact", got)
	}
}

func TestPruneLeavesForeignDirectoriesAlone(t *testing.T) {
	root := t.TempDir()
	now := pruneFixture(t, root)
	stray := filepath.Join(root, "20240101-000000-eeee")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := Prune(root, PruneOptions{OlderThan: time.Hour, Now: now}); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Errorf("a directory without run.json was removed: %v", err)
	}
}

func TestPruneNeedsACriterion(t *testing.T) {
	if _, err := Prune(t.TempDir(), PruneOptions{}); err == nil {
		t.Error("Prune without Keep or OlderThan = nil error, want error")
	}
}
