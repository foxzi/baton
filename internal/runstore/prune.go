package runstore

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// PruneOptions selects the runs that Prune removes. Zero values mean "no
// limit of that kind"; at least one of Keep or OlderThan must be set.
type PruneOptions struct {
	// Keep leaves the newest N runs in place, whatever their age.
	Keep int
	// OlderThan removes runs whose end (or start, while they have no end)
	// is further in the past than this.
	OlderThan time.Duration
	// DryRun selects the runs without deleting anything.
	DryRun bool
	// Now is the reference time for OlderThan; zero means time.Now().
	Now time.Time
}

// Prune deletes run directories and returns the ids it removed, newest
// first. A run is removed when it falls outside the Keep newest ones or is
// older than OlderThan; either condition alone is enough. A run still marked
// running is never removed by Keep alone, since it may be live: only an age
// past OlderThan removes it, on the grounds that a run that old is a crash
// that never wrote its end, not a run still going. Directories without a
// readable run.json are left alone.
func Prune(runsDir string, opts PruneOptions) ([]string, error) {
	if opts.Keep <= 0 && opts.OlderThan <= 0 {
		return nil, fmt.Errorf("runstore: prune needs a keep count or an age")
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	runs, err := List(runsDir)
	if err != nil {
		return nil, err
	}

	var removed []string
	for i, state := range runs {
		beyondKeep := opts.Keep > 0 && i >= opts.Keep
		tooOld := opts.OlderThan > 0 && now.Sub(runEnd(state)) > opts.OlderThan
		if state.Status == StatusRunning && !tooOld {
			continue
		}
		if !beyondKeep && !tooOld {
			continue
		}
		if err := ValidateID(state.ID); err != nil {
			return removed, err
		}
		if !opts.DryRun {
			if err := os.RemoveAll(filepath.Join(runsDir, state.ID)); err != nil {
				return removed, fmt.Errorf("runstore: prune %s: %w", state.ID, err)
			}
		}
		removed = append(removed, state.ID)
	}
	return removed, nil
}

// runEnd is the time a run stopped, or started while it has not stopped.
func runEnd(state *RunState) time.Time {
	if state.FinishedAt != nil {
		return *state.FinishedAt
	}
	return state.StartedAt
}
