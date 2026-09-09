package values_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"text/template"

	"github.com/foxzi/baton/internal/values"
	"gopkg.in/yaml.v3"
)

func TestSecretNameAndReveal(t *testing.T) {
	s := values.NewSecret("jira_token", "s3cr3t")
	if s.Name() != "jira_token" {
		t.Errorf("Name() = %q, want jira_token", s.Name())
	}
	if s.Reveal() != "s3cr3t" {
		t.Errorf("Reveal() = %q, want the plaintext", s.Reveal())
	}
}

func TestSecretIsZero(t *testing.T) {
	if !values.NewSecret("empty", "").IsZero() {
		t.Error("IsZero() = false for a secret without a value")
	}
	if values.NewSecret("token", "x").IsZero() {
		t.Error("IsZero() = true for a secret with a value")
	}
	// A secret with no name but a value still holds something.
	if values.NewSecret("", "x").IsZero() {
		t.Error("IsZero() = true for an unnamed secret with a value")
	}
}

// TestSecretFormattingIsRedacted covers the formatting verbs a log line or a
// diagnostic is likely to reach a secret through.
func TestSecretFormattingIsRedacted(t *testing.T) {
	s := values.NewSecret("token", "s3cr3t")
	for _, verb := range []string{"%s", "%q", "%v", "%+v", "%#v", "%x", "%X", "%d"} {
		got := fmt.Sprintf(verb, s)
		if strings.Contains(got, "s3cr3t") {
			t.Errorf("fmt.Sprintf(%q, secret) = %s, leaks the plaintext", verb, got)
		}
	}
	if got := fmt.Sprintf("%s", s); got != values.Redacted {
		t.Errorf("%%s = %q, want %q", got, values.Redacted)
	}
	if got := fmt.Sprintf("%v", s); got != values.Redacted {
		t.Errorf("%%v = %q, want %q", got, values.Redacted)
	}
	if got := fmt.Sprintf("%#v", s); got != values.Redacted {
		t.Errorf("%%#v = %q, want %q", got, values.Redacted)
	}
	if got := fmt.Sprintf("%q", s); got != `"`+values.Redacted+`"` {
		t.Errorf("%%q = %s, want a quoted %s", got, values.Redacted)
	}
	if s.String() != values.Redacted {
		t.Errorf("String() = %q, want %q", s.String(), values.Redacted)
	}
}

func TestSecretMarshalling(t *testing.T) {
	s := values.NewSecret("token", "s3cr3t")

	text, err := s.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText: %v", err)
	}
	if string(text) != values.Redacted {
		t.Errorf("MarshalText() = %s, want %s", text, values.Redacted)
	}

	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if string(data) != `"`+values.Redacted+`"` {
		t.Errorf("json.Marshal() = %s, want %q", data, values.Redacted)
	}

	out, err := yaml.Marshal(s)
	if err != nil {
		t.Fatalf("yaml.Marshal: %v", err)
	}
	if strings.TrimSpace(string(out)) != "'"+values.Redacted+"'" &&
		strings.TrimSpace(string(out)) != values.Redacted {
		t.Errorf("yaml.Marshal() = %s, want %s", out, values.Redacted)
	}
}

// TestSecretNestedInStructIsRedacted is the case that matters in practice: a
// secret carried inside the structures the engine writes to run.json.
func TestSecretNestedInStructIsRedacted(t *testing.T) {
	payload := struct {
		Name  string         `json:"name"`
		Token values.Secret  `json:"token"`
		Extra map[string]any `json:"extra"`
	}{
		Name:  "jira",
		Token: values.NewSecret("token", "s3cr3t"),
		Extra: map[string]any{"auth": values.NewSecret("auth", "s3cr3t")},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(data), "s3cr3t") {
		t.Errorf("json.Marshal() = %s, leaks the plaintext", data)
	}
}

// TestSecretInTemplateIsRedacted covers rendering, the other path a scenario
// can push a secret through.
func TestSecretInTemplateIsRedacted(t *testing.T) {
	tmpl := template.Must(template.New("t").Parse("{{.token}}"))
	var buf strings.Builder
	if err := tmpl.Execute(&buf, map[string]any{"token": values.NewSecret("token", "s3cr3t")}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if buf.String() != values.Redacted {
		t.Errorf("template output = %q, want %q", buf.String(), values.Redacted)
	}
}
