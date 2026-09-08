// Package config parses and validates the global baton configuration.
//
// The format is specified in docs/ru/spec.md, section 12: a config is looked
// up at two fixed locations and merged, local winning over user-wide. It
// carries the pieces a scenario also declares — apis, secrets, on_failure —
// so they apply to every scenario run without repeating them, plus the
// pieces that only make sense globally: notify channels, llm providers
// (section 8.3), a pricing table for cost estimation, and MCP servers for
// the agent gateway.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/foxzi/baton/internal/scenario"
	"gopkg.in/yaml.v3"
)

// Config is the global baton configuration (spec section 12).
type Config struct {
	// APIs are global packs, available to every scenario (spec section
	// 7.4.1). They reuse scenario.API verbatim.
	APIs map[string]scenario.API `yaml:"apis"`

	// Secrets are resolved the same way as a scenario's own secrets (spec
	// section 6), plus an optional flag providers may need (section 8.3).
	Secrets map[string]SecretRef `yaml:"secrets"`

	// Notify declares channels a notify step or on_failure entry can send
	// to: either the built-in webhook kind, or a pack implementing
	// notify/v1.
	Notify map[string]Channel `yaml:"notify"`

	// Providers configure the llm step's backends (spec section 8.3).
	Providers map[string]Provider `yaml:"providers"`

	// Defaults apply to steps that leave the corresponding field unset,
	// same as a scenario's own defaults (spec section 3).
	Defaults Defaults `yaml:"defaults"`

	// OnFailure runs when a scenario without its own on_failure fails.
	OnFailure []scenario.Step `yaml:"on_failure"`

	// Pricing estimates cost when a provider does not return one itself
	// (spec section 8.3), keyed by the full "<provider>/<model>" string
	// used in a scenario's llm step.
	Pricing map[string]Price `yaml:"pricing"`

	// MCPServers are made available to the agent gateway.
	MCPServers map[string]MCPServer `yaml:"mcp_servers"`
}

// Defaults are the global engine, model and budget applied when a scenario
// or step does not set its own (spec section 12).
type Defaults struct {
	Engine    string  `yaml:"engine"`
	Model     string  `yaml:"model"`
	BudgetUSD float64 `yaml:"budget_usd"`
}

// SecretRef declares where a secret value is read from. It mirrors
// scenario.Secret (spec section 6) field for field, plus Optional, which
// scenario.Secret does not have: section 8.3 shows a provider whose api_key
// is allowed to be missing (the "local" openai_compatible example). Adding
// the field here, rather than to scenario.Secret, keeps the scenario secret
// declaration — which is always required — unchanged.
type SecretRef struct {
	From     scenario.SecretSource `yaml:"from"`
	Key      string                `yaml:"key"`
	Path     string                `yaml:"path"`
	Trim     bool                  `yaml:"trim"`
	Optional bool                  `yaml:"optional"`
}

// Secret converts the reference to a scenario.Secret, for use with
// internal/secrets.Resolve. Optional has no equivalent there and is the
// caller's responsibility.
func (r SecretRef) Secret() scenario.Secret {
	return scenario.Secret{From: r.From, Key: r.Key, Path: r.Path, Trim: r.Trim}
}

// Channel is one notify destination (spec section 12). It has two forms: a
// built-in webhook (Kind + URL), or a pack operation (API + Target); see
// Validate for the rule that only one form may be used at a time.
type Channel struct {
	Kind   string     `yaml:"kind"`
	API    string     `yaml:"api"`
	Target string     `yaml:"target"`
	URL    *SecretRef `yaml:"url"`
	Secret string     `yaml:"secret"`
}

// Built-in Channel.Kind values (spec section 2, "плюс встроенные webhook и
// stdout"); any other non-empty value is rejected by Validate.
const (
	ChannelKindWebhook = "webhook"
	ChannelKindStdout  = "stdout"
)

// Provider configures one llm backend (spec section 8.3).
type Provider struct {
	Kind         ProviderKind  `yaml:"kind"`
	APIKey       *SecretRef    `yaml:"api_key"`
	BaseURL      string        `yaml:"base_url"`
	AppName      string        `yaml:"app_name"`
	Capabilities *Capabilities `yaml:"capabilities"`
}

// ProviderKind names a llm backend implementation (spec section 8.3).
type ProviderKind string

// Provider kinds defined by the specification.
const (
	ProviderAnthropic        ProviderKind = "anthropic"
	ProviderOpenAI           ProviderKind = "openai"
	ProviderOpenRouter       ProviderKind = "openrouter"
	ProviderOpenAICompatible ProviderKind = "openai_compatible"
)

// Capabilities overrides what Provider.Capabilities() would otherwise infer,
// for a backend that does not advertise itself (spec section 8.3). The
// fields are pointers so that "not set in the config" is distinguishable
// from "set to false".
type Capabilities struct {
	StructuredOutput *bool `yaml:"structured_output"`
	Tools            *bool `yaml:"tools"`
}

// Price estimates the cost of one model when a provider does not return a
// cost itself (spec section 8.3 and 12).
type Price struct {
	InputPerMTok  float64 `yaml:"input_per_mtok"`
	OutputPerMTok float64 `yaml:"output_per_mtok"`
}

// MCPServer is one MCP server made available to the agent gateway.
type MCPServer struct {
	Command []string          `yaml:"command"`
	Env     map[string]string `yaml:"env"`
}

// Parse parses config bytes. Path is only used for diagnostics. Unknown
// fields at any depth are rejected, the same mechanism as
// internal/scenario.Parse.
func Parse(data []byte, path string) (*Config, error) {
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)

	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &cfg, nil
}

// EnvConfigOverride is the environment variable that, when set, replaces the
// whole default search list of Load with the single file it names.
const EnvConfigOverride = "BATON_CONFIG"

// DefaultPaths returns the config locations Load searches when called
// without arguments, in the order of spec section 12: the user config
// directory, then the current directory. When os.UserConfigDir fails (no
// home directory available), that entry is simply omitted.
func DefaultPaths() []string {
	var paths []string
	if dir, err := os.UserConfigDir(); err == nil {
		paths = append(paths, filepath.Join(dir, "baton", "config.yaml"))
	}
	paths = append(paths, "baton.yaml")
	return paths
}

// Load reads and merges the config files at paths, in order, with a later
// file's values replacing an earlier file's per Merge. A missing file among
// paths is skipped, not an error.
//
// Called with no arguments, Load instead uses BATON_CONFIG when set — which
// replaces the whole list with that one required file, so a missing file is
// then an error — or DefaultPaths() otherwise.
func Load(paths ...string) (*Config, error) {
	required := false
	if len(paths) == 0 {
		if override := os.Getenv(EnvConfigOverride); override != "" {
			paths = []string{override}
			required = true
		} else {
			paths = DefaultPaths()
		}
	}

	cfg := &Config{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) && !required {
				continue
			}
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		parsed, err := Parse(data, path)
		if err != nil {
			return nil, err
		}
		cfg.Merge(parsed)
	}
	return cfg, nil
}

// Merge overlays other on top of c: every map merges by key, with other's
// value replacing c's for a key both declare; OnFailure is replaced wholesale
// when other declares any steps at all; Defaults fields are replaced one at
// a time, each when other sets it to a non-zero value. This is the rule
// spec section 12 uses for the two config files, and applies equally when a
// global config later merges with a scenario's own secrets/apis/on_failure.
func (c *Config) Merge(other *Config) {
	if other == nil {
		return
	}

	mergeMap(&c.APIs, other.APIs)
	mergeMap(&c.Secrets, other.Secrets)
	mergeMap(&c.Notify, other.Notify)
	mergeMap(&c.Providers, other.Providers)
	mergeMap(&c.Pricing, other.Pricing)
	mergeMap(&c.MCPServers, other.MCPServers)

	if len(other.OnFailure) > 0 {
		c.OnFailure = other.OnFailure
	}

	if other.Defaults.Engine != "" {
		c.Defaults.Engine = other.Defaults.Engine
	}
	if other.Defaults.Model != "" {
		c.Defaults.Model = other.Defaults.Model
	}
	if other.Defaults.BudgetUSD != 0 {
		c.Defaults.BudgetUSD = other.Defaults.BudgetUSD
	}
}

// mergeMap copies every entry of src into *dst, creating *dst if needed. An
// empty src leaves dst untouched, including a nil dst.
func mergeMap[K comparable, V any](dst *map[K]V, src map[K]V) {
	if len(src) == 0 {
		return
	}
	if *dst == nil {
		*dst = make(map[K]V, len(src))
	}
	for k, v := range src {
		(*dst)[k] = v
	}
}

// Price looks up the pricing entry for model, the full "<provider>/<model>"
// string a scenario's llm step uses (spec section 8.3).
func (c *Config) Price(model string) (Price, bool) {
	p, ok := c.Pricing[model]
	return p, ok
}

// sortedKeys returns the keys of m in ascending order, so that Validate
// reports problems in a stable order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Validate collects every problem in the config rather than stopping at the
// first one, matching the style of scenario.Validate (spec section 4).
func (c *Config) Validate() error {
	var errs []error

	for _, name := range sortedKeys(c.Providers) {
		p := c.Providers[name]
		path := fmt.Sprintf("providers.%s", name)
		errs = append(errs, validateProvider(path, p)...)
		if p.APIKey != nil {
			errs = append(errs, validateSecretRef(path+".api_key", *p.APIKey)...)
		}
	}

	for _, name := range sortedKeys(c.Notify) {
		ch := c.Notify[name]
		path := fmt.Sprintf("notify.%s", name)
		errs = append(errs, validateChannel(path, ch)...)
		if ch.URL != nil {
			errs = append(errs, validateSecretRef(path+".url", *ch.URL)...)
		}
	}

	for _, name := range sortedKeys(c.Pricing) {
		errs = append(errs, validatePrice(fmt.Sprintf("pricing.%s", name), c.Pricing[name])...)
	}

	for _, name := range sortedKeys(c.Secrets) {
		errs = append(errs, validateSecretRef(fmt.Sprintf("secrets.%s", name), c.Secrets[name])...)
	}

	for _, name := range sortedKeys(c.MCPServers) {
		errs = append(errs, validateMCPServer(name, c.MCPServers[name])...)
	}

	return errors.Join(errs...)
}

// providerKinds are the backends known to the llm step (spec section 8.3).
var providerKinds = map[ProviderKind]bool{
	ProviderAnthropic:        true,
	ProviderOpenAI:           true,
	ProviderOpenRouter:       true,
	ProviderOpenAICompatible: true,
}

func validateProvider(path string, p Provider) []error {
	var errs []error

	if !providerKinds[p.Kind] {
		return append(errs, fmt.Errorf("%s: unknown kind %q", path, p.Kind))
	}

	switch p.Kind {
	case ProviderAnthropic, ProviderOpenAI, ProviderOpenRouter:
		if p.APIKey == nil {
			errs = append(errs, fmt.Errorf("%s: kind %s requires api_key", path, p.Kind))
		}
	case ProviderOpenAICompatible:
		if p.BaseURL == "" {
			errs = append(errs, fmt.Errorf("%s: kind openai_compatible requires base_url", path))
		}
	}

	// openrouter has a fixed base_url built into the implementation (spec
	// section 8.3); a config entry may still set it explicitly, but not to
	// a blank value, which would silently fall back to no base_url at all.
	if p.Kind == ProviderOpenRouter && p.BaseURL != "" && strings.TrimSpace(p.BaseURL) == "" {
		errs = append(errs, fmt.Errorf("%s: base_url must not be blank", path))
	}

	return errs
}

func validateChannel(path string, ch Channel) []error {
	var errs []error

	builtinForm := ch.Kind != ""
	packForm := ch.API != "" || ch.Target != ""

	switch {
	case builtinForm && ch.Kind != ChannelKindWebhook && ch.Kind != ChannelKindStdout:
		errs = append(errs, fmt.Errorf("%s: unknown kind %q, want %q, %q or empty", path, ch.Kind, ChannelKindWebhook, ChannelKindStdout))
	case builtinForm && packForm:
		errs = append(errs, fmt.Errorf("%s: must not combine kind: %s with api/target", path, ch.Kind))
	case ch.Kind == ChannelKindWebhook:
		if ch.URL == nil {
			errs = append(errs, fmt.Errorf("%s: kind webhook requires url", path))
		}
	case ch.Kind == ChannelKindStdout:
		if ch.URL != nil {
			errs = append(errs, fmt.Errorf("%s: kind stdout takes no url", path))
		}
	case packForm:
		if ch.API == "" {
			errs = append(errs, fmt.Errorf("%s: requires api", path))
		}
		if ch.Target == "" {
			errs = append(errs, fmt.Errorf("%s: requires target", path))
		}
	default:
		errs = append(errs, fmt.Errorf("%s: must declare kind: webhook with url, kind: stdout, or api and target", path))
	}

	return errs
}

func validatePrice(path string, p Price) []error {
	var errs []error
	if p.InputPerMTok < 0 {
		errs = append(errs, fmt.Errorf("%s.input_per_mtok: must not be negative", path))
	}
	if p.OutputPerMTok < 0 {
		errs = append(errs, fmt.Errorf("%s.output_per_mtok: must not be negative", path))
	}
	return errs
}

// validateSecretRef checks a secret source, the same rule
// internal/scenario's validateSecrets applies to scenario.Secret: exactly
// one of from: env (with key) or from: file (with path).
func validateSecretRef(path string, s SecretRef) []error {
	var errs []error
	switch s.From {
	case scenario.SecretFromEnv:
		if s.Key == "" {
			errs = append(errs, fmt.Errorf("%s: from: env needs key", path))
		}
		if s.Path != "" {
			errs = append(errs, fmt.Errorf("%s: path applies to from: file only", path))
		}
	case scenario.SecretFromFile:
		if s.Path == "" {
			errs = append(errs, fmt.Errorf("%s: from: file needs path", path))
		}
		if s.Key != "" {
			errs = append(errs, fmt.Errorf("%s: key applies to from: env only", path))
		}
	case "":
		errs = append(errs, fmt.Errorf("%s: must declare from: env or from: file", path))
	default:
		errs = append(errs, fmt.Errorf("%s: unknown source %q", path, s.From))
	}
	return errs
}

func validateMCPServer(name string, m MCPServer) []error {
	if len(m.Command) == 0 || strings.TrimSpace(m.Command[0]) == "" {
		return []error{fmt.Errorf("mcp_servers.%s: command must not be empty", name)}
	}
	return nil
}
