package ifaces

import (
	"sort"
	"strings"
	"testing"
)

// TestNames checks the registry exposes exactly the three v1 interfaces.
func TestNames(t *testing.T) {
	got := Names()
	want := []string{"forge/v1", "notify/v1", "tracker/v1"}
	if len(got) != len(want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Names() = %v, want %v", got, want)
		}
	}
}

// TestGetUnknown checks Get reports absence for an unregistered name.
func TestGetUnknown(t *testing.T) {
	if _, ok := Get("forge/v2"); ok {
		t.Fatalf("Get(%q) ok = true, want false", "forge/v2")
	}
}

// TestOpsLoaded checks that every op of every interface has a description,
// at least one argument and a result schema that validates a hand-built
// valid instance.
func TestOpsLoaded(t *testing.T) {
	cases := []struct {
		ref    string
		result any
	}{
		{"forge/v1.get_change", map[string]any{
			"id": "1", "title": "t",
			"files": []any{map[string]any{"path": "a.go"}},
		}},
		{"forge/v1.list_files", []any{map[string]any{"path": "a.go"}}},
		{"forge/v1.get_file", map[string]any{"content": "hello"}},
		{"forge/v1.post_comment", map[string]any{"id": "1"}},
		{"forge/v1.post_review", map[string]any{"id": "1"}},
		{"tracker/v1.get_issue", map[string]any{"key": "K-1", "title": "t"}},
		{"tracker/v1.search", []any{map[string]any{"key": "K-1"}}},
		{"tracker/v1.create_issue", map[string]any{"key": "K-1"}},
		{"tracker/v1.comment", map[string]any{"id": "1"}},
		{"notify/v1.send", map[string]any{"id": "1"}},
	}

	for _, c := range cases {
		t.Run(c.ref, func(t *testing.T) {
			iface, op, err := Lookup(c.ref)
			if err != nil {
				t.Fatalf("Lookup(%q) error = %v", c.ref, err)
			}
			if op.Description == "" {
				t.Errorf("%s: empty description", c.ref)
			}
			if len(op.Args) == 0 {
				t.Errorf("%s: no args", c.ref)
			}
			if err := op.ValidateResult(c.result); err != nil {
				t.Errorf("%s: ValidateResult(%v) error = %v", c.ref, c.result, err)
			}
			_ = iface
		})
	}
}

// TestValidateResultMissingRequired checks a result missing a required
// field is rejected.
func TestValidateResultMissingRequired(t *testing.T) {
	_, op, err := Lookup("forge/v1.get_change")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	result := map[string]any{"id": "1", "title": "t"} // missing "files"
	if err := op.ValidateResult(result); err == nil {
		t.Fatalf("ValidateResult() error = nil, want an error for missing files")
	}
}

// TestValidateResultExtraFields checks a result carrying fields the schema
// does not name is accepted.
func TestValidateResultExtraFields(t *testing.T) {
	_, op, err := Lookup("forge/v1.get_change")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	result := map[string]any{
		"id": "1", "title": "t",
		"files":       []any{map[string]any{"path": "a.go"}},
		"extra_field": "vendor-specific",
	}
	if err := op.ValidateResult(result); err != nil {
		t.Fatalf("ValidateResult() error = %v, want nil", err)
	}
}

// TestValidateResultIDStringOrInteger checks post_comment's id accepts
// both a string and an integer.
func TestValidateResultIDStringOrInteger(t *testing.T) {
	_, op, err := Lookup("forge/v1.post_comment")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if err := op.ValidateResult(map[string]any{"id": "1"}); err != nil {
		t.Errorf("ValidateResult(string id) error = %v", err)
	}
	if err := op.ValidateResult(map[string]any{"id": 1}); err != nil {
		t.Errorf("ValidateResult(integer id) error = %v", err)
	}
}

// TestParseRef covers the happy path and every rejected shape.
func TestParseRef(t *testing.T) {
	iface, op, err := ParseRef("forge/v1.get_change")
	if err != nil {
		t.Fatalf("ParseRef() error = %v", err)
	}
	if iface != "forge/v1" || op != "get_change" {
		t.Fatalf("ParseRef() = (%q, %q), want (%q, %q)", iface, op, "forge/v1", "get_change")
	}

	badRefs := []string{"", "forge/v1", ".get_change", "forge/v1."}
	for _, ref := range badRefs {
		if _, _, err := ParseRef(ref); err == nil {
			t.Errorf("ParseRef(%q) error = nil, want an error", ref)
		}
	}
}

// TestLookupUnknownInterface checks the error names the known interfaces.
func TestLookupUnknownInterface(t *testing.T) {
	_, _, err := Lookup("forge/v2.get_change")
	if err == nil {
		t.Fatalf("Lookup() error = nil, want an error")
	}
	for _, name := range []string{"forge/v1", "notify/v1", "tracker/v1"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("Lookup() error = %q, want it to mention %q", err, name)
		}
	}
}

// TestLookupUnknownOp checks the error names the known operations of the
// interface.
func TestLookupUnknownOp(t *testing.T) {
	_, _, err := Lookup("forge/v1.get_diff")
	if err == nil {
		t.Fatalf("Lookup() error = nil, want an error")
	}
	for _, name := range []string{"get_change", "get_file", "list_files", "post_comment", "post_review"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("Lookup() error = %q, want it to mention %q", err, name)
		}
	}
}

// TestLookupHappyPath checks Lookup returns the expected interface and op.
func TestLookupHappyPath(t *testing.T) {
	iface, op, err := Lookup("tracker/v1.get_issue")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if iface.Name != "tracker/v1" {
		t.Errorf("iface.Name = %q, want %q", iface.Name, "tracker/v1")
	}
	if op.Name != "get_issue" {
		t.Errorf("op.Name = %q, want %q", op.Name, "get_issue")
	}
}

// TestArgNamesAndOpNamesSorted checks the two sorted accessors.
func TestArgNamesAndOpNamesSorted(t *testing.T) {
	iface, ok := Get("forge/v1")
	if !ok {
		t.Fatalf("Get(%q) ok = false", "forge/v1")
	}
	opNames := iface.OpNames()
	if !sort.StringsAreSorted(opNames) {
		t.Errorf("OpNames() = %v, not sorted", opNames)
	}

	op, ok := iface.Op("get_change")
	if !ok {
		t.Fatalf("Op(%q) ok = false", "get_change")
	}
	argNames := op.ArgNames()
	if !sort.StringsAreSorted(argNames) {
		t.Errorf("ArgNames() = %v, not sorted", argNames)
	}
	want := []string{"id", "project"}
	if len(argNames) != len(want) || argNames[0] != want[0] || argNames[1] != want[1] {
		t.Errorf("ArgNames() = %v, want %v", argNames, want)
	}
}
