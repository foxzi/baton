//go:build unix

package engine

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A run step that times out is stopped together with the processes it
// started: a child left behind would keep running after the run has moved
// on, or hold the output pipe open and keep the step waiting.
func TestRun_TimeoutStopsTheWholeProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	script := filepath.Join(dir, "parent.sh")
	body := "#!/bin/sh\nsleep 30 &\necho $! > " + pidFile + "\nwait\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	yamlText := `
version: 1
name: group
steps:
  - id: parent
    timeout: 200ms
    run:
      argv: ["` + script + `"]
`
	eng, _, _ := newTestEngine(t, yamlText, nil)

	started := time.Now()
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("the run took %v: the step waited for the child", elapsed)
	}
	if result.Status != "failed" || result.Error == nil || result.Error.Class != ClassTimeout {
		t.Fatalf("status = %s error = %+v, want a timeout", result.Status, result.Error)
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
	t.Fatalf("child %d outlived the timed out step", pid)
}
