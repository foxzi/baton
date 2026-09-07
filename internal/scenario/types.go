// Package scenario parses and validates baton scenario files.
//
// The format is specified in docs/ru/spec.md, section 3, and the validation
// rules in section 4. Step kinds that the runner does not execute yet (http,
// llm, agent, foreach, until, switch) are parsed into a raw node so that a
// scenario using them still loads and reports structural problems; their
// bodies gain typed models together with the code that runs them.
package scenario

import "gopkg.in/yaml.v3"

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

	// APIs and Commands are accepted but not interpreted yet; they belong to
	// the pack loader (spec sections 7.4 and 7.5).
	APIs     *yaml.Node `yaml:"apis"`
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

	Run    *RunStep    `yaml:"run"`
	Assert *AssertStep `yaml:"assert"`

	// Bodies of step kinds the runner does not execute yet.
	HTTP    *yaml.Node `yaml:"http"`
	LLM     *yaml.Node `yaml:"llm"`
	Agent   *yaml.Node `yaml:"agent"`
	Foreach *yaml.Node `yaml:"foreach"`
	Until   *yaml.Node `yaml:"until"`
	Switch  *yaml.Node `yaml:"switch"`
	Cases   *yaml.Node `yaml:"cases"`
	Default *yaml.Node `yaml:"default"`
	Notify  *yaml.Node `yaml:"notify"`
	Message string     `yaml:"message"`

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
	if s.Agent != nil {
		kinds = append(kinds, KindAgent)
	}
	if s.Foreach != nil {
		kinds = append(kinds, KindForeach)
	}
	if s.Until != nil {
		kinds = append(kinds, KindUntil)
	}
	if s.Switch != nil {
		kinds = append(kinds, KindSwitch)
	}
	if s.Notify != nil {
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

// AssertStep stops the run when its condition is false (spec section 3.9).
type AssertStep struct {
	Condition string `yaml:"condition"`
	Message   string `yaml:"message"`
}
