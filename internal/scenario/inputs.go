package scenario

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// BindInputs resolves the scenario inputs from command line pairs and an
// input file, applying defaults, checking required inputs, coercing types
// and enforcing patterns (spec section 3.1).
//
// pairs are raw "key=value" strings from -i, file is the decoded content of
// --input-file (may be nil). Values from pairs win over the file, the file
// wins over defaults. Every problem is reported, joined with errors.Join.
func BindInputs(declared map[string]Input, pairs []string, file map[string]any) (map[string]any, error) {
	result := map[string]any{}
	var errs []error

	// supplied records who provided a value for a given key, so that
	// defaults and required checks only look at inputs still missing.
	supplied := map[string]bool{}

	for _, pair := range pairs {
		key, raw, ok := strings.Cut(pair, "=")
		if !ok {
			errs = append(errs, fmt.Errorf("input %q: expected key=value", pair))
			continue
		}
		input, isDeclared := lookupInput(declared, key)
		if !isDeclared {
			errs = append(errs, fmt.Errorf("input %q: not declared by the scenario", key))
			continue
		}
		value, err := coerceText(input.Type, raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("input %q: %v", key, err))
			continue
		}
		result[key] = value
		supplied[key] = true
	}

	for _, key := range sortedKeys(file) {
		if supplied[key] {
			// A pair already won for this key.
			continue
		}
		input, isDeclared := lookupInput(declared, key)
		if !isDeclared {
			errs = append(errs, fmt.Errorf("input %q: not declared by the scenario", key))
			continue
		}
		value, err := coerceValue(input.Type, file[key])
		if err != nil {
			errs = append(errs, fmt.Errorf("input %q: %v", key, err))
			continue
		}
		result[key] = value
		supplied[key] = true
	}

	for _, name := range sortedKeys(declared) {
		if supplied[name] {
			continue
		}
		input := declared[name]
		if input.Default != nil {
			value, err := coerceValue(input.Type, input.Default)
			if err != nil {
				errs = append(errs, fmt.Errorf("input %q: %v", name, err))
				continue
			}
			result[name] = value
			supplied[name] = true
			continue
		}
		if input.Required {
			errs = append(errs, fmt.Errorf("input %q: required", name))
		}
	}

	for _, name := range sortedKeys(declared) {
		input := declared[name]
		if input.Pattern == "" {
			continue
		}
		value, ok := result[name]
		if !ok {
			continue
		}
		text, ok := value.(string)
		if !ok {
			continue
		}
		re, err := regexp.Compile(input.Pattern)
		if err != nil {
			errs = append(errs, fmt.Errorf("input %q: invalid pattern: %v", name, err))
			continue
		}
		if !re.MatchString(text) {
			errs = append(errs, fmt.Errorf("input %q: value does not match pattern %q", name, input.Pattern))
		}
	}

	return result, errors.Join(errs...)
}

// lookupInput reports the declared input for key, treating a nil map as
// having no declarations.
func lookupInput(declared map[string]Input, key string) (Input, bool) {
	if declared == nil {
		return Input{}, false
	}
	input, ok := declared[key]
	return input, ok
}

// coerceText parses a raw command line value into the declared type.
func coerceText(typ InputType, raw string) (any, error) {
	switch typ {
	case TypeString:
		return raw, nil
	case TypeInt:
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("expected int, got %q", raw)
		}
		return v, nil
	case TypeNumber:
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("expected number, got %q", raw)
		}
		return v, nil
	case TypeBool:
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("expected bool, got %q", raw)
		}
		return v, nil
	case TypeList:
		var v []any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, fmt.Errorf("expected list, got %q", raw)
		}
		return v, nil
	case TypeMap:
		var v map[string]any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, fmt.Errorf("expected map, got %q", raw)
		}
		return v, nil
	default:
		return nil, fmt.Errorf("expected %s, got %q", typ, raw)
	}
}

// coerceValue checks a value that already has a Go type, such as one decoded
// from JSON (an input file or a YAML default), and normalises it to the
// representation BindInputs returns.
func coerceValue(typ InputType, value any) (any, error) {
	switch typ {
	case TypeString:
		s, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("expected string, got %T", value)
		}
		return s, nil
	case TypeInt:
		return coerceInt(value)
	case TypeNumber:
		return coerceNumber(value)
	case TypeBool:
		b, ok := value.(bool)
		if !ok {
			return nil, fmt.Errorf("expected bool, got %T", value)
		}
		return b, nil
	case TypeList:
		v, ok := value.([]any)
		if !ok {
			return nil, fmt.Errorf("expected list, got %T", value)
		}
		return v, nil
	case TypeMap:
		v, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("expected map, got %T", value)
		}
		return v, nil
	default:
		return nil, fmt.Errorf("expected %s, got %T", typ, value)
	}
}

// coerceInt normalises any Go integer or whole-valued float to int64.
func coerceInt(value any) (int64, error) {
	switch v := value.(type) {
	case int:
		return int64(v), nil
	case int8:
		return int64(v), nil
	case int16:
		return int64(v), nil
	case int32:
		return int64(v), nil
	case int64:
		return v, nil
	case uint:
		return int64(v), nil
	case uint8:
		return int64(v), nil
	case uint16:
		return int64(v), nil
	case uint32:
		return int64(v), nil
	case uint64:
		return int64(v), nil
	case float32:
		return coerceFloatToInt(float64(v), value)
	case float64:
		return coerceFloatToInt(v, value)
	default:
		return 0, fmt.Errorf("expected int, got %T", value)
	}
}

func coerceFloatToInt(f float64, original any) (int64, error) {
	if f != float64(int64(f)) {
		return 0, fmt.Errorf("expected int, got %T", original)
	}
	return int64(f), nil
}

// coerceNumber normalises any Go numeric to float64.
func coerceNumber(value any) (float64, error) {
	switch v := value.(type) {
	case int:
		return float64(v), nil
	case int8:
		return float64(v), nil
	case int16:
		return float64(v), nil
	case int32:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case uint:
		return float64(v), nil
	case uint8:
		return float64(v), nil
	case uint16:
		return float64(v), nil
	case uint32:
		return float64(v), nil
	case uint64:
		return float64(v), nil
	case float32:
		return float64(v), nil
	case float64:
		return v, nil
	default:
		return 0, fmt.Errorf("expected number, got %T", value)
	}
}
