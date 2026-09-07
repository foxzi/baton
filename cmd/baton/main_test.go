package main

import (
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
