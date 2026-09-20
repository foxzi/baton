package engine

import (
	"context"
	"fmt"

	"github.com/foxzi/baton/internal/expr"
	"github.com/foxzi/baton/internal/notify"
	"github.com/foxzi/baton/internal/runstore"
	"github.com/foxzi/baton/internal/scenario"
)

// Decision is what a person answered at a gate (spec section 3.11). It is
// passed to a resumed run and becomes the gate's result.
type Decision struct {
	// Step is the gate the decision is for; a decision for another step is
	// a mistake and stops the run.
	Step string
	// Definition is the hash the waiting run recorded for the gate. A gate
	// edited since then is a different question: it asks again.
	Definition string
	// Approved is the answer; Reason is optional free text.
	Approved bool
	Reason   string
}

// Result value of a decided gate, as steps.<id>.result.decision sees it.
const (
	DecisionApproved = "approved"
	DecisionRejected = "rejected"
)

// execGate stops the run at a gate, or records the decision a resume brought
// for it (spec section 3.11). It reports whether the run must stop.
//
// Without a decision the gate asks its question -- on the channel it names,
// when it names one -- and the run ends with status waiting. The step is
// recorded as waiting too, with its definition hash: a resume with a
// decision executes the gate again, and a gate edited in between waits
// again instead of taking an answer given to a different question.
func (e *Engine) runGate(ctx context.Context, step *scenario.Step, path string) bool {
	state := &runstore.StepState{Status: runstore.StatusRunning, StartedAt: e.opts.Now()}
	e.emit(Event{Type: "step_started", Step: step.ID})

	decision := e.opts.Decision
	if decision != nil && decision.Step == step.ID {
		if decision.Definition == definitionHash(step) {
			return e.decideGate(step, path, state, decision)
		}
		e.emit(Event{Type: "step_changed", Step: step.ID, Message: "gate changed since the resumed run, asking again"})
	}

	text, stepErr := e.render(fmt.Sprintf("%s.message", step.ID), step.Gate.Message)
	if stepErr == nil {
		stepErr = e.askGate(ctx, step, path, e.redact(text))
	}
	if stepErr != nil {
		e.failStep(step, state, stepErr, expr.Step{})
		e.fail(step.ID, stepErr)
		return true
	}

	state.Status = runstore.StatusWaiting
	state.Definition = definitionHash(step)
	e.steps[step.ID] = expr.Step{Status: runstore.StatusWaiting}
	e.record(step.ID, state)
	e.waitingStep = step.ID
	e.emit(Event{Type: "step_waiting", Step: step.ID, Message: text})
	return true
}

// askGate records the question in the step directory and sends it to the
// gate's channel, when it has one. The message tells the reader how to
// answer, so that the notification is enough to act on.
func (e *Engine) askGate(ctx context.Context, step *scenario.Step, path, text string) *Error {
	e.writeStepJSON(path, "input.json", map[string]any{
		"message": text,
		"channel": step.Gate.Notify,
	})
	if step.Gate.Notify == "" {
		return nil
	}
	channel, stepErr := e.channel(step.Gate.Notify)
	if stepErr != nil {
		return stepErr
	}
	text = fmt.Sprintf("%s\n\nbaton resume %s --approve | --reject", text, e.state.ID)
	sender := notify.Sender{HTTP: e.httpClient(), Out: e.opts.Stdout, Pack: e.sendViaPack}
	if err := sender.Send(ctx, channel, text); err != nil {
		return e.notifyFailure(ctx, step, path, err)
	}
	return nil
}

// decideGate finishes a gate with the decision a resume brought. Both
// answers succeed: a rejected gate is a result the following steps branch
// on with when, not a failure.
func (e *Engine) decideGate(step *scenario.Step, path string, state *runstore.StepState, decision *Decision) bool {
	verdict := DecisionRejected
	if decision.Approved {
		verdict = DecisionApproved
	}
	result := map[string]any{"decision": verdict, "reason": decision.Reason}
	e.writeStepJSON(path, "output.json", map[string]any{
		"status": expr.StatusSuccess,
		"result": result,
	})

	finishedAt := e.opts.Now()
	state.FinishedAt = &finishedAt
	state.Status = runstore.StatusSuccess
	state.Definition = definitionHash(step)
	e.steps[step.ID] = expr.Step{Status: expr.StatusSuccess, Result: result}
	e.record(step.ID, state)
	e.emit(Event{Type: "step_finished", Step: step.ID, Message: runstore.StatusSuccess, Fields: map[string]any{
		"decision": verdict,
	}})
	return false
}
