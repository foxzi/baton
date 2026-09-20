package engine

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/config"
	"github.com/foxzi/baton/internal/exitcode"
	"github.com/foxzi/baton/internal/notify"
	"github.com/foxzi/baton/internal/runstore"
	"github.com/foxzi/baton/internal/scenario"
)

const gateYAML = `
name: gated
steps:
  - id: plan
    run:
      argv: ["printf", "the plan"]
      parse: text
  - id: approval
    gate:
      message: "Apply {{ .steps.plan.result }}?"
  - id: apply
    needs: [approval]
    when: 'steps.approval.result.decision == "approved"'
    run:
      argv: ["echo", "applied"]
      parse: text
  - id: cancel
    needs: [approval]
    when: 'steps.approval.result.decision == "rejected"'
    run:
      argv: ["echo", "cancelled"]
      parse: text
`

// 1. A run stops at a gate with status waiting: the gate is recorded as
// waiting with its definition hash, the steps after it never run, the
// message is rendered into the step directory and the exit code is
// distinct from success and failure (section 3.11).
func TestGate_RunStopsWaiting(t *testing.T) {
	eng, store, _ := newTestEngine(t, gateYAML, nil)

	result, err := eng.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusWaiting || result.WaitingStep != "approval" {
		t.Fatalf("result = %+v, want waiting at approval", result)
	}
	if result.Error != nil {
		t.Errorf("error = %+v, want none", result.Error)
	}
	if result.ExitCode() != exitcode.Waiting {
		t.Errorf("exit code = %d, want %d", result.ExitCode(), exitcode.Waiting)
	}

	state, err := runstore.ReadRun(filepath.Dir(store.Dir()), "test-run")
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	if state.Status != runstore.StatusWaiting || state.WaitingStep != "approval" {
		t.Errorf("run.json status = %s waiting_step = %q, want waiting at approval", state.Status, state.WaitingStep)
	}
	if state.FinishedAt != nil {
		t.Errorf("finished_at = %v, want unset for a waiting run", state.FinishedAt)
	}
	gate := state.Steps["approval"]
	if gate == nil || gate.Status != runstore.StatusWaiting || gate.Definition == "" {
		t.Fatalf("step approval = %+v, want waiting with a definition hash", gate)
	}
	for _, id := range []string{"apply", "cancel"} {
		if _, ok := state.Steps[id]; ok {
			t.Errorf("step %s ran, want it untouched behind the gate", id)
		}
	}

	input := readFile(t, filepath.Join(store.Dir(), "steps", "approval", "input.json"))
	if !strings.Contains(input, "Apply the plan?") {
		t.Errorf("input.json = %s, want the rendered message", input)
	}
}

// 2. A resume with a decision finishes the gate with result.decision and the
// run continues; the following steps branch on it through when. A rejected
// gate is a success, not a failure.
func TestGate_DecisionContinuesRun(t *testing.T) {
	for _, tc := range []struct {
		approved bool
		verdict  string
		ran      string
		skipped  string
	}{
		{true, DecisionApproved, "apply", "cancel"},
		{false, DecisionRejected, "cancel", "apply"},
	} {
		t.Run(tc.verdict, func(t *testing.T) {
			eng, store, _ := newTestEngine(t, gateYAML, func(opts *Options) {
				opts.Decision = &Decision{
					Step:       "approval",
					Definition: gateDefinition(t),
					Approved:   tc.approved,
					Reason:     "because",
				}
			})

			result, err := eng.Run(t.Context())
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if result.Status != runstore.StatusSuccess {
				t.Fatalf("status = %s, error = %+v, want success", result.Status, result.Error)
			}

			state, err := runstore.ReadRun(filepath.Dir(store.Dir()), "test-run")
			if err != nil {
				t.Fatalf("ReadRun: %v", err)
			}
			if gate := state.Steps["approval"]; gate == nil || gate.Status != runstore.StatusSuccess {
				t.Errorf("step approval = %+v, want success", gate)
			}
			if step := state.Steps[tc.ran]; step == nil || step.Status != runstore.StatusSuccess {
				t.Errorf("step %s = %+v, want success", tc.ran, step)
			}
			if step := state.Steps[tc.skipped]; step == nil || step.Status != runstore.StatusSkipped {
				t.Errorf("step %s = %+v, want skipped", tc.skipped, step)
			}

			output := readFile(t, filepath.Join(store.Dir(), "steps", "approval", "output.json"))
			if !strings.Contains(output, `"decision": "`+tc.verdict+`"`) || !strings.Contains(output, `"reason": "because"`) {
				t.Errorf("output.json = %s, want decision %s with the reason", output, tc.verdict)
			}
		})
	}
}

// 3. A decision for a gate whose definition changed since the waiting run is
// not taken: the edited gate asks again.
func TestGate_ChangedGateAsksAgain(t *testing.T) {
	eng, _, _ := newTestEngine(t, gateYAML, func(opts *Options) {
		opts.Decision = &Decision{Step: "approval", Definition: "stale", Approved: true}
	})

	result, err := eng.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusWaiting {
		t.Fatalf("status = %s, want waiting for a changed gate", result.Status)
	}
}

// 4. A gate with a channel sends its message there, with the resume command
// appended so the reader knows how to answer.
func TestGate_NotifiesChannel(t *testing.T) {
	yamlText := `
name: gated
steps:
  - id: approval
    gate:
      message: "Deploy?"
      notify: ops
`
	var out strings.Builder
	eng, _, _ := newTestEngine(t, yamlText, func(opts *Options) {
		opts.Stdout = &out
		opts.Channels = map[string]notify.Channel{
			"ops": {Name: "ops", Kind: config.ChannelKindStdout},
		}
	})

	result, err := eng.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusWaiting {
		t.Fatalf("status = %s, error = %+v, want waiting", result.Status, result.Error)
	}
	sent := out.String()
	if !strings.Contains(sent, "Deploy?") || !strings.Contains(sent, "baton resume test-run --approve") {
		t.Errorf("channel message = %q, want the question and the resume command", sent)
	}
}

// 5. A gate whose channel is not configured fails with a config error
// instead of waiting: nobody would ever be asked.
func TestGate_UnknownChannelFails(t *testing.T) {
	yamlText := `
name: gated
steps:
  - id: approval
    gate:
      message: "Deploy?"
      notify: nowhere
`
	eng, _, _ := newTestEngine(t, yamlText, nil)

	result, err := eng.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusFailed || result.Error == nil || result.Error.Class != ClassConfig {
		t.Fatalf("result = %+v, want a config failure", result)
	}
}

// gateDefinition is the hash the waiting run recorded for the approval gate
// of gateYAML, as a resume reads it back from run.json.
func gateDefinition(t *testing.T) string {
	t.Helper()
	return definitionHash(&scenario.Step{
		ID:   "approval",
		Gate: &scenario.GateStep{Message: "Apply {{ .steps.plan.result }}?"},
	})
}
