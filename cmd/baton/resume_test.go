package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/exitcode"
	"github.com/foxzi/baton/internal/runstore"
)

// resumeScenario fails in its second step until flag.txt exists.
const resumeScenario = `
version: 1
name: resume-me
steps:
  - id: first
    run:
      argv: ["echo", "one"]
      readonly: true
      parse: text
  - id: second
    run:
      argv: ["cat", "flag.txt"]
      readonly: true
      parse: text
`

// 1. A failed run is continued at the step that failed: the finished step is
// replayed from the original run directory, the new run gets an -r1 id and
// points back at the original (section 10.4).
func TestResumeCmd_ContinuesAtFailedStep(t *testing.T) {
	dir := t.TempDir()
	scenarioPath := writeScenario(t, dir, "s.yaml", resumeScenario)
	runsDir := filepath.Join(dir, "runs")

	var code int
	captureOutput(t, func() {
		code = runCmd([]string{scenarioPath, "--runs-dir", runsDir, "--run-id", "base"})
	})
	if code != exitcode.Failure {
		t.Fatalf("first run: runCmd() = %d, want %d", code, exitcode.Failure)
	}

	if err := os.WriteFile(filepath.Join(dir, "flag.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}

	captureOutput(t, func() {
		code = resumeCmd([]string{"base", "--runs-dir", runsDir})
	})
	if code != exitcode.OK {
		t.Fatalf("resume: resumeCmd() = %d, want %d", code, exitcode.OK)
	}

	state, err := runstore.ReadRun(runsDir, "base-r1")
	if err != nil {
		t.Fatalf("ReadRun(base-r1) error = %v", err)
	}
	if state.Status != runstore.StatusSuccess {
		t.Errorf("status = %s, want %s", state.Status, runstore.StatusSuccess)
	}
	if state.ResumeOf != "base" {
		t.Errorf("resume_of = %q, want base", state.ResumeOf)
	}
	first := state.Steps["first"]
	if first == nil || !first.Resumed {
		t.Fatalf("step first = %+v, want a resumed record", first)
	}
	if second := state.Steps["second"]; second == nil || second.Status != runstore.StatusSuccess {
		t.Fatalf("step second = %+v, want success", second)
	}

	// The replayed step keeps its output in the new run directory.
	output := readStepOutput(t, runsDir, "base-r1", "first")
	if output["resumed"] != true {
		t.Errorf("output.json resumed = %v, want true", output["resumed"])
	}
	if result, _ := output["result"].(string); !strings.Contains(result, "one") {
		t.Errorf("replayed result = %q, want it to contain one", result)
	}
	stdout, err := os.ReadFile(filepath.Join(runsDir, "base-r1", "steps", "first", "stdout.log"))
	if err != nil || !strings.Contains(string(stdout), "one") {
		t.Errorf("replayed stdout.log = %q, err = %v", stdout, err)
	}
}

// 2. Resuming twice keeps the id of the original run and counts up the
// suffix.
func TestResumeCmd_SecondResumeCountsUp(t *testing.T) {
	dir := t.TempDir()
	scenarioPath := writeScenario(t, dir, "s.yaml", resumeScenario)
	runsDir := filepath.Join(dir, "runs")

	var code int
	captureOutput(t, func() {
		code = runCmd([]string{scenarioPath, "--runs-dir", runsDir, "--run-id", "base"})
	})
	if code != exitcode.Failure {
		t.Fatalf("first run: runCmd() = %d, want %d", code, exitcode.Failure)
	}
	captureOutput(t, func() {
		code = resumeCmd([]string{"base", "--runs-dir", runsDir})
	})
	if code != exitcode.Failure {
		t.Fatalf("first resume: resumeCmd() = %d, want %d", code, exitcode.Failure)
	}

	if err := os.WriteFile(filepath.Join(dir, "flag.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}
	captureOutput(t, func() {
		code = resumeCmd([]string{"base-r1", "--runs-dir", runsDir})
	})
	if code != exitcode.OK {
		t.Fatalf("second resume: resumeCmd() = %d, want %d", code, exitcode.OK)
	}

	state, err := runstore.ReadRun(runsDir, "base-r2")
	if err != nil {
		t.Fatalf("ReadRun(base-r2) error = %v", err)
	}
	if state.ResumeOf != "base-r1" {
		t.Errorf("resume_of = %q, want base-r1", state.ResumeOf)
	}
	if state.Status != runstore.StatusSuccess {
		t.Errorf("status = %s, want %s", state.Status, runstore.StatusSuccess)
	}
}

// 3. A successful run has nothing to resume, and an unknown id is a
// configuration error.
func TestResumeCmd_NothingToResume(t *testing.T) {
	dir := t.TempDir()
	scenarioPath := writeScenario(t, dir, "s.yaml", okScenario)
	runsDir := filepath.Join(dir, "runs")

	var code int
	captureOutput(t, func() {
		code = runCmd([]string{scenarioPath, "--runs-dir", runsDir, "--run-id", "base"})
	})
	if code != exitcode.OK {
		t.Fatalf("run: runCmd() = %d, want %d", code, exitcode.OK)
	}

	var stderr string
	_, stderr = captureOutput(t, func() {
		code = resumeCmd([]string{"base", "--runs-dir", runsDir})
	})
	if code != exitcode.Config {
		t.Errorf("resume of a successful run: resumeCmd() = %d, want %d", code, exitcode.Config)
	}
	if !strings.Contains(stderr, "nothing to resume") {
		t.Errorf("stderr = %q, want it to explain that there is nothing to resume", stderr)
	}

	_, stderr = captureOutput(t, func() {
		code = resumeCmd([]string{"missing", "--runs-dir", runsDir})
	})
	if code != exitcode.Config {
		t.Errorf("resume of an unknown run: resumeCmd() = %d, want %d", code, exitcode.Config)
	}
	if !strings.Contains(stderr, "run.json") {
		t.Errorf("stderr = %q, want it to name the missing file", stderr)
	}
}

// 4. Without a run id the command prints its usage.
func TestResumeCmd_WrongArgCount(t *testing.T) {
	var code int
	_, stderr := captureOutput(t, func() {
		code = resumeCmd(nil)
	})
	if code != exitcode.Config {
		t.Errorf("resumeCmd(nil) = %d, want %d", code, exitcode.Config)
	}
	if !strings.Contains(stderr, "Usage: baton resume") {
		t.Errorf("stderr = %q, want the usage message", stderr)
	}
}
