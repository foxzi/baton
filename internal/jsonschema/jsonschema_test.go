package jsonschema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const objectSchema = `{
	"type": "object",
	"required": ["name", "age"],
	"properties": {
		"name": {"type": "string"},
		"age": {"type": "integer"}
	}
}`

func TestCompileAndValidateValid(t *testing.T) {
	sch, err := Compile("test.json", []byte(objectSchema))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	v, err := sch.ValidateJSON([]byte(`{"name": "alice", "age": 30}`))
	if err != nil {
		t.Fatalf("ValidateJSON: %v", err)
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("expected decoded value to be a map, got %T", v)
	}
	if m["name"] != "alice" {
		t.Errorf("name = %v, want alice", m["name"])
	}
}

func TestValidateMissingRequired(t *testing.T) {
	sch, err := Compile("test.json", []byte(objectSchema))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	_, err = sch.ValidateJSON([]byte(`{"name": "alice"}`))
	if err == nil {
		t.Fatal("expected a validation error, got nil")
	}
	if !strings.Contains(err.Error(), "age") {
		t.Errorf("error %q does not mention the missing property name", err.Error())
	}
}

func TestValidateWrongType(t *testing.T) {
	sch, err := Compile("test.json", []byte(objectSchema))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	_, err = sch.ValidateJSON([]byte(`{"name": 123, "age": 30}`))
	if err == nil {
		t.Fatal("expected a validation error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "name") {
		t.Errorf("error %q does not mention the offending property", msg)
	}
	if !strings.Contains(msg, "string") {
		t.Errorf("error %q does not mention the expected type", msg)
	}
}

func TestCompileInvalidSchema(t *testing.T) {
	_, err := Compile("bad.json", []byte(`{"type": 123}`))
	if err == nil {
		t.Fatal("expected an error compiling an invalid schema, got nil")
	}
}

func TestValidateJSONNonJSON(t *testing.T) {
	sch, err := Compile("test.json", []byte(objectSchema))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	_, err = sch.ValidateJSON([]byte(`not json at all`))
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "JSON") {
		t.Errorf("error %q does not mention JSON", err.Error())
	}
}

func TestCompileFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schema.json")
	if err := os.WriteFile(path, []byte(objectSchema), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	sch, err := CompileFile(path)
	if err != nil {
		t.Fatalf("CompileFile: %v", err)
	}
	if _, err := sch.ValidateJSON([]byte(`{"name": "bob", "age": 1}`)); err != nil {
		t.Fatalf("ValidateJSON: %v", err)
	}
}

func TestCompileFileMissing(t *testing.T) {
	_, err := CompileFile(filepath.Join(t.TempDir(), "missing.json"))
	if err == nil {
		t.Fatal("expected an error for a missing file, got nil")
	}
}

func TestValidateDeeplyFailingIsBounded(t *testing.T) {
	// A schema with many independently failing properties: without a cap
	// on causes and total length, the flattened message could grow
	// unbounded.
	const wideSchema = `{
		"type": "object",
		"required": ["a", "b", "c", "d", "e", "f", "g", "h"],
		"properties": {
			"a": {"type": "integer"},
			"b": {"type": "integer"},
			"c": {"type": "integer"},
			"d": {"type": "integer"},
			"e": {"type": "integer"},
			"f": {"type": "integer"},
			"g": {"type": "integer"},
			"h": {"type": "integer"}
		}
	}`
	sch, err := Compile("wide.json", []byte(wideSchema))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	_, err = sch.ValidateJSON([]byte(`{
		"a": "x", "b": "x", "c": "x", "d": "x",
		"e": "x", "f": "x", "g": "x", "h": "x"
	}`))
	if err == nil {
		t.Fatal("expected a validation error, got nil")
	}
	msg := err.Error()
	if strings.Contains(msg, "\n") {
		t.Errorf("expected a single-line message, got %q", msg)
	}
	if len(msg) > maxErrorLen+16 {
		t.Errorf("message length %d exceeds bound: %q", len(msg), msg)
	}
}

func TestJSONPointer(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want string
	}{
		{"root", nil, ""},
		{"single", []string{"name"}, "/name"},
		{"nested", []string{"items", "0", "name"}, "/items/0/name"},
		{"escaping", []string{"a/b", "c~d"}, "/a~1b/c~0d"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := jsonPointer(tc.in); got != tc.want {
				t.Errorf("jsonPointer(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestValidateJSONNormalizesNumbers(t *testing.T) {
	sch, err := Compile("test.json", []byte(`{
		"type": "object",
		"properties": {
			"score": {"type": "integer"},
			"ratio": {"type": "number"},
			"nested": {"type": "array", "items": {"type": "object"}}
		}
	}`))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	value, err := sch.ValidateJSON([]byte(`{"score": 7, "ratio": 0.5, "nested": [{"n": 3}]}`))
	if err != nil {
		t.Fatalf("ValidateJSON: %v", err)
	}

	obj, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value is %T, want map", value)
	}
	// A float64 is what makes {{ if le .score 3.0 }} work in a template: a
	// json.Number compares against no number literal at all, and a cache
	// hit would hand back a float64 anyway.
	if got, want := obj["score"], float64(7); got != want {
		t.Errorf("score is %v (%T), want %v", got, got, want)
	}
	if got, want := obj["ratio"], 0.5; got != want {
		t.Errorf("ratio is %v (%T), want %v", got, got, want)
	}
	nested := obj["nested"].([]any)[0].(map[string]any)
	if got, want := nested["n"], float64(3); got != want {
		t.Errorf("nested n is %v (%T), want %v", got, got, want)
	}
}
