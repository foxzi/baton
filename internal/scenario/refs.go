package scenario

import (
	"regexp"
	"strings"
)

// stepRefPattern matches a reference to a step in an expression or in a
// template action: steps.<id> optionally followed by the field read from it.
var stepRefPattern = regexp.MustCompile(`\bsteps\.([a-z][a-z0-9_]*)(?:\.([a-z][a-z0-9_]*))?`)

// templateActionPattern matches one {{ ... }} action of a template. Only what
// is inside an action is an expression; the text around it is literal.
var templateActionPattern = regexp.MustCompile(`(?s)\{\{(.*?)\}\}`)

// guardPattern matches the ways an expression can handle a missing value
// (spec section 4, check 6).
var guardPattern = regexp.MustCompile(`\bcoalesce\b|\bdefault\b|\?\?`)

// stepScope is the step whose expressions are being validated: references in
// it may only name the steps declared before it.
type stepScope struct {
	path    string
	earlier map[string]bool

	// conditional says the step itself runs under a when:, in which case it
	// already handles being skipped and check 6 does not apply to it.
	conditional bool

	// ordered says references must name a step declared earlier. In
	// on_failure every step of the run has already had its chance to run, so
	// the whole main sequence is in scope and nothing about it is certain.
	ordered bool
}

// stepRef is one reference to a step, kept until every step id is known.
type stepRef struct {
	path    string
	line    int
	scope   *stepScope
	id      string
	field   string
	guarded bool
}

// collectRefs records the step references of one expression source. Templates
// pass their whole text: only the {{ ... }} actions in it are expressions.
func collectRefs(res *Result, path, source string, line int, template bool) {
	if res == nil || res.scope == nil || source == "" {
		return
	}
	if !template {
		res.collectFrom(path, source, line)
		return
	}
	for _, action := range templateActionPattern.FindAllStringSubmatch(source, -1) {
		res.collectFrom(path, action[1], line)
	}
}

func (r *Result) collectFrom(path, source string, line int) {
	guarded := guardPattern.MatchString(source)
	for _, m := range stepRefPattern.FindAllStringSubmatch(source, -1) {
		r.refs = append(r.refs, stepRef{
			path:    path,
			line:    line,
			scope:   r.scope,
			id:      m[1],
			field:   m[2],
			guarded: guarded,
		})
	}
}

// resolveRefs reports the references that cannot hold: to a step that is not
// in scope (section 4, check 5) and to the result of a step that may be
// skipped, from a step that cannot be skipped (check 6).
func resolveRefs(res *Result, conditional map[string]bool) {
	for _, ref := range res.refs {
		switch {
		case !ref.scope.earlier[ref.id]:
			if ref.scope.ordered {
				res.errorf(ref.path, ref.line,
					"references step %q, which is not declared before this step", ref.id)
			} else {
				res.errorf(ref.path, ref.line, "references undeclared step %q", ref.id)
			}
		case ref.field == "" || ref.field == "status":
			// The status of a step is known however the step ended.
		case ref.guarded || ref.scope.conditional || !conditional[ref.id]:
			// Either the reference handles a missing value, or the step
			// holding it is skipped along with the step it references, or the
			// referenced step always runs.
		default:
			res.errorf(ref.path, ref.line,
				"reads %s of step %q, which runs under a when:; guard it with coalesce/default or give this step a when: too",
				ref.field, ref.id)
		}
	}
}

// conditionalSteps collects the ids of the steps that run under a when: and
// may therefore be skipped. A switch case is one of them: the expansion of a
// switch puts the comparison of the subject in its when:.
func conditionalSteps(scn *Scenario) map[string]bool {
	out := map[string]bool{}
	WalkSteps(scn, func(step *Step) {
		if step.ID != "" && strings.TrimSpace(step.When) != "" {
			out[step.ID] = true
		}
	})
	return out
}

// stepIDs collects every step id of a sequence, for the on_failure scope.
func stepIDs(steps []Step) map[string]bool {
	out := map[string]bool{}
	walkSteps(steps, func(step *Step) {
		if step.ID != "" {
			out[step.ID] = true
		}
	})
	return out
}

// cloneIDs snapshots the ids declared so far: the scope of a step is the
// steps before it, and declared keeps growing after that.
func cloneIDs(declared map[string]bool) map[string]bool {
	out := make(map[string]bool, len(declared))
	for id := range declared {
		out[id] = true
	}
	return out
}

// enterStep points the collector at the step being validated.
func (r *Result) enterStep(scope *stepScope) { r.scope = scope }
