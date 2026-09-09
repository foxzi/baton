package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/exitcode"
)

// fileWriteScenario writes a static file as its second step, so a run
// against it always leaves one real artifact on disk.
const fileWriteScenario = `
version: 1
name: file-scenario
steps:
  - id: greet
    run:
      argv: ["echo", "hello"]
      readonly: true
      parse: text
  - id: report
    file:
      write: report.txt
      content: "hello world"
`

// 1. A run that writes a file with a "file" step reports the real path it
// wrote, read back from the step's own output.json.
func TestRunCmd_SummaryReportsArtifacts(t *testing.T) {
	dir := t.TempDir()
	scenarioPath := writeScenario(t, dir, "s.yaml", fileWriteScenario)
	runsDir := filepath.Join(dir, "runs")

	var code int
	_, stderr := captureOutput(t, func() {
		code = runCmd([]string{scenarioPath, "--runs-dir", runsDir, "--run-id", "r1"})
	})
	if code != exitcode.OK {
		t.Fatalf("runCmd() = %d, want %d\nstderr:\n%s", code, exitcode.OK, stderr)
	}

	if !strings.Contains(stderr, "artifacts:") {
		t.Fatalf("stderr missing artifacts section:\n%s", stderr)
	}
	wantPath := filepath.Join(dir, "report.txt")
	if !strings.Contains(stderr, wantPath) {
		t.Errorf("stderr = %q, want it to contain artifact path %q", stderr, wantPath)
	}
}

// 2. A scenario with no file write step reports no artifacts, and one with
// no llm step reports no cost: absent data is omitted, not guessed at or
// printed as zero.
func TestRunCmd_SummaryOmitsMissingCostAndArtifacts(t *testing.T) {
	dir := t.TempDir()
	scenarioPath := writeScenario(t, dir, "s.yaml", okScenario)
	runsDir := filepath.Join(dir, "runs")

	var code int
	_, stderr := captureOutput(t, func() {
		code = runCmd([]string{scenarioPath, "--runs-dir", runsDir, "--run-id", "r1"})
	})
	if code != exitcode.OK {
		t.Fatalf("runCmd() = %d, want %d\nstderr:\n%s", code, exitcode.OK, stderr)
	}
	if strings.Contains(stderr, "cost:") {
		t.Errorf("stderr = %q, want no cost line without a cost report", stderr)
	}
	if strings.Contains(stderr, "artifacts:") {
		t.Errorf("stderr = %q, want no artifacts section without a file write step", stderr)
	}
}

// 3. A failed run prints a ready-to-paste resume command and runs show/logs
// hints, both with the --runs-dir the run actually used rather than the
// CLI's cwd-relative default.
func TestRunCmd_SummaryFailureShowsResumeAndHints(t *testing.T) {
	dir := t.TempDir()
	scenarioPath := writeScenario(t, dir, "s.yaml", resumeScenario)
	runsDir := filepath.Join(dir, "runs")

	var code int
	_, stderr := captureOutput(t, func() {
		code = runCmd([]string{scenarioPath, "--runs-dir", runsDir, "--run-id", "base"})
	})
	if code != exitcode.Failure {
		t.Fatalf("runCmd() = %d, want %d\nstderr:\n%s", code, exitcode.Failure, stderr)
	}

	if !strings.Contains(stderr, "failed step second:") {
		t.Errorf("stderr = %q, want a failed step line", stderr)
	}

	wantResume := fmt.Sprintf("resume: baton resume base --runs-dir %s", shellQuote(runsDir))
	if !strings.Contains(stderr, wantResume) {
		t.Errorf("stderr = %q, want a resume hint %q", stderr, wantResume)
	}
	wantShow := fmt.Sprintf("details: baton runs show base --runs-dir %s", shellQuote(runsDir))
	if !strings.Contains(stderr, wantShow) {
		t.Errorf("stderr = %q, want a runs show hint %q", stderr, wantShow)
	}
	wantLogs := fmt.Sprintf("logs:    baton runs logs base --runs-dir %s", shellQuote(runsDir))
	if !strings.Contains(stderr, wantLogs) {
		t.Errorf("stderr = %q, want a runs logs hint %q", stderr, wantLogs)
	}
}

// 4. resume goes through the same execute() path, so a run that still fails
// after being resumed prints a resume hint pointing at the new run id, with
// the runs directory the resume actually used.
func TestResumeCmd_SummaryKeepsRunsDirHint(t *testing.T) {
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

	// flag.txt is still missing, so the resumed run fails again at the same step.
	_, stderr := captureOutput(t, func() {
		code = resumeCmd([]string{"base", "--runs-dir", runsDir})
	})
	if code != exitcode.Failure {
		t.Fatalf("resumeCmd() = %d, want %d\nstderr:\n%s", code, exitcode.Failure, stderr)
	}

	wantResume := fmt.Sprintf("resume: baton resume base-r1 --runs-dir %s", shellQuote(runsDir))
	if !strings.Contains(stderr, wantResume) {
		t.Errorf("stderr = %q, want a resume hint %q", stderr, wantResume)
	}
}

// 5. Artifacts of a failed file step are not reported: the step's own
// output.json records a failure, not a path.
func TestFileArtifacts_SkipsFailedStep(t *testing.T) {
	dir := t.TempDir()
	scenarioPath := writeScenario(t, dir, "s.yaml", `
version: 1
name: file-fail-scenario
steps:
  - id: report
    file:
      write: nested/../../escape.txt
      content: "nope"
`)
	runsDir := filepath.Join(dir, "runs")

	var code int
	_, stderr := captureOutput(t, func() {
		code = runCmd([]string{scenarioPath, "--runs-dir", runsDir, "--run-id", "r1"})
	})
	if code == exitcode.OK {
		t.Fatalf("runCmd() = %d, want a failure (path escapes the workspace)\nstderr:\n%s", code, stderr)
	}
	if strings.Contains(stderr, "artifacts:") {
		t.Errorf("stderr = %q, want no artifacts section for a failed write", stderr)
	}
}
