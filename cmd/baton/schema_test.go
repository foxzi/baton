package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/exitcode"
)

func TestSchemaCmdJSON(t *testing.T) {
	var code int
	stdout, _ := captureOutput(t, func() { code = schemaCmd(nil) })
	if code != exitcode.OK {
		t.Fatalf("schemaCmd() = %d, want %d", code, exitcode.OK)
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(stdout), &schema); err != nil {
		t.Fatalf("schemaCmd() printed invalid JSON: %v", err)
	}
	if schema["$schema"] == nil {
		t.Error("schemaCmd() printed a document without $schema")
	}
}

func TestSchemaCmdMarkdown(t *testing.T) {
	var code int
	stdout, _ := captureOutput(t, func() { code = schemaCmd([]string{"--markdown", "ru"}) })
	if code != exitcode.OK {
		t.Fatalf("schemaCmd(--markdown ru) = %d, want %d", code, exitcode.OK)
	}
	if !strings.HasPrefix(stdout, "<!--") {
		t.Errorf("schemaCmd(--markdown ru) = %.40q, want a Markdown document", stdout)
	}
	if !strings.Contains(stdout, "## scenario") {
		t.Error("schemaCmd(--markdown ru) printed no scenario section")
	}
}

func TestSchemaCmdRejects(t *testing.T) {
	tests := map[string][]string{
		"unknown language": {"--markdown", "de"},
		"extra argument":   {"extra"},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			var code int
			captureOutput(t, func() { code = schemaCmd(args) })
			if code != exitcode.Config {
				t.Fatalf("schemaCmd(%v) = %d, want %d", args, code, exitcode.Config)
			}
		})
	}
}
