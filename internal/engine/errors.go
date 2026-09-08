package engine

import (
	"fmt"
	"time"

	"github.com/foxzi/baton/internal/exitcode"
)

// Error classes (docs/ru/spec.md, section 9.1). ClassAssert is not an error
// class in the specification: a false assert is a decision, not a failure. It
// is spelled out here because the run still stops and the process still needs
// a distinct exit code.
const (
	ClassTransient = "transient"
	ClassSchema    = "schema"
	ClassCommand   = "command"
	ClassTimeout   = "timeout"
	ClassBudget    = "budget"
	ClassPolicy    = "policy"
	ClassConfig    = "config"
	ClassAssert    = "assert"
)

// Error is a classified failure of a step or of the run.
type Error struct {
	Class string
	Msg   string
	Err   error

	// StderrTail is the last bytes a command wrote before failing, for the
	// on_failure context of section 9.3.
	StderrTail string
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Class, e.Msg, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Class, e.Msg)
}

func (e *Error) Unwrap() error { return e.Err }

// errorf builds an error of the given class.
func errorf(class, format string, args ...any) *Error {
	return &Error{Class: class, Msg: fmt.Sprintf(format, args...)}
}

// wrapf builds an error of the given class around a cause.
func wrapf(class string, cause error, format string, args ...any) *Error {
	return &Error{Class: class, Msg: fmt.Sprintf(format, args...), Err: cause}
}

// ExitCode maps an error class to the process exit code of section 9.5.
func ExitCode(class string) int {
	switch class {
	case "":
		return exitcode.OK
	case ClassAssert:
		return exitcode.Assert
	case ClassConfig:
		return exitcode.Config
	case ClassBudget:
		return exitcode.Budget
	default:
		return exitcode.Failure
	}
}

// retryPolicy is how many attempts a class gets and how long the first pause
// between them is (section 9.1). A class absent from the table is not retried.
type retryPolicy struct {
	attempts int
	backoff  time.Duration
}

var defaultRetries = map[string]retryPolicy{
	ClassTransient: {attempts: 2, backoff: 5 * time.Second},
	ClassSchema:    {attempts: 1, backoff: 0},
}

// neverRetried are the classes the table of section 9.1 marks as never
// retried, whatever the step's retry block says.
var neverRetried = map[string]bool{
	ClassBudget: true,
	ClassPolicy: true,
	ClassConfig: true,
}
