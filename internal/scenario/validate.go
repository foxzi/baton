package scenario

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Diagnostic is one validation finding.
type Diagnostic struct {
	// Path locates the finding in the scenario, such as steps[2].run.argv.
	Path string
	// Line is the scenario line the finding belongs to, or 0 when unknown.
	Line    int
	Message string
}

// String renders the finding as one log line.
func (d Diagnostic) String() string {
	if d.Line > 0 {
		return fmt.Sprintf("line %d: %s: %s", d.Line, d.Path, d.Message)
	}
	return fmt.Sprintf("%s: %s", d.Path, d.Message)
}

// Result collects validation findings. Validation reports every problem it
// finds rather than stopping at the first one (spec section 4).
type Result struct {
	Errors   []Diagnostic
	Warnings []Diagnostic
}

// OK reports whether the scenario is free of errors. Warnings do not fail
// validation.
func (r *Result) OK() bool { return len(r.Errors) == 0 }

func (r *Result) errorf(path string, line int, format string, args ...any) {
	r.Errors = append(r.Errors, Diagnostic{Path: path, Line: line, Message: fmt.Sprintf(format, args...)})
}

func (r *Result) warnf(path string, line int, format string, args ...any) {
	r.Warnings = append(r.Warnings, Diagnostic{Path: path, Line: line, Message: fmt.Sprintf(format, args...)})
}

// idPattern is the required shape of a step id (spec section 3.2).
var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// inputTypes are the declarable input types (spec section 3.1).
var inputTypes = map[InputType]bool{
	TypeString: true, TypeInt: true, TypeNumber: true,
	TypeBool: true, TypeList: true, TypeMap: true,
}

// errorClasses are the retryable-error class names (spec section 9.1).
var errorClasses = map[string]bool{
	"transient": true, "schema": true, "command": true,
	"timeout": true, "budget": true, "policy": true, "config": true,
}

// parseModes are the accepted parse values (spec section 3.3).
var parseModes = map[ParseMode]bool{
	ParseUnset: true, ParseText: true, ParseJSON: true, ParseLines: true,
}

// Validate performs the structural checks of specification section 4. The
// checks that need a compiled expression or template context - items 3 to 8,
// 10, 12 and 13 - land together with internal/expr and internal/tmpl.
func Validate(scn *Scenario) Result {
	var res Result

	if scn.Version != 1 {
		res.errorf("version", 0, "must be 1, got %d", scn.Version)
	}
	if strings.TrimSpace(scn.Name) == "" {
		res.errorf("name", 0, "must not be empty")
	}

	validateInputs(scn, &res)
	validateSecrets(scn, &res)

	if len(scn.Steps) == 0 {
		res.errorf("steps", 0, "must declare at least one step")
	}

	declared := map[string]bool{}
	validateSteps(scn, scn.Steps, "steps", declared, &res)
	// on_failure runs after the scenario failed and has its own id namespace.
	validateSteps(scn, scn.OnFailure, "on_failure", map[string]bool{}, &res)

	return res
}

func validateInputs(scn *Scenario, res *Result) {
	for _, name := range sortedKeys(scn.Inputs) {
		input := scn.Inputs[name]
		path := "inputs." + name
		switch {
		case input.Type == "":
			res.errorf(path, 0, "must declare a type")
		case !inputTypes[input.Type]:
			res.errorf(path, 0, "unknown type %q", input.Type)
		}
		if input.Pattern != "" {
			if input.Type != TypeString {
				res.errorf(path+".pattern", 0, "pattern applies to string inputs only")
			}
			if _, err := regexp.Compile(input.Pattern); err != nil {
				res.errorf(path+".pattern", 0, "invalid regular expression: %v", err)
			}
		}
		if input.Required && input.Default != nil {
			res.warnf(path, 0, "default is unreachable on a required input")
		}
	}
}

func validateSecrets(scn *Scenario, res *Result) {
	for _, name := range sortedKeys(scn.Secrets) {
		secret := scn.Secrets[name]
		path := "secrets." + name
		switch secret.From {
		case SecretFromEnv:
			if secret.Key == "" {
				res.errorf(path, 0, "from: env needs key")
			}
			if secret.Path != "" {
				res.errorf(path, 0, "path applies to from: file only")
			}
		case SecretFromFile:
			if secret.Path == "" {
				res.errorf(path, 0, "from: file needs path")
			}
			if secret.Key != "" {
				res.errorf(path, 0, "key applies to from: env only")
			}
		case "":
			res.errorf(path, 0, "must declare from: env or from: file")
		default:
			res.errorf(path, 0, "unknown source %q", secret.From)
		}
	}
}

// validateSteps checks a step list in declaration order, filling declared
// with the ids it accepts so that later steps can reference them.
func validateSteps(scn *Scenario, steps []Step, prefix string, declared map[string]bool, res *Result) {
	for i := range steps {
		step := &steps[i]
		path := fmt.Sprintf("%s[%d]", prefix, i)
		validateStepID(step, path, declared, res)
		validateStepBody(scn, step, path, res)
		validateStepControl(step, path, declared, res)
	}
}

func validateStepID(step *Step, path string, declared map[string]bool, res *Result) {
	// A switch expands into the steps declared in its cases, so it carries no
	// id of its own.
	if step.Kind() == KindSwitch {
		return
	}
	switch {
	case step.ID == "":
		res.errorf(path+".id", step.Line, "must not be empty")
	case !idPattern.MatchString(step.ID):
		res.errorf(path+".id", step.Line, "%q must match %s", step.ID, idPattern)
	case declared[step.ID]:
		res.errorf(path+".id", step.Line, "duplicate step id %q", step.ID)
	default:
		declared[step.ID] = true
	}
}

func validateStepBody(scn *Scenario, step *Step, path string, res *Result) {
	kinds := step.Kinds()
	switch len(kinds) {
	case 1:
	case 0:
		res.errorf(path, step.Line, "must declare one of run, http, llm, agent, foreach, until, assert")
		return
	default:
		res.errorf(path, step.Line, "must declare exactly one body, got %s", joinKinds(kinds))
		return
	}

	switch kinds[0] {
	case KindRun:
		validateRun(scn, step, path+".run", res)
	case KindAssert:
		if strings.TrimSpace(step.Assert.Condition) == "" {
			res.errorf(path+".assert.condition", step.Line, "must not be empty")
		}
	case KindUntil:
		// Specification section 4, check 9.
		if !hasKey(step.Until, "max_iterations") {
			res.errorf(path+".until", step.Line, "max_iterations is required")
		}
	case KindSwitch:
		validateSwitch(step, path, res)
	}
}

func validateRun(scn *Scenario, step *Step, path string, res *Result) {
	run := step.Run
	switch {
	case len(run.Argv) == 0:
		res.errorf(path+".argv", step.Line, "must not be empty")
	case strings.TrimSpace(run.Argv[0]) == "":
		res.errorf(path+".argv[0]", step.Line, "command must not be empty")
	}
	if run.SplitFromString() {
		res.warnf(path, step.Line, "command string was split on spaces; prefer an explicit argv list")
	}
	if !parseModes[run.Parse] {
		res.errorf(path+".parse", step.Line, "unknown mode %q, want text, json or lines", run.Parse)
	}
	if run.MaxOutputBytes < 0 {
		res.errorf(path+".max_output_bytes", step.Line, "must not be negative")
	}
	for _, name := range sortedKeys(run.Env) {
		entry := run.Env[name]
		if entry.Secret != "" && scn.Secrets[entry.Secret].From == "" {
			res.errorf(path+".env."+name, step.Line, "undeclared secret %q", entry.Secret)
		}
	}
	// Specification section 9.4: a command that may have side effects is not
	// retried automatically unless the run is idempotent.
	if !run.Readonly && step.DedupeKey == "" && step.Retry != nil {
		res.warnf(path, step.Line, "retry on a step without readonly: true or dedupe_key may repeat side effects")
	}
}

func validateSwitch(step *Step, path string, res *Result) {
	if step.Switch.Kind != yaml.ScalarNode || strings.TrimSpace(step.Switch.Value) == "" {
		res.errorf(path+".switch", step.Line, "must be an expression")
	}
	if step.Cases == nil || step.Cases.Kind != yaml.MappingNode || len(step.Cases.Content) == 0 {
		res.errorf(path+".cases", step.Line, "must declare at least one case")
	}
	// Enum coverage (specification section 4, check 10) needs the schema of
	// the referenced step and is checked once schemas are loaded.
	if step.Default == nil {
		res.warnf(path, step.Line, "no default case; every value of the subject must be covered")
	}
}

func validateStepControl(step *Step, path string, declared map[string]bool, res *Result) {
	for i, need := range step.Needs {
		needPath := fmt.Sprintf("%s.needs[%d]", path, i)
		switch {
		case need == step.ID:
			res.errorf(needPath, step.Line, "step %q cannot need itself", step.ID)
		case !declared[need]:
			// Steps run in declaration order, so a dependency must already be
			// declared. This also rules out cycles (section 4, check 2).
			res.errorf(needPath, step.Line, "unknown or later step %q", need)
		}
	}

	switch step.OnError {
	case OnErrorUnset, OnErrorFail, OnErrorContinue:
		if step.Fallback != nil {
			res.warnf(path+".fallback", step.Line, "ignored unless on_error is fallback")
		}
	case OnErrorFallback:
		// Specification section 4, check 11.
		if step.Fallback == nil {
			res.errorf(path+".fallback", step.Line, "on_error: fallback requires a fallback body")
		}
	default:
		res.errorf(path+".on_error", step.Line, "unknown value %q, want fail, continue or fallback", step.OnError)
	}

	if step.Retry != nil {
		validateRetry(step, path+".retry", res)
	}
	if step.Timeout < 0 {
		res.errorf(path+".timeout", step.Line, "must not be negative")
	}
}

func validateRetry(step *Step, path string, res *Result) {
	retry := step.Retry
	if retry.Attempts < 1 {
		res.errorf(path+".attempts", step.Line, "must be at least 1")
	}
	for i, class := range retry.On {
		if !errorClasses[class] {
			res.errorf(fmt.Sprintf("%s.on[%d]", path, i), step.Line, "unknown error class %q", class)
		}
		if class == "budget" || class == "config" || class == "policy" {
			res.errorf(fmt.Sprintf("%s.on[%d]", path, i), step.Line, "class %q is never retried", class)
		}
	}
	if retry.Backoff < 0 {
		res.errorf(path+".backoff", step.Line, "must not be negative")
	}
}

// hasKey reports whether a mapping node contains the given key.
func hasKey(node *yaml.Node, key string) bool {
	if node == nil || node.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return true
		}
	}
	return false
}

func joinKinds(kinds []Kind) string {
	names := make([]string, len(kinds))
	for i, kind := range kinds {
		names[i] = string(kind)
	}
	return strings.Join(names, ", ")
}

// sortedKeys returns map keys in a stable order so that diagnostics do not
// depend on map iteration.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
