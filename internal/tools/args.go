package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/foxzi/baton/internal/scenario"
)

// bindArgs turns the arguments of a call into the values the argv templates
// are rendered with. Everything here is defence: the caller is a model, and
// these values end up in the argument vector of a process.
func bindArgs(declared map[string]scenario.CommandArg, raw json.RawMessage) (map[string]string, error) {
	given, err := decodeArgs(raw)
	if err != nil {
		return nil, err
	}

	var problems []error
	for _, name := range sortedNames(given) {
		if _, ok := declared[name]; !ok {
			problems = append(problems, fmt.Errorf("unknown argument %q", name))
		}
	}

	bound := make(map[string]string, len(declared))
	for _, name := range sortedNames(declared) {
		arg := declared[name]

		value, ok := given[name]
		if !ok {
			// A default comes from the scenario's author, not from the
			// agent, so it is used as written: the checks below would reject
			// an ordinary default such as ./... over its dots.
			if arg.Default != "" {
				bound[name] = arg.Default
				continue
			}
			if arg.Required {
				problems = append(problems, fmt.Errorf("argument %q is required", name))
			}
			continue
		}

		if err := checkValue(name, value, arg.Pattern); err != nil {
			problems = append(problems, err)
			continue
		}
		bound[name] = value
	}

	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	return bound, nil
}

// checkValue holds an argument to what the scenario allows. The path rules
// come first: they say what the pattern is not trusted to cover, and a
// pattern that is loose about a leading slash or a parent directory would
// otherwise hand the agent a way out of the workspace (spec section 7.5).
func checkValue(name, value, pattern string) error {
	switch {
	case value == "":
		return fmt.Errorf("argument %q is empty", name)
	case strings.HasPrefix(value, "/"):
		return fmt.Errorf("argument %q must not be an absolute path", name)
	case strings.Contains(value, ".."):
		return fmt.Errorf("argument %q must not contain ..", name)
	case strings.ContainsAny(value, "\x00\n\r"):
		return fmt.Errorf("argument %q must be a single line", name)
	}

	if pattern == "" {
		// Validation requires a pattern, so a command without one was built
		// by hand; refusing is safer than passing the value through.
		return fmt.Errorf("argument %q has no pattern and cannot be checked", name)
	}

	// The pattern has to match the whole value: a pattern anchored at
	// neither end would accept anything that merely contains something
	// allowed.
	expr, err := regexp.Compile(`\A(?:` + pattern + `)\z`)
	if err != nil {
		return fmt.Errorf("argument %q has an invalid pattern: %w", name, err)
	}
	if !expr.MatchString(value) {
		return fmt.Errorf("argument %q does not match %s", name, pattern)
	}
	return nil
}

// decodeArgs reads the call's arguments as strings. An absent or empty
// argument object is a call without arguments.
func decodeArgs(raw json.RawMessage) (map[string]string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return map[string]string{}, nil
	}

	var fields map[string]any
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil {
		return nil, fmt.Errorf("arguments are not a JSON object: %w", err)
	}

	given := make(map[string]string, len(fields))
	for _, name := range sortedNames(fields) {
		value, ok := scalar(fields[name])
		if !ok {
			return nil, fmt.Errorf("argument %q must be a string", name)
		}
		given[name] = value
	}
	return given, nil
}

// sortedNames keeps the order of the errors a call reports stable.
func sortedNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
