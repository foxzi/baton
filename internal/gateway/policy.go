package gateway

import (
	"errors"
	"fmt"
)

// PolicyError marks a refusal a tool handler makes on policy grounds: a path
// outside the workspace or on the deny list, a host outside the fetch allow
// list, an operation the step may not perform.
//
// The agent hears the message like any other tool error and can try something
// else, but the step still fails with class policy however the agent
// continues, and the audit line says denied rather than error (section 13).
type PolicyError struct{ Err error }

func (e *PolicyError) Error() string { return e.Err.Error() }

func (e *PolicyError) Unwrap() error { return e.Err }

// Refusef builds a PolicyError. Handlers use it instead of fmt.Errorf for the
// refusals the specification classifies as policy.
func Refusef(format string, args ...any) error {
	return &PolicyError{Err: fmt.Errorf(format, args...)}
}

// isPolicy reports whether an error a handler returned is a policy refusal.
func isPolicy(err error) bool {
	var typed *PolicyError
	return errors.As(err, &typed)
}
