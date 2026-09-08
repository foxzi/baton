//go:build !unix

package tools

import (
	"os"
	"os/exec"
)

// isolate does nothing where process groups are not available.
func isolate(*exec.Cmd) {}

// terminate stops the command itself; its children are left to the wait
// delay.
func terminate(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Signal(os.Interrupt)
}
