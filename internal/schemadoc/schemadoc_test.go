package schemadoc

import (
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/scenario"
)

func TestRenderUnknownLanguage(t *testing.T) {
	if _, err := Render(scenario.Schema(), "de"); err == nil {
		t.Fatal("Render() with an unknown language error = nil, want an error")
	}
}

func TestRenderInvalidSchema(t *testing.T) {
	if _, err := Render([]byte("{"), "en"); err == nil {
		t.Fatal("Render() with a broken schema error = nil, want an error")
	}
}

// TestRenderScenarioSchema checks the reference against the schema the binary
// actually ships, so that a definition added without a heading is caught here
// rather than by a reader of the document.
func TestRenderScenarioSchema(t *testing.T) {
	for _, lang := range []string{"en", "ru"} {
		t.Run(lang, func(t *testing.T) {
			out, err := Render(scenario.Schema(), lang)
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}

			for _, heading := range []string{
				"## scenario",
				"### step-body",
				"### agent",
				"### command",
				"#### command.args.*",
				"#### run (" + labelSets[lang].objectForm + ")",
			} {
				if !strings.Contains(out, heading+"\n") {
					t.Errorf("Render() is missing the heading %q", heading)
				}
			}

			// Every reference of the schema must land on a heading of the
			// document, or the links of the reference are broken.
			for _, link := range links(out) {
				if !strings.Contains(out, "#"+link) {
					t.Errorf("Render() links to #%s, which no heading provides", link)
				}
			}
		})
	}
}

func TestRenderRussianTranslates(t *testing.T) {
	out, err := Render(scenario.Schema(), "ru")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !strings.Contains(out, "Справочник схемы сценария") {
		t.Error("Render(ru) does not carry the Russian title")
	}
	if strings.Contains(out, "A Go duration string") {
		t.Error("Render(ru) left the description of duration in English")
	}
	if !strings.Contains(out, "Длительность в формате Go") {
		t.Error("Render(ru) does not carry the Russian description of duration")
	}
}

// TestRenderShapes covers the schema shapes the reference has to name, on a
// document small enough to read in the failure message.
func TestRenderShapes(t *testing.T) {
	schema := []byte(`{
	  "description": "A tiny schema.",
	  "type": "object",
	  "additionalProperties": false,
	  "required": ["name"],
	  "properties": {
	    "name": {"type": "string", "pattern": "^x", "description": "The name."},
	    "count": {"type": "integer", "minimum": 1, "maximum": 9, "default": 3},
	    "mode": {"enum": ["fast", "slow"]},
	    "version": {"const": 1},
	    "items": {"type": "array", "minItems": 1, "items": {"$ref": "#/$defs/leaf"}},
	    "env": {"type": "object", "additionalProperties": {"type": "string"}},
	    "nested": {"type": "object", "properties": {"deep": {"type": "boolean"}}},
	    "anything": true
	  },
	  "$defs": {
	    "leaf": {"description": "A leaf.", "oneOf": [{"type": "string"}, {"type": "number"}]}
	  }
	}`)

	out, err := Render(schema, "en")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	for _, want := range []string{
		"| `name` | string | yes | The name. Pattern `^x`. |",
		"| `count` | integer | no | Default `3`, Minimum 1, Maximum 9. |",
		"| `mode` | `fast`, `slow` | no |  |",
		"| `version` | `1` | no |  |",
		"| `items` | array of [leaf](#leaf) | no | At least items: 1. |",
		"| `env` | map of name to string | no |  |",
		"| `nested` | object | no |  |",
		"| `anything` | any | no |  |",
		"No other fields are allowed.",
		"### scenario.nested",
		"Type: string or number",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Render() is missing %q\ngot:\n%s", want, out)
		}
	}
}

func TestAnchor(t *testing.T) {
	tests := map[string]string{
		"step-body":         "step-body",
		"scenario.defaults": "scenariodefaults",
		"run (object form)": "run-object-form",
		"command.args.*":    "commandargs",
		"Справочник":        "справочник",
	}
	for heading, want := range tests {
		if got := anchor(heading); got != want {
			t.Errorf("anchor(%q) = %q, want %q", heading, got, want)
		}
	}
}

// links returns the fragments the document points at, without the leading
// hash.
func links(document string) []string {
	var found []string
	for _, part := range strings.Split(document, "](#")[1:] {
		if end := strings.IndexByte(part, ')'); end >= 0 {
			found = append(found, part[:end])
		}
	}
	return found
}
