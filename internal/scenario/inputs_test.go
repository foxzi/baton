package scenario

import (
	"strings"
	"testing"
)

// TestBindInputsCoerceFromPairs checks every declared type coerced from a
// "-i key=value" pair.
func TestBindInputsCoerceFromPairs(t *testing.T) {
	declared := map[string]Input{
		"s": {Type: TypeString},
		"i": {Type: TypeInt},
		"n": {Type: TypeNumber},
		"b": {Type: TypeBool},
		"l": {Type: TypeList},
		"m": {Type: TypeMap},
	}
	pairs := []string{
		"s=hello",
		"i=42",
		"n=3.5",
		"b=true",
		`l=["a","b"]`,
		`m={"k":"v"}`,
	}

	got, err := BindInputs(declared, pairs, nil)
	if err != nil {
		t.Fatalf("BindInputs: %v", err)
	}
	if got["s"] != "hello" {
		t.Errorf("s = %#v, want %q", got["s"], "hello")
	}
	if got["i"] != int64(42) {
		t.Errorf("i = %#v, want int64(42)", got["i"])
	}
	if got["n"] != float64(3.5) {
		t.Errorf("n = %#v, want float64(3.5)", got["n"])
	}
	if got["b"] != true {
		t.Errorf("b = %#v, want true", got["b"])
	}
	list, ok := got["l"].([]any)
	if !ok || len(list) != 2 || list[0] != "a" || list[1] != "b" {
		t.Errorf("l = %#v, want [a b]", got["l"])
	}
	m, ok := got["m"].(map[string]any)
	if !ok || m["k"] != "v" {
		t.Errorf("m = %#v, want map[k:v]", got["m"])
	}
}

// TestBindInputsCheckFromFile checks every declared type when supplied
// through the decoded input file instead of a pair.
func TestBindInputsCheckFromFile(t *testing.T) {
	declared := map[string]Input{
		"s": {Type: TypeString},
		"i": {Type: TypeInt},
		"n": {Type: TypeNumber},
		"b": {Type: TypeBool},
		"l": {Type: TypeList},
		"m": {Type: TypeMap},
	}
	file := map[string]any{
		"s": "hello",
		"i": 42,
		"n": 3.5,
		"b": true,
		"l": []any{"a", "b"},
		"m": map[string]any{"k": "v"},
	}

	got, err := BindInputs(declared, nil, file)
	if err != nil {
		t.Fatalf("BindInputs: %v", err)
	}
	if got["s"] != "hello" {
		t.Errorf("s = %#v", got["s"])
	}
	if got["i"] != int64(42) {
		t.Errorf("i = %#v, want int64(42)", got["i"])
	}
	if got["n"] != float64(3.5) {
		t.Errorf("n = %#v", got["n"])
	}
	if got["b"] != true {
		t.Errorf("b = %#v", got["b"])
	}
	list, ok := got["l"].([]any)
	if !ok || len(list) != 2 {
		t.Errorf("l = %#v", got["l"])
	}
	m, ok := got["m"].(map[string]any)
	if !ok || m["k"] != "v" {
		t.Errorf("m = %#v", got["m"])
	}
}

// TestBindInputsPrecedence checks that a pair overrides the file, and the
// file overrides the default.
func TestBindInputsPrecedence(t *testing.T) {
	declared := map[string]Input{
		"a": {Type: TypeString, Default: "default-a"},
		"b": {Type: TypeString, Default: "default-b"},
		"c": {Type: TypeString, Default: "default-c"},
	}
	file := map[string]any{
		"a": "file-a",
		"b": "file-b",
	}
	pairs := []string{"a=pair-a"}

	got, err := BindInputs(declared, pairs, file)
	if err != nil {
		t.Fatalf("BindInputs: %v", err)
	}
	if got["a"] != "pair-a" {
		t.Errorf("a = %#v, want pair-a (pair wins)", got["a"])
	}
	if got["b"] != "file-b" {
		t.Errorf("b = %#v, want file-b (file wins over default)", got["b"])
	}
	if got["c"] != "default-c" {
		t.Errorf("c = %#v, want default-c", got["c"])
	}
}

// TestBindInputsMissingRequired checks that a required input without a
// value or default is reported.
func TestBindInputsMissingRequired(t *testing.T) {
	declared := map[string]Input{
		"project": {Type: TypeString, Required: true},
	}

	_, err := BindInputs(declared, nil, nil)
	if err == nil || !strings.Contains(err.Error(), `input "project": required`) {
		t.Fatalf("err = %v, want mention of required project", err)
	}
}

// TestBindInputsRequiredSatisfiedByDefault checks that a required input with
// a default does not error when unsupplied.
func TestBindInputsRequiredSatisfiedByDefault(t *testing.T) {
	declared := map[string]Input{
		"base": {Type: TypeString, Required: true, Default: "origin/main"},
	}

	got, err := BindInputs(declared, nil, nil)
	if err != nil {
		t.Fatalf("BindInputs: %v", err)
	}
	if got["base"] != "origin/main" {
		t.Errorf("base = %#v, want origin/main", got["base"])
	}
}

// TestBindInputsOptionalUnsuppliedAbsent checks that an optional input
// without a default is simply missing from the result, not a nil entry.
func TestBindInputsOptionalUnsuppliedAbsent(t *testing.T) {
	declared := map[string]Input{
		"optional": {Type: TypeString},
	}

	got, err := BindInputs(declared, nil, nil)
	if err != nil {
		t.Fatalf("BindInputs: %v", err)
	}
	if _, ok := got["optional"]; ok {
		t.Errorf("optional present in result: %#v", got["optional"])
	}
}

// TestBindInputsUndeclaredPair checks that an undeclared key supplied by a
// pair is reported.
func TestBindInputsUndeclaredPair(t *testing.T) {
	_, err := BindInputs(nil, []string{"mystery=1"}, nil)
	if err == nil || !strings.Contains(err.Error(), `input "mystery": not declared by the scenario`) {
		t.Fatalf("err = %v, want mention of undeclared mystery", err)
	}
}

// TestBindInputsUndeclaredFile checks that an undeclared key supplied by the
// input file is reported.
func TestBindInputsUndeclaredFile(t *testing.T) {
	_, err := BindInputs(nil, nil, map[string]any{"mystery": 1})
	if err == nil || !strings.Contains(err.Error(), `input "mystery": not declared by the scenario`) {
		t.Fatalf("err = %v, want mention of undeclared mystery", err)
	}
}

// TestBindInputsMalformedPair checks that a pair without "=" is reported,
// and that a value containing "=" is otherwise accepted.
func TestBindInputsMalformedPair(t *testing.T) {
	_, err := BindInputs(nil, []string{"noequals"}, nil)
	if err == nil || !strings.Contains(err.Error(), `input "noequals": expected key=value`) {
		t.Fatalf("err = %v, want mention of expected key=value", err)
	}

	declared := map[string]Input{"s": {Type: TypeString}}
	got, err := BindInputs(declared, []string{"s=a=b"}, nil)
	if err != nil {
		t.Fatalf("BindInputs: %v", err)
	}
	if got["s"] != "a=b" {
		t.Errorf("s = %#v, want a=b", got["s"])
	}
}

// TestBindInputsIntFromPairFractional checks that "3.5" is rejected for an
// int input supplied as a pair.
func TestBindInputsIntFromPairFractional(t *testing.T) {
	declared := map[string]Input{"i": {Type: TypeInt}}
	_, err := BindInputs(declared, []string{"i=3.5"}, nil)
	if err == nil || !strings.Contains(err.Error(), `input "i": expected int, got "3.5"`) {
		t.Fatalf("err = %v, want expected int, got \"3.5\"", err)
	}
}

// TestBindInputsIntFromFileWholeFloat checks that 3.0 from the input file is
// accepted for an int input.
func TestBindInputsIntFromFileWholeFloat(t *testing.T) {
	declared := map[string]Input{"i": {Type: TypeInt}}
	got, err := BindInputs(declared, nil, map[string]any{"i": 3.0})
	if err != nil {
		t.Fatalf("BindInputs: %v", err)
	}
	if got["i"] != int64(3) {
		t.Errorf("i = %#v, want int64(3)", got["i"])
	}
}

// TestBindInputsIntFromFileFractional checks that 3.5 from the input file is
// rejected for an int input.
func TestBindInputsIntFromFileFractional(t *testing.T) {
	declared := map[string]Input{"i": {Type: TypeInt}}
	_, err := BindInputs(declared, nil, map[string]any{"i": 3.5})
	if err == nil || !strings.Contains(err.Error(), `input "i": expected int, got float64`) {
		t.Fatalf("err = %v, want expected int, got float64", err)
	}
}

// TestBindInputsBadJSON checks that malformed JSON is rejected for list and
// map inputs supplied as pairs.
func TestBindInputsBadJSON(t *testing.T) {
	declared := map[string]Input{
		"l": {Type: TypeList},
		"m": {Type: TypeMap},
	}

	_, err := BindInputs(declared, []string{"l=not-json"}, nil)
	if err == nil || !strings.Contains(err.Error(), `input "l": expected list, got "not-json"`) {
		t.Fatalf("err = %v, want expected list error", err)
	}

	_, err = BindInputs(declared, []string{"m=not-json"}, nil)
	if err == nil || !strings.Contains(err.Error(), `input "m": expected map, got "not-json"`) {
		t.Fatalf("err = %v, want expected map error", err)
	}
}

// TestBindInputsPattern checks pattern matching, mismatch, an invalid
// pattern, and a pattern applied to a non-string value.
func TestBindInputsPattern(t *testing.T) {
	t.Run("match", func(t *testing.T) {
		declared := map[string]Input{
			"name": {Type: TypeString, Pattern: `^[a-z]+$`},
		}
		got, err := BindInputs(declared, []string{"name=abc"}, nil)
		if err != nil {
			t.Fatalf("BindInputs: %v", err)
		}
		if got["name"] != "abc" {
			t.Errorf("name = %#v", got["name"])
		}
	})

	t.Run("mismatch", func(t *testing.T) {
		declared := map[string]Input{
			"name": {Type: TypeString, Pattern: `^[a-z]+$`},
		}
		_, err := BindInputs(declared, []string{"name=ABC"}, nil)
		if err == nil || !strings.Contains(err.Error(), `input "name": value does not match pattern "^[a-z]+$"`) {
			t.Fatalf("err = %v, want pattern mismatch", err)
		}
	})

	t.Run("invalid pattern", func(t *testing.T) {
		declared := map[string]Input{
			"name": {Type: TypeString, Pattern: `(`},
		}
		_, err := BindInputs(declared, []string{"name=abc"}, nil)
		if err == nil || !strings.Contains(err.Error(), `input "name": invalid pattern:`) {
			t.Fatalf("err = %v, want invalid pattern error", err)
		}
	})

	t.Run("non-string value", func(t *testing.T) {
		declared := map[string]Input{
			"count": {Type: TypeInt, Pattern: `^[a-z]+$`},
		}
		got, err := BindInputs(declared, []string{"count=5"}, nil)
		if err != nil {
			t.Fatalf("BindInputs: %v", err)
		}
		if got["count"] != int64(5) {
			t.Errorf("count = %#v, want int64(5)", got["count"])
		}
	})
}

// TestBindInputsNilDeclared checks that a nil declared map treats any
// supplied input as undeclared and still returns a non-nil empty map when
// nothing is supplied.
func TestBindInputsNilDeclared(t *testing.T) {
	got, err := BindInputs(nil, nil, nil)
	if err != nil {
		t.Fatalf("BindInputs: %v", err)
	}
	if got == nil {
		t.Fatal("got nil map, want non-nil empty map")
	}
	if len(got) != 0 {
		t.Errorf("got = %#v, want empty", got)
	}
}

// TestBindInputsMultipleProblems checks that unrelated problems are all
// reported together in one error.
func TestBindInputsMultipleProblems(t *testing.T) {
	declared := map[string]Input{
		"project": {Type: TypeString, Required: true},
	}
	_, err := BindInputs(declared, []string{"mystery=1"}, nil)
	if err == nil {
		t.Fatal("BindInputs: want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, `"mystery"`) {
		t.Errorf("err = %q, want mention of mystery", msg)
	}
	if !strings.Contains(msg, `"project"`) {
		t.Errorf("err = %q, want mention of project", msg)
	}
}
