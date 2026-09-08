//go:build unix

package tools

import (
	"os/exec"
	"syscall"
)

// isolate puts the command in its own process group, so that a command that
// spawns children (a build driver, a test runner) can be stopped whole.
func isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminate asks the command's whole process group to stop. A child holding
// the output pipe open would otherwise keep the runner waiting for as long
// as it likes.
func terminate(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	return nil
}
