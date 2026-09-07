package runstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// ReadRun loads run.json of one run directory.
func ReadRun(runsDir, runID string) (*RunState, error) {
	path := filepath.Join(runsDir, runID, "run.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("runstore: read %s: %w", path, err)
	}
	var state RunState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("runstore: %s: %w", path, err)
	}
	return &state, nil
}

// ReadCost loads cost.json of one run directory. A run without an llm step
// has no cost.json; that is not an error, the report is nil.
func ReadCost(runsDir, runID string) (*CostReport, error) {
	path := filepath.Join(runsDir, runID, "cost.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("runstore: read %s: %w", path, err)
	}
	var report CostReport
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("runstore: %s: %w", path, err)
	}
	return &report, nil
}

// List returns the runs of a directory, newest first. Run ids start with a
// timestamp, so sorting them as strings sorts them by time; a resume keeps
// the id of the run it continues plus an -rN suffix, which sorts it right
// after its original.
func List(runsDir string) ([]*RunState, error) {
	entries, err := os.ReadDir(runsDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("runstore: read %s: %w", runsDir, err)
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		ids = append(ids, entry.Name())
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))

	runs := make([]*RunState, 0, len(ids))
	for _, id := range ids {
		state, err := ReadRun(runsDir, id)
		if err != nil {
			// A directory without a readable run.json is a run that never
			// got started, or something else entirely: skip it rather than
			// failing the whole listing.
			continue
		}
		runs = append(runs, state)
	}
	return runs, nil
}

// StepIDs returns the step ids of a run in declaration order as far as the
// state knows it: run.json holds a map, so the order is the start time.
func (r *RunState) StepIDs() []string {
	ids := make([]string, 0, len(r.Steps))
	for id := range r.Steps {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := r.Steps[ids[i]], r.Steps[ids[j]]
		if a.StartedAt.Equal(b.StartedAt) {
			return ids[i] < ids[j]
		}
		return a.StartedAt.Before(b.StartedAt)
	})
	return ids
}
