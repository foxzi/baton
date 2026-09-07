package engine

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/foxzi/baton/internal/expr"
	"github.com/foxzi/baton/internal/runstore"
	"github.com/foxzi/baton/internal/scenario"
)

// execForeach runs the body once per item of a list (section 3.7). The result
// is one entry per item, in input order, whatever the completion order was.
func (e *Engine) execForeach(ctx context.Context, step *scenario.Step, path string) (expr.Step, *Error) {
	spec := step.Foreach
	switch {
	case spec.Items == "":
		return expr.Step{}, errorf(ClassConfig, "step %s: foreach.items is required", step.ID)
	case spec.As == "":
		return expr.Step{}, errorf(ClassConfig, "step %s: foreach.as is required", step.ID)
	case spec.Step == nil:
		return expr.Step{}, errorf(ClassConfig, "step %s: foreach.step is required", step.ID)
	}
	if spec.Step.Kind() == scenario.KindNone {
		return expr.Step{}, errorf(ClassConfig, "step %s: foreach.step: exactly one body field must be set", step.ID)
	}

	items, stepErr := e.renderList("step "+step.ID+": foreach.items", spec.Items)
	if stepErr != nil {
		return expr.Step{}, stepErr
	}

	parallel := spec.MaxParallel
	if parallel < 1 {
		parallel = 1
	}
	mode := spec.OnItemError
	if mode == scenario.ItemErrorUnset {
		mode = scenario.ItemErrorFail
	}

	// In fail mode the first error cancels the items still running; the ones
	// that never started are reported as skipped.
	itemsCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make([]any, len(items))
	failures := make([]*Error, len(items))

	one := func(index int, item any) {
		if itemsCtx.Err() != nil {
			results[index] = itemEntry(expr.StatusSkipped, expr.Step{}, nil)
			return
		}
		out, itemErr := e.runItem(itemsCtx, spec, index, path, item)
		status := out.Status
		if itemErr != nil {
			status = expr.StatusFailed
			failures[index] = itemErr
			e.emit(Event{Type: "foreach_item_failed", Step: step.ID, Message: itemErr.Error(), Fields: map[string]any{
				"item": index,
			}})
			if mode == scenario.ItemErrorFail {
				cancel()
			}
		}
		results[index] = itemEntry(status, out, itemErr)
	}

	if parallel == 1 {
		// The common case, and the only one with a defined order: an item
		// that fails in fail mode leaves the rest skipped.
		for i, item := range items {
			one(i, item)
		}
	} else {
		tokens := make(chan struct{}, parallel)
		var wg sync.WaitGroup
		for i, item := range items {
			wg.Add(1)
			go func(index int, item any) {
				defer wg.Done()
				select {
				case tokens <- struct{}{}:
				case <-itemsCtx.Done():
					results[index] = itemEntry(expr.StatusSkipped, expr.Step{}, nil)
					return
				}
				defer func() { <-tokens }()
				one(index, item)
			}(i, item)
		}
		wg.Wait()
	}

	out := expr.Step{Items: results}
	e.writeStepJSON(path, "output.json", map[string]any{"items": results})

	succeeded := 0
	var first *Error
	for i, failure := range failures {
		if failure == nil {
			if entry, ok := results[i].(map[string]any); ok && entry["status"] == expr.StatusSuccess {
				succeeded++
			}
			continue
		}
		if first == nil {
			first = failure
		}
	}
	if first == nil {
		return out, nil
	}
	if mode == scenario.ItemErrorFail {
		return out, wrapf(first.Class, first, "step %s: foreach", step.ID)
	}
	if spec.MinSuccess != nil && len(items) > 0 {
		if ratio := float64(succeeded) / float64(len(items)); ratio < *spec.MinSuccess {
			return out, wrapf(first.Class, first, "step %s: foreach: %d of %d items succeeded, min_success is %v",
				step.ID, succeeded, len(items), *spec.MinSuccess)
		}
	}
	return out, nil
}

// runItem executes the body for one item. The item is a template variable
// under its as name; it is deliberately absent from the expression context,
// so a when: on the body sees the run, not the item.
func (e *Engine) runItem(ctx context.Context, spec *scenario.ForeachStep, index int, path string, item any) (expr.Step, *Error) {
	child := e.withVars(map[string]any{spec.As: item})
	body := *spec.Step
	itemPath := filepath.Join(path, strconv.Itoa(index))

	ok, stepErr := child.shouldRun(&body)
	if stepErr != nil {
		return expr.Step{}, stepErr
	}
	if !ok {
		return expr.Step{Status: expr.StatusSkipped}, nil
	}

	state := &runstore.StepState{Status: runstore.StatusRunning, StartedAt: e.opts.Now()}
	out, stepErr := child.executeBody(ctx, &body, itemPath, state)
	if stepErr != nil {
		// on_error: continue on the body means this item is not a failure
		// of the foreach; on_item_error decides that instead.
		if body.OnError == scenario.OnErrorContinue && stepErr.Class != ClassAssert {
			out.Status = expr.StatusFailed
			return out, nil
		}
		return out, stepErr
	}
	out.Status = expr.StatusSuccess
	return out, nil
}

// itemEntry is one element of steps.<id>.items.
func itemEntry(status string, out expr.Step, stepErr *Error) map[string]any {
	entry := map[string]any{
		"status": status,
		"result": out.Result,
		"error":  nil,
	}
	if stepErr != nil {
		entry["error"] = map[string]any{
			"class":   stepErr.Class,
			"message": stepErr.Msg,
		}
	}
	return entry
}

// renderList renders a template that must produce a list. A template writes
// text, so a Go list would come out as "[a b c]": a lone action is rendered
// through toJSON instead, and anything else has to be written as JSON by the
// scenario itself.
func (e *Engine) renderList(name, text string) ([]any, *Error) {
	source := text
	if inner, ok := loneAction(text); ok {
		source = "{{ " + inner + " | toJSON }}"
	}
	rendered, stepErr := e.render(name, source)
	if stepErr != nil {
		return nil, stepErr
	}
	rendered = strings.TrimSpace(rendered)
	if rendered == "" || rendered == "null" {
		return nil, nil
	}
	var items []any
	if err := json.Unmarshal([]byte(rendered), &items); err != nil {
		return nil, errorf(ClassConfig, "%s: not a list", name)
	}
	return items, nil
}

// loneAction reports whether the template is a single {{ ... }} action and
// returns its body. A pipeline that already ends in toJSON is left alone: it
// produces JSON on its own.
func loneAction(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{{") || !strings.HasSuffix(trimmed, "}}") {
		return "", false
	}
	inner := strings.TrimSpace(trimmed[2 : len(trimmed)-2])
	if strings.Contains(inner, "{{") || strings.Contains(inner, "}}") {
		return "", false
	}
	if strings.HasSuffix(inner, "toJSON") {
		return "", false
	}
	return inner, true
}
