package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/foxzi/baton/internal/expr"
	"github.com/foxzi/baton/internal/runstore"
	"github.com/foxzi/baton/internal/scenario"
)

// LoadResume reads the outputs of the steps a previous run finished
// successfully, keyed by step path (section 10.4). A resumed run replays
// these and starts executing at the step that failed.
//
// A successful step whose output.json cannot be read is left out: the step
// runs again, which is slower but never wrong.
func LoadResume(runDir string, state *runstore.RunState) (map[string]expr.Step, error) {
	if state == nil {
		return nil, fmt.Errorf("engine: no run state to resume")
	}
	steps := map[string]expr.Step{}
	for id, step := range state.Steps {
		if step.Status != runstore.StatusSuccess {
			continue
		}
		out, err := readStepOutput(filepath.Join(runDir, "steps", filepath.FromSlash(id)))
		if err != nil {
			continue
		}
		steps[id] = out
	}
	return steps, nil
}

// readStepOutput rebuilds the step result from the files of a step directory.
func readStepOutput(dir string) (expr.Step, error) {
	data, err := os.ReadFile(filepath.Join(dir, "output.json"))
	if err != nil {
		return expr.Step{}, err
	}
	var out cachedOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return expr.Step{}, err
	}
	step := out.step()
	// stdout and stderr live in their own files, not in output.json.
	if stdout, err := os.ReadFile(filepath.Join(dir, "stdout.log")); err == nil {
		step.Stdout = string(stdout)
	}
	if stderr, err := os.ReadFile(filepath.Join(dir, "stderr.log")); err == nil {
		step.Stderr = string(stderr)
	}
	return step, nil
}

// resumeStep replays a step the previous run already finished instead of
// executing it, and reports whether it did (section 10.4). The replayed step
// gets a step directory and a run.json record of its own, so that the
// resumed run directory is complete on its own.
func (e *Engine) resumeStep(step *scenario.Step, path string) bool {
	out, ok := e.opts.Resume[path]
	if !ok {
		return false
	}
	e.replayStepFiles(path, string(step.Kind()), "resumed", newCachedOutput(out))

	now := e.opts.Now()
	state := &runstore.StepState{
		Status:     runstore.StatusSuccess,
		StartedAt:  now,
		FinishedAt: &now,
		Resumed:    true,
	}
	if step.Run != nil {
		code := out.ExitCode
		state.ExitCode = &code
	}
	out.Status = expr.StatusSuccess
	e.steps[step.ID] = out
	e.record(step.ID, state)
	e.emit(Event{Type: "step_resumed", Step: step.ID})
	return true
}

// scenarioPath is the scenario location as run.json records it: absolute
// where possible, so that a resume started from another directory still
// finds the file.
func scenarioPath(path string) string {
	if path == "" {
		return ""
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return absolute
}
