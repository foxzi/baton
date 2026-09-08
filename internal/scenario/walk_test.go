package scenario

import "testing"

// TestWalkSteps checks that the walk reaches the main sequence, the nested
// body of a foreach and the steps of on_failure, in file order.
func TestWalkSteps(t *testing.T) {
	scn, err := Parse([]byte(`
version: 1
name: walk
steps:
  - id: first
    run:
      argv: ["true"]
  - id: each
    foreach:
      items: "{{ .inputs.repos }}"
      as: repo
      step:
        run:
          argv: ["true"]
on_failure:
  - id: tell
    notify: ops
    message: "failed"
`), "test.yaml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	var ids []string
	WalkSteps(scn, func(step *Step) {
		if step.ID == "" {
			ids = append(ids, "<nested>")
			return
		}
		ids = append(ids, step.ID)
	})

	want := []string{"first", "each", "<nested>", "tell"}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids = %v, want %v", ids, want)
		}
	}
}

// TestWalkStepsTolerantOfNil checks that a nil scenario or a nil function is
// not a panic.
func TestWalkStepsTolerantOfNil(t *testing.T) {
	WalkSteps(nil, func(*Step) { t.Error("fn called for a nil scenario") })
	WalkSteps(&Scenario{Steps: []Step{{ID: "a"}}}, nil)
}
