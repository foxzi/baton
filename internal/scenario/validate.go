package scenario

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/foxzi/baton/internal/expr"
	"github.com/foxzi/baton/internal/ifaces"
	"github.com/foxzi/baton/internal/tmpl"
)

// Diagnostic is one validation finding.
type Diagnostic struct {
	// Path locates the finding in the scenario, such as steps[2].run.argv.
	Path string
	// Line is the scenario line the finding belongs to, or 0 when Parse
	// could not attribute it to one (a field the YAML never set, or a
	// Scenario built in code rather than parsed). It is never fabricated.
	Line int
	// StepID is the id of the step the finding belongs to, read from the
	// step's own id field rather than its steps[N] index so it still names
	// the right step after reordering. Empty when the finding is not scoped
	// to one step (inputs, secrets, apis, ...) or the step has no valid id.
	StepID  string
	Message string
}

// suffix renders the step-id annotation, when known, as " (step build)". It
// is appended after the message rather than spliced between Path and
// Message so that "path: message" stays one contiguous substring for
// callers (and tests) that only care about those two.
func (d Diagnostic) suffix() string {
	if d.StepID == "" {
		return ""
	}
	return fmt.Sprintf(" (step %s)", d.StepID)
}

// String renders the finding as one log line, without a scenario path. It
// predates Format and stays for callers that print a single scenario's
// diagnostics without repeating its path on every line (doctor, apis, tools).
func (d Diagnostic) String() string {
	if d.Line > 0 {
		return fmt.Sprintf("line %d: %s: %s%s", d.Line, d.Path, d.Message, d.suffix())
	}
	return fmt.Sprintf("%s: %s%s", d.Path, d.Message, d.suffix())
}

// Format renders the finding as scenarioPath:line: path: message, the shape
// editors and CI log scrapers recognise as a source location. scenarioPath
// is normally the path Load or Parse read the scenario from; it is passed
// in rather than stored on Diagnostic so one Result can be formatted
// against whichever path the caller used. The line is omitted, not
// invented, when Line is 0.
func (d Diagnostic) Format(scenarioPath string) string {
	if d.Line > 0 {
		return fmt.Sprintf("%s:%d: %s: %s%s", scenarioPath, d.Line, d.Path, d.Message, d.suffix())
	}
	return fmt.Sprintf("%s: %s: %s%s", scenarioPath, d.Path, d.Message, d.suffix())
}

// Result collects validation findings. Validation reports every problem it
// finds rather than stopping at the first one (spec section 4).
type Result struct {
	Errors   []Diagnostic
	Warnings []Diagnostic

	// scope is the step whose expressions are being validated and refs the
	// step references found in them, resolved once every id is known.
	scope *stepScope
	refs  []stepRef
}

// OK reports whether the scenario is free of errors. Warnings do not fail
// validation.
func (r *Result) OK() bool { return len(r.Errors) == 0 }

func (r *Result) errorf(path string, line int, format string, args ...any) {
	r.Errors = append(r.Errors, Diagnostic{Path: path, Line: line, StepID: r.stepID(), Message: fmt.Sprintf(format, args...)})
}

func (r *Result) warnf(path string, line int, format string, args ...any) {
	r.Warnings = append(r.Warnings, Diagnostic{Path: path, Line: line, StepID: r.stepID(), Message: fmt.Sprintf(format, args...)})
}

// stepID reports the id of the step currently in scope, so that diagnostics
// raised while validating its body (including a nested foreach/until body,
// which has no id of its own) are attributed to it.
func (r *Result) stepID() string {
	if r.scope == nil {
		return ""
	}
	return r.scope.id
}

// idPattern is the required shape of a step id (spec section 3.2).
var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// inputTypes are the declarable input types (spec section 3.1).
var inputTypes = map[InputType]bool{
	TypeString: true, TypeInt: true, TypeNumber: true,
	TypeBool: true, TypeList: true, TypeMap: true,
}

// inputTypeNames lists inputTypes in a stable order for diagnostic hints.
var inputTypeNames = []string{
	string(TypeString), string(TypeInt), string(TypeNumber),
	string(TypeBool), string(TypeList), string(TypeMap),
}

// errorClasses are the retryable-error class names (spec section 9.1).
var errorClasses = map[string]bool{
	"transient": true, "schema": true, "command": true,
	"timeout": true, "budget": true, "policy": true, "config": true,
}

// parseModes are the accepted parse values (spec section 3.3).
var parseModes = map[ParseMode]bool{
	ParseUnset: true, ParseText: true, ParseJSON: true, ParseLines: true,
}

// Validate performs the structural checks of specification section 4.
// Checks 3, 4 and 7 compile expressions and templates through internal/expr
// and internal/tmpl; checks 5 and 6 run in internal/scenario/refs.go. Checks
// 12 and 13 need the packs, which load at run time, so internal/engine makes
// them instead.
func Validate(scn *Scenario) Result {
	var res Result

	if scn.Version != 1 {
		res.errorf("version", scn.lineOf("version"), "must be 1, got %d", scn.Version)
	}
	if strings.TrimSpace(scn.Name) == "" {
		res.errorf("name", scn.lineOf("name"), "must not be empty")
	}
	if scn.Budget.Tokens < 0 {
		res.errorf("budget.tokens", scn.lineOf("budget.tokens"), "must not be negative")
	}

	validateInputs(scn, &res)
	validateSecrets(scn, &res)
	validateAPIs(scn, &res)
	validateCommands(scn, &res)

	if len(scn.Steps) == 0 {
		res.errorf("steps", scn.lineOf("steps"), "must declare at least one step")
	}

	declared := map[string]bool{}
	validateSteps(scn, scn.Steps, "steps", declared, &res)
	// on_failure runs after the scenario failed and has its own id namespace,
	// but every step of the run is in scope there.
	validateSteps(scn, scn.OnFailure, "on_failure", stepIDs(scn.Steps), &res)
	validateSwitches(scn, &res)
	resolveRefs(&res, conditionalSteps(scn))
	res.scope, res.refs = nil, nil

	return res
}

func validateInputs(scn *Scenario, res *Result) {
	for _, name := range sortedKeys(scn.Inputs) {
		input := scn.Inputs[name]
		path := "inputs." + name
		line := scn.lineOf(path)
		switch {
		case input.Type == "":
			res.errorf(path, line, "must declare a type, one of %s", strings.Join(inputTypeNames, ", "))
		case !inputTypes[input.Type]:
			res.errorf(path, line, "unknown type %q, want one of %s", input.Type, strings.Join(inputTypeNames, ", "))
		}
		if input.Pattern != "" {
			patternLine := scn.lineOf(path + ".pattern")
			if input.Type != TypeString {
				res.errorf(path+".pattern", patternLine, "pattern applies to string inputs only")
			}
			if _, err := regexp.Compile(input.Pattern); err != nil {
				res.errorf(path+".pattern", patternLine, "invalid regular expression: %v", err)
			}
		}
		if input.Required && input.Default != nil {
			res.warnf(path, line, "default is unreachable on a required input")
		}
	}
}

func validateSecrets(scn *Scenario, res *Result) {
	for _, name := range sortedKeys(scn.Secrets) {
		secret := scn.Secrets[name]
		path := "secrets." + name
		line := scn.lineOf(path)
		switch secret.From {
		case SecretFromEnv:
			if secret.Key == "" {
				res.errorf(path, line, "from: env needs key")
			}
			if secret.Path != "" {
				res.errorf(path, line, "path applies to from: file only")
			}
		case SecretFromFile:
			if secret.Path == "" {
				res.errorf(path, line, "from: file needs path")
			}
			if secret.Key != "" {
				res.errorf(path, line, "key applies to from: env only")
			}
		case "":
			res.errorf(path, line, "must declare from: env or from: file")
		default:
			res.errorf(path, line, "unknown source %q, want env or file", secret.From)
		}
	}
}

// validateSteps checks a step list in declaration order, filling declared
// with the ids it accepts so that later steps can reference them.
func validateSteps(scn *Scenario, steps []Step, prefix string, declared map[string]bool, res *Result) {
	for i := range steps {
		step := &steps[i]
		path := fmt.Sprintf("%s[%d]", prefix, i)
		res.enterStep(&stepScope{
			path:        path,
			id:          step.ID,
			earlier:     cloneIDs(declared),
			conditional: strings.TrimSpace(step.When) != "",
			ordered:     prefix == "steps",
		})
		validateStepID(step, path, declared, res)
		validateStepBody(scn, step, path, res)
		validateStepControl(step, path, declared, res)
		validateExpr(path+".when", step.When, step.Line, res)
		res.enterStep(nil)
	}
}

func validateStepID(step *Step, path string, declared map[string]bool, res *Result) {
	// A switch expands into the steps declared in its cases, so it carries no
	// id of its own.
	if step.Kind() == KindSwitch {
		return
	}
	switch {
	case step.ID == "":
		res.errorf(path+".id", step.Line, "must not be empty")
	case !idPattern.MatchString(step.ID):
		res.errorf(path+".id", step.Line, "%q must match %s", step.ID, idPattern)
	case declared[step.ID]:
		res.errorf(path+".id", step.Line, "duplicate step id %q", step.ID)
	default:
		declared[step.ID] = true
	}
}

func validateStepBody(scn *Scenario, step *Step, path string, res *Result) {
	kinds := step.Kinds()
	switch len(kinds) {
	case 1:
	case 0:
		res.errorf(path, step.Line, "must declare one of run, http, llm, agent, foreach, until, file, assert")
		return
	default:
		res.errorf(path, step.Line, "must declare exactly one body, got %s", joinKinds(kinds))
		return
	}

	switch kinds[0] {
	case KindRun:
		validateRun(scn, step, path+".run", res)
	case KindHTTP:
		validateHTTP(scn, step, path+".http", res)
	case KindAssert:
		if strings.TrimSpace(step.Assert.Condition) == "" {
			res.errorf(path+".assert.condition", step.Line, "must not be empty")
		}
		validateExpr(path+".assert.condition", step.Assert.Condition, step.Line, res)
		validateTemplate(path+".assert.message", step.Assert.Message, step.Line, res)
	case KindLLM:
		validateLLM(scn, step, path+".llm", res)
	case KindAgent:
		validateAgent(scn, step, path+".agent", res)
	case KindForeach:
		validateForeach(scn, step, path+".foreach", res)
	case KindUntil:
		validateUntil(scn, step, path+".until", res)
	case KindFile:
		validateFile(step, path+".file", res)
	case KindNotify:
		validateNotify(step, path, res)
	}
}

// validateNotify checks the sugar form of section 9.3: a channel name and
// the message to send. The channel itself lives in the global configuration,
// which the scenario cannot see, so it is only checked at run time.
func validateNotify(step *Step, path string, res *Result) {
	if strings.TrimSpace(step.Message) == "" {
		res.errorf(path+".message", step.Line, "notify requires a message")
	}
	validateTemplate(path+".message", step.Message, step.Line, res)
}

func validateRun(scn *Scenario, step *Step, path string, res *Result) {
	run := step.Run
	switch {
	case len(run.Argv) == 0:
		res.errorf(path+".argv", step.Line, "must not be empty")
	case strings.TrimSpace(run.Argv[0]) == "":
		res.errorf(path+".argv[0]", step.Line, "command must not be empty")
	}
	if run.SplitFromString() {
		res.warnf(path, step.Line, "command string was split on spaces; prefer an explicit argv list")
	}
	if !parseModes[run.Parse] {
		res.errorf(path+".parse", step.Line, "unknown mode %q, want text, json or lines", run.Parse)
	}
	if run.MaxOutputBytes < 0 {
		res.errorf(path+".max_output_bytes", step.Line, "must not be negative")
	}
	for i, arg := range run.Argv {
		validateTemplate(fmt.Sprintf("%s.argv[%d]", path, i), arg, step.Line, res)
	}
	validateTemplate(path+".stdin", run.Stdin, step.Line, res)
	validateTemplate(path+".cwd", run.Cwd, step.Line, res)
	validateEnv(scn, run.Env, path+".env", step.Line, res)
	// Specification section 9.4: a command that may have side effects is not
	// retried automatically unless the run is idempotent.
	if !run.Readonly && step.DedupeKey == "" && step.Retry != nil {
		res.warnf(path, step.Line, "retry on a step without readonly: true or dedupe_key may repeat side effects")
	}
}

// httpMethods are the methods a raw request may use.
var httpMethods = map[string]bool{
	"GET": true, "HEAD": true, "POST": true, "PUT": true,
	"PATCH": true, "DELETE": true,
}

// effectfulMethods have side effects, so section 9.4 wants a dedupe key.
var effectfulMethods = map[string]bool{
	"POST": true, "PUT": true, "PATCH": true, "DELETE": true,
}

// opPattern is the required shape of an op reference: <api>.<op>.
var opPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)

func validateAPIs(scn *Scenario, res *Result) {
	for _, name := range sortedKeys(scn.APIs) {
		api := scn.APIs[name]
		path := "apis." + name
		if !idPattern.MatchString(name) {
			res.errorf(path, api.Line, "%q must match %s", name, idPattern)
		}
		if strings.TrimSpace(api.Pack) == "" {
			res.errorf(path+".pack", api.Line, "must name a pack")
		}
		validateTemplate(path+".pack", api.Pack, api.Line, res)
		if strings.TrimSpace(api.From) == "" {
			res.errorf(path+".from", api.Line, "must name a pack source")
		}
		if api.Auth.Secret != "" && scn.Secrets[api.Auth.Secret].From == "" {
			res.errorf(path+".auth.secret", api.Line, "undeclared secret %q", api.Auth.Secret)
		}
		if api.Timeout < 0 {
			res.errorf(path+".timeout", api.Line, "must not be negative")
		}
		for _, field := range sortedKeys(api.Config) {
			validateTemplate(path+".config."+field, api.Config[field], api.Line, res)
		}
		// The pack itself only loads at run time, so this can only check
		// that the interface name exists in the registry; whether the pack
		// actually implements it is checked in internal/engine.
		if api.Interface != "" {
			if _, ok := ifaces.Get(api.Interface); !ok {
				res.errorf(path+".interface", api.Line, "unknown interface %q: known interfaces are %s", api.Interface, strings.Join(ifaces.Names(), ", "))
			}
		}
	}
}

// validateHTTP checks an http body: exactly one of the two forms of section
// 3.4, a resolvable apis reference, and the side-effect rule of section 9.4.
// Whether the operation exists and its arguments fit its params is checked
// against the loaded pack at run time.
func validateHTTP(scn *Scenario, step *Step, path string, res *Result) {
	body := step.HTTP
	apiName, _ := body.APIName()

	switch {
	case body.Op != "" && (body.API != "" || body.URL != ""):
		res.errorf(path, step.Line, "op cannot be combined with api or url; use one form")
	case body.Op == "" && body.API == "" && body.URL == "":
		res.errorf(path, step.Line, "must set op, api or url")
	case body.Op != "" && !opPattern.MatchString(body.Op):
		res.errorf(path+".op", step.Line, "%q must be <api>.<op>", body.Op)
	}
	if !body.Raw() {
		for _, field := range [][2]string{{"path", body.Path}, {"body", body.Body}} {
			if field[1] != "" {
				res.errorf(path+"."+field[0], step.Line, "belongs to the raw form, not to op")
			}
		}
	}
	if apiName != "" {
		if _, ok := scn.APIs[apiName]; !ok {
			res.errorf(path, step.Line, "unknown api %q", apiName)
		}
	}
	if body.Auth != "" && scn.Secrets[body.Auth].From == "" {
		res.errorf(path+".auth", step.Line, "undeclared secret %q", body.Auth)
	}

	method := strings.ToUpper(body.Method)
	switch {
	case method == "":
	case !body.Raw():
		res.errorf(path+".method", step.Line, "the operation of the pack decides the method")
	case !httpMethods[method]:
		res.errorf(path+".method", step.Line, "unknown method %q", body.Method)
	}
	if !parseModes[body.Parse] {
		res.errorf(path+".parse", step.Line, "unknown mode %q, want text, json or lines", body.Parse)
	}
	if body.MaxBytes < 0 {
		res.errorf(path+".max_bytes", step.Line, "must not be negative")
	}
	for _, status := range body.ExpectStatus {
		if status < 100 || status > 599 {
			res.errorf(path+".expect_status", step.Line, "%d is not an HTTP status", status)
		}
	}

	validateTemplate(path+".url", body.URL, step.Line, res)
	validateTemplate(path+".path", body.Path, step.Line, res)
	validateTemplate(path+".body", body.Body, step.Line, res)
	for _, name := range sortedKeys(body.Headers) {
		validateTemplate(path+".headers."+name, body.Headers[name], step.Line, res)
	}
	for _, name := range sortedKeys(body.Query) {
		validateTemplate(path+".query."+name, body.Query[name], step.Line, res)
	}
	for _, name := range sortedKeys(body.Args) {
		validateArg(fmt.Sprintf("%s.args.%s", path, name), body.Args[name], step.Line, res)
	}

	// Section 9.4: a request that may change something needs a dedupe key
	// before it can be repeated. An op without readonly: true is checked at
	// run time, where the pack is known.
	if effectfulMethods[method] && step.DedupeKey == "" {
		res.warnf(path, step.Line, "%s without dedupe_key cannot be retried safely", method)
	}
	validateTemplate(path+".dedupe_key", step.DedupeKey, step.Line, res)
}

// validateArg checks the templates inside one operation argument, which may
// be a scalar, a list or a mapping.
func validateArg(path string, value any, line int, res *Result) {
	switch typed := value.(type) {
	case string:
		validateTemplate(path, typed, line, res)
	case []any:
		for i, item := range typed {
			validateArg(fmt.Sprintf("%s[%d]", path, i), item, line, res)
		}
	case map[string]any:
		for _, name := range sortedKeys(typed) {
			validateArg(path+"."+name, typed[name], line, res)
		}
	}
}

// validateSwitches reports on the switch steps Parse expanded away. The
// expansion itself already rejected the malformed ones; what is left is the
// subject expression and the coverage of the subject's values (section 4,
// check 10). When the subject reads an enum field of an llm step's schema,
// the cases must cover every value of that enum; otherwise the validator
// cannot prove coverage and only asks for a default.
func validateSwitches(scn *Scenario, res *Result) {
	for _, info := range scn.switches {
		validateExpr(info.path+".switch", info.subject, info.line, res)

		enum := switchEnum(scn, info.subject)
		if len(enum) == 0 {
			if !info.hasDefault {
				res.warnf(info.path, info.line, "no default case; every value of the subject must be covered")
			}
			continue
		}

		covered := make(map[string]bool, len(info.values))
		for _, label := range info.values {
			covered[label] = true
		}
		known := make(map[string]bool, len(enum))
		var missing []string
		for _, value := range enum {
			label, ok := caseLabel(value)
			if !ok {
				continue
			}
			known[label] = true
			if !covered[label] {
				missing = append(missing, label)
			}
		}
		for _, label := range info.values {
			if !known[label] {
				res.warnf(info.path+".cases", info.line, "case %s is not a value of the subject enum, so it can never match", label)
			}
		}
		if len(missing) > 0 && !info.hasDefault {
			res.errorf(info.path, info.line, "does not cover %s of the subject enum; add the missing cases or a default", strings.Join(missing, ", "))
		}
	}
}

// modelPattern is the required shape of a model reference: <provider>/<model>
// (spec section 3.5 and 8.3).
var modelPattern = regexp.MustCompile(`^[^/]+/.+$`)

// structuredModes are the accepted structured_mode values (spec section 8.3).
var structuredModes = map[StructuredMode]bool{
	StructuredModeUnset: true, StructuredNative: true,
	StructuredTool: true, StructuredPrompt: true,
}

// validateLLM checks an llm body (spec section 3.5). Whether tools reference
// readonly operations of a loaded pack is checked once packs load (section
// 4, check 13).
func validateLLM(scn *Scenario, step *Step, path string, res *Result) {
	llm := step.LLM

	if llm.Model != "" && !modelPattern.MatchString(llm.Model) {
		res.errorf(path+".model", step.Line, "%q must be <provider>/<model>", llm.Model)
	}
	for i, model := range llm.FallbackModels {
		if !modelPattern.MatchString(model) {
			res.errorf(fmt.Sprintf("%s.fallback_models[%d]", path, i), step.Line, "%q must be <provider>/<model>", model)
		}
	}

	// schema is required (spec section 3.5).
	if strings.TrimSpace(llm.Schema) == "" {
		res.errorf(path+".schema", step.Line, "must not be empty")
	}

	validateTemplate(path+".system", llm.System, step.Line, res)
	validateTemplate(path+".prompt", llm.Prompt, step.Line, res)
	for _, name := range sortedKeys(llm.With) {
		validateTemplate(path+".with."+name, llm.With[name], step.Line, res)
	}

	for i, tool := range llm.Tools {
		if strings.TrimSpace(tool) == "" {
			res.errorf(fmt.Sprintf("%s.tools[%d]", path, i), step.Line, "must not be empty")
		}
	}

	if llm.MaxTokens < 0 {
		res.errorf(path+".max_tokens", step.Line, "must not be negative")
	}
	if llm.Temperature != nil && *llm.Temperature < 0 {
		res.errorf(path+".temperature", step.Line, "must not be negative")
	}
	if !structuredModes[llm.StructuredMode] {
		res.errorf(path+".structured_mode", step.Line, "unknown value %q, want native, tool or prompt", llm.StructuredMode)
	}
}

// engines are the agent engine implementations (spec section 8).
var engines = map[string]bool{
	"claude-code": true, "codex": true, "fake": true,
}

// profiles are the accepted tool profiles (spec section 7.2).
var profiles = map[Profile]bool{
	ProfileUnset: true, ProfileReview: true, ProfileFix: true, ProfileResearch: true,
}

// fsWrites are the accepted fs.write values (spec section 7.3).
var fsWrites = map[FSWrite]bool{
	FSWriteUnset: true, FSWriteNone: true, FSWriteWorkspace: true,
}

// execModes are the accepted exec.mode values (spec section 7.3).
var execModes = map[ExecMode]bool{
	ExecModeUnset: true, ExecModeNone: true, ExecModeCommands: true,
}

// stateAccesses are the accepted state values (spec section 7.3).
var stateAccesses = map[StateAccess]bool{
	StateUnset: true, StateNone: true, StateRead: true, StateReadWrite: true,
}

// validateAgent checks an agent body (spec section 3.6). Whether the
// operations in tools.apis exist and are readonly is decided when the packs
// load (section 4, check 13).
func validateAgent(scn *Scenario, step *Step, path string, res *Result) {
	agent := step.Agent

	switch {
	case strings.TrimSpace(agent.Engine) == "":
		res.errorf(path+".engine", step.Line, "must name an engine")
	case !engines[agent.Engine]:
		res.errorf(path+".engine", step.Line, "unknown engine %q, want claude-code, codex or fake", agent.Engine)
	}

	// The fake engine replays a behaviour file; a real engine has nothing to
	// replay (spec section 8.4).
	switch {
	case agent.Engine == "fake" && strings.TrimSpace(agent.Script) == "":
		res.errorf(path+".script", step.Line, "engine: fake needs a behaviour script")
	case agent.Engine != "fake" && agent.Script != "":
		res.errorf(path+".script", step.Line, "script applies to engine: fake only")
	}

	// Only codex can reuse a login stored outside the run (spec section 8.2).
	if agent.InheritAuth && agent.Engine != "codex" {
		res.errorf(path+".inherit_auth", step.Line, "applies to engine: codex only")
	}

	if strings.TrimSpace(agent.Prompt) == "" {
		res.errorf(path+".prompt", step.Line, "must not be empty")
	}
	// A step ends successfully only through submit_result, which needs a
	// schema to validate against (spec section 3.6).
	if strings.TrimSpace(agent.Result) == "" {
		res.errorf(path+".result", step.Line, "must name the schema submit_result validates against")
	}

	validateTemplate(path+".prompt", agent.Prompt, step.Line, res)
	validateTemplate(path+".system", agent.System, step.Line, res)
	for _, name := range sortedKeys(agent.With) {
		validateTemplate(path+".with."+name, agent.With[name], step.Line, res)
	}

	if !profiles[agent.Profile] {
		res.errorf(path+".profile", step.Line, "unknown profile %q, want review, fix or research", agent.Profile)
	}
	if agent.MaxTurns < 0 {
		res.errorf(path+".max_turns", step.Line, "must not be negative")
	}
	if agent.BudgetUSD < 0 {
		res.errorf(path+".budget_usd", step.Line, "must not be negative")
	}
	for i, skill := range agent.Skills {
		if strings.TrimSpace(skill) == "" {
			res.errorf(fmt.Sprintf("%s.skills[%d]", path, i), step.Line, "must not be empty")
		}
	}
	validateEnv(scn, agent.Env, path+".env", step.Line, res)

	if agent.Tools != nil {
		validateTools(scn, agent.Tools, path+".tools", step.Line, res)
	}
	if agent.Limits != nil {
		if agent.Limits.MaxToolCalls < 0 {
			res.errorf(path+".limits.max_tool_calls", step.Line, "must not be negative")
		}
		if agent.Limits.MaxResultBytes < 0 {
			res.errorf(path+".limits.max_result_bytes", step.Line, "must not be negative")
		}
	}
}

// validateTools checks the tool overrides of an agent step (spec section
// 7.3).
func validateTools(scn *Scenario, tools *Tools, path string, line int, res *Result) {
	if fs := tools.FS; fs != nil {
		if !fsWrites[fs.Write] {
			res.errorf(path+".fs.write", line, "unknown value %q, want none or workspace", fs.Write)
		}
		for i, pattern := range fs.Deny {
			if strings.TrimSpace(pattern) == "" {
				res.errorf(fmt.Sprintf("%s.fs.deny[%d]", path, i), line, "must not be empty")
			}
		}
	}

	if exec := tools.Exec; exec != nil {
		if !execModes[exec.Mode] {
			res.errorf(path+".exec.mode", line, "unknown value %q, want none or commands", exec.Mode)
		}
		if exec.Mode == ExecModeNone && len(exec.Commands) > 0 {
			res.warnf(path+".exec.commands", line, "ignored unless mode is commands")
		}
		for i, name := range exec.Commands {
			if _, ok := scn.Commands[name]; !ok {
				res.errorf(fmt.Sprintf("%s.exec.commands[%d]", path, i), line, "undeclared command %q", name)
			}
		}
	}

	for i, op := range tools.APIs {
		opPath := fmt.Sprintf("%s.apis[%d]", path, i)
		if !opPattern.MatchString(op) {
			res.errorf(opPath, line, "%q must be <api>.<op>", op)
			continue
		}
		api, _, _ := strings.Cut(op, ".")
		if _, ok := scn.APIs[api]; !ok {
			res.errorf(opPath, line, "undeclared api %q", api)
		}
	}

	if fetch := tools.Fetch; fetch != nil {
		// Fetch without an allow list would be the whole internet.
		if len(fetch.Allow) == 0 {
			res.errorf(path+".fetch.allow", line, "must list the allowed hosts")
		}
		for i, host := range fetch.Allow {
			if strings.TrimSpace(host) == "" {
				res.errorf(fmt.Sprintf("%s.fetch.allow[%d]", path, i), line, "must not be empty")
			}
		}
		if fetch.MaxBytes < 0 {
			res.errorf(path+".fetch.max_bytes", line, "must not be negative")
		}
		if fetch.MaxCalls < 0 {
			res.errorf(path+".fetch.max_calls", line, "must not be negative")
		}
	}

	for i, name := range tools.MCP {
		if strings.TrimSpace(name) == "" {
			res.errorf(fmt.Sprintf("%s.mcp[%d]", path, i), line, "must not be empty")
		}
	}

	if !stateAccesses[tools.State] {
		res.errorf(path+".state", line, "unknown value %q, want none, read or read-write", tools.State)
	}
}

// validateCommands checks the commands agent steps may ask for (spec section
// 7.5).
func validateCommands(scn *Scenario, res *Result) {
	for _, name := range sortedKeys(scn.Commands) {
		command := scn.Commands[name]
		path := "commands." + name
		if !idPattern.MatchString(name) {
			res.errorf(path, command.Line, "%q must match %s", name, idPattern)
		}
		switch {
		case len(command.Argv) == 0:
			res.errorf(path+".argv", command.Line, "must not be empty")
		case strings.TrimSpace(command.Argv[0]) == "":
			res.errorf(path+".argv[0]", command.Line, "command must not be empty")
		}
		for i, arg := range command.Argv {
			validateTemplate(fmt.Sprintf("%s.argv[%d]", path, i), arg, command.Line, res)
		}
		if !parseModes[command.Parse] {
			res.errorf(path+".parse", command.Line, "unknown mode %q, want text, json or lines", command.Parse)
		}
		if command.Timeout < 0 {
			res.errorf(path+".timeout", command.Line, "must not be negative")
		}
		if command.MaxCalls < 0 {
			res.errorf(path+".max_calls", command.Line, "must not be negative")
		}
		validateEnv(scn, command.Env, path+".env", command.Line, res)

		for _, argName := range sortedKeys(command.Args) {
			arg := command.Args[argName]
			argPath := path + ".args." + argName
			// A pattern is the only thing constraining what a model puts in
			// the argument vector of a process, so it is required.
			if strings.TrimSpace(arg.Pattern) == "" {
				res.errorf(argPath+".pattern", command.Line, "must constrain the argument with a pattern")
				continue
			}
			pattern, err := regexp.Compile(arg.Pattern)
			if err != nil {
				res.errorf(argPath+".pattern", command.Line, "invalid regular expression: %v", err)
				continue
			}
			if arg.Default != "" && !pattern.MatchString(arg.Default) {
				res.errorf(argPath+".default", command.Line, "%q does not match the pattern", arg.Default)
			}
			if arg.Required && arg.Default != "" {
				res.warnf(argPath, command.Line, "default is unreachable on a required argument")
			}
		}
	}
}

// validateEnv checks an environment block: a secret reference must name a
// declared secret, and only the literal form is a template (spec section 4,
// check 7).
func validateEnv(scn *Scenario, env map[string]EnvValue, path string, line int, res *Result) {
	for _, name := range sortedKeys(env) {
		entry := env[name]
		if entry.Secret != "" {
			if scn.Secrets[entry.Secret].From == "" {
				res.errorf(path+"."+name, line, "undeclared secret %q", entry.Secret)
			}
			continue
		}
		validateTemplate(path+"."+name, entry.Value, line, res)
	}
}

// itemErrorModes are the accepted on_item_error values (spec section 3.7).
var itemErrorModes = map[ItemErrorMode]bool{
	ItemErrorUnset: true, ItemErrorFail: true, ItemErrorContinue: true,
}

// validateForeach checks a foreach body (spec section 3.7). Its step is a
// body without an id of its own; validateStepBody recurses into it for the
// checks that do not need a place in the DAG.
func validateForeach(scn *Scenario, step *Step, path string, res *Result) {
	each := step.Foreach

	if strings.TrimSpace(each.Items) == "" {
		res.errorf(path+".items", step.Line, "must not be empty")
	}
	validateTemplate(path+".items", each.Items, step.Line, res)

	// as names the item in the body's templates; without it the body has no
	// way to reach the item.
	if strings.TrimSpace(each.As) == "" {
		res.errorf(path+".as", step.Line, "must not be empty")
	}

	if each.MaxParallel < 0 {
		res.errorf(path+".max_parallel", step.Line, "must not be negative")
	}
	// Specification section 4, check 14.
	if each.MaxParallel > 5 {
		res.warnf(path+".max_parallel", step.Line, "more than 5 parallel items")
	}

	if !itemErrorModes[each.OnItemError] {
		res.errorf(path+".on_item_error", step.Line, "unknown value %q, want fail or continue", each.OnItemError)
	}

	if each.MinSuccess != nil {
		if *each.MinSuccess < 0 || *each.MinSuccess > 1 {
			res.errorf(path+".min_success", step.Line, "must be between 0 and 1")
		}
		if each.OnItemError != ItemErrorContinue {
			res.warnf(path+".min_success", step.Line, "ignored unless on_item_error is continue")
		}
	}

	if each.Step == nil {
		res.errorf(path+".step", step.Line, "must declare a body")
		return
	}
	if each.Step.ID != "" {
		res.errorf(path+".step.id", each.Step.Line, "the body of a foreach has no id of its own")
	}
	validateStepBody(scn, each.Step, path+".step", res)
	validateExpr(path+".step.when", each.Step.When, each.Step.Line, res)
}

// validateUntil checks an until body (spec section 3.8).
func validateUntil(scn *Scenario, step *Step, path string, res *Result) {
	loop := step.Until

	if strings.TrimSpace(loop.Condition) == "" {
		res.errorf(path+".condition", step.Line, "must not be empty")
	}
	validateExpr(path+".condition", loop.Condition, step.Line, res)

	// Specification section 4, check 9.
	switch {
	case loop.MaxIterations == 0:
		res.errorf(path+".max_iterations", step.Line, "max_iterations is required")
	case loop.MaxIterations < 1:
		res.errorf(path+".max_iterations", step.Line, "must be at least 1")
	}

	if loop.Step == nil {
		res.errorf(path+".step", step.Line, "must declare a body")
		return
	}
	if loop.Step.ID != "" {
		res.errorf(path+".step.id", loop.Step.Line, "the body of an until has no id of its own")
	}
	validateStepBody(scn, loop.Step, path+".step", res)
	validateExpr(path+".step.when", loop.Step.When, loop.Step.Line, res)
}

// validateFile checks a file step: exactly one of read, write, append and
// glob, and that only the fields its operation uses are set.
func validateFile(step *Step, path string, res *Result) {
	body := step.File
	op, target := body.Op()
	if op == FileOpNone {
		res.errorf(path, step.Line, "must set exactly one of read, write, append, glob")
		return
	}
	validateTemplate(fmt.Sprintf("%s.%s", path, op), target, step.Line, res)

	switch op {
	case FileOpRead:
		if !parseModes[body.Parse] {
			res.errorf(path+".parse", step.Line, "unknown mode %q, want text, json or lines", body.Parse)
		}
		if body.MaxBytes < 0 {
			res.errorf(path+".max_bytes", step.Line, "must not be negative")
		}
		if body.Content != "" {
			res.errorf(path+".content", step.Line, "belongs to write and append, not to read")
		}
	case FileOpWrite, FileOpAppend:
		if body.Content == "" {
			res.errorf(path+".content", step.Line, "%s requires content", op)
		}
		validateTemplate(path+".content", body.Content, step.Line, res)
		if body.Parse != ParseUnset {
			res.errorf(path+".parse", step.Line, "belongs to read, not to %s", op)
		}
		if body.MaxBytes != 0 {
			res.errorf(path+".max_bytes", step.Line, "belongs to read, not to %s", op)
		}
		// Section 9.4: a write may change something, so a retry needs a
		// dedupe key before it can repeat safely.
		if step.DedupeKey == "" && step.Retry != nil {
			res.warnf(path, step.Line, "retry on a %s without dedupe_key may repeat side effects", op)
		}
	case FileOpGlob:
		if body.Content != "" {
			res.errorf(path+".content", step.Line, "belongs to write and append, not to glob")
		}
		if body.Parse != ParseUnset {
			res.errorf(path+".parse", step.Line, "belongs to read, not to glob")
		}
		if body.MaxBytes != 0 {
			res.errorf(path+".max_bytes", step.Line, "belongs to read, not to glob")
		}
	}
}

func validateStepControl(step *Step, path string, declared map[string]bool, res *Result) {
	for i, need := range step.Needs {
		needPath := fmt.Sprintf("%s.needs[%d]", path, i)
		switch {
		case need == step.ID:
			res.errorf(needPath, step.Line, "step %q cannot need itself", step.ID)
		case !declared[need]:
			// Steps run in declaration order, so a dependency must already be
			// declared. This also rules out cycles (section 4, check 2).
			res.errorf(needPath, step.Line, "unknown or later step %q", need)
		}
	}

	switch step.OnError {
	case OnErrorUnset, OnErrorFail, OnErrorContinue:
		if step.Fallback != nil {
			res.warnf(path+".fallback", step.Line, "ignored unless on_error is fallback")
		}
	case OnErrorFallback:
		// Specification section 4, check 11.
		if step.Fallback == nil {
			res.errorf(path+".fallback", step.Line, "on_error: fallback requires a fallback body")
		}
	default:
		res.errorf(path+".on_error", step.Line, "unknown value %q, want fail, continue or fallback", step.OnError)
	}

	if step.Retry != nil {
		validateRetry(step, path+".retry", res)
	}
	if step.Timeout < 0 {
		res.errorf(path+".timeout", step.Line, "must not be negative")
	}
}

func validateRetry(step *Step, path string, res *Result) {
	retry := step.Retry
	if retry.Attempts < 1 {
		res.errorf(path+".attempts", step.Line, "must be at least 1")
	}
	for i, class := range retry.On {
		if !errorClasses[class] {
			res.errorf(fmt.Sprintf("%s.on[%d]", path, i), step.Line, "unknown error class %q", class)
		}
		if class == "budget" || class == "config" || class == "policy" {
			res.errorf(fmt.Sprintf("%s.on[%d]", path, i), step.Line, "class %q is never retried", class)
		}
	}
	if retry.Backoff < 0 {
		res.errorf(path+".backoff", step.Line, "must not be negative")
	}
}

func joinKinds(kinds []Kind) string {
	names := make([]string, len(kinds))
	for i, kind := range kinds {
		names[i] = string(kind)
	}
	return strings.Join(names, ", ")
}

// validateExpr checks specification section 4, check 3: source must compile
// against the expression context and produce a boolean, which is what every
// caller of validateExpr needs (when: and condition: fields).
func validateExpr(path, source string, line int, res *Result) {
	if strings.TrimSpace(source) == "" {
		return
	}
	if _, err := expr.CompileBool(source); err != nil {
		res.errorf(path, line, "%s", err)
	}
	collectRefs(res, path, source, line, false)
}

// validateTemplate checks specification section 4, checks 4 and 7: text must
// parse as a template and must not reference secrets.
func validateTemplate(path, text string, line int, res *Result) {
	if text == "" {
		return
	}
	if err := tmpl.Check(path, text); err != nil {
		res.errorf(path, line, "%s", err)
	}
	collectRefs(res, path, text, line, true)
}

// sortedKeys returns map keys in a stable order so that diagnostics do not
// depend on map iteration.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
