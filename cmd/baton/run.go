package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/foxzi/baton/internal/engine"
	"github.com/foxzi/baton/internal/exitcode"
	"github.com/foxzi/baton/internal/runstore"
	"github.com/foxzi/baton/internal/scenario"
	"github.com/foxzi/baton/internal/secrets"
)

const runUsage = `Usage: baton run <scenario.yaml> [options]

Options:
  -i key=value      Set a scenario input; repeatable
  --input-file FILE  Read inputs from a JSON file
  --run-id ID        Use this run id instead of a generated one
  --runs-dir DIR     Write the run directory here (default: runs/ next to the scenario)
  --workspace DIR    Working directory of run steps (default: the scenario directory)
  --dry-run          Validate, resolve secrets and print the plan without executing
  --json             Print events as JSONL on stdout; the human log stays on stderr
  -v                 Print every event, not just step boundaries

Exit codes: 0 success, 1 step failure, 2 assert, 3 configuration, 4 budget,
130 interrupted.
`

// stringList collects a repeatable flag.
type stringList []string

func (l *stringList) String() string { return fmt.Sprint(*l) }

func (l *stringList) Set(value string) error {
	*l = append(*l, value)
	return nil
}

// runCmd implements `baton run` (spec section 11).
//
// --dry-run stops after validation, input binding and secret resolution and
// prints the plan; rendering the inputs of the first step, which the spec also
// asks for, arrives with the step cache.
func runCmd(args []string) int {
	var (
		inputs    stringList
		inputFile string
		runID     string
		runsDir   string
		workspace string
		dryRun    bool
		asJSON    bool
		verbose   bool
	)

	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { fmt.Fprint(os.Stderr, runUsage) }
	flags.Var(&inputs, "i", "scenario input as key=value")
	flags.StringVar(&inputFile, "input-file", "", "JSON file with inputs")
	flags.StringVar(&runID, "run-id", "", "run id")
	flags.StringVar(&runsDir, "runs-dir", "", "run directory root")
	flags.StringVar(&workspace, "workspace", "", "working directory of run steps")
	flags.BoolVar(&dryRun, "dry-run", false, "print the plan without executing")
	flags.BoolVar(&asJSON, "json", false, "print events as JSONL on stdout")
	flags.BoolVar(&verbose, "v", false, "print every event")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return exitcode.Config
	}
	if len(positional) != 1 {
		fmt.Fprint(os.Stderr, runUsage)
		return exitcode.Config
	}

	path := positional[0]
	scn, err := scenario.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}

	result := scenario.Validate(scn)
	for _, warning := range result.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", warning)
	}
	if !result.OK() {
		for _, problem := range result.Errors {
			fmt.Fprintf(os.Stderr, "error: %s\n", problem)
		}
		fmt.Fprintf(os.Stderr, "%s: %s\n", path, plural(len(result.Errors), "error"))
		return exitcode.Config
	}

	fileInputs, err := readInputFile(inputFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}
	bound, err := scenario.BindInputs(scn.Inputs, inputs, fileInputs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}

	baseDir := filepath.Dir(path)
	secretStore, err := secrets.Resolve(scn.Secrets, baseDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}

	if dryRun {
		printPlan(scn, bound, secretStore)
		return exitcode.OK
	}

	if runsDir == "" {
		runsDir = os.Getenv("BATON_RUNS_DIR")
	}
	if runsDir == "" {
		runsDir = filepath.Join(baseDir, "runs")
	}
	if runID == "" {
		runID = runstore.NewID(time.Now())
	}
	store, err := runstore.Create(runsDir, runID, secretStore.Redactor())
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}
	defer store.Close()

	eng, err := engine.New(engine.Options{
		Scenario:  scn,
		Inputs:    bound,
		Secrets:   secretStore,
		Store:     store,
		Workspace: workspace,
		Observer:  observer(asJSON, verbose),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	outcome, err := eng.Run(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Failure
	}

	fmt.Fprintf(os.Stderr, "run %s: %s in %s\n", runID, outcome.Status, outcome.Duration.Round(time.Millisecond))
	if outcome.Error != nil {
		fmt.Fprintf(os.Stderr, "failed step %s: %s: %s\n", outcome.FailedStep, outcome.Error.Class, outcome.Error.Message)
	}
	fmt.Fprintf(os.Stderr, "run directory: %s\n", store.Dir())

	if errors.Is(ctx.Err(), context.Canceled) {
		return exitcode.Interrupted
	}
	return outcome.ExitCode()
}

// observer prints events as they happen (section 11).
func observer(asJSON, verbose bool) func(engine.Event) {
	encoder := json.NewEncoder(os.Stdout)
	return func(event engine.Event) {
		if asJSON {
			payload := map[string]any{"type": event.Type}
			if event.Step != "" {
				payload["step"] = event.Step
			}
			if event.Message != "" {
				payload["message"] = event.Message
			}
			for key, value := range event.Fields {
				payload[key] = value
			}
			_ = encoder.Encode(payload)
		}
		if !verbose && !interesting(event.Type) {
			return
		}
		line := event.Type
		if event.Step != "" {
			line = fmt.Sprintf("[%s] %s", event.Step, event.Type)
		}
		if event.Message != "" {
			line += ": " + event.Message
		}
		if duration, ok := event.Fields["duration"]; ok {
			line += fmt.Sprintf(" (%v)", duration)
		}
		fmt.Fprintln(os.Stderr, line)
	}
}

// interesting selects the events the human log shows without -v.
func interesting(eventType string) bool {
	switch eventType {
	case "step_started", "step_finished", "step_failed", "step_skipped", "step_retry",
		"step_fallback", "on_failure_failed", "store_error":
		return true
	default:
		return false
	}
}

// readInputFile decodes --input-file.
func readInputFile(path string) (map[string]any, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var values map[string]any
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return values, nil
}

// printPlan is the output of --dry-run.
func printPlan(scn *scenario.Scenario, inputs map[string]any, store *secrets.Store) {
	fmt.Printf("scenario: %s\n", scn.Name)
	fmt.Println("inputs:")
	for _, name := range sortedInputNames(scn.Inputs) {
		if value, ok := inputs[name]; ok {
			fmt.Printf("  %s = %v\n", name, value)
			continue
		}
		fmt.Printf("  %s = <unset>\n", name)
	}
	if names := store.Names(); len(names) > 0 {
		fmt.Println("secrets resolved:")
		for _, name := range names {
			fmt.Printf("  %s\n", name)
		}
	}
	fmt.Println("plan:")
	for i := range scn.Steps {
		step := &scn.Steps[i]
		line := fmt.Sprintf("  %d. %s (%s)", i+1, step.ID, step.Kind())
		if step.When != "" {
			line += fmt.Sprintf(" when %s", step.When)
		}
		if step.OnError != scenario.OnErrorUnset {
			line += fmt.Sprintf(" on_error %s", step.OnError)
		}
		fmt.Println(line)
	}
}

func sortedInputNames(declared map[string]scenario.Input) []string {
	names := make([]string, 0, len(declared))
	for name := range declared {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
