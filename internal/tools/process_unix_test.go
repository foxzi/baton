//go:build unix

package tools

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/foxzi/baton/internal/scenario"
)

// TestCommandTimeoutStopsTheWholeProcessGroup checks that when a command
// times out, a child it spawned is stopped too. A surviving child would hold
// the output pipe open and keep running after the runner has moved on.
func TestCommandTimeoutStopsTheWholeProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	bin := script(t, dir, "parent.sh", "sleep 30 &\necho $! > "+pidFile+"\nwait")

	c := newCommands(t, t.TempDir(), map[string]scenario.Command{
		"parent": {Argv: []string{bin}, Timeout: scenario.Duration(200 * time.Millisecond)},
	})

	if _, err := call(t, c, "parent", `{}`); err == nil {
		t.Fatal("call: error = nil, want the timeout reported")
	}

	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the parent never recorded its child: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("child pid %q: %v", data, err)
	}

	// The signal is delivered asynchronously, so give the child a moment.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("child %d outlived the timed out command", pid)
}
