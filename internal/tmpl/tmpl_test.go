package tmpl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/values"
)

// TestRenderFunctions checks each template function through Renderer.Render,
// covering the common case for every function in the template FuncMap.
func TestRenderFunctions(t *testing.T) {
	cases := []struct {
		name string
		text string
		data map[string]any
		want string
	}{
		{
			name: "toJSON",
			text: `{{ toJSON .m }}`,
			data: map[string]any{"m": map[string]any{"a": 1}},
			want: `{"a":1}`,
		},
		{
			name: "fromJSON",
			text: `{{ (fromJSON .txt).a }}`,
			data: map[string]any{"txt": `{"a":"value"}`},
			want: "value",
		},
		{
			name: "coalesce picks first non-empty",
			text: `{{ coalesce .a .b }}`,
			data: map[string]any{"a": "", "b": "second"},
			want: "second",
		},
		{
			name: "default falls back on empty",
			text: `{{ .a | default "none" }}`,
			data: map[string]any{"a": ""},
			want: "none",
		},
		{
			name: "default keeps non-empty value",
			text: `{{ .a | default "none" }}`,
			data: map[string]any{"a": "present"},
			want: "present",
		},
		{
			name: "trunc ascii",
			text: `{{ trunc 3 .s }}`,
			data: map[string]any{"s": "hello"},
			want: "hel",
		},
		{
			name: "trunc multi-byte runes",
			text: `{{ trunc 3 .s }}`,
			data: map[string]any{"s": "привет"},
			want: "при",
		},
		{
			name: "indent",
			text: `{{ indent 2 .s }}`,
			data: map[string]any{"s": "a\nb"},
			want: "  a\n  b",
		},
		{
			name: "join",
			text: `{{ join "," .list }}`,
			data: map[string]any{"list": []string{"a", "b", "c"}},
			want: "a,b,c",
		},
		{
			name: "dict",
			text: `{{ (dict "a" 1).a }}`,
			data: map[string]any{},
			want: "1",
		},
		{
			name: "quote",
			text: `{{ quote .s }}`,
			data: map[string]any{"s": "hello"},
			want: `"hello"`,
		},
	}

	r := NewRenderer(t.TempDir())
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.Render(tc.name, tc.text, tc.data)
			if err != nil {
				t.Fatalf("Render(%q) returned error: %v", tc.text, err)
			}
			if got != tc.want {
				t.Errorf("Render(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

// TestTruncEdgeCases checks that trunc returns an empty string for n <= 0.
func TestTruncEdgeCases(t *testing.T) {
	cases := []struct {
		name string
		n    int
	}{
		{name: "zero", n: 0},
		{name: "negative", n: -1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := trunc(tc.n, "hello"); got != "" {
				t.Errorf("trunc(%d, ...) = %q, want empty string", tc.n, got)
			}
		})
	}
}

// TestIndentEdgeCases checks that indent leaves text unchanged for n <= 0.
func TestIndentEdgeCases(t *testing.T) {
	cases := []struct {
		name string
		n    int
	}{
		{name: "zero", n: 0},
		{name: "negative", n: -1},
	}

	text := "a\nb"
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := indent(tc.n, text); got != text {
				t.Errorf("indent(%d, ...) = %q, want unchanged %q", tc.n, got, text)
			}
		})
	}
}

// TestDictErrors checks that dict rejects an odd number of arguments and a
// non-string key.
func TestDictErrors(t *testing.T) {
	t.Run("odd number of arguments", func(t *testing.T) {
		if _, err := dict("a"); err == nil {
			t.Fatalf("dict(\"a\") succeeded, want error")
		}
	})

	t.Run("non-string key", func(t *testing.T) {
		if _, err := dict(1, 2); err == nil {
			t.Fatalf("dict(1, 2) succeeded, want error")
		}
	})
}

// TestJoinNonList checks that join rejects a value that is not a slice or
// array.
func TestJoinNonList(t *testing.T) {
	if _, err := join(",", 42); err == nil {
		t.Fatalf("join(\",\", 42) succeeded, want error")
	}
}

// TestMissingKeys checks the behaviour around absent map keys: default and
// coalesce handle them, while dereferencing a field on a missing key is an
// execution error.
func TestMissingKeys(t *testing.T) {
	r := NewRenderer(t.TempDir())

	t.Run("default on missing key", func(t *testing.T) {
		data := map[string]any{"inputs": map[string]any{}}
		got, err := r.Render("t", `{{ .inputs.missing | default "none" }}`, data)
		if err != nil {
			t.Fatalf("Render returned error: %v", err)
		}
		if got != "none" {
			t.Errorf("Render() = %q, want %q", got, "none")
		}
	})

	t.Run("coalesce skips missing key", func(t *testing.T) {
		data := map[string]any{"inputs": map[string]any{"name": "bob"}}
		got, err := r.Render("t", `{{ coalesce .inputs.nope .inputs.name }}`, data)
		if err != nil {
			t.Fatalf("Render returned error: %v", err)
		}
		if got != "bob" {
			t.Errorf("Render() = %q, want %q", got, "bob")
		}
	})

	t.Run("reading through a missing root key is an error", func(t *testing.T) {
		data := map[string]any{"inputs": map[string]any{}}
		if _, err := r.Render("t", `{{ .nosuchroot.x }}`, data); err == nil {
			t.Fatalf("Render succeeded, want error")
		}
	})
}

// TestRenderRejectsSecrets checks that Render refuses data containing a
// values.Secret, whether it appears directly or nested inside a slice inside
// a map, and that the plaintext never reaches the output.
func TestRenderRejectsSecrets(t *testing.T) {
	r := NewRenderer(t.TempDir())

	t.Run("secret as a direct map value", func(t *testing.T) {
		secret := values.NewSecret("gitlab_token", "s3cr3t-plaintext")
		data := map[string]any{"token": secret}

		out, err := r.Render("t", "ok", data)
		if err == nil {
			t.Fatalf("Render succeeded, want error")
		}
		if !strings.Contains(err.Error(), "gitlab_token") {
			t.Errorf("error = %q, want it to mention %q", err.Error(), "gitlab_token")
		}
		if strings.Contains(out, "s3cr3t-plaintext") {
			t.Errorf("output contains secret plaintext: %q", out)
		}
	})

	t.Run("secret nested inside a slice inside a map", func(t *testing.T) {
		secret := values.NewSecret("nested_token", "another-plaintext")
		data := map[string]any{
			"list": []any{
				map[string]any{"secret": secret},
			},
		}

		out, err := r.Render("t", "ok", data)
		if err == nil {
			t.Fatalf("Render succeeded, want error")
		}
		if !strings.Contains(err.Error(), "nested_token") {
			t.Errorf("error = %q, want it to mention %q", err.Error(), "nested_token")
		}
		if strings.Contains(out, "another-plaintext") {
			t.Errorf("output contains secret plaintext: %q", out)
		}
	})
}

// TestCheckRejectsSecretReferences checks that Check rejects a template
// referencing .secrets, whether directly or nested inside if/range blocks,
// accepts a normal template, and reports a parse error for broken syntax.
func TestCheckRejectsSecretReferences(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		wantErr bool
		wantSub string
	}{
		{
			name:    "direct secrets reference",
			text:    `{{ .secrets.gitlab }}`,
			wantErr: true,
			wantSub: "secrets",
		},
		{
			name:    "secrets reference inside if",
			text:    `{{ if true }}{{ .secrets.gitlab }}{{ end }}`,
			wantErr: true,
			wantSub: "secrets",
		},
		{
			name:    "secrets reference inside range",
			text:    `{{ range .items }}{{ .secrets.gitlab }}{{ end }}`,
			wantErr: true,
			wantSub: "secrets",
		},
		{
			name:    "normal template",
			text:    `{{ .inputs.name }}`,
			wantErr: false,
		},
		{
			name:    "broken syntax",
			text:    `{{ .x `,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Check(tc.name, tc.text)
			if tc.wantErr && err == nil {
				t.Fatalf("Check(%q) succeeded, want error", tc.text)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Check(%q) returned error: %v", tc.text, err)
			}
			if tc.wantSub != "" && !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("Check(%q) error = %q, want it to mention %q", tc.text, err.Error(), tc.wantSub)
			}
		})
	}
}

// TestFunctionsRejectSecrets checks that toJSON, quote and join each refuse
// to serialize a values.Secret and never leak the plaintext.
func TestFunctionsRejectSecrets(t *testing.T) {
	secret := values.NewSecret("api_key", "top-secret-value")

	t.Run("toJSON", func(t *testing.T) {
		out, err := toJSON(secret)
		if err == nil {
			t.Fatalf("toJSON succeeded, want error")
		}
		if strings.Contains(out, "top-secret-value") {
			t.Errorf("toJSON output contains secret plaintext: %q", out)
		}
	})

	t.Run("quote", func(t *testing.T) {
		out, err := quote(secret)
		if err == nil {
			t.Fatalf("quote succeeded, want error")
		}
		if strings.Contains(out, "top-secret-value") {
			t.Errorf("quote output contains secret plaintext: %q", out)
		}
	})

	t.Run("join", func(t *testing.T) {
		out, err := join(",", []any{secret})
		if err == nil {
			t.Fatalf("join succeeded, want error")
		}
		if strings.Contains(out, "top-secret-value") {
			t.Errorf("join output contains secret plaintext: %q", out)
		}
	})
}

// TestRootFields checks that RootFields returns the distinct root field names
// in order of first appearance.
func TestRootFields(t *testing.T) {
	text := `{{ .inputs.name }} {{ .steps.lint.status }} {{ if .run.id }}{{ .run.name }}{{ end }}`
	got, err := RootFields("t", text)
	if err != nil {
		t.Fatalf("RootFields returned error: %v", err)
	}
	want := []string{"inputs", "steps", "run"}
	if len(got) != len(want) {
		t.Fatalf("RootFields() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("RootFields()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestRenderIncludesChildTemplate checks that the render() template function
// reads and embeds another template file relative to the renderer base
// directory, and returns an error for a missing file.
func TestRenderIncludesChildTemplate(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "child.tmpl"), []byte("child:{{ .name }}"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	r := NewRenderer(dir)

	t.Run("includes child output", func(t *testing.T) {
		data := map[string]any{"name": "joe"}
		got, err := r.Render("parent", `parent {{ render "child.tmpl" . }}`, data)
		if err != nil {
			t.Fatalf("Render returned error: %v", err)
		}
		want := "parent child:joe"
		if got != want {
			t.Errorf("Render() = %q, want %q", got, want)
		}
	})

	t.Run("missing file errors", func(t *testing.T) {
		data := map[string]any{"name": "joe"}
		if _, err := r.Render("parent", `{{ render "missing.tmpl" . }}`, data); err == nil {
			t.Fatalf("Render succeeded, want error")
		}
	})
}

// TestRenderRecursionDepthLimit checks that a template including itself fails
// with a nesting-depth error instead of hanging or crashing.
func TestRenderRecursionDepthLimit(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "self.tmpl"), []byte(`{{ render "self.tmpl" . }}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	r := NewRenderer(dir)
	_, err := r.RenderFile("self.tmpl", map[string]any{})
	if err == nil {
		t.Fatalf("RenderFile succeeded, want error")
	}
	if !strings.Contains(err.Error(), "nested") {
		t.Errorf("error = %q, want it to mention nesting depth", err.Error())
	}
}

// TestRenderFileChecksIncludedFiles checks that RenderFile applies Check to
// the file content, rejecting a reference to .secrets.
func TestRenderFileChecksIncludedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secret.tmpl"), []byte(`{{ .secrets.gitlab }}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	r := NewRenderer(dir)
	if _, err := r.RenderFile("secret.tmpl", map[string]any{}); err == nil {
		t.Fatalf("RenderFile succeeded, want error")
	}
}
