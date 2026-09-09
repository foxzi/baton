package scenario

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

// subjectPattern matches a switch subject that reads a field out of the
// result of an llm step: only then is there a schema to look an enum up in
// (spec section 3.10).
var subjectPattern = regexp.MustCompile(`^\s*steps\.([a-z][a-z0-9_]*)\.result((?:\.[a-z][a-z0-9_]*)+)\s*$`)

// switchEnum returns the enum values the subject of a switch is limited to,
// or nil when they cannot be established: the subject may be any expression,
// the step it reads may not be an llm step, and the schema may not constrain
// the field. Not knowing is normal, so every failure here is silent and the
// caller falls back to only asking for a default (section 4, check 10).
func switchEnum(scn *Scenario, subject string) []any {
	m := subjectPattern.FindStringSubmatch(subject)
	if m == nil || scn.Path == "" {
		return nil
	}

	var llm *LLMStep
	WalkSteps(scn, func(step *Step) {
		if step.ID == m[1] && step.LLM != nil {
			llm = step.LLM
		}
	})
	if llm == nil || llm.Schema == "" {
		return nil
	}

	path := llm.Schema
	if !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(scn.Path), path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc map[string]any
	if json.Unmarshal(data, &doc) != nil {
		return nil
	}

	// Walk the field path through properties. A schema that hides the field
	// behind a $ref or a composition keyword is left alone.
	fields := splitFields(m[2])
	for i, field := range fields {
		props, ok := doc["properties"].(map[string]any)
		if !ok {
			return nil
		}
		doc, ok = props[field].(map[string]any)
		if !ok {
			return nil
		}
		if i == len(fields)-1 {
			enum, _ := doc["enum"].([]any)
			return enum
		}
	}
	return nil
}

// splitFields turns ".a.b" into [a b].
func splitFields(path string) []string {
	var out []string
	for start := 1; start <= len(path); {
		end := start
		for end < len(path) && path[end] != '.' {
			end++
		}
		out = append(out, path[start:end])
		start = end + 1
	}
	return out
}

// caseLabel renders an enum value the way caseLiteral renders a case label,
// so that the two can be compared. It returns false for a value no case
// label can name.
func caseLabel(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		return strconv.Quote(v), true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(v), true
	default:
		return "", false
	}
}
