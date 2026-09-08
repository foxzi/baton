package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/agent"
	"github.com/foxzi/baton/internal/gateway"
	"github.com/foxzi/baton/internal/scenario"
)

// callState finds a tool by name in the state set and calls it.
func callState(t *testing.T, s *State, name, args string) (any, error) {
	t.Helper()
	var handler gateway.Handler
	for _, tool := range s.Tools() {
		if tool.Name == name {
			handler = tool.Handler
		}
	}
	if handler == nil {
		t.Fatalf("tool %q is not in the set", name)
	}
	return handler(context.Background(), json.RawMessage(args))
}

// 1. A step whose policy grants no state access gets no tools: the profiles
// of section 7.2 leave the state out on purpose.
func TestNewStateNoAccessGivesNoTools(t *testing.T) {
	for _, access := range []scenario.StateAccess{scenario.StateUnset, scenario.StateNone} {
		s, err := NewState(StateOptions{Policy: agent.Policy{State: access}})
		if err != nil {
			t.Fatalf("NewState(%q) error = %v", access, err)
		}
		if tools := s.Tools(); len(tools) != 0 {
			t.Fatalf("Tools() with access %q = %v, want none", access, tools)
		}
	}
}

// 2. Policy read gives only state.get.
func TestNewStateReadGivesGetOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "demo.json")
	s, err := NewState(StateOptions{Path: path, Policy: agent.Policy{State: scenario.StateRead}})
	if err != nil {
		t.Fatalf("NewState() error = %v", err)
	}
	tools := s.Tools()
	if len(tools) != 1 || tools[0].Name != "state.get" {
		t.Fatalf("Tools() = %v, want [state.get]", tools)
	}
}

// 3. Policy read-write gives state.get and state.set, in that order.
func TestNewStateReadWriteGivesGetThenSet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "demo.json")
	s, err := NewState(StateOptions{Path: path, Policy: agent.Policy{State: scenario.StateReadWrite}})
	if err != nil {
		t.Fatalf("NewState() error = %v", err)
	}
	tools := s.Tools()
	if len(tools) != 2 || tools[0].Name != "state.get" || tools[1].Name != "state.set" {
		t.Fatalf("Tools() = %v, want [state.get, state.set]", tools)
	}
}

// 4. An unknown access value is an error from NewState.
func TestNewStateRejectsUnknownAccess(t *testing.T) {
	_, err := NewState(StateOptions{Path: "x.json", Policy: agent.Policy{State: scenario.StateAccess("bogus")}})
	if err == nil {
		t.Fatal("NewState() error = nil, want an unknown access value to be rejected")
	}
}

// 5. Read and read-write without a Path are errors: there is nowhere to
// store or load the state.
func TestNewStateRejectsMissingPath(t *testing.T) {
	for _, access := range []scenario.StateAccess{scenario.StateRead, scenario.StateReadWrite} {
		_, err := NewState(StateOptions{Policy: agent.Policy{State: access}})
		if err == nil {
			t.Errorf("NewState(%q) without a path: error = nil, want an error", access)
		}
	}
}

// 6. state.get of a key that was never written reports Found=false, not an
// error: a fresh scenario has nothing stored yet.
func TestStateGetMissingKeyNotFound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "demo.json")
	s, err := NewState(StateOptions{Path: path, Policy: agent.Policy{State: scenario.StateRead}})
	if err != nil {
		t.Fatalf("NewState() error = %v", err)
	}

	out, err := callState(t, s, "state.get", `{"key":"nope"}`)
	if err != nil {
		t.Fatalf("state.get error = %v", err)
	}
	value, ok := out.(StateValue)
	if !ok {
		t.Fatalf("state.get returned %T, want StateValue", out)
	}
	if value.Found {
		t.Errorf("Found = true, want false for a key never written")
	}
}

// 7. state.get of a stored key returns the value with Found=true.
func TestStateGetStoredKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "demo.json")
	s, err := NewState(StateOptions{Path: path, Policy: agent.Policy{State: scenario.StateReadWrite}})
	if err != nil {
		t.Fatalf("NewState() error = %v", err)
	}

	if _, err := callState(t, s, "state.set", `{"key":"counter","value":42}`); err != nil {
		t.Fatalf("state.set error = %v", err)
	}

	out, err := callState(t, s, "state.get", `{"key":"counter"}`)
	if err != nil {
		t.Fatalf("state.get error = %v", err)
	}
	value, ok := out.(StateValue)
	if !ok {
		t.Fatalf("state.get returned %T, want StateValue", out)
	}
	if !value.Found {
		t.Fatalf("Found = false, want true for a stored key")
	}
	if value.Value.(json.Number).String() != "42" {
		t.Errorf("Value = %#v, want 42", value.Value)
	}
}

// 8. state.set writes the file, including creating a parent directory that
// does not exist yet, and the file it writes is valid JSON.
func TestStateSetCreatesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "demo.json")
	s, err := NewState(StateOptions{Path: path, Policy: agent.Policy{State: scenario.StateReadWrite}})
	if err != nil {
		t.Fatalf("NewState() error = %v", err)
	}

	if _, err := callState(t, s, "state.set", `{"key":"a","value":"one"}`); err != nil {
		t.Fatalf("state.set error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var stored map[string]any
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("the state file is not valid JSON: %v", err)
	}
	if stored["a"] != "one" {
		t.Errorf("stored = %#v, want a=one", stored)
	}
}

// 9. A second state.set of another key keeps the first key untouched.
func TestStateSetKeepsOtherKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "demo.json")
	s, err := NewState(StateOptions{Path: path, Policy: agent.Policy{State: scenario.StateReadWrite}})
	if err != nil {
		t.Fatalf("NewState() error = %v", err)
	}

	if _, err := callState(t, s, "state.set", `{"key":"a","value":"one"}`); err != nil {
		t.Fatalf("state.set a: %v", err)
	}
	if _, err := callState(t, s, "state.set", `{"key":"b","value":"two"}`); err != nil {
		t.Fatalf("state.set b: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var stored map[string]any
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("unmarshal state file: %v", err)
	}
	if stored["a"] != "one" || stored["b"] != "two" {
		t.Errorf("stored = %#v, want both a=one and b=two", stored)
	}
}

// 10. A large integer is preserved exactly: the code decodes with
// UseNumber so a stored identifier does not lose precision as a float64.
func TestStateLargeNumberIsPreservedExactly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "demo.json")
	s, err := NewState(StateOptions{Path: path, Policy: agent.Policy{State: scenario.StateReadWrite}})
	if err != nil {
		t.Fatalf("NewState() error = %v", err)
	}

	const want = "12345678901234567890"
	if _, err := callState(t, s, "state.set", `{"key":"id","value":`+want+`}`); err != nil {
		t.Fatalf("state.set error = %v", err)
	}

	out, err := callState(t, s, "state.get", `{"key":"id"}`)
	if err != nil {
		t.Fatalf("state.get error = %v", err)
	}
	value := out.(StateValue)
	got, ok := value.Value.(json.Number)
	if !ok {
		t.Fatalf("Value = %#v (%T), want a json.Number", value.Value, value.Value)
	}
	if got.String() != want {
		t.Errorf("Value = %s, want %s: the number lost precision", got.String(), want)
	}
}

// 11. state.set with a value above MaxStateValueBytes is an error and
// leaves the file unchanged.
func TestStateSetRejectsValueOverTheLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "demo.json")
	s, err := NewState(StateOptions{Path: path, Policy: agent.Policy{State: scenario.StateReadWrite}})
	if err != nil {
		t.Fatalf("NewState() error = %v", err)
	}

	if _, err := callState(t, s, "state.set", `{"key":"a","value":"one"}`); err != nil {
		t.Fatalf("state.set a: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	huge := `"` + strings.Repeat("x", MaxStateValueBytes+1) + `"`
	if _, err := callState(t, s, "state.set", `{"key":"b","value":`+huge+`}`); err == nil {
		t.Fatal("state.set error = nil, want a value over the limit to be rejected")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(before) != string(after) {
		t.Errorf("the file changed after a rejected state.set:\nbefore: %s\nafter:  %s", before, after)
	}
}

// 12. A missing or empty key is an error for both state.get and state.set.
func TestStateRejectsMissingKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "demo.json")
	s, err := NewState(StateOptions{Path: path, Policy: agent.Policy{State: scenario.StateReadWrite}})
	if err != nil {
		t.Fatalf("NewState() error = %v", err)
	}

	for _, args := range []string{`{}`, `{"key":""}`} {
		if _, err := callState(t, s, "state.get", args); err == nil {
			t.Errorf("state.get(%s) error = nil, want a missing key to be an error", args)
		}
		if _, err := callState(t, s, "state.set", args+``); err == nil {
			t.Errorf("state.set(%s) error = nil, want a missing key to be an error", args)
		}
	}
}

// 13. state.set without a value is an error.
func TestStateSetRejectsMissingValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "demo.json")
	s, err := NewState(StateOptions{Path: path, Policy: agent.Policy{State: scenario.StateReadWrite}})
	if err != nil {
		t.Fatalf("NewState() error = %v", err)
	}
	if _, err := callState(t, s, "state.set", `{"key":"a"}`); err == nil {
		t.Fatal("state.set error = nil, want a missing value to be an error")
	}
}

// 14. A state file that is not a JSON object makes state.get fail with an
// error naming the path.
func TestStateGetRejectsNonObjectFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "demo.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(`[1, 2, 3]`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	s, err := NewState(StateOptions{Path: path, Policy: agent.Policy{State: scenario.StateRead}})
	if err != nil {
		t.Fatalf("NewState() error = %v", err)
	}
	_, err = callState(t, s, "state.get", `{"key":"a"}`)
	if err == nil {
		t.Fatal("state.get error = nil, want a non-object state file to be rejected")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error = %q, want it to name the path %q", err, path)
	}
}
