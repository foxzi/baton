package engine

import (
	"context"
	"path/filepath"
	"strconv"

	"github.com/foxzi/baton/internal/expr"
	"github.com/foxzi/baton/internal/runstore"
	"github.com/foxzi/baton/internal/scenario"
)

// execUntil repeats a body until its condition holds (section 3.8). The
// condition is checked after every iteration, so the body always runs at
// least once, and at most max_iterations times. The result of the step is the
// result of the last iteration plus the number of iterations.
//
// Running out of iterations is not an error in itself: the step reports what
// the last iteration produced, and a scenario that needs the condition to
// hold says so with an assert.
func (e *Engine) execUntil(ctx context.Context, step *scenario.Step, path string) (expr.Step, *Error) {
	loop := step.Until
	switch {
	case loop.Condition == "":
		return expr.Step{}, errorf(ClassConfig, "step %s: until.condition is required", step.ID)
	case loop.MaxIterations < 1:
		return expr.Step{}, errorf(ClassConfig, "step %s: until.max_iterations is required", step.ID)
	case loop.Step == nil:
		return expr.Step{}, errorf(ClassConfig, "step %s: until.step is required", step.ID)
	}
	if loop.Step.Kind() == scenario.KindNone {
		return expr.Step{}, errorf(ClassConfig, "step %s: until.step: exactly one body field must be set", step.ID)
	}

	program, err := expr.CompileBool(loop.Condition)
	if err != nil {
		return expr.Step{}, wrapf(ClassConfig, err, "step %s: until.condition", step.ID)
	}

	var out expr.Step
	var iter map[string]any
	for i := 0; i < loop.MaxIterations; i++ {
		child := e.withIter(iter)
		body := *loop.Step
		iterPath := filepath.Join(path, strconv.Itoa(i))

		state := &runstore.StepState{Status: runstore.StatusRunning, StartedAt: e.opts.Now()}
		iterOut, stepErr := child.executeBody(ctx, &body, iterPath, state)
		iterOut.Iterations = i + 1
		out = iterOut
		if stepErr != nil {
			e.emit(Event{Type: "until_iteration_failed", Step: step.ID, Message: stepErr.Error(), Fields: map[string]any{
				"iteration": i,
			}})
			return out, wrapf(stepErr.Class, stepErr, "step %s: until", step.ID)
		}
		out.Status = expr.StatusSuccess

		iter = iterVars(out)
		done, evalErr := program.EvalBool(child.withIter(iter).exprContext())
		if evalErr != nil {
			return out, wrapf(ClassConfig, evalErr, "step %s: until.condition", step.ID)
		}
		e.emit(Event{Type: "until_iteration", Step: step.ID, Fields: map[string]any{
			"iteration": i,
			"condition": done,
		}})
		if done {
			break
		}
	}

	e.writeStepJSON(path, "output.json", map[string]any{
		"result":     out.Result,
		"iterations": out.Iterations,
	})
	return out, nil
}

// withIter returns an engine that evaluates expressions and renders templates
// with the previous iteration in scope.
func (e *Engine) withIter(iter map[string]any) *Engine {
	child := e.withVars(map[string]any{"iter": iter})
	child.iter = iter
	return child
}

// iterVars is the shape the previous iteration takes in iter: the fields of a
// finished step, so a condition reads iter.exit_code or iter.result the same
// way it would read them off steps.<id>.
func iterVars(out expr.Step) map[string]any {
	return map[string]any{
		"status":    out.Status,
		"result":    out.Result,
		"stdout":    out.Stdout,
		"stderr":    out.Stderr,
		"exit_code": out.ExitCode,
		"items":     out.Items,
	}
}
