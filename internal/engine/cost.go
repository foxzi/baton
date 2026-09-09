package engine

import (
	"errors"
	"fmt"
	"sync"

	"github.com/foxzi/baton/internal/runstore"
)

// errRunBudget marks a failure caused by the run budget rather than by the
// provider. Both are class budget, but only a provider budget moves the step
// to the next model of fallback_models (section 3.5).
var errRunBudget = errors.New("run budget exhausted")

// costLedger accumulates what a run spent (section 8.3). A step that used a
// model missing from the pricing table contributes tokens but no dollars, so
// knownUSD stays false and the total is reported as null.
type costLedger struct {
	mu           sync.Mutex
	entries      []runstore.CostEntry
	usd          float64
	knownUSD     bool
	inputTokens  int
	outputTokens int
}

// add records one step's spending and returns the run total in dollars so
// far, or nil when no step reported a price.
func (l *costLedger) add(entry runstore.CostEntry) *float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, entry)
	l.inputTokens += entry.InputTokens
	l.outputTokens += entry.OutputTokens
	if entry.CostUSD != nil {
		l.usd += *entry.CostUSD
		l.knownUSD = true
	}
	return l.totalUSD()
}

// totalUSD is the run cost in dollars, or nil when nothing priced was spent.
// The caller must hold the lock.
func (l *costLedger) totalUSD() *float64 {
	if !l.knownUSD {
		return nil
	}
	usd := l.usd
	return &usd
}

// totalTokens is the run's input+output token count so far. The caller must
// hold the lock.
func (l *costLedger) totalTokens() int {
	return l.inputTokens + l.outputTokens
}

// tokens returns the run's total input+output tokens spent so far.
func (l *costLedger) tokens() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.totalTokens()
}

// report is the content of cost.json.
func (l *costLedger) report() *runstore.CostReport {
	l.mu.Lock()
	defer l.mu.Unlock()
	return &runstore.CostReport{
		SchemaVersion:     runstore.SchemaVersion,
		TotalUSD:          l.totalUSD(),
		TotalInputTokens:  l.inputTokens,
		TotalOutputTokens: l.outputTokens,
		Steps:             l.entries,
	}
}

// spend records a step's cost and reports whether the run went over
// budget.usd or budget.tokens. Money and tokens already spent are never
// unspent, so the step that crossed the line is the one that fails
// (section 9.1: budget is never retried).
func (e *Engine) spend(entry runstore.CostEntry) *Error {
	total := e.cost.add(entry)
	limit := e.opts.Scenario.Budget.USD
	if limit > 0 && total != nil && *total > limit {
		return &Error{
			Class: ClassBudget,
			Msg:   fmt.Sprintf("run budget exceeded: spent $%.4f of $%.4f", *total, limit),
			Err:   errRunBudget,
		}
	}
	if tokenLimit := e.opts.Scenario.Budget.Tokens; tokenLimit > 0 {
		if tokens := e.cost.tokens(); tokens > tokenLimit {
			return &Error{
				Class: ClassBudget,
				Msg:   fmt.Sprintf("run token budget exceeded: spent %d of %d tokens", tokens, tokenLimit),
				Err:   errRunBudget,
			}
		}
	}
	return nil
}
