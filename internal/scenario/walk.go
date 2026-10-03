package scenario

// WalkSteps calls fn for every step of the scenario: the steps of the main
// sequence, the steps of on_failure, the bodies nested in a foreach or an
// until and the fallback bodies. The order is the order of the file, a nested
// body right after the step that holds it.
func WalkSteps(scn *Scenario, fn func(step *Step)) {
	if scn == nil || fn == nil {
		return
	}
	walkSteps(scn.Steps, fn)
	walkSteps(scn.OnFailure, fn)
}

func walkSteps(steps []Step, fn func(step *Step)) {
	for i := range steps {
		walkStep(&steps[i], fn)
	}
}

func walkStep(step *Step, fn func(step *Step)) {
	fn(step)
	if step.Foreach != nil && step.Foreach.Step != nil {
		walkStep(step.Foreach.Step, fn)
	}
	if step.Foreach != nil {
		walkSteps(step.Foreach.Steps, fn)
	}
	if step.Until != nil && step.Until.Step != nil {
		walkStep(step.Until.Step, fn)
	}
	if step.Fallback != nil {
		walkStep(step.Fallback, fn)
	}
}
