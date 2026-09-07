// Package exitcode defines the process exit codes of baton.
//
// The set is fixed by the specification (docs/ru/spec.md, section 9.5) and is
// part of the CLI contract: callers in CI pipelines branch on these values.
package exitcode

// Exit codes returned by the baton binary.
const (
	// OK means the run finished successfully.
	OK = 0
	// Failure means a step failed: transient, schema, command, timeout or policy.
	Failure = 1
	// Assert means an assert step tripped. This is not an error class.
	Assert = 2
	// Config means configuration, validation or secret resolution failed.
	Config = 3
	// Budget means the run exhausted its budget.
	Budget = 4
	// Interrupted means the run was cancelled by a signal.
	Interrupted = 130
)
