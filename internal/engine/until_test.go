package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/foxzi/baton/internal/runstore"
)

// countingScenario runs a script that counts its own invocations in a file
// and exits 1 until it has been called succeedAt times.
func countingScenario(t *testing.T, succeedAt, maxIterations int) (string, string) {
	t.Helper()
	counter := filepath.Join(t.TempDir(), "count")
	script := fmt.Sprintf(
		`n=$(cat %[1]s 2>/dev/null || echo 0); n=$((n+1)); printf %%s "$n" > %[1]s; printf %%s "$n"; test "$n" -ge %[2]d`,
		counter, succeedAt)
	return fmt.Sprintf(`
name: until
version: 1
steps:
  - id: fix
    until:
      condition: "iter.exit_code == 0"
      max_iterations: %d
      step:
        run:
          argv: ["sh", "-c", %q]
          parse: text
          allow_exit_codes: [0, 1]
`, maxIterations, script), counter
}

func TestRun_UntilStopsOnCondition(t *testing.T) {
	source, counter := countingScenario(t, 3, 5)
	eng, store, _ := newTestEngine(t, source, nil)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("run failed: %v", result.Error)
	}

	step := eng.steps["fix"]
	if step.Iterations != 3 {
		t.Errorf("iterations = %d, want 3", step.Iterations)
	}
	if step.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", step.ExitCode)
	}
	// The result is the result of the last iteration.
	if got, want := step.Result, "3"; got != want {
		t.Errorf("result = %v, want %v", got, want)
	}
	if got := readFile(t, counter); got != "3" {
		t.Errorf("the body ran %s times, want 3", got)
	}

	// Every iteration has its own directory under the step.
	for i, want := range []string{"1", "2", "3"} {
		path := filepath.Join(store.Dir(), "steps", "fix", fmt.Sprint(i), "stdout.log")
		if got := readFile(t, path); got != want {
			t.Errorf("iteration %d stdout = %q, want %q", i, got, want)
		}
	}

	var output struct {
		Result     any `json:"result"`
		Iterations int `json:"iterations"`
	}
	data, err := os.ReadFile(filepath.Join(store.Dir(), "steps", "fix", "output.json"))
	if err != nil {
		t.Fatalf("read output.json: %v", err)
	}
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatalf("output.json: %v", err)
	}
	if output.Iterations != 3 {
		t.Errorf("output.json iterations = %d, want 3", output.Iterations)
	}
}

// A condition that never holds is not an error: the step reports the last
// iteration, and the scenario decides what that means.
func TestRun_UntilExhaustsIterations(t *testing.T) {
	source, counter := countingScenario(t, 9, 2)
	eng, _, _ := newTestEngine(t, source, nil)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("run failed: %v", result.Error)
	}
	if got := eng.steps["fix"].Iterations; got != 2 {
		t.Errorf("iterations = %d, want 2", got)
	}
	if got := eng.steps["fix"].ExitCode; got != 1 {
		t.Errorf("exit_code = %d, want 1", got)
	}
	if got := readFile(t, counter); got != "2" {
		t.Errorf("the body ran %s times, want 2", got)
	}
}

// The previous iteration is in scope of the body's templates too.
func TestRun_UntilIterInTemplates(t *testing.T) {
	eng, _, _ := newTestEngine(t, `
name: until
version: 1
steps:
  - id: count
    until:
      condition: "iter.result == 'xxx'"
      max_iterations: 5
      step:
        run:
          argv: ["sh", "-c", "printf 'x{{ default \"\" .iter.result }}'"]
          parse: text
`, nil)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("run failed: %v", result.Error)
	}
	step := eng.steps["count"]
	if step.Iterations != 3 {
		t.Errorf("iterations = %d, want 3", step.Iterations)
	}
	if step.Result != "xxx" {
		t.Errorf("result = %v, want xxx", step.Result)
	}
}

// A failing iteration fails the step; the iterations already done are kept.
func TestRun_UntilBodyFails(t *testing.T) {
	var events []Event
	eng, _, _ := newTestEngine(t, `
name: until
version: 1
steps:
  - id: fix
    until:
      condition: "false"
      max_iterations: 3
      step:
        run: { argv: ["sh", "-c", "exit 7"] }
`, func(o *Options) {
		o.Observer = func(e Event) { events = append(events, e) }
	})

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusFailed {
		t.Fatalf("status = %s, want failed", result.Status)
	}
	if result.Error == nil || result.Error.Class != ClassCommand {
		t.Fatalf("error = %+v, want a command-class error", result.Error)
	}
	found := false
	for _, event := range events {
		if event.Type == "until_iteration_failed" {
			found = true
		}
	}
	if !found {
		t.Errorf("no until_iteration_failed event in %v", events)
	}
}
