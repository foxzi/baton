package scenario

import (
	"strings"
	"testing"
)

// TestParseRunStringForm checks that a bare command string splits into argv
// and is flagged as split-from-string.
func TestParseRunStringForm(t *testing.T) {
	data := []byte(`
version: 1
name: t
steps:
  - id: s
    run: "git status"
`)
	scn, err := Parse(data, "test.yaml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	run := scn.Steps[0].Run
	if run == nil {
		t.Fatalf("Steps[0].Run = nil")
	}
	want := []string{"git", "status"}
	if len(run.Argv) != len(want) {
		t.Fatalf("Argv = %v, want %v", run.Argv, want)
	}
	for i := range want {
		if run.Argv[i] != want[i] {
			t.Errorf("Argv[%d] = %q, want %q", i, run.Argv[i], want[i])
		}
	}
	if !run.SplitFromString() {
		t.Errorf("SplitFromString() = false, want true")
	}
}

// TestParseRunStringWithMetacharacter checks that shell metacharacters in
// the bare-string form are rejected.
func TestParseRunStringWithMetacharacter(t *testing.T) {
	data := []byte(`
version: 1
name: t
steps:
  - id: s
    run: "cat a | wc -l"
`)
	_, err := Parse(data, "test.yaml")
	if err == nil {
		t.Fatalf("Parse() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "shell metacharacters") {
		t.Errorf("Parse() error = %q, want mention of shell metacharacters", err)
	}
}

// TestParseUnknownStepField checks that an unknown step field is rejected by
// name.
func TestParseUnknownStepField(t *testing.T) {
	data := []byte(`
version: 1
name: t
steps:
  - id: s
    runn:
      argv: ["echo", "hi"]
`)
	_, err := Parse(data, "test.yaml")
	if err == nil {
		t.Fatalf("Parse() error = nil, want error")
	}
	if !strings.Contains(err.Error(), `"runn"`) {
		t.Errorf("Parse() error = %q, want mention of field %q", err, "runn")
	}
}

// TestParseUnknownTopLevelField checks that an unknown top-level field is
// rejected.
func TestParseUnknownTopLevelField(t *testing.T) {
	data := []byte(`
version: 1
name: t
bogus: true
steps:
  - id: s
    run:
      argv: ["echo", "hi"]
`)
	_, err := Parse(data, "test.yaml")
	if err == nil {
		t.Fatalf("Parse() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("Parse() error = %q, want mention of field %q", err, "bogus")
	}
}

// TestParseEnvValue checks the two accepted forms of an env entry.
func TestParseEnvValue(t *testing.T) {
	cases := []struct {
		name       string
		yaml       string
		wantErr    bool
		wantValue  string
		wantSecret string
	}{
		{
			name: "literal string",
			yaml: `
version: 1
name: t
steps:
  - id: s
    run:
      argv: ["echo", "hi"]
      env:
        FOO: bar
`,
			wantValue: "bar",
		},
		{
			name: "secret reference",
			yaml: `
version: 1
name: t
steps:
  - id: s
    run:
      argv: ["echo", "hi"]
      env:
        FOO:
          secret: my_secret
`,
			wantSecret: "my_secret",
		},
		{
			name: "empty secret reference",
			yaml: `
version: 1
name: t
steps:
  - id: s
    run:
      argv: ["echo", "hi"]
      env:
        FOO:
          secret:
`,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scn, err := Parse([]byte(tc.yaml), "test.yaml")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Parse() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			entry := scn.Steps[0].Run.Env["FOO"]
			if entry.Value != tc.wantValue {
				t.Errorf("Value = %q, want %q", entry.Value, tc.wantValue)
			}
			if entry.Secret != tc.wantSecret {
				t.Errorf("Secret = %q, want %q", entry.Secret, tc.wantSecret)
			}
		})
	}
}

// TestParseDuration checks the string and numeric forms of Duration.
func TestParseDuration(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr bool
		want    Duration
	}{
		{name: "string form", value: "10m", want: Duration(10 * 60 * 1e9)},
		{name: "numeric seconds", value: "90", want: Duration(90 * 1e9)},
		{name: "garbage", value: "10x", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(`
version: 1
name: t
steps:
  - id: s
    timeout: ` + tc.value + `
    run:
      argv: ["echo", "hi"]
`)
			scn, err := Parse(data, "test.yaml")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Parse() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if got := scn.Steps[0].Timeout; got != tc.want {
				t.Errorf("Timeout = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestParseByteSize checks the suffixed and plain forms of ByteSize.
func TestParseByteSize(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr bool
		want    ByteSize
	}{
		{name: "megabyte suffix", value: "1m", want: 1048576},
		{name: "kilobyte suffix", value: "512k", want: 524288},
		{name: "plain number", value: "2048", want: 2048},
		{name: "garbage", value: "10x", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(`
version: 1
name: t
steps:
  - id: s
    run:
      argv: ["echo", "hi"]
      max_output_bytes: ` + tc.value + `
`)
			scn, err := Parse(data, "test.yaml")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Parse() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if got := scn.Steps[0].Run.MaxOutputBytes; got != tc.want {
				t.Errorf("MaxOutputBytes = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestParseStepLine checks that Step.Line records the line the step starts
// on.
func TestParseStepLine(t *testing.T) {
	data := []byte(`version: 1
name: t
steps:
  - id: first
    run:
      argv: ["echo", "one"]
  - id: second
    run:
      argv: ["echo", "two"]
`)
	scn, err := Parse(data, "test.yaml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(scn.Steps) != 2 {
		t.Fatalf("len(Steps) = %d, want 2", len(scn.Steps))
	}
	if scn.Steps[0].Line != 4 {
		t.Errorf("Steps[0].Line = %d, want 4", scn.Steps[0].Line)
	}
	if scn.Steps[1].Line != 7 {
		t.Errorf("Steps[1].Line = %d, want 7", scn.Steps[1].Line)
	}
}
