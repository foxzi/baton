// Package tmpl renders the scenario templates (docs/ru/spec.md, section 5.2).
//
// Templates are text/template with the function set of the specification.
// md2html and md2text are not implemented yet: their only consumers are the
// API packs of a later milestone, and adding a Markdown engine before then
// would ship an unused dependency.
//
// Secrets never reach a template. Section 5.2 requires a render error rather
// than a redacted string, so Render refuses data containing a values.Secret,
// and Check rejects a template that references .secrets at all.
package tmpl

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"text/template"
	"text/template/parse"

	"github.com/foxzi/baton/internal/values"
)

// maxRenderDepth bounds render() recursion, so that two templates including
// each other fail with an error instead of exhausting the stack.
const maxRenderDepth = 8

// missingKey is zero rather than error: default and coalesce exist precisely
// to handle an absent value, and missingkey=error would fail the render before
// either of them was reached. A missing key therefore renders as <no value>,
// while reading through it stays an error.
const missingKey = "missingkey=zero"

// Renderer renders templates. Paths passed to render() are resolved against
// BaseDir, the directory of the scenario file.
type Renderer struct {
	baseDir string
}

// NewRenderer returns a renderer resolving relative paths against baseDir.
func NewRenderer(baseDir string) *Renderer { return &Renderer{baseDir: baseDir} }

// Render renders text with data. The name is used in error messages.
func (r *Renderer) Render(name, text string, data any) (string, error) {
	if err := guardSecrets(data); err != nil {
		return "", fmt.Errorf("render %s: %w", name, err)
	}
	return r.render(name, text, data, 0)
}

// RenderFile renders the template file at path, which may be relative to the
// renderer base directory.
func (r *Renderer) RenderFile(path string, data any) (string, error) {
	if err := guardSecrets(data); err != nil {
		return "", fmt.Errorf("render %s: %w", path, err)
	}
	return r.renderFile(path, data, 0)
}

func (r *Renderer) render(name, text string, data any, depth int) (string, error) {
	parsed, err := template.New(name).Option(missingKey).Funcs(r.funcs(depth)).Parse(text)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", name, err)
	}
	var out bytes.Buffer
	if err := parsed.Execute(&out, data); err != nil {
		return "", fmt.Errorf("render %s: %w", name, err)
	}
	return out.String(), nil
}

func (r *Renderer) renderFile(path string, data any, depth int) (string, error) {
	if depth >= maxRenderDepth {
		return "", fmt.Errorf("render %s: nested more than %d levels deep", path, maxRenderDepth)
	}
	resolved := path
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(r.baseDir, resolved)
	}
	text, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("read template %s: %w", path, err)
	}
	if err := Check(path, string(text)); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return r.render(path, string(text), data, depth+1)
}

// Check parses text and rejects references to secrets. Validation runs it over
// every template of a scenario before the run starts (section 4, item 4).
//
// The name is only used inside parse errors, which text/template formats with
// it; the secret error carries no name, so a caller that already reports a
// field path does not print it twice.
func Check(name, text string) error {
	renderer := &Renderer{}
	if _, err := template.New(name).Option(missingKey).Funcs(renderer.funcs(0)).Parse(text); err != nil {
		return fmt.Errorf("parse %s: %w", name, err)
	}
	fields, err := RootFields(name, text)
	if err != nil {
		return err
	}
	for _, field := range fields {
		if field == "secrets" {
			return errors.New("templates cannot reference secrets")
		}
	}
	return nil
}

// RootFields returns the distinct top-level field names text reads from its
// data, in order of first appearance.
//
// Dot rebinding by with and range is deliberately not tracked: a field read
// inside a with block is reported as a root field too. That makes the secrets
// check of Check conservative, which is the safe direction for it.
func RootFields(name, text string) ([]string, error) {
	trees, err := parse.Parse(name, text, "", "", builtinNames())
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}

	var fields []string
	seen := make(map[string]bool)
	add := func(idents []string) {
		if len(idents) == 0 || seen[idents[0]] {
			return
		}
		seen[idents[0]] = true
		fields = append(fields, idents[0])
	}
	for _, tree := range trees {
		walk(tree.Root, add)
	}
	return fields, nil
}

// walk visits every node of a parsed template, reporting the identifier chain
// of each field reference. Node kinds that cannot contain a field reference
// fall through to the default case.
func walk(node parse.Node, add func([]string)) {
	switch n := node.(type) {
	case nil:
		return
	case *parse.ListNode:
		if n == nil {
			return
		}
		for _, child := range n.Nodes {
			walk(child, add)
		}
	case *parse.ActionNode:
		walk(n.Pipe, add)
	case *parse.PipeNode:
		if n == nil {
			return
		}
		for _, cmd := range n.Cmds {
			walk(cmd, add)
		}
	case *parse.CommandNode:
		for _, arg := range n.Args {
			walk(arg, add)
		}
	case *parse.BranchNode:
		walk(n.Pipe, add)
		walk(n.List, add)
		walk(n.ElseList, add)
	case *parse.IfNode:
		walk(&n.BranchNode, add)
	case *parse.RangeNode:
		walk(&n.BranchNode, add)
	case *parse.WithNode:
		walk(&n.BranchNode, add)
	case *parse.TemplateNode:
		walk(n.Pipe, add)
	case *parse.FieldNode:
		add(n.Ident)
	case *parse.ChainNode:
		walk(n.Node, add)
		add(n.Field)
	}
}

// textTemplateBuiltins are the functions text/template defines itself. The
// parser adds them when it is driven through template.Parse, but parse.Parse
// only sees the maps it is given, so they are spelled out here.
var textTemplateBuiltins = []string{
	"and", "call", "html", "index", "slice", "js", "len", "not", "or",
	"print", "printf", "println", "urlquery",
	"eq", "ge", "gt", "le", "lt", "ne",
}

// builtinNames are the function names parse.Parse must accept while parsing a
// template outside of an executing template.
func builtinNames() map[string]any {
	names := map[string]any{}
	for name := range (&Renderer{}).funcs(0) {
		names[name] = struct{}{}
	}
	for _, name := range textTemplateBuiltins {
		names[name] = struct{}{}
	}
	return names
}

// funcs returns the template function set. depth carries the current render()
// nesting level.
func (r *Renderer) funcs(depth int) template.FuncMap {
	return template.FuncMap{
		"render": func(path string, data any) (string, error) {
			return r.renderFile(path, data, depth)
		},
		"toJSON":   toJSON,
		"fromJSON": fromJSON,
		"coalesce": coalesce,
		"default":  defaultValue,
		"trunc":    trunc,
		"indent":   indent,
		"join":     join,
		"dict":     dict,
		"quote":    quote,
	}
}

func toJSON(value any) (string, error) {
	if err := guardSecrets(value); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("toJSON: %w", err)
	}
	return string(encoded), nil
}

func fromJSON(text string) (any, error) {
	var value any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		return nil, fmt.Errorf("fromJSON: %w", err)
	}
	return value, nil
}

// coalesce returns the first argument that is neither nil nor an empty value.
func coalesce(candidates ...any) any {
	for _, candidate := range candidates {
		if !isEmpty(candidate) {
			return candidate
		}
	}
	return nil
}

// defaultValue mirrors the usual template helper: the fallback comes first, so
// it reads as `{{ .x | default "none" }}`.
func defaultValue(fallback, value any) any {
	if isEmpty(value) {
		return fallback
	}
	return value
}

func isEmpty(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.String, reflect.Slice, reflect.Map, reflect.Array:
		return v.Len() == 0
	case reflect.Ptr, reflect.Interface:
		return v.IsNil() || isEmpty(v.Elem().Interface())
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	default:
		return false
	}
}

// trunc shortens text to at most n runes, counting runes rather than bytes so
// that it cannot cut a multi-byte character in half.
func trunc(n int, text string) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	return string(runes[:n])
}

// indent prefixes every line with n spaces, including the first, which is what
// embedding a block under a YAML or Markdown key needs.
func indent(n int, text string) string {
	if n <= 0 {
		return text
	}
	prefix := strings.Repeat(" ", n)
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if line == "" {
			continue
		}
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

func join(separator string, list any) (string, error) {
	v := reflect.ValueOf(list)
	switch v.Kind() {
	case reflect.Slice, reflect.Array:
	default:
		return "", fmt.Errorf("join: expected a list, got %T", list)
	}
	parts := make([]string, 0, v.Len())
	for i := 0; i < v.Len(); i++ {
		item := v.Index(i).Interface()
		if err := guardSecrets(item); err != nil {
			return "", fmt.Errorf("join: %w", err)
		}
		parts = append(parts, fmt.Sprint(item))
	}
	return strings.Join(parts, separator), nil
}

// dict builds a map from alternating key and value arguments.
func dict(pairs ...any) (map[string]any, error) {
	if len(pairs)%2 != 0 {
		return nil, errors.New("dict: expected an even number of arguments")
	}
	out := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict: key %d is %T, want string", i/2, pairs[i])
		}
		out[key] = pairs[i+1]
	}
	return out, nil
}

// quote renders value as a double-quoted Go string literal. It is for
// embedding text in JSON or a shell-free argument list, not for shell quoting.
func quote(value any) (string, error) {
	if err := guardSecrets(value); err != nil {
		return "", fmt.Errorf("quote: %w", err)
	}
	return fmt.Sprintf("%q", fmt.Sprint(value)), nil
}

var secretType = reflect.TypeOf(values.Secret{})

// guardSecrets reports an error if value holds a secret anywhere inside it.
//
// The check is a deep scan rather than an interception at print time, because
// values.Secret prints as *** and text/template offers no hook to turn that
// into an error.
func guardSecrets(value any) error {
	return scan(reflect.ValueOf(value), make(map[uintptr]bool), 0)
}

func scan(v reflect.Value, visited map[uintptr]bool, depth int) error {
	// A data structure deeper than this is not something a template renders;
	// the limit keeps a cyclic structure the pointer set cannot catch from
	// looping forever.
	if depth > 64 || !v.IsValid() {
		return nil
	}
	if v.Type() == secretType {
		name := v.Interface().(values.Secret).Name()
		if name == "" {
			return errors.New("a secret cannot be rendered into a template")
		}
		return fmt.Errorf("secret %q cannot be rendered into a template", name)
	}

	switch v.Kind() {
	case reflect.Ptr, reflect.Interface:
		if v.IsNil() {
			return nil
		}
		if v.Kind() == reflect.Ptr {
			if visited[v.Pointer()] {
				return nil
			}
			visited[v.Pointer()] = true
		}
		return scan(v.Elem(), visited, depth+1)
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if err := scan(v.Index(i), visited, depth+1); err != nil {
				return err
			}
		}
	case reflect.Map:
		for _, key := range v.MapKeys() {
			if err := scan(key, visited, depth+1); err != nil {
				return err
			}
			if err := scan(v.MapIndex(key), visited, depth+1); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			field := v.Field(i)
			// An unexported field cannot be read as an interface, but its
			// type is still enough to spot a secret hidden behind it.
			if !v.Type().Field(i).IsExported() {
				if fieldHoldsSecret(field.Type(), 0) {
					return fmt.Errorf("%s holds a secret and cannot be rendered into a template", v.Type())
				}
				continue
			}
			if err := scan(field, visited, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// fieldHoldsSecret reports whether t is, or structurally contains, a secret.
func fieldHoldsSecret(t reflect.Type, depth int) bool {
	if depth > 16 {
		return false
	}
	if t == secretType {
		return true
	}
	switch t.Kind() {
	case reflect.Ptr, reflect.Slice, reflect.Array:
		return fieldHoldsSecret(t.Elem(), depth+1)
	case reflect.Map:
		return fieldHoldsSecret(t.Key(), depth+1) || fieldHoldsSecret(t.Elem(), depth+1)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			if fieldHoldsSecret(t.Field(i).Type, depth+1) {
				return true
			}
		}
	}
	return false
}
