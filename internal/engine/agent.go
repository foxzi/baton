package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/foxzi/baton/internal/agent"
	"github.com/foxzi/baton/internal/agent/claudecode"
	"github.com/foxzi/baton/internal/agent/fake"
	"github.com/foxzi/baton/internal/expr"
	"github.com/foxzi/baton/internal/gateway"
	"github.com/foxzi/baton/internal/httpx"
	"github.com/foxzi/baton/internal/jsonschema"
	"github.com/foxzi/baton/internal/runstore"
	"github.com/foxzi/baton/internal/scenario"
	"github.com/foxzi/baton/internal/secrets"
	"github.com/foxzi/baton/internal/tools"
	"github.com/foxzi/baton/internal/workspace"
)

// agentRuntime is what agent steps of one run share: the gateway serving
// their tools, the engines already built and the workspace preparation that
// happens once (spec sections 7.1 and 7.7).
type agentRuntime struct {
	mu        sync.Mutex
	gateway   *gateway.Gateway
	engines   map[string]agent.Engine
	workspace string
}

// execAgent hands a prompt to an agent engine and returns the result the
// agent submitted through the gateway (section 3.6).
//
// The tool set is the step's commands (section 7.5), the readonly pack
// operations its policy names (section 7.4.8) and the fetch and state tools
// of section 7.6. Third-party MCP servers are not proxied yet, so a policy
// naming one gets nothing of the sort.
func (e *Engine) execAgent(ctx context.Context, step *scenario.Step, path string) (expr.Step, *Error) {
	call, stepErr := e.prepareAgent(step)
	if stepErr != nil {
		return expr.Step{}, stepErr
	}

	e.writeStepJSON(path, "input.json", map[string]any{
		"engine":  call.engine,
		"model":   step.Agent.Model,
		"profile": string(step.Agent.Profile),
		"system":  call.system,
		"prompt":  call.prompt,
		"result":  step.Agent.Result,
	})

	out, stepErr := e.runAgent(ctx, step, path, call)
	if stepErr != nil {
		e.writeStepJSON(path, "output.json", map[string]any{
			"status": expr.StatusFailed,
			"error":  map[string]any{"class": stepErr.Class, "message": stepErr.Msg},
		})
		return expr.Step{}, stepErr
	}
	return out, nil
}

// agentCall is an agent step with everything the scenario left to the runner
// already resolved.
type agentCall struct {
	engine string
	script string
	prompt string
	system string
	schema *jsonschema.Schema
	policy agent.Policy
	skills []string
	env    map[string]string
	// budget is the step's dollar cap, from the step or from defaults.
	budget float64
	// startedAt is when the step began, for the duration in cost.json.
	startedAt time.Time
}

// prepareAgent renders the prompts, loads the result schema and resolves the
// tool policy. Everything here is a config error: none of it depends on the
// agent doing anything.
func (e *Engine) prepareAgent(step *scenario.Step) (*agentCall, *Error) {
	call, stepErr := e.prepareAgentPolicy(step)
	if stepErr != nil {
		return nil, stepErr
	}

	body := step.Agent
	data, stepErr := e.withData(step.ID, "agent", body.With)
	if stepErr != nil {
		return nil, stepErr
	}
	if call.system, stepErr = e.promptText(step.ID, "agent.system", body.System, data); stepErr != nil {
		return nil, stepErr
	}
	if call.prompt, stepErr = e.promptText(step.ID, "agent.prompt", body.Prompt, data); stepErr != nil {
		return nil, stepErr
	}
	if call.prompt == "" {
		return nil, errorf(ClassConfig, "step %s: agent.prompt is empty", step.ID)
	}

	for _, skill := range body.Skills {
		dir := e.resolvePath(skill)
		if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
			return nil, wrapf(ClassConfig, err, "step %s: agent.skills %s", step.ID, skill)
		}
		call.skills = append(call.skills, dir)
	}

	if call.env, _, stepErr = e.envMap(step.ID, "agent.env", body.Env); stepErr != nil {
		return nil, stepErr
	}
	return call, nil
}

// prepareAgentPolicy resolves the half of an agent call the tool set is built
// from: the engine, the policy and the schema submit_result validates
// against. None of it reads the results of earlier steps, which is what lets
// `baton tools` report a step's tools before the run reaches it.
func (e *Engine) prepareAgentPolicy(step *scenario.Step) (*agentCall, *Error) {
	body := step.Agent
	call := &agentCall{
		engine:    body.Engine,
		script:    body.Script,
		policy:    agent.ResolvePolicy(body),
		budget:    body.BudgetUSD,
		startedAt: e.opts.Now(),
	}
	if call.engine == "" {
		return nil, errorf(ClassConfig, "step %s: agent.engine is not set", step.ID)
	}
	if call.budget == 0 {
		call.budget = e.opts.Scenario.Defaults.BudgetUSD
	}

	if body.Result == "" {
		return nil, errorf(ClassConfig, "step %s: agent.result is required", step.ID)
	}
	raw, err := os.ReadFile(e.resolvePath(body.Result))
	if err != nil {
		return nil, wrapf(ClassConfig, err, "step %s: agent.result", step.ID)
	}
	if call.schema, err = jsonschema.Compile(body.Result, raw); err != nil {
		return nil, wrapf(ClassConfig, err, "step %s: agent.result", step.ID)
	}
	return call, nil
}

// runAgent opens the step's session on the gateway, runs the engine against
// it and turns what came back into the step result.
func (e *Engine) runAgent(ctx context.Context, step *scenario.Step, path string, call *agentCall) (expr.Step, *Error) {
	dir, err := e.opts.Store.StepDir(path)
	if err != nil {
		return expr.Step{}, wrapf(ClassConfig, err, "step %s: run directory", step.ID)
	}
	root, stepErr := e.agentWorkspace(path)
	if stepErr != nil {
		return expr.Step{}, stepErr
	}

	toolSet, stepErr := e.agentToolSet(step, call, root)
	if stepErr != nil {
		return expr.Step{}, stepErr
	}

	audit, closeAudit := e.auditWriter(path, dir)
	defer closeAudit()

	gw, stepErr := e.agentGateway()
	if stepErr != nil {
		return expr.Step{}, stepErr
	}
	session, err := gw.Open(gateway.StepOptions{
		StepID:         path,
		Tools:          toolSet,
		Result:         call.schema,
		MaxToolCalls:   call.policy.MaxToolCalls,
		MaxResultBytes: call.policy.MaxResultBytes,
		Audit:          audit,
	})
	if err != nil {
		return expr.Step{}, wrapf(ClassConfig, err, "step %s: gateway", step.ID)
	}
	defer session.Close()

	engine, stepErr := e.agentEngine(ctx, step, call)
	if stepErr != nil {
		return expr.Step{}, stepErr
	}

	e.emit(Event{Type: "agent_started", Step: step.ID, Message: call.engine, Fields: map[string]any{
		"model": step.Agent.Model,
		"tools": len(toolSet),
	}})

	res, err := engine.Run(ctx, agent.Request{
		Prompt:         call.prompt,
		System:         call.system,
		Model:          step.Agent.Model,
		Workspace:      root,
		Skills:         call.skills,
		Gateway:        session.Info(),
		Tools:          call.policy,
		MaxTurns:       step.Agent.MaxTurns,
		BudgetUSD:      call.budget,
		Env:            call.env,
		TranscriptPath: filepath.Join(dir, "transcript.jsonl"),
	})
	if err != nil {
		return expr.Step{}, e.agentFailure(ctx, step, err)
	}
	return e.agentResult(step, path, call, session, res)
}

// agentToolSet builds every tool the step's agent is given: its commands
// (section 7.5), the file tools of section 7.3, the readonly pack operations
// of its policy (section 7.4.8), the git tools of section 7.4.7 and the
// fetch and state tools of section 7.6. The gateway adds submit_result
// itself.
func (e *Engine) agentToolSet(step *scenario.Step, call *agentCall, root string) ([]gateway.Tool, *Error) {
	commands, err := tools.NewCommands(tools.CommandOptions{
		Workspace: root,
		Declared:  e.opts.Scenario.Commands,
		Policy:    call.policy,
		Secrets:   e.secretSource(),
		// The commands of a step share half of its timeout (section 7.5).
		Budget:         e.stepLimit(step) / 2,
		MaxOutputBytes: call.policy.MaxResultBytes,
	})
	if err != nil {
		return nil, wrapf(ClassConfig, err, "step %s: agent.tools", step.ID)
	}

	fs, stepErr := e.agentFS(call, root)
	if stepErr != nil {
		return nil, stepErr
	}
	git, stepErr := e.agentGit(call, root)
	if stepErr != nil {
		return nil, stepErr
	}
	apis, stepErr := e.agentAPIs(call)
	if stepErr != nil {
		return nil, stepErr
	}
	state, stepErr := e.agentState(call)
	if stepErr != nil {
		return nil, stepErr
	}
	fetch, stepErr := e.agentFetch(call)
	if stepErr != nil {
		return nil, stepErr
	}

	set := append(commands.Tools(), fs.Tools()...)
	set = append(set, git.Tools()...)
	set = append(set, apis.Tools()...)
	set = append(set, state.Tools()...)
	set = append(set, fetch.Tools()...)
	return set, nil
}

// agentFS builds the file tools of the step, but only for an engine that
// has none of its own: claude-code reads and writes with its built-in Read,
// Glob, Grep and Write (section 8.2), and serving it fs.read as well would
// only give it two ways to do one thing.
func (e *Engine) agentFS(call *agentCall, root string) (*tools.FS, *Error) {
	if nativeFileTools(call.engine) {
		return &tools.FS{}, nil
	}
	set, err := tools.NewFS(tools.FSOptions{
		Workspace: root,
		Policy:    call.policy,
	})
	if err != nil {
		return nil, wrapf(ClassConfig, err, "agent.tools.fs")
	}
	return set, nil
}

// nativeFileTools reports whether an engine brings its own file tools.
func nativeFileTools(engine string) bool {
	return engine == "claude-code"
}

// agentGit builds the git tools of the step. They run in the prepared
// workspace, whose configuration no longer carries a credential (section
// 7.1), and they honour the same deny list as the file tools.
func (e *Engine) agentGit(call *agentCall, root string) (*tools.Git, *Error) {
	set, err := tools.NewGit(tools.GitOptions{
		Workspace: root,
		Policy:    call.policy,
	})
	if err != nil {
		return nil, wrapf(ClassConfig, err, "agent.tools.git")
	}
	return set, nil
}

// agentAPIs builds the tools of the pack operations the step's policy names.
// Resolving them here means an operation the scenario cannot reach is a
// configuration error before the agent starts, not a failed tool call.
func (e *Engine) agentAPIs(call *agentCall) (*tools.APIs, *Error) {
	set, err := tools.NewAPIs(tools.APIOptions{
		Policy: call.policy,
		Client: e.httpClient(),
		Resolve: func(name string) (*httpx.API, error) {
			api, stepErr := e.api(name, "")
			if stepErr != nil {
				return nil, stepErr
			}
			return api, nil
		},
	})
	if err != nil {
		return nil, wrapf(ClassConfig, err, "agent.tools.apis")
	}
	return set, nil
}

// agentState builds the state tools of the step. The file is named after the
// scenario and lives next to the run directories, so that two runs of the
// same scenario share it and two scenarios do not (section 7.6).
func (e *Engine) agentState(call *agentCall) (*tools.State, *Error) {
	set, err := tools.NewState(tools.StateOptions{
		Path:   e.stateFile(),
		Policy: call.policy,
	})
	if err != nil {
		return nil, wrapf(ClassConfig, err, "agent.tools.state")
	}
	return set, nil
}

// agentFetch builds the fetch tool of the step. The allow list comes from
// the policy, so a step that names no hosts and has no profile granting the
// tool gets none (section 7.6).
func (e *Engine) agentFetch(call *agentCall) (*tools.Fetch, *Error) {
	set, err := tools.NewFetch(tools.FetchOptions{
		Policy: call.policy,
		Client: e.opts.FetchClient,
	})
	if err != nil {
		return nil, wrapf(ClassConfig, err, "agent.tools.fetch")
	}
	return set, nil
}

// stateFile is <state dir>/<scenario name>.json. The default state directory
// is a sibling of the run directory root, which keeps it out of the runs
// themselves: the state outlives any single run.
func (e *Engine) stateFile() string {
	dir := e.opts.StateDir
	if dir == "" {
		runsDir := filepath.Dir(e.opts.Store.Dir())
		dir = filepath.Join(filepath.Dir(runsDir), "state")
	}
	return filepath.Join(dir, e.opts.Scenario.Name+".json")
}

// agentResult validates the submitted result and records what the step
// spent. The gateway's session is the authority on whether the agent
// submitted anything: an engine that hands the tools to a separate process
// cannot know (section 3.6).
func (e *Engine) agentResult(step *scenario.Step, path string, call *agentCall, session *gateway.Session, res agent.Result) (expr.Step, *Error) {
	raw, ok := session.Result()
	if !ok && res.Submitted {
		raw = res.Result
		ok = len(raw) > 0
	}
	if !ok {
		return expr.Step{}, errorf(ClassSchema, "step %s: the agent finished without submit_result", step.ID)
	}
	value, err := call.schema.ValidateJSON(raw)
	if err != nil {
		return expr.Step{}, wrapf(ClassSchema, err, "step %s: the submitted result does not match the schema", step.ID)
	}

	var cost *float64
	if res.CostUSD > 0 {
		usd := res.CostUSD
		cost = &usd
	}
	if budgetErr := e.spend(runstore.CostEntry{
		Step:         path,
		Model:        e.agentModel(step, call),
		InputTokens:  res.Usage.InputTokens,
		OutputTokens: res.Usage.OutputTokens,
		CachedTokens: res.Usage.CachedTokens,
		CostUSD:      cost,
		Duration:     e.opts.Now().Sub(call.startedAt).String(),
	}); budgetErr != nil {
		return expr.Step{}, budgetErr
	}
	// An engine is asked to stay within budget_usd, but nothing forces it
	// to; a step that reports more has still spent the money (section 9.1:
	// budget is never retried).
	if call.budget > 0 && res.CostUSD > call.budget {
		return expr.Step{}, errorf(ClassBudget, "step %s: agent spent $%.4f of $%.4f", step.ID, res.CostUSD, call.budget)
	}

	e.writeStepJSON(path, "output.json", map[string]any{
		"status":     expr.StatusSuccess,
		"result":     value,
		"usage":      usageMap(res.Usage),
		"cost_usd":   cost,
		"turns":      res.Turns,
		"tool_calls": session.Calls(),
		"engine":     call.engine,
	})
	return expr.Step{Result: value}, nil
}

// agentModel is how the step is labelled in cost.json. An engine that picks
// the model itself leaves agent.model empty, and then the engine's own name
// is all the label there is.
func (e *Engine) agentModel(step *scenario.Step, call *agentCall) string {
	if step.Agent.Model != "" {
		return call.engine + "/" + step.Agent.Model
	}
	return call.engine
}

// agentFailure classifies an engine failure. A step whose context expired is
// a timeout whatever the engine reported (section 9.1).
func (e *Engine) agentFailure(ctx context.Context, step *scenario.Step, err error) *Error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &Error{Class: ClassTimeout, Msg: fmt.Sprintf("step %s: agent timed out", step.ID), Err: err}
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return &Error{Class: ClassConfig, Msg: fmt.Sprintf("step %s: cancelled", step.ID), Err: err}
	}
	return wrapf(ClassCommand, err, "step %s: agent", step.ID)
}

// agentWorkspace prepares the workspace once per run and reports what it had
// to rewrite (section 7.1).
func (e *Engine) agentWorkspace(path string) (string, *Error) {
	e.agents.mu.Lock()
	defer e.agents.mu.Unlock()

	if e.agents.workspace != "" {
		return e.agents.workspace, nil
	}
	info, err := workspace.Prepare(e.opts.Workspace)
	if err != nil {
		return "", wrapf(ClassConfig, err, "prepare the workspace")
	}
	e.agents.workspace = info.Root
	if len(info.Changed) > 0 {
		e.emit(Event{Type: "workspace_prepared", Step: path, Message: info.Root, Fields: map[string]any{
			"changed": info.Changed,
		}})
	}
	return info.Root, nil
}

// agentGateway starts the run's gateway on first use.
func (e *Engine) agentGateway() (*gateway.Gateway, *Error) {
	e.agents.mu.Lock()
	defer e.agents.mu.Unlock()

	if e.agents.gateway != nil {
		return e.agents.gateway, nil
	}
	gw, err := gateway.New(gateway.Options{Redactor: e.redactor()})
	if err != nil {
		return nil, wrapf(ClassConfig, err, "start the gateway")
	}
	e.agents.gateway = gw
	return gw, nil
}

// agentEngine builds the engine a step names, reusing one already built for
// the same configuration: checking a CLI installation costs a process.
func (e *Engine) agentEngine(ctx context.Context, step *scenario.Step, call *agentCall) (agent.Engine, *Error) {
	e.agents.mu.Lock()
	defer e.agents.mu.Unlock()

	key := call.engine + "\x00" + call.script
	if engine, ok := e.agents.engines[key]; ok {
		return engine, nil
	}

	var (
		engine agent.Engine
		err    error
	)
	switch call.engine {
	case "fake":
		if call.script == "" {
			return nil, errorf(ClassConfig, "step %s: engine: fake needs a behaviour script", step.ID)
		}
		engine, err = fake.New(e.resolvePath(call.script))
	case "claude-code":
		engine, err = claudecode.New(ctx, claudecode.Options{})
	default:
		return nil, errorf(ClassConfig, "step %s: unknown agent engine %q", step.ID, call.engine)
	}
	if err != nil {
		return nil, wrapf(ClassConfig, err, "step %s: engine %s", step.ID, call.engine)
	}

	e.agents.engines[key] = engine
	return engine, nil
}

// closeAgents shuts the gateway down at the end of the run.
func (e *Engine) closeAgents() {
	e.agents.mu.Lock()
	gw := e.agents.gateway
	e.agents.gateway = nil
	e.agents.mu.Unlock()

	if gw == nil {
		return
	}
	if err := gw.Close(); err != nil {
		e.emit(Event{Type: "gateway_error", Message: err.Error()})
	}
}

// auditWriter opens the step's tool call log (section 7.7). A run directory
// that cannot take it is reported and the step goes on without the log,
// which is what writeStepFile does with the other step files.
func (e *Engine) auditWriter(path, dir string) (io.Writer, func()) {
	file, err := os.OpenFile(filepath.Join(dir, "tool-calls.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		e.emit(Event{Type: "store_error", Step: path, Message: err.Error()})
		return nil, func() {}
	}
	return file, func() { file.Close() }
}

// secretSource is the secret store as the tools package wants it, keeping a
// nil store out of the interface.
func (e *Engine) secretSource() tools.SecretSource {
	if e.opts.Secrets == nil {
		return nil
	}
	return e.opts.Secrets
}

// redactor is the run's redactor, or nil when the scenario declares no
// secrets.
func (e *Engine) redactor() *secrets.Redactor {
	if e.opts.Secrets == nil {
		return nil
	}
	return e.opts.Secrets.Redactor()
}

// stepLimit is the step's own time limit, the one stepContext applies.
func (e *Engine) stepLimit(step *scenario.Step) time.Duration {
	if limit := step.Timeout.Duration(); limit > 0 {
		return limit
	}
	return e.opts.Scenario.Defaults.Timeout.Duration()
}
