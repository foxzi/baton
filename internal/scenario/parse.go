package scenario

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Load reads and parses the scenario at path.
func Load(path string) (*Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data, path)
}

// Parse parses scenario bytes. Path is recorded on the scenario so that
// relative prompt and template paths can be resolved later; it is only used
// for diagnostics here.
func Parse(data []byte, path string) (*Scenario, error) {
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)

	var scn Scenario
	if err := decoder.Decode(&scn); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	scn.Path = path
	return &scn, nil
}

// stepKeys are the field names accepted on a step. Step decodes itself to
// record line numbers, so the strict-field check of the decoder does not
// reach inside it and is done here instead.
var stepKeys = map[string]bool{
	"id": true, "when": true, "needs": true, "timeout": true, "retry": true,
	"on_error": true, "fallback": true, "cache": true, "dedupe_key": true,
	"run": true, "assert": true, "http": true, "llm": true, "agent": true,
	"foreach": true, "until": true, "switch": true, "cases": true, "default": true,
	"notify": true, "message": true,
}

// UnmarshalYAML decodes a step and remembers where it was declared.
func (s *Step) UnmarshalYAML(node *yaml.Node) error {
	if err := checkKeys(node, stepKeys, "step"); err != nil {
		return err
	}
	type plain Step
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*s = Step(decoded)
	s.Line = node.Line
	return nil
}

// runKeys are the field names accepted on a run body.
var runKeys = map[string]bool{
	"argv": true, "cwd": true, "env": true, "stdin": true, "parse": true,
	"allow_exit_codes": true, "max_output_bytes": true, "readonly": true,
}

// shellMetacharacters are rejected in the bare-string form of run, because
// baton never hands the command to a shell (spec section 3.3).
const shellMetacharacters = "|&;<>()$`\"'\\*?[]{}~\n\r\t"

// UnmarshalYAML decodes a run body in either the mapping form or the bare
// command-string form.
func (r *RunStep) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		if strings.ContainsAny(node.Value, shellMetacharacters) {
			return fmt.Errorf("line %d: run as a string must not contain shell metacharacters; use run.argv", node.Line)
		}
		fields := strings.Fields(node.Value)
		if len(fields) == 0 {
			return fmt.Errorf("line %d: run is empty", node.Line)
		}
		*r = RunStep{Argv: fields, fromString: true}
		return nil
	}

	if err := checkKeys(node, runKeys, "run"); err != nil {
		return err
	}
	type plain RunStep
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*r = RunStep(decoded)
	return nil
}

// SplitFromString reports whether the body was written as a bare command
// string and split by baton.
func (r *RunStep) SplitFromString() bool { return r.fromString }

// UnmarshalYAML decodes an environment entry: a literal scalar, or a mapping
// holding a single secret reference.
func (e *EnvValue) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		e.Value = node.Value
		return nil
	case yaml.MappingNode:
		var ref struct {
			Secret string `yaml:"secret"`
		}
		if err := checkKeys(node, map[string]bool{"secret": true}, "env entry"); err != nil {
			return err
		}
		if err := node.Decode(&ref); err != nil {
			return err
		}
		if ref.Secret == "" {
			return fmt.Errorf("line %d: env entry needs a secret name", node.Line)
		}
		e.Secret = ref.Secret
		return nil
	default:
		return fmt.Errorf("line %d: env entry must be a string or { secret: name }", node.Line)
	}
}

// checkKeys rejects mapping keys outside allowed, reporting every offender.
func checkKeys(node *yaml.Node, allowed map[string]bool, what string) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: %s must be a mapping", node.Line, what)
	}
	var errs []error
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i]
		if !allowed[key.Value] {
			errs = append(errs, fmt.Errorf("line %d: unknown %s field %q", key.Line, what, key.Value))
		}
	}
	return errors.Join(errs...)
}
