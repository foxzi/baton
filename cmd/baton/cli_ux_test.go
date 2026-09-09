package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/exitcode"
)

// TestHelpFlag_AllCommandsExitOK checks that -h/--help on every top-level
// command exits 0 and performs no action, instead of the flag package's
// default ErrHelp being folded into a configuration error.
func TestHelpFlag_AllCommandsExitOK(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"init -h", []string{"init", "-h"}},
		{"init --help", []string{"init", "--help"}},
		{"run -h", []string{"run", "-h"}},
		{"validate -h", []string{"validate", "-h"}},
		{"resume -h", []string{"resume", "-h"}},
		{"tools -h", []string{"tools", "-h"}},
		{"schema -h", []string{"schema", "-h"}},
		{"apis -h", []string{"apis", "-h"}},
		{"apis import -h", []string{"apis", "import", "-h"}},
		{"apis validate -h", []string{"apis", "validate", "-h"}},
		{"apis call -h", []string{"apis", "call", "-h"}},
		{"runs -h", []string{"runs", "-h"}},
		{"runs list -h", []string{"runs", "list", "-h"}},
		{"runs show -h", []string{"runs", "show", "-h"}},
		{"runs logs -h", []string{"runs", "logs", "-h"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var code int
			captureOutput(t, func() {
				code = run(tc.args)
			})
			if code != exitcode.OK {
				t.Fatalf("run(%v) = %d, want %d", tc.args, code, exitcode.OK)
			}
		})
	}
}

// TestHelpFlag_RejectsUnknownFlagAsConfigError checks that an ordinary
// parse error (not -h/--help) still exits Config, so the ErrHelp carve-out
// does not swallow real mistakes.
func TestHelpFlag_RejectsUnknownFlagAsConfigError(t *testing.T) {
	var code int
	captureOutput(t, func() {
		code = run([]string{"run", "--bogus-flag"})
	})
	if code != exitcode.Config {
		t.Fatalf("run() = %d, want %d", code, exitcode.Config)
	}
}

// TestHelpCmd_RejectsExtraArguments checks `baton help <a> <b>` is an error
// rather than silently printing <a>'s usage and ignoring <b>.
func TestHelpCmd_RejectsExtraArguments(t *testing.T) {
	var code int
	_, stderr := captureOutput(t, func() {
		code = run([]string{"help", "init", "extra"})
	})
	if code != exitcode.Config {
		t.Fatalf("exit code = %d, want %d", code, exitcode.Config)
	}
	if !strings.Contains(stderr, "at most one command name") {
		t.Fatalf("stderr = %q, want it to reject the extra argument", stderr)
	}
}

// TestUnknownCommand_SuggestsCloseMatch checks a one-letter typo of a
// top-level command name gets a "did you mean" hint.
func TestUnknownCommand_SuggestsCloseMatch(t *testing.T) {
	var code int
	_, stderr := captureOutput(t, func() {
		code = run([]string{"vaidate"})
	})
	if code != exitcode.Config {
		t.Fatalf("exit code = %d, want %d", code, exitcode.Config)
	}
	if !strings.Contains(stderr, `did you mean "validate"?`) {
		t.Fatalf("stderr = %q, want a suggestion for validate", stderr)
	}
}

// TestUnknownCommand_NoSuggestionWhenFar checks a name unrelated to any
// command gets no hint at all.
func TestUnknownCommand_NoSuggestionWhenFar(t *testing.T) {
	var code int
	_, stderr := captureOutput(t, func() {
		code = run([]string{"frobnicate"})
	})
	if code != exitcode.Config {
		t.Fatalf("exit code = %d, want %d", code, exitcode.Config)
	}
	if strings.Contains(stderr, "did you mean") {
		t.Fatalf("stderr = %q, want no suggestion for an unrelated name", stderr)
	}
}

// TestApisUnknownSubcommand_SuggestsCloseMatch mirrors the top-level
// suggestion for apis's own subcommands.
func TestApisUnknownSubcommand_SuggestsCloseMatch(t *testing.T) {
	var code int
	_, stderr := captureOutput(t, func() {
		code = apisCmd([]string{"improt"})
	})
	if code != exitcode.Config {
		t.Fatalf("exit code = %d, want %d", code, exitcode.Config)
	}
	if !strings.Contains(stderr, `did you mean "import"?`) {
		t.Fatalf("stderr = %q, want a suggestion for import", stderr)
	}
}

// TestRunsUnknownSubcommand_SuggestsCloseMatch mirrors the top-level
// suggestion for runs's own subcommands.
func TestRunsUnknownSubcommand_SuggestsCloseMatch(t *testing.T) {
	var code int
	_, stderr := captureOutput(t, func() {
		code = runsCmd([]string{"lst"})
	})
	if code != exitcode.Config {
		t.Fatalf("exit code = %d, want %d", code, exitcode.Config)
	}
	if !strings.Contains(stderr, `did you mean "list"?`) {
		t.Fatalf("stderr = %q, want a suggestion for list", stderr)
	}
}

// TestInitCmd_UnknownTemplate_SuggestsCloseMatch checks the same hint for
// a mistyped --template value.
func TestInitCmd_UnknownTemplate_SuggestsCloseMatch(t *testing.T) {
	dir := t.TempDir()
	var code int
	_, stderr := captureOutput(t, func() {
		code = run([]string{"init", dir, "--template", "helo"})
	})
	if code != exitcode.Config {
		t.Fatalf("exit code = %d, want %d", code, exitcode.Config)
	}
	if !strings.Contains(stderr, `did you mean "hello"?`) {
		t.Fatalf("stderr = %q, want a suggestion for hello", stderr)
	}
}

// TestInitCmd_ProviderModelWithHelloIsAnError checks that --provider or
// --model given explicitly alongside the (default or explicit) hello
// template is rejected instead of being silently ignored, and that nothing
// is written.
func TestInitCmd_ProviderModelWithHelloIsAnError(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"explicit provider", []string{"--provider", "anthropic"}},
		{"explicit model", []string{"--model", "some/model"}},
		{"explicit template hello and provider", []string{"--template", "hello", "--provider", "openai"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			args := append([]string{"init", dir}, tc.args...)

			var code int
			_, stderr := captureOutput(t, func() {
				code = run(args)
			})
			if code != exitcode.Config {
				t.Fatalf("run(%v) = %d, want %d, stderr: %s", args, code, exitcode.Config, stderr)
			}
			if !strings.Contains(stderr, "--provider/--model apply only to --template summarize") {
				t.Fatalf("stderr = %q, want a clear error naming the mismatch", stderr)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("ReadDir(%s): %v", dir, err)
			}
			if len(entries) != 0 {
				t.Errorf("dir has %d entries, want 0: nothing should be written", len(entries))
			}
		})
	}
}

// TestInitCmd_ProviderDefaultWithSummarizeStillWorks checks the guard above
// does not misfire on --provider given together with --template summarize,
// where it is meaningful.
func TestInitCmd_ProviderDefaultWithSummarizeStillWorks(t *testing.T) {
	dir := t.TempDir()
	var code int
	_, stderr := captureOutput(t, func() {
		code = run([]string{"init", dir, "--template", "summarize", "--provider", "anthropic"})
	})
	if code != exitcode.OK {
		t.Fatalf("exit code = %d, want %d, stderr: %s", code, exitcode.OK, stderr)
	}
}

// TestInitCmd_NextCommandIsShellQuoted checks a directory with a space in
// it is quoted in the printed "next:" hint, so it can be pasted into a
// shell and run as one argument.
func TestInitCmd_NextCommandIsShellQuoted(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "has space")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	var code int
	stdout, stderr := captureOutput(t, func() {
		code = run([]string{"init", dir})
	})
	if code != exitcode.OK {
		t.Fatalf("exit code = %d, want %d, stderr: %s", code, exitcode.OK, stderr)
	}
	quoted := "'" + filepath.Join(dir, "hello.yaml") + "'"
	if !strings.Contains(stdout, quoted) {
		t.Fatalf("stdout = %q, want it to quote the scenario path as %q", stdout, quoted)
	}
}

// TestShellQuote checks the quoting helper directly, including the
// embedded single-quote case that needs the '\” escape.
func TestShellQuote(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"plain", "plain"},
		{"has space", "'has space'"},
		{"quo'te", `'quo'\''te'`},
		{"", "''"},
	}
	for _, tc := range cases {
		if got := shellQuote(tc.in); got != tc.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
