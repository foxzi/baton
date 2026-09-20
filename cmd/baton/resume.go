package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/foxzi/baton/internal/engine"
	"github.com/foxzi/baton/internal/exitcode"
	"github.com/foxzi/baton/internal/runstore"
)

const resumeUsage = `Usage: baton resume <run-id> [options]

Options:
  --runs-dir DIR   Directory holding the runs (default: $BATON_RUNS_DIR or ./runs)
  --json           Print events as JSONL on stdout; the human log stays on stderr
  -v               Print every event, not just step boundaries
  --approve        Answer the gate a waiting run stopped at: decision approved
  --reject         Answer the gate a waiting run stopped at: decision rejected
  --reason TEXT    Free-text reason recorded with the decision

The steps the original run finished are replayed from its run directory; the
run continues at the step that failed. Secrets are resolved again. The new run
keeps the id of the original with an -rN suffix.

A run that stopped at a gate (status waiting) needs --approve or --reject.
Either continues the run; the gate's result.decision tells the following
steps which way to branch.
`

// resumeSuffix matches the -rN suffix of an already resumed run.
var resumeSuffix = regexp.MustCompile(`-r([0-9]+)$`)

// resumeCmd implements `baton resume` (spec sections 10.4 and 11).
func resumeCmd(args []string) int {
	var (
		runsDir string
		asJSON  bool
		verbose bool
		approve bool
		reject  bool
		reason  string
	)

	flags := flag.NewFlagSet("resume", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { fmt.Fprint(os.Stderr, resumeUsage) }
	flags.StringVar(&runsDir, "runs-dir", "", "run directory root")
	flags.BoolVar(&asJSON, "json", false, "print events as JSONL on stdout")
	flags.BoolVar(&verbose, "v", false, "print every event")
	flags.BoolVar(&approve, "approve", false, "approve the gate the run waits at")
	flags.BoolVar(&reject, "reject", false, "reject the gate the run waits at")
	flags.StringVar(&reason, "reason", "", "reason recorded with the decision")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return flagsExitCode(err)
	}
	if len(positional) != 1 {
		fmt.Fprint(os.Stderr, resumeUsage)
		return exitcode.Config
	}
	if approve && reject {
		fmt.Fprintln(os.Stderr, "baton: --approve and --reject exclude each other")
		return exitcode.Config
	}

	runsDir = runsDirFlag(runsDir)
	runID := positional[0]
	state, err := runstore.ReadRun(runsDir, runID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}
	if state.Status == runstore.StatusSuccess {
		fmt.Fprintf(os.Stderr, "baton: run %s succeeded, there is nothing to resume\n", runID)
		return exitcode.Config
	}
	if state.Scenario == "" {
		fmt.Fprintf(os.Stderr, "baton: run %s does not record its scenario, it cannot be resumed\n", runID)
		return exitcode.Config
	}

	var decision *engine.Decision
	switch {
	case state.Status == runstore.StatusWaiting && !approve && !reject:
		fmt.Fprintf(os.Stderr, "baton: run %s waits at gate %s: resume it with --approve or --reject\n", runID, state.WaitingStep)
		return exitcode.Config
	case state.Status != runstore.StatusWaiting && (approve || reject):
		fmt.Fprintf(os.Stderr, "baton: run %s is not waiting at a gate, --approve and --reject do not apply\n", runID)
		return exitcode.Config
	case approve || reject:
		gate := state.Steps[state.WaitingStep]
		if gate == nil {
			fmt.Fprintf(os.Stderr, "baton: run %s does not record gate %s, it cannot be answered\n", runID, state.WaitingStep)
			return exitcode.Config
		}
		decision = &engine.Decision{
			Step:       state.WaitingStep,
			Definition: gate.Definition,
			Approved:   approve,
			Reason:     reason,
		}
	}

	resume, err := engine.LoadResume(filepath.Join(runsDir, runID), state)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}

	newID, err := nextResumeID(runsDir, runID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}
	fmt.Fprintf(os.Stderr, "resuming %s as %s: %s replayed\n", runID, newID, plural(len(resume), "step"))

	return execute(runRequest{
		scenarioPath: state.Scenario,
		inputs:       state.Inputs,
		runID:        newID,
		runsDir:      runsDir,
		resume:       resume,
		resumeOf:     runID,
		decision:     decision,
		asJSON:       asJSON,
		verbose:      verbose,
	})
}

// nextResumeID is the id of a run that continues runID: the id of the
// original with the next free -rN suffix (section 10.4).
func nextResumeID(runsDir, runID string) (string, error) {
	base := runID
	if match := resumeSuffix.FindStringSubmatch(runID); match != nil {
		base = runID[:len(runID)-len(match[0])]
	}
	for attempt := 1; attempt <= 1000; attempt++ {
		candidate := base + "-r" + strconv.Itoa(attempt)
		if _, err := os.Stat(filepath.Join(runsDir, candidate)); os.IsNotExist(err) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("run %s has been resumed too many times", base)
}
