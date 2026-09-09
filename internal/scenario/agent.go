package scenario

// AgentStep hands a prompt to an agent engine and waits for it to submit a
// result through the gateway (spec section 3.6).
type AgentStep struct {
	// Engine names the engine implementation: claude-code or codex, or fake
	// in tests (spec sections 8.2 and 8.4).
	Engine string `yaml:"engine"`

	// Model is the engine's own model name, not the <provider>/<model> of an
	// llm step: the engine talks to its provider itself.
	Model string `yaml:"model"`

	// Prompt is a file path or an inline template, System likewise.
	Prompt string            `yaml:"prompt"`
	System string            `yaml:"system"`
	With   map[string]string `yaml:"with"`

	// Skills are directories holding a SKILL.md, made visible to the engine
	// for the duration of the step (spec section 8.2).
	Skills []string `yaml:"skills"`

	// Profile is the starting tool policy; Tools and Limits override single
	// fields of it (spec sections 7.2 and 7.3).
	Profile Profile `yaml:"profile"`
	Tools   *Tools  `yaml:"tools"`
	Limits  *Limits `yaml:"limits"`

	MaxTurns  int     `yaml:"max_turns"`
	BudgetUSD float64 `yaml:"budget_usd"`

	// AllowUnsafe opens the MCP tools the gateway marks unsafe, the ones
	// taking a url, uri or headers parameter (spec section 7.6).
	AllowUnsafe bool `yaml:"allow_unsafe"`

	// Env are the environment variables the engine's process needs on top of
	// the minimal allow list (spec section 8.2).
	Env map[string]EnvValue `yaml:"env"`

	// InheritAuth reuses the credentials the CLI keeps in the user's home
	// directory instead of an API key from env; only codex supports it
	// (spec section 8.2).
	InheritAuth bool `yaml:"inherit_auth"`

	// Result is the path to the JSON Schema submit_result validates against.
	// It is required: a step ends successfully only through submit_result.
	Result string `yaml:"result"`

	// Script is the behaviour file of the fake engine (spec section 8.4) and
	// is rejected for any other engine.
	Script string `yaml:"script"`
}

// Profile is a named tool policy an agent step starts from.
type Profile string

// Profiles defined by the format (spec section 7.2).
const (
	ProfileUnset    Profile = ""
	ProfileReview   Profile = "review"
	ProfileFix      Profile = "fix"
	ProfileResearch Profile = "research"
)

// Tools overrides single fields of the step's profile (spec section 7.3).
// Every field is a pointer or a slice so that an absent field keeps what the
// profile says.
type Tools struct {
	FS    *FSTools    `yaml:"fs"`
	Git   *GitTools   `yaml:"git"`
	Exec  *ExecTools  `yaml:"exec"`
	APIs  []string    `yaml:"apis"`
	Fetch *FetchTools `yaml:"fetch"`
	MCP   []string    `yaml:"mcp"`
	State StateAccess `yaml:"state"`
}

// FSTools is the filesystem policy of an agent step.
type FSTools struct {
	Read *bool `yaml:"read"`

	// Write is none or workspace; there is no form that writes outside the
	// workspace.
	Write FSWrite `yaml:"write"`

	// Deny extends the default deny list rather than replacing it.
	Deny []string `yaml:"deny"`
}

// FSWrite is the write half of the filesystem policy.
type FSWrite string

// fs.write values (spec section 7.3).
const (
	FSWriteUnset     FSWrite = ""
	FSWriteNone      FSWrite = "none"
	FSWriteWorkspace FSWrite = "workspace"
)

// GitTools is the git policy of an agent step.
type GitTools struct {
	Read   *bool `yaml:"read"`
	Commit *bool `yaml:"commit"`
}

// ExecTools is the command policy of an agent step.
type ExecTools struct {
	Mode ExecMode `yaml:"mode"`

	// Commands is a subset of the scenario's commands; empty means all of
	// them when Mode allows commands at all.
	Commands []string `yaml:"commands"`
}

// ExecMode selects whether an agent may run commands.
type ExecMode string

// exec.mode values (spec section 7.3).
const (
	ExecModeUnset    ExecMode = ""
	ExecModeNone     ExecMode = "none"
	ExecModeCommands ExecMode = "commands"
)

// FetchTools is the fetch policy of an agent step (spec section 7.6).
type FetchTools struct {
	Allow    []string `yaml:"allow"`
	MaxBytes ByteSize `yaml:"max_bytes"`
	MaxCalls int      `yaml:"max_calls"`
}

// StateAccess is the access an agent step has to the scenario's state file.
type StateAccess string

// state values (spec section 7.3).
const (
	StateUnset     StateAccess = ""
	StateNone      StateAccess = "none"
	StateRead      StateAccess = "read"
	StateReadWrite StateAccess = "read-write"
)

// Limits caps how much an agent step may ask of the gateway (spec section
// 7.3).
type Limits struct {
	MaxToolCalls   int      `yaml:"max_tool_calls"`
	MaxResultBytes ByteSize `yaml:"max_result_bytes"`
}

// Command is a command the runner executes on behalf of an agent, never the
// agent's own process (spec section 7.5).
type Command struct {
	// Argv is the executable and its arguments; entries are templates over
	// the args of the call. There is no shell.
	Argv        []string `yaml:"argv"`
	Description string   `yaml:"description"`
	Timeout     Duration `yaml:"timeout"`

	// Args are the parameters the agent may pass, each constrained by a
	// pattern.
	Args map[string]CommandArg `yaml:"args"`

	Parse ParseMode `yaml:"parse"`

	// Readonly marks a command that has no side effects, which is what makes
	// it retryable.
	Readonly bool `yaml:"readonly"`

	// MaxCalls caps the calls per step; zero means the step's own limit.
	MaxCalls int `yaml:"max_calls"`

	// Env are the environment variables the command needs on top of the
	// minimal allow list.
	Env map[string]EnvValue `yaml:"env"`

	// Line is the line the entry starts on, for diagnostics.
	Line int `yaml:"-"`
}

// CommandArg is one parameter of a command.
type CommandArg struct {
	// Pattern is required: it is the only thing standing between a model and
	// the argument vector of a process.
	Pattern  string `yaml:"pattern"`
	Default  string `yaml:"default"`
	Required bool   `yaml:"required"`
}
