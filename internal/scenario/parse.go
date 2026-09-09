package scenario

import (
	"errors"
	"fmt"
	"os"
	"reflect"
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
	// A second, lenient decode into a plain node tree builds the line index
	// Validate uses for fields that carry no Line of their own (inputs,
	// secrets, version, name, budget). It is not expected to fail given the
	// strict decode above already succeeded; if it somehow does, diagnostics
	// on those fields just fall back to line 0 rather than the load failing.
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err == nil {
		scn.lines = buildLineIndex(&root)
	}
	if err := expandSwitches(&scn); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &scn, nil
}

// buildLineIndex walks a parsed document and records the source line of
// every mapping key and sequence element, keyed by the same dotted path
// convention Validate uses (steps[2].run.argv). It only sees the document as
// written, so a path steps[i] created by switch expansion after Parse is not
// in it; validateSteps never needs it for that, since every Step already
// carries its own Line from UnmarshalYAML.
func buildLineIndex(root *yaml.Node) map[string]int {
	idx := map[string]int{}
	indexNode(root, "", idx)
	if len(idx) == 0 {
		return nil
	}
	return idx
}

func indexNode(node *yaml.Node, prefix string, idx map[string]int) {
	if node == nil {
		return
	}
	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			indexNode(child, prefix, idx)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, val := node.Content[i], node.Content[i+1]
			path := key.Value
			if prefix != "" {
				path = prefix + "." + key.Value
			}
			idx[path] = key.Line
			indexNode(val, path, idx)
		}
	case yaml.SequenceNode:
		for i, item := range node.Content {
			path := fmt.Sprintf("%s[%d]", prefix, i)
			idx[path] = item.Line
			indexNode(item, path, idx)
		}
	}
}

// stepKeys are the field names accepted on a step. Step decodes itself to
// record line numbers, so the strict-field check of the decoder does not
// reach inside it and is done here instead.
var stepKeys = map[string]bool{
	"id": true, "when": true, "needs": true, "timeout": true, "retry": true,
	"on_error": true, "fallback": true, "cache": true, "dedupe_key": true,
	"run": true, "assert": true, "http": true, "llm": true, "agent": true,
	"foreach": true, "until": true, "switch": true, "cases": true, "default": true,
	"notify": true, "message": true, "file": true,
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

// apiKeys are the field names accepted on an apis entry.
var apiKeys = map[string]bool{
	"interface": true, "pack": true, "from": true, "sha256": true,
	"config": true, "auth": true, "timeout": true,
}

// UnmarshalYAML decodes an apis entry and remembers where it was declared.
func (a *API) UnmarshalYAML(node *yaml.Node) error {
	if err := checkKeys(node, apiKeys, "apis entry"); err != nil {
		return err
	}
	type plain API
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*a = API(decoded)
	a.Line = node.Line
	return nil
}

// UnmarshalYAML decodes the auth block of an apis entry.
func (a *APIAuth) UnmarshalYAML(node *yaml.Node) error {
	if err := checkKeys(node, map[string]bool{"secret": true}, "apis auth"); err != nil {
		return err
	}
	type plain APIAuth
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*a = APIAuth(decoded)
	return nil
}

// httpKeys are the field names accepted on an http body.
var httpKeys = map[string]bool{
	"op": true, "args": true, "api": true, "auth": true, "method": true,
	"url": true, "path": true, "headers": true, "query": true, "body": true,
	"expect_status": true, "parse": true, "max_bytes": true,
}

// UnmarshalYAML decodes an http body. The strict-field check of the decoder
// does not reach inside a step, so it is done here.
func (h *HTTPStep) UnmarshalYAML(node *yaml.Node) error {
	if err := checkKeys(node, httpKeys, "http"); err != nil {
		return err
	}
	type plain HTTPStep
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*h = HTTPStep(decoded)
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

// llmKeys are the field names accepted on an llm body.
var llmKeys = map[string]bool{
	"model": true, "fallback_models": true, "system": true, "prompt": true,
	"with": true, "schema": true, "tools": true, "max_tokens": true,
	"temperature": true, "structured_mode": true,
}

// UnmarshalYAML decodes an llm body. The strict-field check of the decoder
// does not reach inside a step, so it is done here.
func (l *LLMStep) UnmarshalYAML(node *yaml.Node) error {
	if err := checkKeys(node, llmKeys, "llm"); err != nil {
		return err
	}
	type plain LLMStep
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*l = LLMStep(decoded)
	return nil
}

// foreachKeys are the field names accepted on a foreach body.
var foreachKeys = map[string]bool{
	"items": true, "as": true, "max_parallel": true, "on_item_error": true,
	"min_success": true, "step": true,
}

// UnmarshalYAML decodes a foreach body. The strict-field check of the
// decoder does not reach inside a step, so it is done here.
func (f *ForeachStep) UnmarshalYAML(node *yaml.Node) error {
	if err := checkKeys(node, foreachKeys, "foreach"); err != nil {
		return err
	}
	type plain ForeachStep
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*f = ForeachStep(decoded)
	return nil
}

// untilKeys are the field names accepted on an until body.
var untilKeys = map[string]bool{
	"condition": true, "max_iterations": true, "step": true,
}

// UnmarshalYAML decodes an until body. The strict-field check of the decoder
// does not reach inside a step, so it is done here.
func (u *UntilStep) UnmarshalYAML(node *yaml.Node) error {
	if err := checkKeys(node, untilKeys, "until"); err != nil {
		return err
	}
	type plain UntilStep
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*u = UntilStep(decoded)
	return nil
}

// UnmarshalYAML decodes an agent body. The strict-field check of the decoder
// does not reach inside a step, so it is done here; unlike the other bodies
// the agent body nests blocks several levels deep, so the check walks the
// type rather than a flat list of field names.
func (a *AgentStep) UnmarshalYAML(node *yaml.Node) error {
	if err := checkFields(node, reflect.TypeFor[AgentStep](), "agent"); err != nil {
		return err
	}
	type plain AgentStep
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*a = AgentStep(decoded)
	return nil
}

// UnmarshalYAML decodes a commands entry and remembers where it was
// declared.
func (c *Command) UnmarshalYAML(node *yaml.Node) error {
	if err := checkFields(node, reflect.TypeFor[Command](), "commands entry"); err != nil {
		return err
	}
	type plain Command
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*c = Command(decoded)
	c.Line = node.Line
	return nil
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

// yamlUnmarshaler is the type of a value that decodes itself, and so decides
// for itself what its fields are.
var yamlUnmarshaler = reflect.TypeFor[yaml.Unmarshaler]()

// checkFields rejects mapping keys that typ has no field for, at any depth
// below node.
func checkFields(node *yaml.Node, typ reflect.Type, what string) error {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}

	var errs []error
	switch typ.Kind() {
	case reflect.Struct:
		if node.Kind != yaml.MappingNode {
			return fmt.Errorf("line %d: %s must be a mapping", node.Line, what)
		}
		fields := yamlFields(typ)
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			field, ok := fields[key.Value]
			if !ok {
				errs = append(errs, fmt.Errorf("line %d: unknown %s field %q", key.Line, what, key.Value))
				continue
			}
			errs = append(errs, checkNested(value, field, key.Value))
		}
	case reflect.Map:
		if node.Kind != yaml.MappingNode {
			return fmt.Errorf("line %d: %s must be a mapping", node.Line, what)
		}
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			errs = append(errs, checkNested(value, typ.Elem(), what+" "+key.Value))
		}
	case reflect.Slice:
		if node.Kind != yaml.SequenceNode {
			return fmt.Errorf("line %d: %s must be a list", node.Line, what)
		}
		for _, item := range node.Content {
			errs = append(errs, checkNested(item, typ.Elem(), what+" entry"))
		}
	}
	return errors.Join(errs...)
}

// checkNested is checkFields below the top level: types that decode
// themselves stop the walk, since they are checked by their own
// UnmarshalYAML, as do scalars and raw nodes.
func checkNested(node *yaml.Node, typ reflect.Type, what string) error {
	probe := typ
	for probe.Kind() == reflect.Pointer {
		probe = probe.Elem()
	}
	if reflect.PointerTo(probe).Implements(yamlUnmarshaler) {
		return nil
	}
	return checkFields(node, typ, what)
}

// yamlFields maps the yaml names of a struct's fields to their types,
// skipping the ones the format does not accept.
func yamlFields(typ reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type)
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("yaml"), ",")
		switch name {
		case "", "-":
			continue
		}
		fields[name] = field.Type
	}
	return fields
}
