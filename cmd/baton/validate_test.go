package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/exitcode"
)

func TestValidateJSON(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "s.yaml")
		if err := os.WriteFile(path, []byte(helloScenario), 0o644); err != nil {
			t.Fatalf("write scenario: %v", err)
		}

		var code int
		stdout, _ := captureOutput(t, func() {
			code = validateCmd([]string{path, "--json"})
		})
		if code != exitcode.OK {
			t.Fatalf("exit code = %d, want %d", code, exitcode.OK)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(stdout), &out); err != nil {
			t.Fatalf("unmarshal: %v\noutput: %s", err, stdout)
		}
		if out["ok"] != true {
			t.Fatalf("ok = %v, want true", out["ok"])
		}
		errs, ok := out["errors"].([]any)
		if !ok {
			t.Fatalf("errors is not an array: %v", out["errors"])
		}
		if len(errs) != 0 {
			t.Fatalf("errors = %v, want empty", errs)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "s.yaml")
		bad := "name: bad\nversion: 1\nsteps:\n  - run:\n      argv: [\"true\"]\n"
		if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
			t.Fatalf("write scenario: %v", err)
		}

		var code int
		stdout, stderr := captureOutput(t, func() {
			code = validateCmd([]string{path, "--json"})
		})
		if code != exitcode.Config {
			t.Fatalf("exit code = %d, want %d", code, exitcode.Config)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(stdout), &out); err != nil {
			t.Fatalf("unmarshal: %v\noutput: %s", err, stdout)
		}
		if out["ok"] != false {
			t.Fatalf("ok = %v, want false", out["ok"])
		}
		errs, ok := out["errors"].([]any)
		if !ok || len(errs) < 1 {
			t.Fatalf("errors = %v, want at least one", out["errors"])
		}
		if strings.Contains(stderr, "error:") {
			t.Fatalf("stderr should not contain error: in JSON mode, got %q", stderr)
		}
	})
}
