package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/foxzi/baton/internal/exitcode"
)

func TestRunExitCodes(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"no arguments", nil, exitcode.Config},
		{"version", []string{"version"}, exitcode.OK},
		{"version flag", []string{"--version"}, exitcode.OK},
		{"help", []string{"help"}, exitcode.OK},
		{"unknown command", []string{"nope"}, exitcode.Config},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(tc.args); got != tc.want {
				t.Errorf("run(%q) = %d, want %d", tc.args, got, tc.want)
			}
		})
	}
}

// TestRunValidateCommand checks the exit codes of `baton validate` for
// missing arguments, a valid scenario, a missing file and an invalid
// scenario.
func TestRunValidateCommand(t *testing.T) {
	invalidPath := filepath.Join(t.TempDir(), "invalid.yaml")
	invalidYAML := []byte("version: 2\nname: \"\"\nsteps: []\n")
	if err := os.WriteFile(invalidPath, invalidYAML, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"no file argument", []string{"validate"}, exitcode.Config},
		{"valid scenario", []string{"validate", "../../examples/hello.yaml"}, exitcode.OK},
		{"missing file", []string{"validate", "does-not-exist.yaml"}, exitcode.Config},
		{"invalid scenario", []string{"validate", invalidPath}, exitcode.Config},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(tc.args); got != tc.want {
				t.Errorf("run(%q) = %d, want %d", tc.args, got, tc.want)
			}
		})
	}
}
