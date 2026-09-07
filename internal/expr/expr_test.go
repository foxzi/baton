package expr

import (
	"fmt"
	"strings"
	"testing"
)

// TestCompileValid checks that Compile succeeds for expressions that only
// touch fields present in Context.
func TestCompileValid(t *testing.T) {
	cases := []struct {
		name   string
		source string
	}{
		{name: "inputs comparison", source: `inputs.mr_iid > 0`},
		{name: "step status equality", source: `steps.lint.status == "success"`},
		{name: "step exit code", source: `steps.lint.exit_code == 0`},
		{name: "run name not empty", source: `run.name != ""`},
		{name: "iter field", source: `iter.n`},
		{name: "len of step items", source: `len(steps.build.items) > 0`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			program, err := Compile(tc.source)
			if err != nil {
				t.Fatalf("Compile(%q) returned error: %v", tc.source, err)
			}
			if program == nil {
				t.Fatalf("Compile(%q) returned nil program with nil error", tc.source)
			}
		})
	}
}

// TestCompileSecretsAbsent checks that an expression reading a secret fails to
// compile, since Context has no secrets field.
func TestCompileSecretsAbsent(t *testing.T) {
	source := `secrets.token != ""`
	_, err := Compile(source)
	if err == nil {
		t.Fatalf("Compile(%q) succeeded, want error", source)
	}
	if !strings.Contains(err.Error(), "secrets") {
		t.Errorf("Compile(%q) error = %q, want it to mention %q", source, err.Error(), "secrets")
	}
}

// TestCompileUnknownField checks that referencing a name outside of Context,
// or a field a Step does not have, fails to compile.
func TestCompileUnknownField(t *testing.T) {
	cases := []struct {
		name   string
		source string
		want   string
	}{
		{name: "unknown root name", source: `nope.field`, want: "nope"},
		{name: "unknown step field", source: `steps.lint.nosuch`, want: "nosuch"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compile(tc.source)
			if err == nil {
				t.Fatalf("Compile(%q) succeeded, want error", tc.source)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Compile(%q) error = %q, want it to mention %q", tc.source, err.Error(), tc.want)
			}
		})
	}
}

// TestCompileSyntaxError checks that a syntactically broken expression fails
// to compile instead of panicking.
func TestCompileSyntaxError(t *testing.T) {
	source := `inputs.n >`
	_, err := Compile(source)
	if err == nil {
		t.Fatalf("Compile(%q) succeeded, want error", source)
	}
}

// TestCompileBool checks that CompileBool rejects a non-boolean expression
// and accepts one that evaluates to a boolean.
func TestCompileBool(t *testing.T) {
	t.Run("rejects non-boolean", func(t *testing.T) {
		source := `run.name`
		_, err := CompileBool(source)
		if err == nil {
			t.Fatalf("CompileBool(%q) succeeded, want error", source)
		}
	})

	t.Run("accepts boolean", func(t *testing.T) {
		source := `run.name != ""`
		program, err := CompileBool(source)
		if err != nil {
			t.Fatalf("CompileBool(%q) returned error: %v", source, err)
		}
		if program == nil {
			t.Fatalf("CompileBool(%q) returned nil program with nil error", source)
		}
	})
}

// TestProgramSource checks that Source returns the exact text the program was
// compiled from.
func TestProgramSource(t *testing.T) {
	source := `inputs.mr_iid > 0`
	program, err := Compile(source)
	if err != nil {
		t.Fatalf("Compile(%q) returned error: %v", source, err)
	}
	if got := program.Source(); got != source {
		t.Errorf("Source() = %q, want %q", got, source)
	}
}

// TestEvalValue checks that Eval computes the expression against the given
// context and returns the expected value.
func TestEvalValue(t *testing.T) {
	program, err := Compile(`inputs.n * 2`)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}

	ctx := Context{Inputs: map[string]any{"n": 3}}
	out, err := program.Eval(ctx)
	if err != nil {
		t.Fatalf("Eval returned error: %v", err)
	}

	if fmt.Sprint(out) != "6" {
		t.Errorf("Eval() = %v (%T), want 6", out, out)
	}
}

// TestEvalBool checks that EvalBool returns the correct boolean for a
// compound expression combining a step status and an input.
func TestEvalBool(t *testing.T) {
	program, err := CompileBool(`steps.lint.status == "success" && inputs.n > 1`)
	if err != nil {
		t.Fatalf("CompileBool returned error: %v", err)
	}

	cases := []struct {
		name string
		ctx  Context
		want bool
	}{
		{
			name: "status success and n above threshold",
			ctx: Context{
				Inputs: map[string]any{"n": 2},
				Steps:  map[string]Step{"lint": {Status: StatusSuccess}},
			},
			want: true,
		},
		{
			name: "status failed",
			ctx: Context{
				Inputs: map[string]any{"n": 2},
				Steps:  map[string]Step{"lint": {Status: StatusFailed}},
			},
			want: false,
		},
		{
			name: "n at threshold",
			ctx: Context{
				Inputs: map[string]any{"n": 1},
				Steps:  map[string]Step{"lint": {Status: StatusSuccess}},
			},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := program.EvalBool(tc.ctx)
			if err != nil {
				t.Fatalf("EvalBool returned error: %v", err)
			}
			if got != tc.want {
				t.Errorf("EvalBool() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestEvalBoolNonBoolean checks that EvalBool rejects, at evaluation time, a
// program compiled with Compile (not CompileBool) that produces a
// non-boolean value.
func TestEvalBoolNonBoolean(t *testing.T) {
	program, err := Compile(`run.name`)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}

	_, err = program.EvalBool(Context{Run: Run{Name: "release"}})
	if err == nil {
		t.Fatalf("EvalBool succeeded, want error")
	}
	if !strings.Contains(err.Error(), "expected bool") {
		t.Errorf("EvalBool error = %q, want it to mention %q", err.Error(), "expected bool")
	}
}

// TestEvalZeroContext checks that Eval on a completely zero Context does not
// panic on the nil maps and evaluates the expression correctly.
func TestEvalZeroContext(t *testing.T) {
	program, err := Compile(`run.id == ""`)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}

	out, err := program.Eval(Context{})
	if err != nil {
		t.Fatalf("Eval returned error: %v", err)
	}
	if out != true {
		t.Errorf("Eval() = %v, want true", out)
	}
}

// TestEvalRuntimeError checks that a runtime failure (reading a field off a
// nil result) surfaces as an error rather than a panic.
func TestEvalRuntimeError(t *testing.T) {
	program, err := Compile(`steps.missing.result.field`)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}

	if _, err := program.Eval(Context{}); err == nil {
		t.Fatalf("Eval succeeded, want error")
	}
}
