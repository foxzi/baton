package engine

import (
	"context"
	"testing"

	"github.com/foxzi/baton/internal/runstore"
)

// switchScenario branches on an input: one case per risk level plus a default.
const switchScenario = `
name: switch
version: 1
inputs:
  risk: { type: string, required: true }
steps:
  - switch: inputs.risk
    cases:
      high: { id: deep, run: { argv: ["echo", "deep"], parse: text } }
      low: { id: light, run: { argv: ["echo", "light"], parse: text } }
    default: { id: unknown, run: { argv: ["echo", "unknown"], parse: text } }
`

func TestRun_SwitchTakesTheMatchingCase(t *testing.T) {
	tests := []struct {
		risk string
		want string
	}{
		{risk: "high", want: "deep"},
		{risk: "low", want: "light"},
		{risk: "other", want: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.risk, func(t *testing.T) {
			eng, _, _ := newTestEngine(t, switchScenario, func(o *Options) {
				o.Inputs = map[string]any{"risk": tt.risk}
			})

			result, err := eng.Run(context.Background())
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if result.Status != runstore.StatusSuccess {
				t.Fatalf("run failed: %v", result.Error)
			}

			for _, id := range []string{"deep", "light", "unknown"} {
				step, ok := eng.steps[id]
				if !ok {
					t.Fatalf("step %q was not recorded", id)
				}
				taken := step.Status == runstore.StatusSuccess
				if taken != (id == tt.want) {
					t.Errorf("step %q status = %q, want it %s",
						id, step.Status, map[bool]string{true: "taken", false: "skipped"}[id == tt.want])
				}
			}
		})
	}
}
