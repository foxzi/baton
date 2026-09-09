package scenario

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// demoPack is the smallest pack the checks below need: one readonly
// operation with a described argument and one that writes.
const demoPack = `
pack: demo
version: 1
description: Demo service

config:
  base_url: { default: "https://demo.invalid" }

ops:
  get_thing:
    get: /things/{id}
    description: Read one thing
    readonly: true
    params:
      id: { in: path, pattern: '^\d+$' }
  make_thing:
    post: /things
    encode: json
    params:
      title: { in: body, max_len: 50 }
      note: { in: body, max_len: 50, required: false }
`

// writeDemoScenario writes the demo pack and a scenario that binds it into a
// fresh directory and returns the scenario path.
func writeDemoScenario(t *testing.T, steps string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "demo.yaml"), []byte(demoPack), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	const header = `
version: 1
name: demo
apis:
  demo:
    pack: demo
    from: ./
steps:
`
	path := filepath.Join(dir, "scn.yaml")
	if err := os.WriteFile(path, []byte(header+steps), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

// TestCheckPacksOperations covers section 4 check 13: the operation must
// exist, the arguments must be the ones it declares, and the required ones
// must be there.
func TestCheckPacksOperations(t *testing.T) {
	cases := []struct {
		name     string
		steps    string
		wantErr  string
		wantWarn string
	}{
		{
			name: "a good call passes",
			steps: `  - id: thing
    http: { op: demo.get_thing, args: { id: "7" } }
`,
		},
		{
			name: "unknown operation",
			steps: `  - id: thing
    http: { op: demo.get_thin, args: { id: "7" } }
`,
			wantErr: `no operation "get_thin"`,
		},
		{
			name: "unknown argument",
			steps: `  - id: thing
    http: { op: demo.get_thing, args: { id: "7", depth: "2" } }
`,
			wantErr: "operation get_thing has no such argument",
		},
		{
			name: "missing required argument",
			steps: `  - id: thing
    http: { op: demo.get_thing, args: {} }
`,
			wantErr: "operation get_thing requires the argument id",
		},
		{
			name: "an optional argument may be left out",
			steps: `  - id: thing
    http:
      op: demo.make_thing
      args: { title: hello }
    dedupe_key: "thing-{{ .run.id }}"
`,
		},
		{
			name: "a writing operation wants a dedupe key",
			steps: `  - id: thing
    http: { op: demo.make_thing, args: { title: hello } }
`,
			wantWarn: "operation make_thing is not readonly",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scn, err := Load(writeDemoScenario(t, tc.steps))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			res := CheckPacks(scn)

			if tc.wantErr == "" {
				if !res.OK() {
					t.Fatalf("OK() = false, Errors = %v", res.Errors)
				}
			} else if !diagnosticsContain(res.Errors, tc.wantErr) {
				t.Errorf("Errors = %v, want one about %s", res.Errors, tc.wantErr)
			}

			if tc.wantWarn == "" {
				if len(res.Warnings) != 0 {
					t.Errorf("Warnings = %v, want none", res.Warnings)
				}
			} else if !diagnosticsContain(res.Warnings, tc.wantWarn) {
				t.Errorf("Warnings = %v, want one about %s", res.Warnings, tc.wantWarn)
			}
		})
	}
}

// TestCheckPacksAgentTools covers the other half of check 13: an agent may
// only be handed readonly operations.
func TestCheckPacksAgentTools(t *testing.T) {
	steps := `  - id: work
    agent:
      engine: claude_code
      prompt: work
      tools:
        apis: [demo.make_thing]
`
	scn, err := Load(writeDemoScenario(t, steps))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	res := CheckPacks(scn)
	if !diagnosticsContain(res.Errors, "is not readonly and cannot be given to a model") {
		t.Errorf("Errors = %v, want one about a writing operation", res.Errors)
	}
}

// TestCheckPacksTemplatedPack checks that a pack chosen at run time is left
// to the engine rather than guessed at.
func TestCheckPacksTemplatedPack(t *testing.T) {
	dir := t.TempDir()
	yaml := `
version: 1
name: demo
inputs:
  forge: { type: string, default: gitlab }
apis:
  demo:
    pack: "{{ .inputs.forge }}"
    from: ./
steps:
  - id: thing
    http: { op: demo.get_thing, args: { id: "7" } }
`
	path := filepath.Join(dir, "scn.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	scn, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	res := CheckPacks(scn)
	if !res.OK() {
		t.Fatalf("OK() = false, Errors = %v", res.Errors)
	}
	if !diagnosticsContain(res.Warnings, "pack name is a template") {
		t.Errorf("Warnings = %v, want one about the templated pack name", res.Warnings)
	}
}

// TestCheckPacksMissingPack reports an unloadable pack as an error of the
// apis entry (section 4, check 12).
func TestCheckPacksMissingPack(t *testing.T) {
	scn, err := Load(writeDemoScenario(t, "  - id: noop\n    run: { argv: [\"true\"] }\n"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	entry := scn.APIs["demo"]
	entry.Pack = "absent"
	scn.APIs["demo"] = entry

	res := CheckPacks(scn)
	if res.OK() {
		t.Fatal("OK() = true, want an error for the missing pack")
	}
	if !strings.Contains(res.Errors[0].Path, "apis.demo") {
		t.Errorf("Errors[0].Path = %q, want the apis entry", res.Errors[0].Path)
	}
}
