package scenario

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// switchInfo remembers a switch that was expanded away, so that the
// validator can still report on the subject and on missing coverage.
type switchInfo struct {
	path    string
	subject string
	line    int

	// values are the case labels, as they appear in the expanded conditions.
	values []string

	hasDefault bool
}

// expandSwitches replaces every switch step with the steps of its cases,
// each guarded by a when: that compares the subject to the case labels
// (section 3.10). A switch is sugar: after loading, the scenario holds plain
// steps and nothing downstream has to know about branches.
func expandSwitches(scn *Scenario) error {
	steps, infos, err := expandSequence(scn.Steps, "steps")
	if err != nil {
		return err
	}
	failure, failureInfos, failureErr := expandSequence(scn.OnFailure, "on_failure")
	if failureErr != nil {
		return failureErr
	}
	scn.Steps = steps
	scn.OnFailure = failure
	scn.switches = append(infos, failureInfos...)

	var errs []error
	for i := range scn.Steps {
		errs = append(errs, checkNestedSwitch(&scn.Steps[i]))
	}
	for i := range scn.OnFailure {
		errs = append(errs, checkNestedSwitch(&scn.OnFailure[i]))
	}
	return errors.Join(errs...)
}

func expandSequence(steps []Step, prefix string) ([]Step, []switchInfo, error) {
	var (
		out   []Step
		infos []switchInfo
		errs  []error
	)
	for i := range steps {
		step := &steps[i]
		if step.Switch == "" {
			out = append(out, *step)
			continue
		}
		path := fmt.Sprintf("%s[%d]", prefix, i)
		expanded, info, err := expandSwitch(step, path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, expanded...)
		infos = append(infos, info)
	}
	return out, infos, errors.Join(errs...)
}

// switchGroup collects the cases that name the same step: two labels may lead
// to the same work, and the step is then declared once, guarded by both.
type switchGroup struct {
	step       Step
	conditions []string
	body       string
}

func expandSwitch(step *Step, path string) ([]Step, switchInfo, error) {
	info := switchInfo{path: path, subject: step.Switch, line: step.Line}

	if kind := step.Kind(); kind != KindSwitch {
		return nil, info, fmt.Errorf("line %d: a switch must be the only body of its step", step.Line)
	}
	if step.ID != "" {
		return nil, info, fmt.Errorf("line %d: a switch has no id of its own; the ids are on its cases", step.Line)
	}
	if err := checkSwitchControl(step); err != nil {
		return nil, info, err
	}
	if step.Cases.Kind != yaml.MappingNode || len(step.Cases.Content) == 0 {
		return nil, info, fmt.Errorf("line %d: a switch must declare at least one case", step.Line)
	}

	var (
		order  []string
		groups = map[string]*switchGroup{}
		errs   []error
	)
	add := func(node *yaml.Node, condition string) {
		// An alias marshals back as "*anchor", so two labels pointing at the
		// same step would look like different bodies; follow it instead.
		for node.Kind == yaml.AliasNode && node.Alias != nil {
			node = node.Alias
		}
		body, err := yaml.Marshal(node)
		if err != nil {
			errs = append(errs, fmt.Errorf("line %d: %w", node.Line, err))
			return
		}
		var branch Step
		if err := node.Decode(&branch); err != nil {
			errs = append(errs, err)
			return
		}
		if branch.ID == "" {
			errs = append(errs, fmt.Errorf("line %d: a case of a switch needs an id", node.Line))
			return
		}
		group, seen := groups[branch.ID]
		if !seen {
			groups[branch.ID] = &switchGroup{step: branch, conditions: []string{condition}, body: string(body)}
			order = append(order, branch.ID)
			return
		}
		if group.body != string(body) {
			errs = append(errs, fmt.Errorf("line %d: cases naming step %q must declare the same body", node.Line, branch.ID))
			return
		}
		group.conditions = append(group.conditions, condition)
	}

	var labels []string
	for i := 0; i+1 < len(step.Cases.Content); i += 2 {
		key, value := step.Cases.Content[i], step.Cases.Content[i+1]
		literal, err := caseLiteral(key)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		labels = append(labels, literal)
		add(value, comparison(step.Switch, literal))
	}
	if !step.Default.IsZero() {
		info.hasDefault = true
		add(&step.Default, negate(step.Switch, labels))
	}
	info.values = labels
	if err := errors.Join(errs...); err != nil {
		return nil, info, err
	}

	out := make([]Step, 0, len(order))
	for _, id := range order {
		group := groups[id]
		branch := group.step
		branch.When = and(strings.Join(group.conditions, " || "), branch.When)
		branch.When = and(step.When, branch.When)
		branch.Needs = append(append([]string(nil), step.Needs...), branch.Needs...)
		out = append(out, branch)
	}
	return out, info, nil
}

// checkSwitchControl rejects the step-level fields that a switch cannot carry:
// they describe how one step runs, and a switch is not a step of its own.
func checkSwitchControl(step *Step) error {
	var named []string
	if step.Retry != nil {
		named = append(named, "retry")
	}
	if step.Timeout != 0 {
		named = append(named, "timeout")
	}
	if step.OnError != OnErrorUnset {
		named = append(named, "on_error")
	}
	if step.Fallback != nil {
		named = append(named, "fallback")
	}
	if step.Cache != nil {
		named = append(named, "cache")
	}
	if step.DedupeKey != "" {
		named = append(named, "dedupe_key")
	}
	if len(named) == 0 {
		return nil
	}
	return fmt.Errorf("line %d: a switch cannot carry %s; set it on the case steps",
		step.Line, strings.Join(named, ", "))
}

// checkNestedSwitch rejects a switch used as the body of a foreach or an
// until: those hold one step, and a switch expands into several.
func checkNestedSwitch(step *Step) error {
	var nested *Step
	switch {
	case step.Foreach != nil && step.Foreach.Step != nil:
		nested = step.Foreach.Step
	case step.Until != nil && step.Until.Step != nil:
		nested = step.Until.Step
	default:
		return nil
	}
	if nested.Switch != "" {
		return fmt.Errorf("line %d: a switch cannot be the body of a %s; use when: on the body",
			nested.Line, step.Kind())
	}
	return checkNestedSwitch(nested)
}

// caseLiteral renders a case label as an expression literal.
func caseLiteral(key *yaml.Node) (string, error) {
	switch key.Tag {
	case "!!int", "!!float", "!!bool":
		return key.Value, nil
	case "!!str":
		return strconv.Quote(key.Value), nil
	default:
		return "", fmt.Errorf("line %d: case %q must be a string, a number or a boolean", key.Line, key.Value)
	}
}

func comparison(subject, literal string) string {
	return fmt.Sprintf("(%s) == %s", subject, literal)
}

func negate(subject string, literals []string) string {
	parts := make([]string, len(literals))
	for i, literal := range literals {
		parts[i] = comparison(subject, literal)
	}
	return "!(" + strings.Join(parts, " || ") + ")"
}

func and(outer, inner string) string {
	switch {
	case strings.TrimSpace(outer) == "":
		return inner
	case strings.TrimSpace(inner) == "":
		return outer
	default:
		return fmt.Sprintf("(%s) && (%s)", outer, inner)
	}
}
