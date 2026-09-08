package openapi

import (
	"fmt"
	"strings"

	"github.com/pb33f/libopenapi/datamodel/high/base"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"

	"github.com/foxzi/baton/internal/packs"
)

// authPlan is what writeAuth needs to render the auth section: either the
// body of a usable scheme, or a comment explaining why there is none.
type authPlan struct {
	lines     []string // "kind: header", "name: X-Api-Key", ...
	needsUser bool
	comment   string // replaces the auth block entirely
	note      string // added after a usable auth block
}

// resolveAuth turns the document's security schemes into an auth block, or
// into a comment when the generator cannot express what it found: OAuth2,
// OpenID Connect, mutual TLS, an apiKey in cookie, or several schemes
// required together all need a human decision (spec section 7.4.3).
func resolveAuth(model *v3.Document) authPlan {
	names := securitySchemeOrder(model)
	if len(names) == 0 {
		return authPlan{}
	}
	if requiresSeveralTogether(model) {
		return authPlan{comment: "the document requires several schemes together, declare the scheme by hand (spec section 7.4.3)"}
	}

	type candidate struct {
		name      string
		kind      string
		lines     []string
		needsUser bool
		usable    bool
	}
	var candidates []candidate
	for _, name := range names {
		scheme, ok := lookupScheme(model, name)
		if !ok {
			continue
		}
		lines, needsUser, kind, usable := classifyScheme(scheme)
		candidates = append(candidates, candidate{name, kind, lines, needsUser, usable})
	}
	if len(candidates) == 0 {
		return authPlan{}
	}

	chosen := -1
	for i, c := range candidates {
		if c.usable {
			chosen = i
			break
		}
	}
	if chosen == -1 {
		kind := candidates[0].kind
		if len(candidates) > 1 {
			kind = "several schemes"
		}
		return authPlan{comment: fmt.Sprintf("the document uses %s, declare the scheme by hand (spec section 7.4.3)", kind)}
	}

	plan := authPlan{lines: candidates[chosen].lines, needsUser: candidates[chosen].needsUser}
	var others []string
	for i, c := range candidates {
		if i != chosen {
			others = append(others, c.name)
		}
	}
	if len(others) > 0 {
		plan.note = fmt.Sprintf("the document also declares %s, switch schemes by hand if you need one of them", strings.Join(others, ", "))
	}
	return plan
}

// securitySchemeOrder lists the security scheme names the document
// references, in the order they first appear under the global security
// requirement, followed by any scheme declared but never required globally.
func securitySchemeOrder(model *v3.Document) []string {
	seen := map[string]bool{}
	var order []string
	for _, req := range model.Security {
		if req == nil || req.Requirements == nil {
			continue
		}
		for pair := req.Requirements.Oldest(); pair != nil; pair = pair.Next() {
			if !seen[pair.Key] {
				seen[pair.Key] = true
				order = append(order, pair.Key)
			}
		}
	}
	if model.Components != nil && model.Components.SecuritySchemes != nil {
		for pair := model.Components.SecuritySchemes.Oldest(); pair != nil; pair = pair.Next() {
			if !seen[pair.Key] {
				seen[pair.Key] = true
				order = append(order, pair.Key)
			}
		}
	}
	return order
}

// requiresSeveralTogether reports whether any global security requirement
// demands more than one scheme at once (a logical AND); the pack format has
// no way to express that, so the generator cannot pick one for the author.
func requiresSeveralTogether(model *v3.Document) bool {
	for _, req := range model.Security {
		if req != nil && req.Requirements != nil && req.Requirements.Len() > 1 {
			return true
		}
	}
	return false
}

func lookupScheme(model *v3.Document, name string) (*v3.SecurityScheme, bool) {
	if model.Components == nil || model.Components.SecuritySchemes == nil {
		return nil, false
	}
	return model.Components.SecuritySchemes.Get(name)
}

// classifyScheme turns a security scheme into the lines of an auth block, or
// reports it is not one the generator can express (usable = false), along
// with a human-readable kind for the resulting comment.
func classifyScheme(s *v3.SecurityScheme) (lines []string, needsUser bool, kind string, usable bool) {
	switch s.Type {
	case "apiKey":
		switch s.In {
		case "header":
			return []string{"kind: header", "name: " + quote(s.Name)}, false, "an apiKey in header", true
		case "query":
			return []string{"kind: query", "name: " + quote(s.Name)}, false, "an apiKey in query", true
		default:
			return nil, false, fmt.Sprintf("an apiKey in %s", s.In), false
		}
	case "http":
		switch strings.ToLower(s.Scheme) {
		case "bearer":
			return []string{"kind: bearer"}, false, "bearer", true
		case "basic":
			return []string{"kind: basic"}, true, "basic", true
		default:
			return nil, false, fmt.Sprintf("http %s", s.Scheme), false
		}
	default:
		return nil, false, s.Type, false
	}
}

// writeAuth renders the auth block, or a comment explaining why there is
// none.
func writeAuth(b *strings.Builder, plan authPlan) {
	if len(plan.lines) == 0 {
		if plan.comment != "" {
			fmt.Fprintf(b, "# auth: %s\n", plan.comment)
		}
		return
	}
	b.WriteString("auth:\n")
	for _, l := range plan.lines {
		fmt.Fprintf(b, "  %s\n", l)
	}
	if plan.note != "" {
		fmt.Fprintf(b, "# auth: %s\n", plan.note)
	}
}

// paramLine is one rendered params entry.
type paramLine struct {
	key     string
	attrs   []string
	comment string
}

func (p paramLine) render() string {
	body := "{}"
	if len(p.attrs) > 0 {
		body = "{ " + strings.Join(p.attrs, ", ") + " }"
	}
	line := fmt.Sprintf("      %s: %s", p.key, body)
	if p.comment != "" {
		line += " # " + p.comment
	}
	return line
}

// writeOp renders one ops entry.
func writeOp(b *strings.Builder, key string, e opEntry, iface string) error {
	fmt.Fprintf(b, "  %s:\n", key)
	fmt.Fprintf(b, "    %s: %s\n", e.method, e.path)
	if e.op.Summary != "" {
		fmt.Fprintf(b, "    description: %s\n", quote(e.op.Summary))
	} else {
		b.WriteString("    description: \"\" # TODO: describe the operation, the agent picks tools by description\n")
	}
	b.WriteString("    readonly: false # TODO: true if the operation only reads\n")

	params, headerNames, encodeForm, err := collectParams(e)
	if err != nil {
		return fmt.Errorf("ops.%s: %w", key, err)
	}
	if encodeForm {
		b.WriteString("    encode: form\n")
	}
	if len(headerNames) > 0 {
		fmt.Fprintf(b, "    # skipped parameters with no placement in the pack format: %s\n", strings.Join(headerNames, ", "))
	}
	if len(params) > 0 {
		b.WriteString("    params:\n")
		for _, p := range params {
			b.WriteString(p.render())
			b.WriteString("\n")
		}
	}
	b.WriteString("    transform: \"\" # TODO: jq transform, see spec section 7.4.2\n")
	if iface != "" {
		fmt.Fprintf(b, "    # implements: %s.%s\n", iface, key)
	}
	b.WriteString("\n")
	return nil
}

// collectParams turns an operation's parameters and request body into
// params entries, in path, then query, then body/form order. Parameters
// with no placement in the pack format (header, cookie) are returned
// separately so the caller can note them instead of silently dropping them.
func collectParams(e opEntry) (params []paramLine, skipped []string, encodeForm bool, err error) {
	used := map[string]bool{}
	var pathParams, queryParams []paramLine
	for _, p := range e.op.Parameters {
		switch p.In {
		case "path":
			line, perr := buildPositionalParam(p, e.method)
			if perr != nil {
				return nil, nil, false, perr
			}
			line.key = uniqueKey(line.key, used)
			pathParams = append(pathParams, line)
		case "query":
			line, perr := buildPositionalParam(p, e.method)
			if perr != nil {
				return nil, nil, false, perr
			}
			line.key = uniqueKey(line.key, used)
			queryParams = append(queryParams, line)
		default:
			// header, cookie, or anything else libopenapi might report: the
			// pack format has no placement for it.
			skipped = append(skipped, p.Name)
		}
	}
	params = append(params, pathParams...)
	params = append(params, queryParams...)

	if e.op.RequestBody != nil && e.op.RequestBody.Content != nil {
		if media, ok := e.op.RequestBody.Content.Get("application/json"); ok {
			params = append(params, buildBodyFields(schemaOf(media.Schema), used)...)
		} else if media, ok := e.op.RequestBody.Content.Get("application/x-www-form-urlencoded"); ok {
			encodeForm = true
			params = append(params, buildBodyFields(schemaOf(media.Schema), used)...)
		}
	}
	return params, skipped, encodeForm, nil
}

// buildPositionalParam renders a path or query parameter. Its pattern comes
// from the parameter's JSON Schema type, since the pack validator requires a
// pattern (or an enum) for anything that lands in the path or the query.
func buildPositionalParam(p *v3.Parameter, method string) (paramLine, error) {
	schema := schemaOf(p.Schema)
	if schema == nil {
		return paramLine{}, fmt.Errorf("%s: has no schema to derive a pattern from, add it by hand", p.Name)
	}

	line := paramLine{key: toIdent(p.Name)}
	actualIn := packs.ParamIn(p.In)
	if actualIn != defaultParamIn(actualIn, method) {
		line.attrs = append(line.attrs, "in: "+string(actualIn))
	}

	// the pack validator requires a pattern for anything that lands in the
	// path or the query regardless of enum, so a schema enum only adds an
	// extra enum constraint alongside the pattern, it never replaces it.
	isEnum := len(schema.Enum) > 0 && primaryType(schema.Type) == "string"
	pattern, todo, err := paramPattern(primaryType(schema.Type))
	if err != nil {
		return paramLine{}, fmt.Errorf("%s: %w", p.Name, err)
	}
	// an enum states the whole value set, so its pattern needs no narrowing
	// by hand: it is built from the values themselves.
	if isEnum {
		if alt := enumPattern(schema.Enum); alt != "" {
			pattern, todo = alt, ""
		}
	}
	line.attrs = append(line.attrs, "pattern: "+quote(pattern))
	if isEnum {
		line.attrs = append(line.attrs, "enum: ["+strings.Join(enumValues(schema.Enum), ", ")+"]")
	}
	if todo != "" {
		line.comment = todo
	}

	if actualIn == packs.InQuery {
		required := p.Required != nil && *p.Required
		if !required {
			line.attrs = append(line.attrs, "required: false")
		}
	}
	if def := renderDefault(schema.Default); def != "" {
		line.attrs = append(line.attrs, "default: "+def)
	}
	// a free-form string may carry a slash, which would split the path, so
	// the author has to decide; an enum-derived pattern already spells out
	// every value the argument can take.
	if actualIn == packs.InPath && primaryType(schema.Type) == "string" && todo != "" {
		line.attrs = append(line.attrs, "encode: path")
		if line.comment != "" {
			line.comment += "; "
		}
		line.comment += "TODO: drop encode if the value has no slashes"
	}
	return line, nil
}

// defaultParamIn is the placement a path or query parameter would get if
// the pack's own placement rule were applied: a path parameter is always in
// the path; anything else follows the method (GET -> query, else body).
func defaultParamIn(actualIn packs.ParamIn, method string) packs.ParamIn {
	if actualIn == packs.InPath {
		return packs.InPath
	}
	if method == "get" {
		return packs.InQuery
	}
	return packs.InBody
}

// buildBodyFields renders the properties of a JSON or form body schema.
// They need no pattern (the pack validator only requires one in the path or
// the query) and no "in:", since a body/form field always uses the pack's
// default placement for its op.
func buildBodyFields(schema *base.Schema, used map[string]bool) []paramLine {
	if schema == nil || schema.Properties == nil {
		return nil
	}
	required := map[string]bool{}
	for _, name := range schema.Required {
		required[name] = true
	}

	var out []paramLine
	for pair := schema.Properties.Oldest(); pair != nil; pair = pair.Next() {
		key := uniqueKey(toIdent(pair.Key), used)
		line := paramLine{key: key}
		if !required[pair.Key] {
			line.attrs = append(line.attrs, "required: false")
		}
		if fieldSchema := schemaOf(pair.Value); fieldSchema != nil {
			if def := renderDefault(fieldSchema.Default); def != "" {
				line.attrs = append(line.attrs, "default: "+def)
			}
		}
		out = append(out, line)
	}
	return out
}
