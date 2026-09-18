package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/expr"
	"github.com/foxzi/baton/internal/runstore"
	"github.com/foxzi/baton/internal/scenario"
)

// 1. A resumed step is not executed: the command that would fail is never
// started, and its replayed result is visible to the steps that follow
// (section 10.4).
func TestResume_ReplayedStepIsNotExecuted(t *testing.T) {
	yamlText := `
name: resume
steps:
  - id: first
    run:
      argv: ["false"]
  - id: second
    run:
      argv: ["echo", "{{ .steps.first.result }}"]
      parse: text
`
	eng, store, _ := newTestEngine(t, yamlText, func(opts *Options) {
		opts.Resume = map[string]ResumedStep{
			"first": {
				Output:     expr.Step{Result: "one", Stdout: "one\n", ExitCode: 0},
				Definition: definitionHash(&scenario.Step{ID: "first", Run: &scenario.RunStep{Argv: []string{"false"}}}),
			},
		}
		opts.ResumeOf = "base"
	})

	result, err := eng.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != "success" {
		t.Fatalf("status = %s, error = %+v", result.Status, result.Error)
	}

	second := readFile(t, filepath.Join(store.Dir(), "steps", "second", "stdout.log"))
	if strings.TrimSpace(second) != "one" {
		t.Errorf("second step stdout = %q, want the replayed result of first", second)
	}

	state, err := runstore.ReadRun(filepath.Dir(store.Dir()), "test-run")
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	if state.ResumeOf != "base" {
		t.Errorf("resume_of = %q, want base", state.ResumeOf)
	}
	first := state.Steps["first"]
	if first == nil || !first.Resumed || first.Status != runstore.StatusSuccess {
		t.Fatalf("step first = %+v, want a successful resumed record", first)
	}
	output := readStepJSON(t, filepath.Join(store.Dir(), "steps", "first", "output.json"))
	if output["resumed"] != true || output["result"] != "one" {
		t.Errorf("output.json = %v, want the replayed result marked resumed", output)
	}
	if stdout := readFile(t, filepath.Join(store.Dir(), "steps", "first", "stdout.log")); stdout != "one\n" {
		t.Errorf("stdout.log = %q, want the replayed stdout", stdout)
	}
	if _, err := os.Stat(filepath.Join(store.Dir(), "steps", "first", "input.json")); !os.IsNotExist(err) {
		t.Errorf("a replayed step wrote input.json (err = %v)", err)
	}
}

// 2. LoadResume takes the outputs of the successful steps of a run directory
// and leaves the failed and skipped ones out.
func TestLoadResume_OnlySuccessfulSteps(t *testing.T) {
	yamlText := `
name: partial
steps:
  - id: ok
    run:
      argv: ["echo", "done"]
      parse: text
  - id: nope
    run:
      argv: ["false"]
`
	eng, store, _ := newTestEngine(t, yamlText, nil)
	result, err := eng.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != "failed" {
		t.Fatalf("status = %s, want failed", result.Status)
	}

	runsDir := filepath.Dir(store.Dir())
	state, err := runstore.ReadRun(runsDir, "test-run")
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	resume, err := LoadResume(store.Dir(), state)
	if err != nil {
		t.Fatalf("LoadResume: %v", err)
	}
	if len(resume) != 1 {
		t.Fatalf("resume = %v, want only the successful step", resume)
	}
	ok, found := resume["ok"]
	if !found {
		t.Fatalf("resume = %v, want the ok step", resume)
	}
	if ok.Output.Result != "done\n" {
		t.Errorf("resumed result = %q, want the parsed output", ok.Output.Result)
	}
	if ok.Output.Stdout != "done\n" {
		t.Errorf("resumed stdout = %q, want the recorded stdout", ok.Output.Stdout)
	}
	if ok.Definition == "" || ok.Definition != state.Steps["ok"].Definition {
		t.Errorf("resumed definition = %q, want the hash run.json recorded (%q)", ok.Definition, state.Steps["ok"].Definition)
	}
}

// 4. A step edited since the previous run is executed again instead of
// replayed, and so is a record without a definition hash from an older
// baton (section 10.4).
func TestResume_ChangedStepIsExecutedAgain(t *testing.T) {
	yamlText := `
name: changed
steps:
  - id: first
    run:
      argv: ["echo", "fresh"]
      parse: text
`
	cases := map[string]string{
		"changed definition": "stale-hash",
		"missing definition": "",
	}
	for name, definition := range cases {
		t.Run(name, func(t *testing.T) {
			eng, store, _ := newTestEngine(t, yamlText, func(opts *Options) {
				opts.Resume = map[string]ResumedStep{
					"first": {Output: expr.Step{Result: "stale", Stdout: "stale\n"}, Definition: definition},
				}
				opts.ResumeOf = "base"
			})
			result, err := eng.Run(t.Context())
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if result.Status != "success" {
				t.Fatalf("status = %s, error = %+v", result.Status, result.Error)
			}
			stdout := readFile(t, filepath.Join(store.Dir(), "steps", "first", "stdout.log"))
			if strings.TrimSpace(stdout) != "fresh" {
				t.Errorf("stdout = %q, want the step executed again, not replayed", stdout)
			}
			state, err := runstore.ReadRun(filepath.Dir(store.Dir()), "test-run")
			if err != nil {
				t.Fatalf("ReadRun: %v", err)
			}
			if first := state.Steps["first"]; first == nil || first.Resumed || first.Definition == "" {
				t.Errorf("step first = %+v, want an executed record with a definition hash", first)
			}
		})
	}
}

// 3. Without a run state there is nothing to resume.
func TestLoadResume_NoState(t *testing.T) {
	if _, err := LoadResume("", nil); err == nil {
		t.Fatal("LoadResume(nil) error = nil, want an error")
	}
}
