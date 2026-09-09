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

	"github.com/foxzi/baton/internal/cache"
	"github.com/foxzi/baton/internal/config"
	"github.com/foxzi/baton/internal/engine"
	"github.com/foxzi/baton/internal/exitcode"
	"github.com/foxzi/baton/internal/expr"
	"github.com/foxzi/baton/internal/notify"
	"github.com/foxzi/baton/internal/runstore"
	"github.com/foxzi/baton/internal/scenario"
	"github.com/foxzi/baton/internal/secrets"
	"github.com/foxzi/baton/internal/values"
)

const runUsage = `Usage: baton run <scenario.yaml> [options]

Options:
  -i key=value      Set a scenario input; repeatable
  --input-file FILE  Read inputs from a JSON file
  --run-id ID        Use this run id instead of a generated one
  --runs-dir DIR     Write the run directory here (default: runs/ next to the scenario)
  --workspace DIR    Working directory of run steps (default: the scenario directory)
  --config FILE      Global configuration file; repeatable, later files win
  --cache-dir DIR    Step cache directory (default: cache/ next to the runs directory)
  --no-cache         Ignore cached step results; fresh results are still stored
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
		configs   stringList
		cacheDir  string
		noCache   bool
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
	flags.Var(&configs, "config", "global configuration file")
	flags.StringVar(&cacheDir, "cache-dir", "", "step cache directory")
	flags.BoolVar(&noCache, "no-cache", false, "ignore cached step results")
	flags.BoolVar(&dryRun, "dry-run", false, "print the plan without executing")
	flags.BoolVar(&asJSON, "json", false, "print events as JSONL on stdout")
	flags.BoolVar(&verbose, "v", false, "print every event")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return flagsExitCode(err)
	}
	if len(positional) != 1 {
		fmt.Fprint(os.Stderr, runUsage)
		return exitcode.Config
	}

	fileInputs, err := readInputFile(inputFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}

	return execute(runRequest{
		scenarioPath: positional[0],
		inputPairs:   inputs,
		inputs:       fileInputs,
		runID:        runID,
		runsDir:      runsDir,
		workspace:    workspace,
		configs:      configs,
		cacheDir:     cacheDir,
		noCache:      noCache,
		dryRun:       dryRun,
		asJSON:       asJSON,
		verbose:      verbose,
	})
}

// runRequest is one execution of a scenario, as `baton run` and
// `baton resume` both need it.
type runRequest struct {
	scenarioPath string
	// inputPairs are -i key=value arguments; inputs is a typed set, from
	// --input-file or from the run.json of a resumed run.
	inputPairs []string
	inputs     map[string]any
	runID      string
	runsDir    string
	workspace  string
	configs    []string
	cacheDir   string
	noCache    bool
	dryRun     bool
	asJSON     bool
	verbose    bool
	// resume replays the steps a previous run already finished
	// (section 10.4).
	resume   map[string]expr.Step
	resumeOf string
}

// execute validates the scenario, prepares the run directory and runs it.
func execute(req runRequest) int {
	path := req.scenarioPath
	scn, err := scenario.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}

	result := scenario.Validate(scn)
	if !result.OK() {
		for _, problem := range result.Errors {
			fmt.Fprintf(os.Stderr, "error: %s\n", problem.Format(path))
		}
		fmt.Fprintf(os.Stderr, "%s: %s\n", path, plural(len(result.Errors), "error"))
		return exitcode.Config
	}

	bound, err := scenario.BindInputs(scn.Inputs, req.inputPairs, req.inputs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}

	cfg, err := config.Load(req.configs...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}

	// A model with no price is a warning of the pair scenario+configuration,
	// so it joins the scenario warnings once the configuration is loaded.
	result.Warnings = append(result.Warnings, pricingWarnings(scn, cfg)...)
	for _, warning := range result.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", warning.Format(path))
	}

	baseDir := filepath.Dir(path)
	secretStore, err := secrets.Resolve(scn.Secrets, baseDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}

	// Provider keys join the redactor but not the scenario namespace: a
	// template must not be able to print one (section 13).
	providerKeys, err := cfg.ResolveProviderKeys(baseDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}
	secretStore = secretStore.WithHidden(secretValues(providerKeys)...)

	// The secrets of the global packs are the same: a scenario must not be
	// able to name one, but every value they carry is masked.
	apiSecrets, err := cfg.ResolveAPISecrets(baseDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}
	secretStore = secretStore.WithHidden(secretValues(apiSecrets)...)

	// A webhook url is a secret too: it goes to the redactor, and the
	// engine only sees the channel it actually sends to.
	channels, channelErrors := notify.ResolveAll(cfg.Notify, baseDir)
	secretStore = secretStore.WithHidden(channelURLs(channels)...)

	if req.dryRun {
		printPlan(scn, bound, secretStore)
		return exitcode.OK
	}

	runsDir := req.runsDir
	if runsDir == "" {
		runsDir = os.Getenv("BATON_RUNS_DIR")
	}
	if runsDir == "" {
		runsDir = filepath.Join(baseDir, "runs")
	}
	runID := req.runID
	if runID == "" {
		runID = runstore.NewID(time.Now())
	}
	// The cache lives next to the runs directory (section 10.3).
	cacheDir := req.cacheDir
	if cacheDir == "" {
		cacheDir = filepath.Join(filepath.Dir(filepath.Clean(runsDir)), "cache")
	}

	store, err := runstore.Create(runsDir, runID, secretStore.Redactor())
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}
	defer store.Close()

	eng, err := engine.New(engine.Options{
		Scenario:      scn,
		Inputs:        bound,
		Secrets:       secretStore,
		Store:         store,
		Cache:         cache.Open(cacheDir),
		NoCache:       req.noCache,
		Resume:        req.resume,
		ResumeOf:      req.resumeOf,
		Workspace:     req.workspace,
		Config:        cfg,
		Channels:      channels,
		ChannelErrors: channelErrors,
		Stdout:        os.Stdout,
		ProviderKeys:  providerKeys,
		APISecrets:    apiSecrets,
		Observer:      observer(req.asJSON, req.verbose),
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

	workspace := req.workspace
	if workspace == "" {
		workspace = baseDir
	}
	printRunSummary(scn, runsDir, runID, outcome, workspace, store.Dir())

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
		"step_fallback", "cache_hit", "on_failure_failed", "store_error":
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

// channelURLs is the webhook url of every resolved channel, for the
// redactor.
func channelURLs(channels map[string]notify.Channel) []values.Secret {
	out := make([]values.Secret, 0, len(channels))
	for _, channel := range channels {
		if !channel.URL.IsZero() {
			out = append(out, channel.URL)
		}
	}
	return out
}

// secretValues is the values of a provider key map, for the redactor.
func secretValues(keys map[string]values.Secret) []values.Secret {
	out := make([]values.Secret, 0, len(keys))
	for _, secret := range keys {
		out = append(out, secret)
	}
	return out
}

// printRunSummary is the compact human-readable report at the end of `run`
// and `resume` (both go through execute): status and duration always, cost
// only when a step actually priced one, written artifacts only when a file
// step actually wrote one, and on failure a ready-to-paste resume command.
// --runs-dir is always spelled out because the CLI default depends on the
// current directory, which need not be the one this run used.
func printRunSummary(scn *scenario.Scenario, runsDir, runID string, outcome *engine.Result, workspace, runDir string) {
	fmt.Fprintf(os.Stderr, "run %s: %s in %s\n", runID, outcome.Status, outcome.Duration.Round(time.Millisecond))

	if report, err := runstore.ReadCost(runsDir, runID); err == nil && report != nil && report.TotalUSD != nil {
		fmt.Fprintf(os.Stderr, "cost: %s\n", usd(report.TotalUSD))
	}

	if paths := fileArtifacts(scn, runsDir, runID, workspace); len(paths) > 0 {
		fmt.Fprintln(os.Stderr, "artifacts:")
		for _, path := range paths {
			fmt.Fprintf(os.Stderr, "  %s\n", path)
		}
	}

	if outcome.Error != nil {
		fmt.Fprintf(os.Stderr, "failed step %s: %s: %s\n", outcome.FailedStep, outcome.Error.Class, outcome.Error.Message)
		fmt.Fprintf(os.Stderr, "resume: baton resume %s --runs-dir %s\n", shellQuote(runID), shellQuote(runsDir))
	}

	fmt.Fprintf(os.Stderr, "run directory: %s\n", runDir)
	fmt.Fprintf(os.Stderr, "details: baton runs show %s --runs-dir %s\n", shellQuote(runID), shellQuote(runsDir))
	fmt.Fprintf(os.Stderr, "logs:    baton runs logs %s --runs-dir %s\n", shellQuote(runID), shellQuote(runsDir))
}

// fileArtifacts is the paths a top-level file write/append step actually
// wrote, read back from its own output.json rather than guessed from the
// scenario: a step that failed, was skipped or never ran leaves nothing to
// report.
func fileArtifacts(scn *scenario.Scenario, runsDir, runID, workspace string) []string {
	var paths []string
	for i := range scn.Steps {
		step := &scn.Steps[i]
		if step.Kind() != scenario.KindFile || step.File == nil {
			continue
		}
		op, _ := step.File.Op()
		if op != scenario.FileOpWrite && op != scenario.FileOpAppend {
			continue
		}
		data, err := os.ReadFile(filepath.Join(runsDir, runID, "steps", step.ID, "output.json"))
		if err != nil {
			continue
		}
		var output struct {
			Status string `json:"status"`
			Result struct {
				Path string `json:"path"`
			} `json:"result"`
		}
		if err := json.Unmarshal(data, &output); err != nil {
			continue
		}
		if output.Status != "success" || output.Result.Path == "" {
			continue
		}
		paths = append(paths, filepath.Join(workspace, filepath.FromSlash(output.Result.Path)))
	}
	return paths
}
