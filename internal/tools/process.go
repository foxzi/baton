package tools

import "os/exec"

// Isolate arranges for cmd to be stopped whole when its context ends: the
// command gets its own process group where the platform has them, the group
// is signalled on cancellation, and the wait gives up after terminateGrace
// even if a child still holds the output pipes open. Every command the runner
// starts on a scenario's behalf goes through here, so a timed out step cannot
// leave work running behind it.
func Isolate(cmd *exec.Cmd) {
	isolate(cmd)
	cmd.Cancel = func() error { return terminate(cmd) }
	cmd.WaitDelay = terminateGrace
}
