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
	"path/filepath"
	"time"

	"github.com/foxzi/baton/internal/expr"
	"github.com/foxzi/baton/internal/httpx"
	"github.com/foxzi/baton/internal/runstore"
	"github.com/foxzi/baton/internal/scenario"
	"github.com/foxzi/baton/internal/secrets"
	"github.com/foxzi/baton/internal/tmpl"
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
	// Store is the run directory to record the run in.
	Store *runstore.Store
	// Workspace is the default working directory of run steps. It defaults
	// to the directory of the scenario file.
	Workspace string
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
	dir       string
	steps     map[string]expr.Step
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
		opts:     opts,
		dir:      baseDir,
		renderer: tmpl.NewRenderer(baseDir),
		steps:    map[string]expr.Step{},
		apis:     map[string]*httpx.API{},
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
		Steps:         map[string]*runstore.StepState{},
	}
	if err := e.opts.Store.WriteRun(e.state); err != nil {
		return nil, err
	}
	e.emit(Event{Type: "run_started", Message: scn.Name, Fields: map[string]any{"id": e.state.ID}})

	runCtx, cancel := e.budgetContext(ctx)
	defer cancel()

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

	out, stepErr := e.attempt(ctx, step, path, state)
	if stepErr != nil && step.OnError == scenario.OnErrorFallback && step.Fallback != nil {
		e.emit(Event{Type: "step_fallback", Step: step.ID, Message: stepErr.Msg})
		fallback := *step.Fallback
		fallback.ID = step.ID
		fallback.Retry = nil
		fallback.OnError = scenario.OnErrorFail
		fallback.Fallback = nil
		out, stepErr = e.attempt(ctx, &fallback, path, state)
		state.FallbackUsed = stepErr == nil
	}

	finishedAt := e.opts.Now()
	state.FinishedAt = &finishedAt
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
	case scenario.KindAssert:
		return e.execAssert(step)
	case scenario.KindNone:
		return expr.Step{}, errorf(ClassConfig, "step %s: exactly one body field must be set", step.ID)
	default:
		return expr.Step{}, errorf(ClassConfig, "step %s: %s steps are not implemented yet", step.ID, kind)
	}
}

// stepContext applies the step timeout, falling back to defaults.timeout.
func (e *Engine) stepContext(ctx context.Context, step *scenario.Step) (context.Context, context.CancelFunc) {
	limit := step.Timeout.Duration()
	if limit <= 0 {
		limit = e.opts.Scenario.Defaults.Timeout.Duration()
	}
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
			"status":    step.Status,
			"result":    step.Result,
			"stdout":    step.Stdout,
			"stderr":    step.Stderr,
			"exit_code": step.ExitCode,
			"items":     step.Items,
		}
	}
	run := e.runContext()
	return map[string]any{
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
