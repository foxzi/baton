// Package jsonschema validates LLM answers against the schema a step
// declares (docs/ru/spec.md, section 3.5).
//
// It is a thin wrapper over github.com/santhosh-tekuri/jsonschema/v6: the
// library's own error tree is multi-line and verbose, which is unreadable
// once it has to be fed back to the model on a schema retry, so Validate
// flattens it into a single bounded line.
package jsonschema

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	js "github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// maxErrorLen bounds the flattened validation message so a schema that
// fails in dozens of places cannot blow up the retry prompt.
const maxErrorLen = 1024

// maxCauses caps how many leaf causes are reported.
const maxCauses = 5

// Schema is a compiled JSON Schema.
type Schema struct {
	compiled *js.Schema
}

// Compile compiles schema bytes. The name appears in error messages.
func Compile(name string, data []byte) (*Schema, error) {
	doc, err := js.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("jsonschema: %s: invalid schema document: %w", name, err)
	}

	// AddResource under the given name and compile that same name: no
	// loader is registered, so nothing is ever fetched over the network.
	c := js.NewCompiler()
	if err := c.AddResource(name, doc); err != nil {
		return nil, fmt.Errorf("jsonschema: %s: %w", name, err)
	}
	compiled, err := c.Compile(name)
	if err != nil {
		return nil, fmt.Errorf("jsonschema: %s: %w", name, err)
	}
	return &Schema{compiled: compiled}, nil
}

// CompileFile reads and compiles a schema file.
func CompileFile(path string) (*Schema, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("jsonschema: read %s: %w", path, err)
	}
	return Compile(path, data)
}

// Validate checks an already decoded JSON value.
func (s *Schema) Validate(instance any) error {
	err := s.compiled.Validate(instance)
	if err == nil {
		return nil
	}
	var ve *js.ValidationError
	if !errors.As(err, &ve) {
		return err
	}
	return errors.New(flatten(ve))
}

// ValidateJSON decodes raw JSON and validates it, returning the decoded
// value. It uses the library's own unmarshaller (json.Number under the
// hood) so numeric keywords such as multipleOf behave correctly.
func (s *Schema) ValidateJSON(data []byte) (any, error) {
	v, err := js.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("jsonschema: not valid JSON: %w", err)
	}
	if err := s.Validate(v); err != nil {
		return v, err
	}
	return v, nil
}

// flatten turns a validation error tree into a short single-line message:
// at most maxCauses leaf messages, each prefixed with its instance
// location, joined by "; " and capped at maxErrorLen.
func flatten(ve *js.ValidationError) string {
	printer := message.NewPrinter(language.English)

	var leaves []string
	collectLeaves(ve, printer, &leaves)

	if len(leaves) == 0 {
		// No cause carried its own message (should not normally happen);
		// fall back to whatever the root node says rather than an empty
		// string.
		leaves = []string{ve.ErrorKind.LocalizedString(printer)}
	}

	msg := strings.Join(leaves, "; ")
	// Cut on a rune boundary: a message may carry a property name in any
	// language, and half a rune in a retry prompt is noise.
	if runes := []rune(msg); len(runes) > maxErrorLen {
		msg = string(runes[:maxErrorLen]) + "..."
	}
	return msg
}

// collectLeaves walks the cause tree depth-first and appends the message
// of every leaf (a node with no further causes) to out, stopping once
// maxCauses have been collected. Container kinds (schema/group/allOf/...)
// carry no useful text of their own, so only leaves are reported.
func collectLeaves(e *js.ValidationError, p *message.Printer, out *[]string) {
	if len(*out) >= maxCauses {
		return
	}
	if len(e.Causes) == 0 {
		loc := jsonPointer(e.InstanceLocation)
		msg := e.ErrorKind.LocalizedString(p)
		if loc != "" {
			msg = loc + ": " + msg
		}
		*out = append(*out, msg)
		return
	}
	for _, cause := range e.Causes {
		collectLeaves(cause, p, out)
		if len(*out) >= maxCauses {
			return
		}
	}
}

// jsonPointer renders instance location tokens as a JSON-pointer-ish
// path. An empty token slice (the root) renders as "" so the caller can
// decide whether to prefix the message at all.
func jsonPointer(tokens []string) string {
	if len(tokens) == 0 {
		return ""
	}
	r := strings.NewReplacer("~", "~0", "/", "~1")
	var b strings.Builder
	for _, t := range tokens {
		b.WriteByte('/')
		b.WriteString(r.Replace(t))
	}
	return b.String()
}
