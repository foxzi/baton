package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/exitcode"
)

// makeRun executes a small scenario and returns the runs directory and the id
// of the run it produced.
func makeRun(t *testing.T, scenarioYAML string) (runsDir, runID string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "s.yaml")
	if err := os.WriteFile(path, []byte(scenarioYAML), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}
	runsDir = filepath.Join(dir, "runs")
	runID = "20240101-120000-abcd"
	captureOutput(t, func() {
		runCmd([]string{path, "--runs-dir", runsDir, "--run-id", runID})
	})
	if _, err := os.Stat(filepath.Join(runsDir, runID, "run.json")); err != nil {
		t.Fatalf("run.json: %v", err)
	}
	return runsDir, runID
}

const helloScenario = `
name: hello
version: 1
steps:
  - id: greet
    run:
      argv: ["sh", "-c", "printf 'hi there'; printf 'oops' >&2"]
      parse: text
`

func TestRunsList(t *testing.T) {
	runsDir, runID := makeRun(t, helloScenario)

	var code int
	stdout, _ := captureOutput(t, func() {
		code = runsCmd([]string{"list", "--runs-dir", runsDir})
	})
	if code != exitcode.OK {
		t.Fatalf("exit code = %d, want %d", code, exitcode.OK)
	}
	if !strings.Contains(stdout, runID) || !strings.Contains(stdout, "success") || !strings.Contains(stdout, "hello") {
		t.Fatalf("list output = %q", stdout)
	}
}

// -n caps the listing, newest first.
func TestRunsListLimit(t *testing.T) {
	runsDir, _ := makeRun(t, helloScenario)
	for _, id := range []string{"20240101-130000-bbbb", "20240101-140000-cccc"} {
		captureOutput(t, func() {
			path := filepath.Join(t.TempDir(), "s.yaml")
			os.WriteFile(path, []byte(helloScenario), 0o644)
			runCmd([]string{path, "--runs-dir", runsDir, "--run-id", id})
		})
	}

	stdout, _ := captureOutput(t, func() {
		runsCmd([]string{"list", "--runs-dir", runsDir, "-n", "1"})
	})
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1:\n%s", len(lines), stdout)
	}
	if !strings.Contains(lines[0], "20240101-140000-cccc") {
		t.Fatalf("newest run is not first: %q", lines[0])
	}
}

// A directory with no runs lists nothing and still succeeds.
func TestRunsListEmpty(t *testing.T) {
	var code int
	stdout, _ := captureOutput(t, func() {
		code = runsCmd([]string{"list", "--runs-dir", filepath.Join(t.TempDir(), "absent")})
	})
	if code != exitcode.OK {
		t.Fatalf("exit code = %d, want %d", code, exitcode.OK)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("output = %q, want none", stdout)
	}
}

func TestRunsShow(t *testing.T) {
	runsDir, runID := makeRun(t, helloScenario)

	var code int
	stdout, _ := captureOutput(t, func() {
		code = runsCmd([]string{"show", runID, "--runs-dir", runsDir})
	})
	if code != exitcode.OK {
		t.Fatalf("exit code = %d, want %d", code, exitcode.OK)
	}
	for _, want := range []string{runID, "hello", "success", "greet"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("show output does not mention %q:\n%s", want, stdout)
		}
	}
}

// A failed run shows the failing step and its error class.
func TestRunsShowFailure(t *testing.T) {
	runsDir, runID := makeRun(t, `
name: broken
version: 1
steps:
  - id: boom
    run:
      argv: ["false"]
`)

	stdout, _ := captureOutput(t, func() {
		runsCmd([]string{"show", runID, "--runs-dir", runsDir})
	})
	for _, want := range []string{"failed", "boom", "command"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("show output does not mention %q:\n%s", want, stdout)
		}
	}
}

func TestRunsShowUnknownRun(t *testing.T) {
	runsDir, _ := makeRun(t, helloScenario)
	var code int
	captureOutput(t, func() {
		code = runsCmd([]string{"show", "nope", "--runs-dir", runsDir})
	})
	if code != exitcode.Config {
		t.Fatalf("exit code = %d, want %d", code, exitcode.Config)
	}
}

func TestRunsLogs(t *testing.T) {
	runsDir, runID := makeRun(t, helloScenario)

	var code int
	stdout, _ := captureOutput(t, func() {
		code = runsCmd([]string{"logs", runID, "--runs-dir", runsDir})
	})
	if code != exitcode.OK {
		t.Fatalf("exit code = %d, want %d", code, exitcode.OK)
	}
	if !strings.Contains(stdout, "hi there") || !strings.Contains(stdout, "oops") {
		t.Fatalf("logs output = %q", stdout)
	}
	if !strings.Contains(stdout, "=== greet stdout.log") {
		t.Fatalf("logs output has no step header: %q", stdout)
	}
}

func TestRunsLogsUnknownStep(t *testing.T) {
	runsDir, runID := makeRun(t, helloScenario)
	var code int
	captureOutput(t, func() {
		code = runsCmd([]string{"logs", runID, "--runs-dir", runsDir, "--step", "absent"})
	})
	if code != exitcode.Config {
		t.Fatalf("exit code = %d, want %d", code, exitcode.Config)
	}
}

func TestRunsUnknownSubcommand(t *testing.T) {
	var code int
	captureOutput(t, func() {
		code = runsCmd([]string{"nope"})
	})
	if code != exitcode.Config {
		t.Fatalf("exit code = %d, want %d", code, exitcode.Config)
	}
}

// BATON_RUNS_DIR is the fallback when --runs-dir is absent.
func TestRunsDirFromEnv(t *testing.T) {
	runsDir, runID := makeRun(t, helloScenario)
	t.Setenv("BATON_RUNS_DIR", runsDir)

	stdout, _ := captureOutput(t, func() {
		runsCmd([]string{"list"})
	})
	if !strings.Contains(stdout, runID) {
		t.Fatalf("list output = %q", stdout)
	}
}
