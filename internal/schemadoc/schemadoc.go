// Package schemadoc renders the scenario JSON Schema as a Markdown
// reference. The reference is written to docs/en/schema.md and
// docs/ru/schema.md by `make docs`; those files are generated and are not
// meant to be edited by hand, so that the schema shipped to editors and the
// reference read by humans can never drift apart.
package schemadoc

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Render walks schema and returns the Markdown reference in lang, which is
// either "en" or "ru".
func Render(schema []byte, lang string) (string, error) {
	labels, ok := labelSets[lang]
	if !ok {
		langs := make([]string, 0, len(labelSets))
		for name := range labelSets {
			langs = append(langs, name)
		}
		sort.Strings(langs)
		return "", fmt.Errorf("schemadoc: unknown language %q, want one of %s", lang, strings.Join(langs, ", "))
	}

	var root node
	if err := json.Unmarshal(schema, &root); err != nil {
		return "", fmt.Errorf("schemadoc: %w", err)
	}

	r := &renderer{labels: labels, translate: translator(lang)}
	r.document(&root)
	return r.out.String(), nil
}

// renderer accumulates the document. Sections are emitted depth first, so an
// inline object is documented right after the table that mentions it.
type renderer struct {
	out       strings.Builder
	labels    labels
	translate func(string) string
}

func (r *renderer) document(root *node) {
	r.printf("<!-- %s -->\n\n", r.labels.generated)
	r.printf("# %s\n\n", r.labels.title)
	if root.Description != "" {
		r.printf("%s\n\n", r.translate(root.Description))
	}
	r.printf("%s\n\n", r.labels.intro)

	// The prose of the root already stands under the title; repeating it
	// under its own heading would only say the same thing twice.
	top := *root
	top.Description = ""
	r.section("scenario", "scenario", &top, 2)

	if root.Defs.len() == 0 {
		return
	}
	r.printf("## %s\n\n", r.labels.definitions)
	for _, name := range root.Defs.keys {
		r.section(name, name, root.Defs.value(name), 3)
	}
}

// section documents one object at the given heading level and then recurses
// into the inline objects it declares, which have no name of their own in the
// schema and would otherwise be invisible. path is the dotted route to the
// node and names its children; heading is how this node is announced, which
// differs from path only when a name has to be disambiguated.
func (r *renderer) section(path, heading string, n *node, level int) {
	r.printf("%s %s\n\n", strings.Repeat("#", level), heading)
	if n.Description != "" {
		r.printf("%s\n\n", r.translate(n.Description))
	}

	if n.Properties.len() == 0 {
		r.printf("%s: %s\n\n", r.labels.typeLabel, r.typeOf(n))
		if notes := r.constraints(n); notes != "" {
			r.printf("%s\n\n", notes)
		}
		r.inlineSections(path, n, level)
		return
	}

	required := make(map[string]bool, len(n.Required))
	for _, field := range n.Required {
		required[field] = true
	}

	r.printf("| %s | %s | %s | %s |\n", r.labels.field, r.labels.typeLabel, r.labels.required, r.labels.description)
	r.printf("|---|---|---|---|\n")
	for _, field := range n.Properties.keys {
		prop := n.Properties.value(field)
		yesNo := r.labels.no
		if required[field] {
			yesNo = r.labels.yes
		}
		r.printf("| `%s` | %s | %s | %s |\n", field, r.typeOf(prop), yesNo, r.cell(prop))
	}
	r.out.WriteString("\n")

	if n.closed() {
		r.printf("%s\n\n", r.labels.closed)
	}

	for _, field := range n.Properties.keys {
		r.inlineSections(path+"."+field, n.Properties.value(field), level)
	}
}

// inlineSections documents the anonymous objects a node reaches, naming them
// after the path that leads there.
func (r *renderer) inlineSections(path string, n *node, level int) {
	for _, found := range inlineObjects(n) {
		heading := path + found.suffix
		if found.suffix == "" && found.viaUnion {
			// The object is one form of a union whose other forms are
			// scalars, so the plain path already names the union itself.
			heading += " (" + r.labels.objectForm + ")"
		}
		r.section(path+found.suffix, heading, found.node, min(level+1, 6))
	}
}

// inline is an anonymous object reachable from a node: the node itself, the
// items of an array, the value type of a map, or one form of a union.
type inline struct {
	suffix   string
	viaUnion bool
	node     *node
}

func inlineObjects(n *node) []inline {
	if n == nil {
		return nil
	}
	if n.Properties.len() > 0 {
		return []inline{{node: n}}
	}
	var found []inline
	found = append(found, prefixed("[]", inlineObjects(n.Items))...)
	found = append(found, prefixed(".*", inlineObjects(n.additional()))...)
	for _, branch := range n.branches() {
		for _, object := range inlineObjects(branch) {
			object.viaUnion = true
			found = append(found, object)
		}
	}
	return found
}

func prefixed(prefix string, found []inline) []inline {
	for i := range found {
		found[i].suffix = prefix + found[i].suffix
	}
	return found
}

// cell is the description column: the prose of the schema followed by the
// constraints that are short enough to read inline.
func (r *renderer) cell(n *node) string {
	parts := make([]string, 0, 2)
	if n.Description != "" {
		parts = append(parts, r.translate(n.Description))
	}
	if notes := r.constraints(n); notes != "" {
		parts = append(parts, notes)
	}
	return strings.Join(parts, " ")
}

func (r *renderer) constraints(n *node) string {
	if n == nil {
		return ""
	}
	var notes []string
	if n.Default != nil {
		notes = append(notes, fmt.Sprintf("%s `%s`", r.labels.defaultLabel, literal(n.Default)))
	}
	if n.Pattern != "" {
		notes = append(notes, fmt.Sprintf("%s `%s`", r.labels.pattern, n.Pattern))
	}
	if n.Format != "" {
		notes = append(notes, fmt.Sprintf("%s `%s`", r.labels.format, n.Format))
	}
	if n.Minimum != nil {
		notes = append(notes, fmt.Sprintf("%s %s", r.labels.minimum, number(*n.Minimum)))
	}
	if n.Maximum != nil {
		notes = append(notes, fmt.Sprintf("%s %s", r.labels.maximum, number(*n.Maximum)))
	}
	if n.MinItems != nil {
		notes = append(notes, fmt.Sprintf("%s %d", r.labels.minItems, *n.MinItems))
	}
	if n.MinProperties != nil {
		notes = append(notes, fmt.Sprintf("%s %d", r.labels.minProperties, *n.MinProperties))
	}
	if len(notes) == 0 {
		return ""
	}
	return strings.Join(notes, ", ") + "."
}

// typeOf names the type of a node the way a reader of the reference thinks of
// it, resolving a reference to a link to the section that documents it.
func (r *renderer) typeOf(n *node) string {
	if n == nil {
		return "any"
	}
	switch {
	case n.always != nil && !*n.always:
		return "never"
	case n.Ref != "":
		name := strings.TrimPrefix(n.Ref, "#/$defs/")
		return fmt.Sprintf("[%s](#%s)", name, anchor(name))
	case n.Const != nil:
		return fmt.Sprintf("`%s`", literal(n.Const))
	case len(n.Enum) > 0:
		values := make([]string, 0, len(n.Enum))
		for _, value := range n.Enum {
			values = append(values, fmt.Sprintf("`%s`", literal(value)))
		}
		return strings.Join(values, ", ")
	}
	if branches := n.branches(); len(branches) > 0 {
		seen := make(map[string]bool, len(branches))
		names := make([]string, 0, len(branches))
		for _, branch := range branches {
			name := r.typeOf(branch)
			if name == "any" || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
		if len(names) > 0 {
			return strings.Join(names, r.labels.or)
		}
	}

	names := n.types()
	for i, name := range names {
		switch name {
		case "array":
			names[i] = fmt.Sprintf(r.labels.arrayOf, r.typeOf(n.Items))
		case "object":
			if extra := n.additional(); extra != nil {
				names[i] = fmt.Sprintf(r.labels.mapOf, r.typeOf(extra))
			}
		}
	}
	if len(names) == 0 {
		return "any"
	}
	return strings.Join(names, r.labels.or)
}

func (r *renderer) printf(format string, args ...any) {
	fmt.Fprintf(&r.out, format, args...)
}

// anchor is the fragment GitHub gives a heading: lowercase, punctuation
// dropped, spaces turned into hyphens.
func anchor(heading string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(heading) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c >= 0x400 && c <= 0x4ff:
			b.WriteRune(c)
		case c == ' ':
			b.WriteRune('-')
		}
	}
	return b.String()
}

func literal(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return number(v)
	case bool:
		return fmt.Sprintf("%t", v)
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return fmt.Sprintf("%v", value)
		}
		return string(encoded)
	}
}

func number(value float64) string {
	if value == float64(int64(value)) {
		return fmt.Sprintf("%d", int64(value))
	}
	return fmt.Sprintf("%g", value)
}
