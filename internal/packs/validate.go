package packs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/itchyny/gojq"
)

// packNameRe and identRe constrain the names a pack may define.
var (
	packNameRe    = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	identRe       = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	placeholderRe = regexp.MustCompile(`\{([a-z][a-z0-9_]*)\}`)
	implementsRe  = regexp.MustCompile(`^[a-z][a-z0-9_]*/v[0-9]+\.[a-z][a-z0-9_]*$`)
)

// validate checks the pack and fills in the fields derived from it: the HTTP
// method and path of every operation, the resolved placement of every
// argument and the compiled jq expressions and patterns.
func (p *Pack) validate() error {
	var problems []error
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Errorf(format, args...))
	}

	if !packNameRe.MatchString(p.Pack) {
		add("pack: %q is not a pack name like gitlab", p.Pack)
	}
	if p.Version != 1 {
		add("version: must be 1, got %d", p.Version)
	}
	for name := range p.Config {
		if !identRe.MatchString(name) {
			add("config.%s: %q is not a config field name", name, name)
		}
	}
	if err := p.validateAuth(); err != nil {
		problems = append(problems, err)
	}
	if err := p.Envelope.compile(); err != nil {
		problems = append(problems, err)
	}
	if err := p.Pagination.check("pagination"); err != nil {
		problems = append(problems, err)
	}
	if len(p.Ops) == 0 {
		add("ops: a pack must declare at least one operation")
	}
	for _, name := range p.OpNames() {
		if !identRe.MatchString(name) {
			add("ops.%s: %q is not an operation name", name, name)
		}
		if err := p.validateOp(p.Ops[name]); err != nil {
			problems = append(problems, err)
		}
	}
	return errors.Join(problems...)
}

// validateAuth checks the authorisation scheme of the pack.
func (p *Pack) validateAuth() error {
	if p.Auth == nil {
		return nil
	}
	return p.validateScheme("auth", p.Auth)
}

// validateScheme checks one authorisation scheme, either the pack's own or
// the base scheme of an exchange. Prefix names the field in error messages,
// so the same checks read as auth.* at the top level and auth.base.* when
// applied to the base of an exchange.
func (p *Pack) validateScheme(prefix string, scheme *Auth) error {
	switch scheme.Kind {
	case AuthHeader, AuthQuery:
		if scheme.Name == "" {
			return fmt.Errorf("%s.name: required for the %s scheme", prefix, scheme.Kind)
		}
	case AuthBearer, AuthPath:
		// Nothing to configure: the value goes into Authorization or the
		// {auth} placeholder.
	case AuthBasic:
		field := scheme.User
		if field == "" {
			field = "user"
		}
		if _, ok := p.Config[field]; !ok {
			return fmt.Errorf("%s.user: the basic scheme takes the user from config.%s, which the pack does not declare", prefix, field)
		}
	case AuthExchange:
		return p.validateExchange(prefix, scheme)
	case "":
		return errors.New(prefix + ".kind: required")
	default:
		return fmt.Errorf("%s.kind: %q is not an authorisation scheme", prefix, scheme.Kind)
	}
	if scheme.Op != "" || scheme.Base != nil || scheme.Extract != "" || scheme.Inject != nil ||
		scheme.TTL != 0 || scheme.Session != "" || scheme.DependsOn != "" {
		return fmt.Errorf("%s: op, base, extract, inject, ttl, session and depends_on only apply to the exchange scheme", prefix)
	}
	return nil
}

// validateExchange checks the exchange scheme: the operation that trades the
// secret for a token, the scheme that authorises that call, how the token is
// pulled out of the response and where it goes in the calls that follow.
func (p *Pack) validateExchange(prefix string, scheme *Auth) error {
	var problems []error
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Errorf(format, args...))
	}

	if scheme.DependsOn != "" {
		add("%s.depends_on: chained exchanges are not supported yet", prefix)
	}
	if scheme.Op == "" {
		add("%s.op: required for the exchange scheme", prefix)
	} else if p.Ops[scheme.Op] == nil {
		add("%s.op: %q is not an operation of the pack", prefix, scheme.Op)
	}
	if scheme.Base == nil {
		add("%s.base: required for the exchange scheme: the exchange call needs an authorisation of its own", prefix)
	} else if scheme.Base.IsExchange() {
		add("%s.base.kind: an exchange cannot be authorised by another exchange", prefix)
	} else if err := p.validateScheme(prefix+".base", scheme.Base); err != nil {
		problems = append(problems, err)
	}
	if scheme.Extract == "" {
		add("%s.extract: required for the exchange scheme", prefix)
	} else {
		code, err := compileJQ(prefix+".extract", scheme.Extract)
		if err != nil {
			problems = append(problems, err)
		}
		scheme.extract = code
	}
	if scheme.Inject == nil {
		add("%s.inject: required for the exchange scheme", prefix)
	} else {
		switch scheme.Inject.In {
		case InHeader, InQuery, InBody, InForm:
		default:
			add("%s.inject.in: %q is not a placement for a token, use header, query, body or form", prefix, scheme.Inject.In)
		}
		if scheme.Inject.Name == "" {
			add("%s.inject.name: required", prefix)
		}
	}
	if scheme.TTL < 0 {
		add("%s.ttl: must not be negative", prefix)
	}
	if scheme.Session != "" && scheme.Session != SessionCookies {
		add("%s.session: %q is not a session mode, only cookies is", prefix, scheme.Session)
	}
	return errors.Join(problems...)
}

// validateOp checks one operation and derives its method, path and argument
// placements.
func (p *Pack) validateOp(op *Op) error {
	var problems []error
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Errorf("ops.%s.%s", op.name, fmt.Sprintf(format, args...)))
	}

	if op.Kind != "" && !op.IsGraphQL() {
		add("kind: %q is not an operation kind", op.Kind)
	}
	methods := []struct {
		name string
		path string
	}{
		{"GET", op.Get}, {"POST", op.Post}, {"PUT", op.Put},
		{"PATCH", op.Patch}, {"DELETE", op.Delete},
	}
	for _, method := range methods {
		if method.path == "" {
			continue
		}
		if op.method != "" {
			add("%s: the operation already uses %s, declare exactly one method", strings.ToLower(method.name), op.method)
			continue
		}
		op.method, op.path = method.name, method.path
	}
	switch {
	case op.IsGraphQL():
		if op.method != "" && op.method != "POST" {
			add("%s: a graphql operation is a POST, declare its endpoint with post or leave it to base_url", strings.ToLower(op.method))
		}
		op.method = "POST"
		if op.path != "" && !strings.HasPrefix(op.path, "/") {
			add("post: %q must start with /", op.path)
		}
		if err := p.resolveQuery(op); err != nil {
			problems = append(problems, fmt.Errorf("ops.%s.%w", op.name, err))
		}
	case op.method == "" && op.Kind == "":
		add("get: declare one of get, post, put, patch or delete")
	case op.method != "" && !strings.HasPrefix(op.path, "/"):
		add("%s: %q must start with /", strings.ToLower(op.method), op.path)
	}

	switch op.Encode {
	case EncodeUnset, EncodeJSON:
	case EncodeForm:
		if op.IsGraphQL() {
			add("encode: a graphql operation sends JSON")
		}
	default:
		add("encode: %q is not a body encoding, use json or form", op.Encode)
	}
	if op.MaxBytes < 0 {
		add("max_bytes: must not be negative")
	}
	if op.Implements != "" && !implementsRe.MatchString(op.Implements) {
		add("implements: %q is not an interface operation like forge/v1.get_change", op.Implements)
	}
	if len(op.Pick) > 0 && op.Transform != "" {
		add("pick: pick and transform are two ways to say the same thing, keep one")
	}
	if err := p.compileTransform(op); err != nil {
		problems = append(problems, fmt.Errorf("ops.%s.%w", op.name, err))
	}
	if op.Paginate {
		strategy := p.PageStrategy(op)
		if strategy == nil {
			add("paginate: neither the operation nor the pack declares a pagination strategy")
		}
	}
	if err := op.Pagination.check(fmt.Sprintf("ops.%s.pagination", op.name)); err != nil {
		problems = append(problems, err)
	}
	problems = append(problems, p.resolveParams(op)...)
	return errors.Join(problems...)
}

// compileTransform compiles the transform of an operation, building one from
// pick when that shorthand is used.
func (p *Pack) compileTransform(op *Op) error {
	source := op.Transform
	if len(op.Pick) > 0 {
		fields := make([]string, 0, len(op.Pick))
		for _, field := range op.Pick {
			if !identRe.MatchString(field) {
				return fmt.Errorf("pick: %q is not a field name", field)
			}
			fields = append(fields, field)
		}
		source = "{ " + strings.Join(fields, ", ") + " }"
	}
	if source == "" {
		return nil
	}
	code, err := compileJQ("transform", source)
	if err != nil {
		return err
	}
	op.transform = code
	return nil
}

// resolveQuery reads the GraphQL document of an operation. The query field
// is either the document itself or the name of a *.graphql file next to the
// pack.
func (p *Pack) resolveQuery(op *Op) error {
	source := strings.TrimSpace(op.Query)
	if source == "" {
		return errors.New("query: required for a graphql operation")
	}
	if isQueryFile(source) {
		if p.Path == "" {
			return fmt.Errorf("query: %s can only be read from a pack loaded from disk", source)
		}
		clean := filepath.ToSlash(source)
		if !fs.ValidPath(path.Clean(clean)) || strings.HasPrefix(clean, "/") {
			return fmt.Errorf("query: %s must be a path inside the pack directory", source)
		}
		data, err := os.ReadFile(filepath.Join(filepath.Dir(p.Path), filepath.FromSlash(clean)))
		if err != nil {
			return fmt.Errorf("query: %s", err)
		}
		source = strings.TrimSpace(string(data))
		if source == "" {
			return fmt.Errorf("query: %s is empty", op.Query)
		}
	}
	op.query = source
	return nil
}

// isQueryFile tells a file name from an inline GraphQL document.
func isQueryFile(source string) bool {
	if strings.ContainsAny(source, " \t\n{") {
		return false
	}
	return strings.HasSuffix(source, ".graphql") || strings.HasSuffix(source, ".gql")
}

// resolveParams fills in the placement of every argument and compiles the
// argument patterns.
func (p *Pack) resolveParams(op *Op) []error {
	var problems []error
	placeholders := map[string]bool{}
	for _, match := range placeholderRe.FindAllStringSubmatch(op.path, -1) {
		placeholders[match[1]] = true
	}

	names := make([]string, 0, len(op.Params))
	for name := range op.Params {
		names = append(names, name)
	}
	slices.Sort(names)

	// wires maps the name an argument is sent under to the argument that
	// claimed it, so that two params cannot overwrite each other.
	wires := make(map[string]string, len(op.Params))

	for _, name := range names {
		param := op.Params[name]
		prefix := fmt.Sprintf("ops.%s.params.%s", op.name, name)
		if !identRe.MatchString(name) {
			problems = append(problems, fmt.Errorf("%s: %q is not an argument name", prefix, name))
		}
		if param.In == InUnset {
			param.In = op.placement(name, placeholders[name])
		}
		switch param.In {
		case InPath:
			if !placeholders[name] {
				problems = append(problems, fmt.Errorf("%s: in: path but the operation path has no {%s}", prefix, name))
			}
			if param.Name != "" {
				problems = append(problems, fmt.Errorf("%s: name: a path argument is named by its {%s} placeholder", prefix, name))
			}
		case InQuery, InBody, InForm:
		default:
			problems = append(problems, fmt.Errorf("%s: in: %q is not a placement", prefix, param.In))
		}
		if wire := param.Wire(name); param.In != InPath {
			if other, taken := wires[wire]; taken {
				problems = append(problems, fmt.Errorf("%s: name: %q is already sent for params.%s", prefix, wire, other))
			} else {
				wires[wire] = name
			}
		}
		if param.Encode != EncodeUnset && param.Encode != EncodePath {
			problems = append(problems, fmt.Errorf("%s: encode: only path is a value here", prefix))
		}
		if param.Pattern == "" && (param.In == InPath || param.In == InQuery) {
			problems = append(problems, fmt.Errorf("%s: pattern: required for arguments that go into the path or the query", prefix))
		}
		if param.Pattern != "" {
			compiled, err := regexp.Compile(param.Pattern)
			if err != nil {
				problems = append(problems, fmt.Errorf("%s: pattern: %w", prefix, err))
			}
			param.pattern = compiled
		}
		if param.MaxLen < 0 {
			problems = append(problems, fmt.Errorf("%s: max_len: must not be negative", prefix))
		}
	}

	for name := range placeholders {
		if _, ok := op.Params[name]; !ok {
			problems = append(problems, fmt.Errorf("ops.%s: the path uses {%s}, which is not declared in params", op.name, name))
		}
	}
	slices.SortFunc(problems, func(a, b error) int { return strings.Compare(a.Error(), b.Error()) })
	return problems
}

// placement is the default location of an argument: a path placeholder, the
// query of a GET, or the request body, encoded the way the operation says.
func (o *Op) placement(name string, isPlaceholder bool) ParamIn {
	switch {
	case isPlaceholder:
		return InPath
	case o.method == "GET":
		return InQuery
	case o.Encode == EncodeForm:
		return InForm
	default:
		return InBody
	}
}

// check validates a pagination strategy and compiles its item selector.
func (p *Pagination) check(prefix string) error {
	if p == nil {
		return nil
	}
	var problems []error
	switch p.Style {
	case PageLinkHeader:
	case PagePage:
		if p.Param == "" {
			problems = append(problems, fmt.Errorf("%s.param: required for the page style", prefix))
		}
		if p.SizeParam != "" && p.Size <= 0 {
			problems = append(problems, fmt.Errorf("%s.size: required when size_param is set", prefix))
		}
	case PageOffset:
		if p.Param == "" {
			problems = append(problems, fmt.Errorf("%s.param: required for the offset style", prefix))
		}
		if p.LimitParam != "" && p.Size <= 0 {
			problems = append(problems, fmt.Errorf("%s.size: required when limit_param is set", prefix))
		}
	case PageCursor:
		if p.Next == "" {
			problems = append(problems, fmt.Errorf("%s.next: required for the cursor style", prefix))
		}
		if p.Param == "" {
			// A cursor that is a whole URL needs no parameter, but the pack
			// cannot say which one the service returns, so the parameter is
			// asked for and left unused when the cursor is a URL.
			problems = append(problems, fmt.Errorf("%s.param: required for the cursor style", prefix))
		}
	case "":
		problems = append(problems, fmt.Errorf("%s.style: required", prefix))
	default:
		problems = append(problems, fmt.Errorf("%s.style: %q is not a pagination style", prefix, p.Style))
	}
	switch p.In {
	case InUnset, InQuery, InBody:
	default:
		problems = append(problems, fmt.Errorf("%s.in: %q is not a placement here, use query or body", prefix, p.In))
	}
	if p.MaxPages < 0 {
		problems = append(problems, fmt.Errorf("%s.max_pages: must not be negative", prefix))
	}
	if p.Items != "" {
		code, err := compileJQ(prefix+".items", p.Items)
		if err != nil {
			problems = append(problems, err)
		}
		p.items = code
	}
	for _, field := range []struct {
		name   string
		source string
		into   **gojq.Code
	}{
		{prefix + ".next", p.Next, &p.next},
		{prefix + ".total", p.Total, &p.total},
	} {
		if field.source == "" {
			continue
		}
		code, err := compileJQ(field.name, field.source)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		*field.into = code
	}
	return errors.Join(problems...)
}

// compile compiles the jq expressions of the envelope.
func (e *Envelope) compile() error {
	if e == nil {
		return nil
	}
	var problems []error
	for _, field := range []struct {
		name   string
		source string
		into   **gojq.Code
	}{
		{"envelope.unwrap", e.Unwrap, &e.unwrap},
		{"envelope.error_when", e.ErrorWhen, &e.errorWhen},
		{"envelope.error_message", e.ErrorMessage, &e.errorMessage},
	} {
		if field.source == "" {
			continue
		}
		code, err := compileJQ(field.name, field.source)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		*field.into = code
	}
	if e.ErrorMessage != "" && e.ErrorWhen == "" {
		problems = append(problems, errors.New("envelope.error_message: only used together with error_when"))
	}
	return errors.Join(problems...)
}
