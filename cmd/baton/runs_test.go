package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestRunsListJSON(t *testing.T) {
	runsDir, runID := makeRun(t, helloScenario)

	var code int
	stdout, _ := captureOutput(t, func() {
		code = runsListCmd([]string{"--runs-dir", runsDir, "--json"})
	})
	if code != exitcode.OK {
		t.Fatalf("exit code = %d, want %d", code, exitcode.OK)
	}
	var runs []map[string]any
	if err := json.Unmarshal([]byte(stdout), &runs); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, stdout)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	if runs[0]["id"] != runID {
		t.Fatalf("id = %v, want %q", runs[0]["id"], runID)
	}
}

func TestRunsShowJSON(t *testing.T) {
	runsDir, runID := makeRun(t, helloScenario)

	var code int
	stdout, _ := captureOutput(t, func() {
		code = runsShowCmd([]string{runID, "--runs-dir", runsDir, "--json"})
	})
	if code != exitcode.OK {
		t.Fatalf("exit code = %d, want %d", code, exitcode.OK)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, stdout)
	}
	run, ok := out["run"].(map[string]any)
	if !ok {
		t.Fatalf("run field missing or not an object: %v", out)
	}
	if run["id"] != runID {
		t.Fatalf("run.id = %v, want %q", run["id"], runID)
	}
	if _, ok := out["cost"]; !ok {
		t.Fatalf("cost field missing: %v", out)
	}
}

func TestRunsPruneKeep(t *testing.T) {
	runsDir, first := makeRun(t, helloScenario)
	second := "20240102-120000-abcd"
	captureOutput(t, func() {
		runCmd([]string{filepath.Join(filepath.Dir(runsDir), "s.yaml"), "--runs-dir", runsDir, "--run-id", second})
	})

	var code int
	stdout, stderr := captureOutput(t, func() {
		code = runsCmd([]string{"prune", "--runs-dir", runsDir, "--keep", "1"})
	})
	if code != exitcode.OK {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if strings.TrimSpace(stdout) != first {
		t.Errorf("stdout = %q, want the pruned id %q", stdout, first)
	}
	if !strings.Contains(stderr, "removed 1 run") {
		t.Errorf("stderr = %q", stderr)
	}
	if _, err := os.Stat(filepath.Join(runsDir, first)); !os.IsNotExist(err) {
		t.Errorf("%s still exists: %v", first, err)
	}
	if _, err := os.Stat(filepath.Join(runsDir, second, "run.json")); err != nil {
		t.Errorf("newest run was removed: %v", err)
	}
}

func TestRunsPruneDryRunAndAge(t *testing.T) {
	runsDir, runID := makeRun(t, helloScenario)

	var code int
	stdout, stderr := captureOutput(t, func() {
		code = runsCmd([]string{"prune", "--runs-dir", runsDir, "--older-than", "1s", "--dry-run"})
	})
	// The run just finished; 1s has not necessarily passed, so only the
	// dry-run wording is certain.
	if code != exitcode.OK || !strings.Contains(stderr, "would remove") {
		t.Fatalf("exit code = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(runsDir, runID, "run.json")); err != nil {
		t.Errorf("dry run removed the run: %v", err)
	}

	stdout, _ = captureOutput(t, func() {
		code = runsCmd([]string{"prune", "--runs-dir", runsDir, "--older-than", "30d"})
	})
	if code != exitcode.OK || stdout != "" {
		t.Errorf("30d: exit code = %d, stdout = %q, want nothing removed", code, stdout)
	}
}

func TestRunsPruneRejectsBadArguments(t *testing.T) {
	for _, args := range [][]string{
		{"prune", "--runs-dir", "x"},
		{"prune", "--runs-dir", "x", "--older-than", "yesterday"},
		{"prune", "--runs-dir", "x", "--older-than", "-1d"},
	} {
		var code int
		captureOutput(t, func() { code = runsCmd(args) })
		if code != exitcode.Config {
			t.Errorf("%v: exit code = %d, want %d", args, code, exitcode.Config)
		}
	}
}

func TestParseAge(t *testing.T) {
	for text, want := range map[string]time.Duration{"30d": 30 * 24 * time.Hour, "12h": 12 * time.Hour, "90m": 90 * time.Minute} {
		got, err := parseAge(text)
		if err != nil || got != want {
			t.Errorf("parseAge(%q) = %v, %v; want %v", text, got, err, want)
		}
	}
	for _, text := range []string{"", "d", "0d", "1.5d", "soon", "-2h"} {
		if _, err := parseAge(text); err == nil {
			t.Errorf("parseAge(%q) = nil error, want error", text)
		}
	}
}
