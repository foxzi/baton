// Package scenario parses and validates baton scenario files.
//
// The format is specified in docs/ru/spec.md, section 3, and the validation
// rules in section 4. The llm and foreach step bodies have typed models with
// full validation. Step kinds the runner does not execute yet (agent, until,
// switch, notify) are still parsed into a raw node so that a scenario using
// them loads and reports structural problems; their bodies gain typed models
// together with the code that runs them.
package scenario

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// Scenario is a parsed scenario file.
type Scenario struct {
	Version     int               `yaml:"version"`
	Name        string            `yaml:"name"`
	Description string            `yaml:"description"`
	Inputs      map[string]Input  `yaml:"inputs"`
	Defaults    Defaults          `yaml:"defaults"`
	Budget      Budget            `yaml:"budget"`
	Secrets     map[string]Secret `yaml:"secrets"`
	Steps       []Step            `yaml:"steps"`
	OnFailure   []Step            `yaml:"on_failure"`

	// APIs binds API packs to the names their operations are called under
	// (spec section 7.4.1).
	APIs map[string]API `yaml:"apis"`

	// Commands is accepted but not interpreted yet; it belongs to the agent
	// gateway (spec section 7.5).
	Commands *yaml.Node `yaml:"commands"`

	// Path is the file the scenario was read from. Prompt, schema and
	// template paths in the scenario resolve relative to its directory.
	Path string `yaml:"-"`
}

// Input is a declared scenario input (spec section 3.1).
type Input struct {
	Type     InputType `yaml:"type"`
	Required bool      `yaml:"required"`
	Default  any       `yaml:"default"`
	Pattern  string    `yaml:"pattern"`
}

// InputType is the value type of an input.
type InputType string

// Input types accepted by the scenario format.
const (
	TypeString InputType = "string"
	TypeInt    InputType = "int"
	TypeNumber InputType = "number"
	TypeBool   InputType = "bool"
	TypeList   InputType = "list"
	TypeMap    InputType = "map"
)

// Defaults are applied to steps that leave the corresponding field unset.
type Defaults struct {
	Engine    string   `yaml:"engine"`
	Model     string   `yaml:"model"`
	Timeout   Duration `yaml:"timeout"`
	BudgetUSD float64  `yaml:"budget_usd"`
}

// Budget caps the whole run.
type Budget struct {
	USD  float64  `yaml:"usd"`
	Time Duration `yaml:"time"`
}

// Secret declares where a secret value is read from (spec section 6).
type Secret struct {
	From SecretSource `yaml:"from"`
	Key  string       `yaml:"key"`
	Path string       `yaml:"path"`
	Trim bool         `yaml:"trim"`
}

// SecretSource is a secret provider.
type SecretSource string

// Secret sources supported in v1.
const (
	SecretFromEnv  SecretSource = "env"
	SecretFromFile SecretSource = "file"
)

// Step is one scenario step. Exactly one body field must be set; Kind reports
// which one after parsing.
type Step struct {
	ID        string   `yaml:"id"`
	When      string   `yaml:"when"`
	Needs     []string `yaml:"needs"`
	Timeout   Duration `yaml:"timeout"`
	Retry     *Retry   `yaml:"retry"`
	OnError   OnError  `yaml:"on_error"`
	Fallback  *Step    `yaml:"fallback"`
	Cache     *bool    `yaml:"cache"`
	DedupeKey string   `yaml:"dedupe_key"`

	Run     *RunStep     `yaml:"run"`
	HTTP    *HTTPStep    `yaml:"http"`
	Assert  *AssertStep  `yaml:"assert"`
	LLM     *LLMStep     `yaml:"llm"`
	Foreach *ForeachStep `yaml:"foreach"`

	// Bodies of step kinds the runner does not execute yet. These are kept
	// as raw nodes; yaml.v3 only decodes into a yaml.Node value, never into
	// a *yaml.Node, so presence is reported by IsZero rather than by nil.
	Agent   yaml.Node `yaml:"agent"`
	Until   yaml.Node `yaml:"until"`
	Switch  string    `yaml:"switch"`
	Cases   yaml.Node `yaml:"cases"`
	Default yaml.Node `yaml:"default"`
	Notify  yaml.Node `yaml:"notify"`
	Message string    `yaml:"message"`

	// Line is the line the step starts on, for diagnostics.
	Line int `yaml:"-"`
}

// Kind names a step body.
type Kind string

// Step kinds defined by the scenario format.
const (
	KindNone    Kind = ""
	KindRun     Kind = "run"
	KindAssert  Kind = "assert"
	KindHTTP    Kind = "http"
	KindLLM     Kind = "llm"
	KindAgent   Kind = "agent"
	KindForeach Kind = "foreach"
	KindUntil   Kind = "until"
	KindSwitch  Kind = "switch"
	KindNotify  Kind = "notify"
)

// Kinds returns every body present on the step, in format order.
func (s *Step) Kinds() []Kind {
	var kinds []Kind
	if s.Run != nil {
		kinds = append(kinds, KindRun)
	}
	if s.Assert != nil {
		kinds = append(kinds, KindAssert)
	}
	if s.HTTP != nil {
		kinds = append(kinds, KindHTTP)
	}
	if s.LLM != nil {
		kinds = append(kinds, KindLLM)
	}
	if !s.Agent.IsZero() {
		kinds = append(kinds, KindAgent)
	}
	if s.Foreach != nil {
		kinds = append(kinds, KindForeach)
	}
	if !s.Until.IsZero() {
		kinds = append(kinds, KindUntil)
	}
	if s.Switch != "" {
		kinds = append(kinds, KindSwitch)
	}
	if !s.Notify.IsZero() {
		kinds = append(kinds, KindNotify)
	}
	return kinds
}

// Kind returns the single body of the step, or KindNone when the step has no
// body or more than one.
func (s *Step) Kind() Kind {
	if kinds := s.Kinds(); len(kinds) == 1 {
		return kinds[0]
	}
	return KindNone
}

// Retry configures automatic retries of a step (spec section 3.2).
type Retry struct {
	On       []string `yaml:"on"`
	Attempts int      `yaml:"attempts"`
	Backoff  Duration `yaml:"backoff"`
}

// OnError selects what happens when a step fails.
type OnError string

// on_error values (spec section 9.2).
const (
	OnErrorUnset    OnError = ""
	OnErrorFail     OnError = "fail"
	OnErrorContinue OnError = "continue"
	OnErrorFallback OnError = "fallback"
)

// RunStep executes a command without a shell (spec section 3.3).
type RunStep struct {
	Argv           []string            `yaml:"argv"`
	Cwd            string              `yaml:"cwd"`
	Env            map[string]EnvValue `yaml:"env"`
	Stdin          string              `yaml:"stdin"`
	Parse          ParseMode           `yaml:"parse"`
	AllowExitCodes []int               `yaml:"allow_exit_codes"`
	MaxOutputBytes ByteSize            `yaml:"max_output_bytes"`

	// Readonly marks a command without side effects, which makes the step
	// cacheable and retryable. Spec sections 9.4 and 10.3 rely on the field
	// while section 3.3 omits it; see docs/ru/spec-review.md, finding 5.
	Readonly bool `yaml:"readonly"`

	// fromString records that the body was written as a bare command string
	// and split by baton, which validation reports as a warning.
	fromString bool
}

// ParseMode selects how step output is turned into a result value.
type ParseMode string

// parse values accepted by run and http steps.
const (
	ParseUnset ParseMode = ""
	ParseText  ParseMode = "text"
	ParseJSON  ParseMode = "json"
	ParseLines ParseMode = "lines"
)

// EnvValue is an environment entry: either a literal or a secret reference.
type EnvValue struct {
	Value  string
	Secret string
}

// API is an apis entry: a pack, where to load it from, and the configuration
// and authorisation it is bound to (spec section 7.4.1).
type API struct {
	// Interface names the interface the pack must implement, such as
	// forge/v1. Checking it is part of the pack milestone.
	Interface string `yaml:"interface"`

	// Pack is the pack name and may be a template, so that a scenario can
	// pick gitlab or github from an input.
	Pack string `yaml:"pack"`

	// From is the pack source: a local directory in v1.
	From string `yaml:"from"`

	// SHA256 pins remote sources and is unused for local directories.
	SHA256 string `yaml:"sha256"`

	Config  map[string]string `yaml:"config"`
	Auth    APIAuth           `yaml:"auth"`
	Timeout Duration          `yaml:"timeout"`

	// Line is the line the entry starts on, for diagnostics.
	Line int `yaml:"-"`
}

// APIAuth names the secret that feeds the authorisation scheme of the pack.
type APIAuth struct {
	Secret string `yaml:"secret"`
}

// HTTPStep calls an API (spec section 3.4). It has two forms: an operation of
// a pack, or a raw request. Op belongs to the first form, Method and the
// fields after it to the second, and API to both.
type HTTPStep struct {
	Op   string         `yaml:"op"`
	Args map[string]any `yaml:"args"`

	// API names the apis entry to call. The operation form takes it from the
	// <api>.<op> prefix of Op instead.
	API string `yaml:"api"`

	// Auth overrides the secret declared in the apis entry, which lets one
	// step write with a token that the read-only steps do not carry.
	Auth string `yaml:"auth"`

	Method       string            `yaml:"method"`
	URL          string            `yaml:"url"`
	Path         string            `yaml:"path"`
	Headers      map[string]string `yaml:"headers"`
	Query        map[string]string `yaml:"query"`
	Body         string            `yaml:"body"`
	ExpectStatus []int             `yaml:"expect_status"`
	Parse        ParseMode         `yaml:"parse"`
	MaxBytes     ByteSize          `yaml:"max_bytes"`
}

// APIName returns the apis entry the step calls and, for the operation form,
// the operation name.
func (h *HTTPStep) APIName() (api, op string) {
	if h.Op == "" {
		return h.API, ""
	}
	name, operation, found := strings.Cut(h.Op, ".")
	if !found {
		return "", h.Op
	}
	return name, operation
}

// Raw reports whether the step is the raw-request form.
func (h *HTTPStep) Raw() bool { return h.Op == "" }

// AssertStep stops the run when its condition is false (spec section 3.9).
type AssertStep struct {
	Condition string `yaml:"condition"`
	Message   string `yaml:"message"`
}

// LLMStep calls a language model provider (spec section 3.5).
type LLMStep struct {
	// Model is <provider>/<model>; provider names a providers entry (spec
	// section 8.3). The first slash is the separator, so an OpenRouter model
	// that itself contains a slash still works.
	Model string `yaml:"model"`

	// FallbackModels are tried in order on a transient provider error. A
	// schema error retries the same model instead (spec section 3.5).
	FallbackModels []string `yaml:"fallback_models"`

	// System and Prompt are a file path or an inline template.
	System string            `yaml:"system"`
	Prompt string            `yaml:"prompt"`
	With   map[string]string `yaml:"with"`

	// Schema is the path to the JSON Schema the result must validate
	// against. It is required (spec section 3.5).
	Schema string `yaml:"schema"`

	// Tools are operation references the runner exposes to the model in its
	// tool loop.
	Tools []string `yaml:"tools"`

	MaxTokens   int      `yaml:"max_tokens"`
	Temperature *float64 `yaml:"temperature"`

	// StructuredMode overrides the automatic structured-output level chosen
	// from the provider's Capabilities() (spec section 8.3).
	StructuredMode StructuredMode `yaml:"structured_mode"`
}

// StructuredMode is a structured-output strategy for an llm step.
type StructuredMode string

// Structured-output modes, in descending order of provider support (spec
// section 8.3).
const (
	StructuredModeUnset StructuredMode = ""
	StructuredNative    StructuredMode = "native"
	StructuredTool      StructuredMode = "tool"
	StructuredPrompt    StructuredMode = "prompt"
)

// ForeachStep runs its body once per item of a list (spec section 3.7).
type ForeachStep struct {
	// Items is a template that evaluates to the list to iterate.
	Items string `yaml:"items"`

	// As names the current item in the body's templates.
	As string `yaml:"as"`

	MaxParallel int           `yaml:"max_parallel"`
	OnItemError ItemErrorMode `yaml:"on_item_error"`
	MinSuccess  *float64      `yaml:"min_success"`

	// Step is the per-item body. It has no id of its own; it is not a step
	// in the scenario's own DAG.
	Step *Step `yaml:"step"`
}

// ItemErrorMode selects what happens when one foreach item fails.
type ItemErrorMode string

// on_item_error values (spec section 3.7).
const (
	ItemErrorUnset    ItemErrorMode = ""
	ItemErrorFail     ItemErrorMode = "fail"
	ItemErrorContinue ItemErrorMode = "continue"
)
