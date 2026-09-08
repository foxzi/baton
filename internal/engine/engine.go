// Package engine executes a validated scenario (docs/ru/spec.md, sections 9
// and 10).
//
// The engine owns the run: it evaluates when: conditions, executes steps in
// order, applies retries and on_error, records everything in the run
// directory and finally runs on_failure. Step kinds that v1 does not execute
// yet fail with the config class, so a scenario using them stops with a clear
// message instead of being silently skipped.
package engine

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/foxzi/baton/internal/agent"
	"github.com/foxzi/baton/internal/cache"
	"github.com/foxzi/baton/internal/config"
	"github.com/foxzi/baton/internal/expr"
	"github.com/foxzi/baton/internal/httpx"
	"github.com/foxzi/baton/internal/notify"
	"github.com/foxzi/baton/internal/provider"
	"github.com/foxzi/baton/internal/runstore"
	"github.com/foxzi/baton/internal/scenario"
	"github.com/foxzi/baton/internal/secrets"
	"github.com/foxzi/baton/internal/tmpl"
	"github.com/foxzi/baton/internal/values"
)

// onFailureTimeout is the per-step timeout of on_failure steps (section 9.3).
const onFailureTimeout = 60 * time.Second

// Options configures a run.
type Options struct {
	// Scenario is a scenario that already passed validation.
	Scenario *scenario.Scenario
	// Inputs are the bound scenario inputs (scenario.BindInputs).
	Inputs map[string]any
	// Secrets holds the resolved secrets; it may be nil when the scenario
	// declares none.
	Secrets *secrets.Store
	// Config is the global configuration (section 12). It may be nil, in
	// which case a scenario that needs a provider or a channel fails with
	// the config class.
	Config *config.Config
	// Channels are the notify channels of the configuration with their
	// secrets already read, by channel name (section 12). ChannelErrors
	// holds the channels that could not be resolved, so that a run only
	// fails when it sends to one of them.
	Channels      map[string]notify.Channel
	ChannelErrors map[string]error
	// ProviderKeys are the resolved api keys of the providers, by provider
	// name. The caller resolves them so that a key is part of the redactor
	// before any provider can echo it back (section 13).
	ProviderKeys map[string]values.Secret
	// Store is the run directory to record the run in.
	Store *runstore.Store
	// Cache holds step results between runs (section 10.3). It may be nil,
	// in which case nothing is cached.
	Cache *cache.Cache
	// Resume holds the outputs of the steps a previous run already finished,
	// by step path (section 10.4). Those steps are replayed instead of
	// executed; ResumeOf names the run they come from.
	Resume map[string]expr.Step
	// ResumeOf is the id of the run this one continues, recorded in
	// run.json (section 10.4).
	ResumeOf string
	// NoCache disables reading from the cache; writing continues, so that
	// the next run can reuse the fresh results (section 10.3).
	NoCache bool
	// Workspace is the default working directory of run steps. It defaults
	// to the directory of the scenario file.
	Workspace string
	// StateDir holds the state files an agent step reads and writes
	// (section 7.6). It defaults to the state/ directory next to the run
	// directory root.
	StateDir string
	// FetchClient performs the requests of the agent fetch tool, for tests.
	// A nil client gets the one the tool builds itself.
	FetchClient *http.Client
	// Stdout is where the built-in stdout notify channel writes. It may be
	// nil, in which case such a message is dropped.
	Stdout io.Writer
	// Observer receives events as they happen, for the human readable log.
	// It may be nil.
	Observer func(Event)
	// Now supplies the clock, for tests. It defaults to time.Now.
	Now func() time.Time
}

// Event is something worth reporting to the user or to events.jsonl.
type Event struct {
	Type    string
	Step    string
	Message string
	Fields  map[string]any
}

// Result is the outcome of a run.
type Result struct {
	Status     string
	FailedStep string
	Error      *runstore.RunError
	Duration   time.Duration
}

// ExitCode is the process exit code for the result (section 9.5).
func (r *Result) ExitCode() int {
	if r.Error == nil {
		return ExitCode("")
	}
	return ExitCode(r.Error.Class)
}

// Engine runs one scenario once.
type Engine struct {
	opts     Options
	renderer *tmpl.Renderer
	http     *httpx.Client
	apis     map[string]*httpx.API
	// dir is the directory of the scenario file; pack sources and template
	// paths resolve against it.
	dir string
	// mu guards the lazily built caches below, which foreach bodies may
	// reach concurrently.
	mu        *sync.Mutex
	providers map[string]provider.Provider
	// agents is the machinery agent steps share: the gateway, the engines
	// and the prepared workspace (section 3.6).
	agents *agentRuntime
	cost   *costLedger
	steps  map[string]expr.Step
	// hits records step paths served from the cache, so that runStep can
	// flag them in run.json.
	hits map[string]bool
	// vars are extra template variables of the enclosing construct: the
	// foreach item under its as name. They are per body, never shared.
	vars map[string]any

	// iter is the result of the previous until iteration, empty outside an
	// until body (section 3.8).
	iter      map[string]any
	state     *runstore.RunState
	startedAt time.Time
	// failure is the error that stopped the run, if any.
	failure *Error
	// failedStep is the id of the step that produced failure.
	failedStep string
}

// New checks the options and prepares an engine.
func New(opts Options) (*Engine, error) {
	if opts.Scenario == nil {
		return nil, errors.New("engine: scenario is required")
	}
	if opts.Store == nil {
		return nil, errors.New("engine: run store is required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	baseDir := filepath.Dir(opts.Scenario.Path)
	if baseDir == "" {
		baseDir = "."
	}
	if opts.Workspace == "" {
		opts.Workspace = baseDir
	}
	if opts.Inputs == nil {
		opts.Inputs = map[string]any{}
	}
	return &Engine{
		opts:      opts,
		dir:       baseDir,
		renderer:  tmpl.NewRenderer(baseDir),
		mu:        &sync.Mutex{},
		cost:      &costLedger{},
		steps:     map[string]expr.Step{},
		hits:      map[string]bool{},
		apis:      map[string]*httpx.API{},
		providers: map[string]provider.Provider{},
		agents:    &agentRuntime{engines: map[string]agent.Engine{}},
	}, nil
}

// Run executes the scenario and returns its outcome. The error return is
// reserved for problems with the run directory itself; a failed scenario is
// reported through Result.
func (e *Engine) Run(ctx context.Context) (*Result, error) {
	scn := e.opts.Scenario
	e.startedAt = e.opts.Now()
	e.state = &runstore.RunState{
		SchemaVersion: runstore.SchemaVersion,
		ID:            e.opts.Store.ID(),
		Name:          scn.Name,
		Status:        runstore.StatusRunning,
		StartedAt:     e.startedAt,
		Inputs:        e.opts.Inputs,
		Scenario:      scenarioPath(scn.Path),
		Steps:         map[string]*runstore.StepState{},
		ResumeOf:      e.opts.ResumeOf,
	}
	if err := e.opts.Store.WriteRun(e.state); err != nil {
		return nil, err
	}
	e.emit(Event{Type: "run_started", Message: scn.Name, Fields: map[string]any{"id": e.state.ID}})

	runCtx, cancel := e.budgetContext(ctx)
	defer cancel()
	defer e.closeAgents()

	for i := range scn.Steps {
		step := &scn.Steps[i]
		stop := e.runStep(runCtx, step, step.ID)
		if stop {
			break
		}
	}

	e.finish(ctx)
	if err := e.opts.Store.WriteRun(e.state); err != nil {
		return nil, err
	}

	result := &Result{
		Status:     e.state.Status,
		FailedStep: e.state.FailedStep,
		Error:      e.state.Error,
		Duration:   e.opts.Now().Sub(e.startedAt),
	}
	e.emit(Event{Type: "run_finished", Message: result.Status, Fields: map[string]any{
		"duration": result.Duration.String(),
	}})
	return result, nil
}

// budgetContext applies budget.time to the run (section 3.1).
func (e *Engine) budgetContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if limit := e.opts.Scenario.Budget.Time.Duration(); limit > 0 {
		return context.WithTimeout(ctx, limit)
	}
	return context.WithCancel(ctx)
}

// finish records the terminal state of the run and executes on_failure.
func (e *Engine) finish(ctx context.Context) {
	finishedAt := e.opts.Now()
	e.state.FinishedAt = &finishedAt
	defer e.writeCost()

	if e.failure == nil {
		e.state.Status = runstore.StatusSuccess
		return
	}

	e.state.Status = runstore.StatusFailed
	e.state.FailedStep = e.failedStep
	e.state.Error = &runstore.RunError{Class: e.failure.Class, Message: e.failure.Msg}

	// A false assert is not a failure class, so on_failure does not run
	// (section 9.3).
	if e.failure.Class == ClassAssert {
		return
	}
	e.runOnFailure(ctx)
}

// runStep executes one step of the main list and reports whether the run must
// stop.
func (e *Engine) runStep(ctx context.Context, step *scenario.Step, path string) bool {
	if step.ID == "" {
		e.fail(path, errorf(ClassConfig, "step without id"))
		return true
	}

	if e.resumeStep(step, path) {
		return false
	}

	run, err := e.shouldRun(step)
	if err != nil {
		e.fail(step.ID, err)
		return true
	}
	if !run {
		e.record(step.ID, &runstore.StepState{
			Status:    runstore.StatusSkipped,
			StartedAt: e.opts.Now(),
		})
		e.steps[step.ID] = expr.Step{Status: expr.StatusSkipped}
		e.emit(Event{Type: "step_skipped", Step: step.ID, Message: "when is false"})
		return false
	}

	state := &runstore.StepState{Status: runstore.StatusRunning, StartedAt: e.opts.Now()}
	e.emit(Event{Type: "step_started", Step: step.ID})

	out, stepErr := e.executeBody(ctx, step, path, state)

	finishedAt := e.opts.Now()
	state.FinishedAt = &finishedAt
	state.CacheHit = e.takeCacheHit(path)
	if step.Run != nil {
		code := out.ExitCode
		state.ExitCode = &code
	}

	if stepErr == nil {
		state.Status = runstore.StatusSuccess
		out.Status = expr.StatusSuccess
		e.steps[step.ID] = out
		e.record(step.ID, state)
		e.emit(Event{Type: "step_finished", Step: step.ID, Message: runstore.StatusSuccess, Fields: map[string]any{
			"duration": finishedAt.Sub(state.StartedAt).String(),
		}})
		return false
	}

	state.Status = runstore.StatusFailed
	state.Error = &runstore.RunError{Class: stepErr.Class, Message: stepErr.Msg}
	out.Status = expr.StatusFailed
	out.Result = nil
	e.steps[step.ID] = out
	e.record(step.ID, state)
	e.emit(Event{Type: "step_failed", Step: step.ID, Message: stepErr.Error()})

	// continue keeps the run going with a failed step (section 9.2). An
	// assert is never continued: it stops the run by definition.
	if step.OnError == scenario.OnErrorContinue && stepErr.Class != ClassAssert {
		return false
	}
	e.fail(step.ID, stepErr)
	return true
}

// attempt executes the step body, retrying per section 9.1 and 9.4.
func (e *Engine) attempt(ctx context.Context, step *scenario.Step, path string, state *runstore.StepState) (expr.Step, *Error) {
	var (
		out     expr.Step
		stepErr *Error
	)
	for attempt := 0; ; attempt++ {
		state.Attempts = attempt + 1
		out, stepErr = e.execute(ctx, step, path)
		if stepErr == nil {
			return out, nil
		}

		policy, ok := e.retryFor(step, stepErr.Class)
		if !ok || attempt >= policy.attempts {
			return out, stepErr
		}
		pause := backoffFor(policy.backoff, attempt)
		e.emit(Event{Type: "step_retry", Step: step.ID, Message: stepErr.Error(), Fields: map[string]any{
			"attempt": state.Attempts,
			"backoff": pause.String(),
		}})
		if err := sleep(ctx, pause); err != nil {
			return out, wrapf(ClassTimeout, err, "step %s cancelled while waiting to retry", step.ID)
		}
	}
}

// retryFor decides whether a class is retried for this step. retry.attempts
// counts retries after the first try, matching the defaults of section 9.1.
func (e *Engine) retryFor(step *scenario.Step, class string) (retryPolicy, bool) {
	// Classes the table of section 9.1 marks as never retried: no retry
	// block turns them back on. Trying again cannot help — the money or the
	// turns are already spent, the policy answer will not change, and a
	// broken configuration stays broken.
	if neverRetried[class] {
		return retryPolicy{}, false
	}
	// An llm step owns its schema retry: retrying it here would start from
	// a fresh conversation and lose the validation message (section 3.5).
	if step.LLM != nil && class == ClassSchema {
		return retryPolicy{}, false
	}
	if step.Retry != nil {
		if len(step.Retry.On) > 0 && !containsClass(step.Retry.On, class) {
			return retryPolicy{}, false
		}
		policy := retryPolicy{attempts: step.Retry.Attempts, backoff: step.Retry.Backoff.Duration()}
		if policy.attempts <= 0 {
			return retryPolicy{}, false
		}
		if policy.backoff <= 0 {
			policy.backoff = 5 * time.Second
		}
		if !e.retryable(step) {
			return retryPolicy{}, false
		}
		return policy, true
	}

	policy, ok := defaultRetries[class]
	if !ok || !e.retryable(step) {
		return retryPolicy{}, false
	}
	return policy, true
}

// retryable reports whether a step may be executed twice. A step with side
// effects is only retried when it carries a dedupe_key (section 9.4).
func (e *Engine) retryable(step *scenario.Step) bool {
	if step.DedupeKey != "" {
		return true
	}
	if step.Run != nil {
		return step.Run.Readonly
	}
	if step.HTTP != nil {
		return e.httpReadonly(step.HTTP)
	}
	if step.Notify != "" {
		// Sending a message twice is a side effect like any other.
		return false
	}
	return true
}

// execute dispatches on the step kind.
func (e *Engine) execute(ctx context.Context, step *scenario.Step, path string) (expr.Step, *Error) {
	kind := step.Kind()
	stepCtx, cancel := e.stepContext(ctx, step)
	defer cancel()

	switch kind {
	case scenario.KindRun:
		return e.execRun(stepCtx, step, path)
	case scenario.KindHTTP:
		return e.execHTTP(stepCtx, step, path)
	case scenario.KindLLM:
		return e.execLLM(stepCtx, step, path)
	case scenario.KindAgent:
		return e.execAgent(stepCtx, step, path)
	case scenario.KindAssert:
		return e.execAssert(step)
	case scenario.KindForeach:
		return e.execForeach(stepCtx, step, path)
	case scenario.KindUntil:
		return e.execUntil(stepCtx, step, path)
	case scenario.KindNotify:
		return e.execNotify(stepCtx, step, path)
	case scenario.KindNone:
		return expr.Step{}, errorf(ClassConfig, "step %s: exactly one body field must be set", step.ID)
	default:
		return expr.Step{}, errorf(ClassConfig, "step %s: %s steps are not implemented yet", step.ID, kind)
	}
}

// executeBody runs one step body with its retry policy and, when that fails,
// its fallback (sections 9.1 and 9.2). The state is updated in place. It is
// also the entry point for a foreach item, which has a body and a policy but
// no id of its own.
func (e *Engine) executeBody(ctx context.Context, step *scenario.Step, path string, state *runstore.StepState) (expr.Step, *Error) {
	out, stepErr := e.attempt(ctx, step, path, state)
	if stepErr == nil || step.OnError != scenario.OnErrorFallback || step.Fallback == nil {
		return out, stepErr
	}
	e.emit(Event{Type: "step_fallback", Step: step.ID, Message: stepErr.Msg})
	fallback := *step.Fallback
	fallback.ID = step.ID
	fallback.Retry = nil
	fallback.OnError = scenario.OnErrorFail
	fallback.Fallback = nil
	out, stepErr = e.attempt(ctx, &fallback, path, state)
	state.FallbackUsed = stepErr == nil
	return out, stepErr
}

// withVars returns an engine that renders templates with extra variables in
// scope. Everything else -- the run store, the step results, the cost ledger
// -- is shared with the parent.
func (e *Engine) withVars(vars map[string]any) *Engine {
	child := *e
	child.vars = vars
	return &child
}

// stepContext applies the step timeout, falling back to defaults.timeout.
func (e *Engine) stepContext(ctx context.Context, step *scenario.Step) (context.Context, context.CancelFunc) {
	limit := e.stepLimit(step)
	if limit > 0 {
		return context.WithTimeout(ctx, limit)
	}
	return context.WithCancel(ctx)
}

// shouldRun evaluates when: for the step.
func (e *Engine) shouldRun(step *scenario.Step) (bool, *Error) {
	if step.When == "" {
		return true, nil
	}
	program, err := expr.CompileBool(step.When)
	if err != nil {
		return false, wrapf(ClassConfig, err, "step %s: when", step.ID)
	}
	ok, err := program.EvalBool(e.exprContext())
	if err != nil {
		return false, wrapf(ClassConfig, err, "step %s: when", step.ID)
	}
	return ok, nil
}

// runOnFailure executes the on_failure steps (section 9.3). They run outside
// the run context so that a cancelled or exhausted run still notifies, get a
// fixed timeout and are never retried. Their own failures are logged only.
func (e *Engine) runOnFailure(ctx context.Context) {
	steps := e.opts.Scenario.OnFailure
	if len(steps) == 0 {
		return
	}
	for i := range steps {
		step := steps[i]
		step.Retry = nil
		step.OnError = scenario.OnErrorContinue
		if step.Timeout.Duration() <= 0 {
			step.Timeout = scenario.Duration(onFailureTimeout)
		}

		path := filepath.Join("on_failure", step.ID)
		stepCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), onFailureTimeout)
		out, stepErr := e.execute(stepCtx, &step, path)
		cancel()
		if stepErr != nil {
			e.emit(Event{Type: "on_failure_failed", Step: step.ID, Message: stepErr.Error()})
			continue
		}
		out.Status = expr.StatusSuccess
		e.emit(Event{Type: "on_failure_finished", Step: step.ID})
	}
}

// fail records the error that stops the run.
func (e *Engine) fail(stepID string, err *Error) {
	if e.failure != nil {
		return
	}
	e.failure = err
	e.failedStep = stepID
}

// record stores the step state and persists run.json (section 10.2).
func (e *Engine) record(stepID string, state *runstore.StepState) {
	e.state.Steps[stepID] = state
	if err := e.opts.Store.WriteRun(e.state); err != nil {
		e.emit(Event{Type: "store_error", Step: stepID, Message: err.Error()})
	}
}

// emit sends an event to the observer and to events.jsonl.
func (e *Engine) emit(event Event) {
	if e.opts.Observer != nil {
		e.opts.Observer(event)
	}
	fields := map[string]any{}
	for k, v := range event.Fields {
		fields[k] = v
	}
	if event.Step != "" {
		fields["step"] = event.Step
	}
	if event.Message != "" {
		fields["message"] = event.Message
	}
	if err := e.opts.Store.Event(event.Type, fields); err != nil && e.opts.Observer != nil {
		e.opts.Observer(Event{Type: "store_error", Message: err.Error()})
	}
}

// exprContext builds the expression context of section 5.1.
func (e *Engine) exprContext() expr.Context {
	return expr.Context{
		Inputs: e.opts.Inputs,
		Steps:  e.steps,
		Run:    e.runContext(),
		Iter:   e.iter,
	}
}

func (e *Engine) runContext() expr.Run {
	run := expr.Run{
		ID:        e.opts.Store.ID(),
		Name:      e.opts.Scenario.Name,
		StartedAt: e.startedAt,
		Dir:       e.opts.Store.Dir(),
		Duration:  e.opts.Now().Sub(e.startedAt),
	}
	if total := e.cost.report().TotalUSD; total != nil {
		run.CostUSD = *total
	}
	if e.failure != nil {
		run.FailedStep = e.failedStep
		run.Error = expr.RunError{
			Class:      e.failure.Class,
			Message:    e.failure.Msg,
			StderrTail: e.failure.StderrTail,
		}
	}
	return run
}

// templateData is the expression context in the shape templates expect
// (section 5.2).
func (e *Engine) templateData() map[string]any {
	steps := make(map[string]any, len(e.steps))
	for id, step := range e.steps {
		steps[id] = map[string]any{
			"status":     step.Status,
			"result":     step.Result,
			"stdout":     step.Stdout,
			"stderr":     step.Stderr,
			"exit_code":  step.ExitCode,
			"items":      step.Items,
			"iterations": step.Iterations,
		}
	}
	run := e.runContext()
	data := map[string]any{
		"inputs": e.opts.Inputs,
		"steps":  steps,
		"run": map[string]any{
			"id":          run.ID,
			"name":        run.Name,
			"started_at":  run.StartedAt,
			"dir":         run.Dir,
			"duration":    run.Duration.String(),
			"failed_step": run.FailedStep,
			"cost_usd":    run.CostUSD,
			"error": map[string]any{
				"class":       run.Error.Class,
				"message":     run.Error.Message,
				"stderr_tail": run.Error.StderrTail,
			},
		},
	}
	for name, value := range e.vars {
		if _, taken := data[name]; !taken {
			data[name] = value
		}
	}
	return data
}

// render renders one scenario string.
func (e *Engine) render(name, text string) (string, *Error) {
	out, err := e.renderer.Render(name, text, e.templateData())
	if err != nil {
		return "", wrapf(ClassConfig, err, "render %s", name)
	}
	return out, nil
}

func containsClass(list []string, class string) bool {
	for _, item := range list {
		if item == class {
			return true
		}
	}
	return false
}

// backoffFor grows the pause exponentially with the attempt number
// (section 9.1).
func backoffFor(base time.Duration, attempt int) time.Duration {
	if base <= 0 {
		return 0
	}
	pause := base
	for i := 0; i < attempt; i++ {
		pause *= 2
	}
	return pause
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// writeCost stores cost.json and the run total in run.json (section 10.1). A
// run directory that cannot take the report is not worth failing the run
// over: the cost is also in each step's output.json.
func (e *Engine) writeCost() {
	report := e.cost.report()
	e.state.CostUSD = report.TotalUSD
	if err := e.opts.Store.WriteCost(report); err != nil {
		e.emit(Event{Type: "warning", Message: err.Error()})
	}
}
