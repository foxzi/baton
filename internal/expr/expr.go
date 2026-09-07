// Package expr compiles and evaluates the scenario expression language
// (docs/ru/spec.md, section 5.1).
//
// Expressions appear in when:, condition: and similar fields. The engine is
// github.com/expr-lang/expr, compiled against the typed context below, so a
// misspelled name fails validation instead of the run.
//
// Secrets are absent from the context as a class: there is no secrets field,
// which makes any expression reading a secret a compile error.
package expr

import (
	"fmt"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

// Step statuses reported through steps.<id>.status.
const (
	StatusSuccess = "success"
	StatusFailed  = "failed"
	StatusSkipped = "skipped"
)

// Context is the evaluation context. Field names follow the specification, so
// they are spelled out in expr tags rather than derived from the Go names.
type Context struct {
	Inputs map[string]any  `expr:"inputs"`
	Steps  map[string]Step `expr:"steps"`
	Run    Run             `expr:"run"`

	// Iter is populated inside until bodies and is empty elsewhere.
	Iter map[string]any `expr:"iter"`
}

// Step is the result of an already finished step.
type Step struct {
	Status   string `expr:"status"`
	Result   any    `expr:"result"`
	Stdout   string `expr:"stdout"`
	Stderr   string `expr:"stderr"`
	ExitCode int    `expr:"exit_code"`
	Items    []any  `expr:"items"`
}

// Run describes the current run. The fields after StartedAt are the failure
// context of section 9.3 and are only populated inside on_failure steps.
type Run struct {
	ID        string    `expr:"id"`
	Name      string    `expr:"name"`
	StartedAt time.Time `expr:"started_at"`

	Dir        string        `expr:"dir"`
	FailedStep string        `expr:"failed_step"`
	Error      RunError      `expr:"error"`
	Duration   time.Duration `expr:"duration"`
	CostUSD    float64       `expr:"cost_usd"`
}

// RunError describes why a run failed, for on_failure steps.
type RunError struct {
	Class      string `expr:"class"`
	Message    string `expr:"message"`
	StderrTail string `expr:"stderr_tail"`
}

// Program is a compiled expression.
type Program struct {
	source  string
	program *vm.Program
}

// Source returns the expression the program was compiled from.
func (p *Program) Source() string { return p.source }

// Compile compiles source against the typed context.
func Compile(source string) (*Program, error) {
	return compile(source, expr.Env(Context{}))
}

// CompileBool compiles source and additionally requires it to produce a
// boolean, which is what when: and condition: fields need.
func CompileBool(source string) (*Program, error) {
	return compile(source, expr.Env(Context{}), expr.AsBool())
}

func compile(source string, options ...expr.Option) (*Program, error) {
	program, err := expr.Compile(source, options...)
	if err != nil {
		return nil, fmt.Errorf("compile %q: %w", source, err)
	}
	return &Program{source: source, program: program}, nil
}

// Eval runs the program against ctx.
//
// A failure here is a config error per section 5.1: the expression compiled,
// so what went wrong is the shape of the data it was given.
func (p *Program) Eval(ctx Context) (any, error) {
	if ctx.Inputs == nil {
		ctx.Inputs = map[string]any{}
	}
	if ctx.Steps == nil {
		ctx.Steps = map[string]Step{}
	}
	if ctx.Iter == nil {
		ctx.Iter = map[string]any{}
	}

	out, err := expr.Run(p.program, ctx)
	if err != nil {
		return nil, fmt.Errorf("evaluate %q: %w", p.source, err)
	}
	return out, nil
}

// EvalBool runs the program and requires a boolean result. Programs from
// CompileBool are already checked at compile time; the check is repeated here
// because a program compiled with Compile can still be used as a condition.
func (p *Program) EvalBool(ctx Context) (bool, error) {
	out, err := p.Eval(ctx)
	if err != nil {
		return false, err
	}
	result, ok := out.(bool)
	if !ok {
		return false, fmt.Errorf("evaluate %q: expected bool, got %T", p.source, out)
	}
	return result, nil
}
