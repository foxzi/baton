package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/foxzi/baton/internal/exitcode"
	"github.com/foxzi/baton/internal/runstore"
)

const runsUsage = `Usage:
  baton runs list [--runs-dir DIR] [-n 20]
  baton runs show <run-id> [--runs-dir DIR]
  baton runs logs <run-id> [--runs-dir DIR] [--step ID]

Options:
  --runs-dir DIR   Directory holding the runs (default: $BATON_RUNS_DIR or ./runs)
  -n N             Number of runs to list (default 20)
  --step ID        Show the logs of this step only
`

// runsCmd implements `baton runs` (spec section 11).
func runsCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, runsUsage)
		return exitcode.Config
	}
	switch args[0] {
	case "list":
		return runsListCmd(args[1:])
	case "show":
		return runsShowCmd(args[1:])
	case "logs":
		return runsLogsCmd(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "baton: unknown runs subcommand %q\n\n%s", args[0], runsUsage)
		return exitcode.Config
	}
}

// runsDirFlag resolves --runs-dir, then BATON_RUNS_DIR, then ./runs.
func runsDirFlag(dir string) string {
	if dir != "" {
		return dir
	}
	if env := os.Getenv("BATON_RUNS_DIR"); env != "" {
		return env
	}
	return "runs"
}

func runsListCmd(args []string) int {
	var (
		runsDir string
		limit   int
	)
	flags := flag.NewFlagSet("runs list", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { fmt.Fprint(os.Stderr, runsUsage) }
	flags.StringVar(&runsDir, "runs-dir", "", "run directory root")
	flags.IntVar(&limit, "n", 20, "number of runs to list")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return exitcode.Config
	}
	if len(positional) != 0 {
		fmt.Fprint(os.Stderr, runsUsage)
		return exitcode.Config
	}

	runs, err := runstore.List(runsDirFlag(runsDir))
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}
	if limit > 0 && len(runs) > limit {
		runs = runs[:limit]
	}
	for _, state := range runs {
		fmt.Printf("%-24s %-8s %-10s %s%s\n",
			state.ID,
			state.Status,
			runDuration(state),
			state.Name,
			failedSuffix(state))
	}
	return exitcode.OK
}

func runsShowCmd(args []string) int {
	var runsDir string
	flags := flag.NewFlagSet("runs show", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { fmt.Fprint(os.Stderr, runsUsage) }
	flags.StringVar(&runsDir, "runs-dir", "", "run directory root")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return exitcode.Config
	}
	if len(positional) != 1 {
		fmt.Fprint(os.Stderr, runsUsage)
		return exitcode.Config
	}

	dir := runsDirFlag(runsDir)
	state, err := runstore.ReadRun(dir, positional[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}

	fmt.Printf("run:      %s\n", state.ID)
	fmt.Printf("scenario: %s\n", state.Name)
	fmt.Printf("status:   %s\n", state.Status)
	fmt.Printf("started:  %s\n", state.StartedAt.Format(time.RFC3339))
	if duration := runDuration(state); duration != "" {
		fmt.Printf("duration: %s\n", duration)
	}
	if state.ResumeOf != "" {
		fmt.Printf("resume of: %s\n", state.ResumeOf)
	}
	if state.CostUSD != nil {
		fmt.Printf("cost:     $%.4f\n", *state.CostUSD)
	}
	if state.Error != nil {
		fmt.Printf("error:    %s: %s (step %s)\n", state.Error.Class, state.Error.Message, state.FailedStep)
	}

	ids := state.StepIDs()
	if len(ids) > 0 {
		fmt.Println("steps:")
		for _, id := range ids {
			step := state.Steps[id]
			line := fmt.Sprintf("  %-20s %-8s %s", id, step.Status, stepDuration(step))
			if step.Attempts > 1 {
				line += fmt.Sprintf(" attempts=%d", step.Attempts)
			}
			if step.CacheHit {
				line += " cached"
			}
			if step.FallbackUsed {
				line += " fallback"
			}
			if step.Error != nil {
				line += fmt.Sprintf(" %s: %s", step.Error.Class, step.Error.Message)
			}
			fmt.Println(strings.TrimRight(line, " "))
		}
	}

	report, err := runstore.ReadCost(dir, state.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}
	if report != nil && len(report.Steps) > 0 {
		fmt.Println("cost by step:")
		for _, entry := range report.Steps {
			fmt.Printf("  %-20s %-24s in=%d out=%d %s\n",
				entry.Step, entry.Model, entry.InputTokens, entry.OutputTokens, usd(entry.CostUSD))
		}
	}
	return exitcode.OK
}

func runsLogsCmd(args []string) int {
	var (
		runsDir string
		stepID  string
	)
	flags := flag.NewFlagSet("runs logs", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { fmt.Fprint(os.Stderr, runsUsage) }
	flags.StringVar(&runsDir, "runs-dir", "", "run directory root")
	flags.StringVar(&stepID, "step", "", "step id")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return exitcode.Config
	}
	if len(positional) != 1 {
		fmt.Fprint(os.Stderr, runsUsage)
		return exitcode.Config
	}

	dir := runsDirFlag(runsDir)
	state, err := runstore.ReadRun(dir, positional[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}

	ids := state.StepIDs()
	if stepID != "" {
		if _, ok := state.Steps[stepID]; !ok {
			fmt.Fprintf(os.Stderr, "baton: run %s has no step %q\n", state.ID, stepID)
			return exitcode.Config
		}
		ids = []string{stepID}
	}

	stepsDir := filepath.Join(dir, state.ID, "steps")
	for _, id := range ids {
		for _, name := range []string{"stdout.log", "stderr.log"} {
			data, err := os.ReadFile(filepath.Join(stepsDir, filepath.FromSlash(id), name))
			if err != nil || len(data) == 0 {
				continue
			}
			fmt.Printf("=== %s %s\n", id, name)
			os.Stdout.Write(data)
			if !strings.HasSuffix(string(data), "\n") {
				fmt.Println()
			}
		}
	}
	return exitcode.OK
}

// runDuration is the wall time of a finished run, or an empty string while it
// is still running.
func runDuration(state *runstore.RunState) string {
	if state.FinishedAt == nil {
		return ""
	}
	return state.FinishedAt.Sub(state.StartedAt).Round(time.Millisecond).String()
}

func stepDuration(step *runstore.StepState) string {
	if step.FinishedAt == nil {
		return ""
	}
	return step.FinishedAt.Sub(step.StartedAt).Round(time.Millisecond).String()
}

func failedSuffix(state *runstore.RunState) string {
	if state.FailedStep == "" {
		return ""
	}
	return fmt.Sprintf(" (failed at %s)", state.FailedStep)
}

// usd formats a price that may be unknown, as when the model is missing from
// the pricing table.
func usd(value *float64) string {
	if value == nil {
		return "$?"
	}
	return fmt.Sprintf("$%.4f", *value)
}
