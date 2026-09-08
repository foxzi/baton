package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/scenario"
)

// specExample is the exact config example of docs/ru/spec.md section 12.
const specExample = `
apis:
  telegram:
    pack: telegram
    from: github.com/org/baton-apis@v1.3.0
    sha256: "..."
    auth: { secret: tg_bot }
secrets:
  tg_bot: { from: env, key: TELEGRAM_BOT_TOKEN }
notify:
  telegram:
    api: telegram
    target: "-100123"
  ops:
    kind: webhook
    url: { from: env, key: SLACK_OPS_WEBHOOK }
providers:
  anthropic:  { kind: anthropic,  api_key: { from: env, key: ANTHROPIC_API_KEY } }
  openai:     { kind: openai,     api_key: { from: env, key: OPENAI_API_KEY } }
  openrouter: { kind: openrouter, api_key: { from: env, key: OPENROUTER_API_KEY } }
defaults:
  engine: claude-code
  model: anthropic/claude-sonnet-4-6
  budget_usd: 3
on_failure:
  - notify: telegram
    message: "{{ .run.name }} upal: {{ .run.error.class }} na {{ .run.failed_step }}"
pricing:
  anthropic/claude-sonnet-4-6: { input_per_mtok: 3,    output_per_mtok: 15 }
  anthropic/claude-haiku-4-5:  { input_per_mtok: 0.8,  output_per_mtok: 4 }
  openai/gpt-4.1-mini:         { input_per_mtok: 0.4,  output_per_mtok: 1.6 }
mcp_servers:
  context7: { command: ["npx", "-y", "@upstash/context7-mcp"], env: {} }
`

// TestParseSpecExample checks that the exact config example of spec section
// 12 parses and produces the expected values.
func TestParseSpecExample(t *testing.T) {
	cfg, err := Parse([]byte(specExample), "config.yaml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	api, ok := cfg.APIs["telegram"]
	if !ok {
		t.Fatalf("APIs[telegram] missing")
	}
	if api.Pack != "telegram" || api.From != "github.com/org/baton-apis@v1.3.0" {
		t.Errorf("APIs[telegram] = %+v, unexpected", api)
	}
	if api.Auth.Secret != "tg_bot" {
		t.Errorf("APIs[telegram].Auth.Secret = %q, want tg_bot", api.Auth.Secret)
	}

	secret, ok := cfg.Secrets["tg_bot"]
	if !ok || secret.Key != "TELEGRAM_BOT_TOKEN" {
		t.Errorf("Secrets[tg_bot] = %+v, ok=%v", secret, ok)
	}

	tg, ok := cfg.Notify["telegram"]
	if !ok || tg.API != "telegram" || tg.Target != "-100123" {
		t.Errorf("Notify[telegram] = %+v, ok=%v", tg, ok)
	}
	ops, ok := cfg.Notify["ops"]
	if !ok || ops.Kind != "webhook" || ops.URL == nil || ops.URL.Key != "SLACK_OPS_WEBHOOK" {
		t.Errorf("Notify[ops] = %+v, ok=%v", ops, ok)
	}

	anthropic, ok := cfg.Providers["anthropic"]
	if !ok || anthropic.Kind != ProviderAnthropic || anthropic.APIKey == nil || anthropic.APIKey.Key != "ANTHROPIC_API_KEY" {
		t.Errorf("Providers[anthropic] = %+v, ok=%v", anthropic, ok)
	}
	if _, ok := cfg.Providers["openai"]; !ok {
		t.Errorf("Providers[openai] missing")
	}
	if _, ok := cfg.Providers["openrouter"]; !ok {
		t.Errorf("Providers[openrouter] missing")
	}

	if cfg.Defaults.Engine != "claude-code" || cfg.Defaults.Model != "anthropic/claude-sonnet-4-6" || cfg.Defaults.BudgetUSD != 3 {
		t.Errorf("Defaults = %+v, unexpected", cfg.Defaults)
	}

	if len(cfg.OnFailure) != 1 || cfg.OnFailure[0].Message == "" {
		t.Fatalf("OnFailure = %+v, want one step with a message", cfg.OnFailure)
	}

	price, ok := cfg.Pricing["anthropic/claude-sonnet-4-6"]
	if !ok || price.InputPerMTok != 3 || price.OutputPerMTok != 15 {
		t.Errorf("Pricing[anthropic/claude-sonnet-4-6] = %+v, ok=%v", price, ok)
	}

	mcp, ok := cfg.MCPServers["context7"]
	if !ok || len(mcp.Command) != 3 || mcp.Command[0] != "npx" {
		t.Errorf("MCPServers[context7] = %+v, ok=%v", mcp, ok)
	}
}

// TestParseLocalProviderOptionalKey checks the openai_compatible example
// with optional: true on api_key.
func TestParseLocalProviderOptionalKey(t *testing.T) {
	data := `
providers:
  local:
    kind: openai_compatible
    base_url: http://ollama:11434/v1
    api_key: { from: env, key: OLLAMA_KEY, optional: true }
    capabilities: { structured_output: false, tools: true }
`
	cfg, err := Parse([]byte(data), "config.yaml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	local, ok := cfg.Providers["local"]
	if !ok {
		t.Fatalf("Providers[local] missing")
	}
	if local.APIKey == nil || !local.APIKey.Optional {
		t.Fatalf("Providers[local].APIKey = %+v, want Optional true", local.APIKey)
	}
	if local.Capabilities == nil || local.Capabilities.StructuredOutput == nil || *local.Capabilities.StructuredOutput {
		t.Errorf("Capabilities.StructuredOutput = %+v, want false", local.Capabilities)
	}
	if local.Capabilities.Tools == nil || !*local.Capabilities.Tools {
		t.Errorf("Capabilities.Tools = %+v, want true", local.Capabilities.Tools)
	}
}

// TestParseUnknownField checks that an unknown top-level field is rejected.
func TestParseUnknownField(t *testing.T) {
	data := `
bogus: 1
`
	_, err := Parse([]byte(data), "config.yaml")
	if err == nil {
		t.Fatalf("Parse() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("Parse() error = %q, want mention of bogus", err)
	}
}

// TestParseUnknownNestedField checks that an unknown field nested inside a
// map entry is rejected too, proving KnownFields cascades.
func TestParseUnknownNestedField(t *testing.T) {
	data := `
providers:
  anthropic:
    kind: anthropic
    bogus: 1
`
	_, err := Parse([]byte(data), "config.yaml")
	if err == nil {
		t.Fatalf("Parse() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("Parse() error = %q, want mention of bogus", err)
	}
}

// TestParseErrorHasPath checks that the file path given to Parse appears in
// the error.
func TestParseErrorHasPath(t *testing.T) {
	_, err := Parse([]byte("bogus: 1\n"), "/some/config.yaml")
	if err == nil {
		t.Fatalf("Parse() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "/some/config.yaml") {
		t.Errorf("Parse() error = %q, want mention of path", err)
	}
}

// TestLoadMergesAndTeratesMissing checks that Load merges an ordered list of
// explicit paths, a later file winning, tolerating a path that does not
// exist.
func TestLoadMergesAndTeratesMissing(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.yaml")
	second := filepath.Join(dir, "second.yaml")
	missing := filepath.Join(dir, "missing.yaml")

	if err := os.WriteFile(first, []byte(`
defaults:
  engine: claude-code
  model: anthropic/claude-haiku-4-5
secrets:
  tg_bot: { from: env, key: TELEGRAM_BOT_TOKEN }
`), 0o644); err != nil {
		t.Fatalf("WriteFile(first) error = %v", err)
	}
	if err := os.WriteFile(second, []byte(`
defaults:
  model: anthropic/claude-sonnet-4-6
secrets:
  slack: { from: env, key: SLACK_TOKEN }
`), 0o644); err != nil {
		t.Fatalf("WriteFile(second) error = %v", err)
	}

	cfg, err := Load(first, missing, second)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Defaults.Engine != "claude-code" {
		t.Errorf("Defaults.Engine = %q, want claude-code (kept from first)", cfg.Defaults.Engine)
	}
	if cfg.Defaults.Model != "anthropic/claude-sonnet-4-6" {
		t.Errorf("Defaults.Model = %q, want anthropic/claude-sonnet-4-6 (from second)", cfg.Defaults.Model)
	}
	if len(cfg.Secrets) != 2 {
		t.Fatalf("Secrets = %+v, want 2 entries merged from both files", cfg.Secrets)
	}
}

// TestLoadNoArgsMissingDefaultsIsNotError checks that Load(), falling back
// to DefaultPaths, tolerates every default path missing.
func TestLoadNoArgsMissingDefaultsIsNotError(t *testing.T) {
	dir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	defer os.Chdir(cwd)
	t.Setenv(EnvConfigOverride, "")
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Providers) != 0 {
		t.Errorf("Providers = %+v, want empty", cfg.Providers)
	}
}

// TestLoadEnvOverride checks that BATON_CONFIG replaces the default search
// list with the one path it names.
func TestLoadEnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.yaml")
	if err := os.WriteFile(path, []byte(`
defaults:
  engine: claude-code
`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv(EnvConfigOverride, path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Defaults.Engine != "claude-code" {
		t.Errorf("Defaults.Engine = %q, want claude-code", cfg.Defaults.Engine)
	}
}

// TestLoadEnvOverrideMissingIsError checks that a BATON_CONFIG path that
// does not exist is an error, unlike a missing default path.
func TestLoadEnvOverrideMissingIsError(t *testing.T) {
	t.Setenv(EnvConfigOverride, filepath.Join(t.TempDir(), "does-not-exist.yaml"))

	_, err := Load()
	if err == nil {
		t.Fatalf("Load() error = nil, want error for missing BATON_CONFIG file")
	}
}

// TestValidateGoodConfig checks that the spec example config validates
// clean.
func TestValidateGoodConfig(t *testing.T) {
	cfg, err := Parse([]byte(specExample), "config.yaml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}
}

// TestValidateProviderUnknownKind checks that an unknown provider kind is
// reported.
func TestValidateProviderUnknownKind(t *testing.T) {
	cfg := &Config{Providers: map[string]Provider{
		"x": {Kind: "made-up"},
	}}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "unknown kind") {
		t.Errorf("Validate() error = %v, want mention of unknown kind", err)
	}
}

// TestValidateProviderMissingAPIKey checks that anthropic/openai/openrouter
// require an api_key.
func TestValidateProviderMissingAPIKey(t *testing.T) {
	for _, kind := range []ProviderKind{ProviderAnthropic, ProviderOpenAI, ProviderOpenRouter} {
		cfg := &Config{Providers: map[string]Provider{"x": {Kind: kind}}}
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), "requires api_key") {
			t.Errorf("Validate() for kind %s error = %v, want mention of requires api_key", kind, err)
		}
	}
}

// TestValidateOpenAICompatibleMissingBaseURL checks that openai_compatible
// requires base_url.
func TestValidateOpenAICompatibleMissingBaseURL(t *testing.T) {
	cfg := &Config{Providers: map[string]Provider{
		"local": {Kind: ProviderOpenAICompatible},
	}}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "requires base_url") {
		t.Errorf("Validate() error = %v, want mention of requires base_url", err)
	}
}

// TestValidateOpenRouterBlankBaseURL checks that openrouter rejects a
// whitespace-only base_url override.
func TestValidateOpenRouterBlankBaseURL(t *testing.T) {
	cfg := &Config{Providers: map[string]Provider{
		"openrouter": {
			Kind:    ProviderOpenRouter,
			APIKey:  &SecretRef{From: scenario.SecretFromEnv, Key: "K"},
			BaseURL: "   ",
		},
	}}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "base_url must not be blank") {
		t.Errorf("Validate() error = %v, want mention of blank base_url", err)
	}
}

// TestValidateChannelBothForms checks that a notify channel combining
// kind: webhook with api/target is rejected.
func TestValidateChannelBothForms(t *testing.T) {
	cfg := &Config{Notify: map[string]Channel{
		"x": {Kind: "webhook", API: "telegram", Target: "-1", URL: &SecretRef{From: scenario.SecretFromEnv, Key: "K"}},
	}}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "must not combine") {
		t.Errorf("Validate() error = %v, want mention of must not combine", err)
	}
}

// TestValidateChannelNeitherForm checks that a notify channel declaring
// neither form is rejected.
func TestValidateChannelNeitherForm(t *testing.T) {
	cfg := &Config{Notify: map[string]Channel{"x": {}}}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "must declare kind: webhook") {
		t.Errorf("Validate() error = %v, want mention of must declare", err)
	}
}

// TestValidateChannelWebhookMissingURL checks that kind: webhook without a
// url is rejected.
func TestValidateChannelWebhookMissingURL(t *testing.T) {
	cfg := &Config{Notify: map[string]Channel{"x": {Kind: "webhook"}}}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "requires url") {
		t.Errorf("Validate() error = %v, want mention of requires url", err)
	}
}

// TestValidateChannelStdout checks that kind: stdout needs nothing else and
// takes no url.
func TestValidateChannelStdout(t *testing.T) {
	cfg := &Config{Notify: map[string]Channel{"x": {Kind: ChannelKindStdout}}}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}

	cfg = &Config{Notify: map[string]Channel{
		"x": {Kind: ChannelKindStdout, URL: &SecretRef{From: scenario.SecretFromEnv, Key: "K"}},
	}}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "takes no url") {
		t.Errorf("Validate() error = %v, want mention of takes no url", err)
	}
}

// TestValidateChannelPackMissingTarget checks that the api/target form needs
// both fields.
func TestValidateChannelPackMissingTarget(t *testing.T) {
	cfg := &Config{Notify: map[string]Channel{"x": {API: "telegram"}}}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "requires target") {
		t.Errorf("Validate() error = %v, want mention of requires target", err)
	}
}

// TestValidatePricingNegative checks that a negative rate is rejected.
func TestValidatePricingNegative(t *testing.T) {
	cfg := &Config{Pricing: map[string]Price{
		"anthropic/claude-sonnet-4-6": {InputPerMTok: -1, OutputPerMTok: 15},
	}}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "must not be negative") {
		t.Errorf("Validate() error = %v, want mention of must not be negative", err)
	}
}

// TestValidateSecretUnsupportedSource checks that a secret without a known
// source is rejected, same rule as internal/scenario.
func TestValidateSecretUnsupportedSource(t *testing.T) {
	cfg := &Config{Secrets: map[string]SecretRef{"x": {}}}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "must declare from") {
		t.Errorf("Validate() error = %v, want mention of must declare from", err)
	}
}

// TestParseMCPServerEnv checks that an mcp_servers env takes both forms of an
// environment entry: a literal and a secret reference.
func TestParseMCPServerEnv(t *testing.T) {
	data := `
mcp_servers:
  ctx:
    command: ["npx", "-y", "@upstash/context7-mcp"]
    env:
      LOG_LEVEL: debug
      API_KEY: { secret: ctx_key }
`
	cfg, err := Parse([]byte(data), "config.yaml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	env := cfg.MCPServers["ctx"].Env
	if env["LOG_LEVEL"].Value != "debug" || env["LOG_LEVEL"].Secret != "" {
		t.Errorf("env[LOG_LEVEL] = %+v, want the literal debug", env["LOG_LEVEL"])
	}
	if env["API_KEY"].Secret != "ctx_key" || env["API_KEY"].Value != "" {
		t.Errorf("env[API_KEY] = %+v, want the secret ctx_key", env["API_KEY"])
	}
}

// TestValidateMCPServerEmptyCommand checks that an mcp_servers entry needs a
// non-empty command.
func TestValidateMCPServerEmptyCommand(t *testing.T) {
	cfg := &Config{MCPServers: map[string]MCPServer{"x": {}}}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "command must not be empty") {
		t.Errorf("Validate() error = %v, want mention of command must not be empty", err)
	}
}

// TestValidateReportsSeveralAtOnce checks that Validate joins every problem
// instead of stopping at the first.
func TestValidateReportsSeveralAtOnce(t *testing.T) {
	cfg := &Config{
		Providers: map[string]Provider{"x": {Kind: "made-up"}},
		Notify:    map[string]Channel{"y": {}},
		Pricing:   map[string]Price{"z": {InputPerMTok: -1}},
		Secrets:   map[string]SecretRef{"w": {}},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatalf("Validate() error = nil, want error")
	}
	for _, want := range []string{"unknown kind", "must declare kind: webhook", "must not be negative", "must declare from"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Validate() error = %v, missing mention of %q", err, want)
		}
	}
}

// TestPrice checks the pricing lookup helper.
func TestPrice(t *testing.T) {
	cfg := &Config{Pricing: map[string]Price{
		"anthropic/claude-sonnet-4-6": {InputPerMTok: 3, OutputPerMTok: 15},
	}}
	if p, ok := cfg.Price("anthropic/claude-sonnet-4-6"); !ok || p.InputPerMTok != 3 {
		t.Errorf("Price() = %+v, ok=%v, want InputPerMTok 3", p, ok)
	}
	if _, ok := cfg.Price("does/not-exist"); ok {
		t.Errorf("Price() ok = true, want false for unknown model")
	}
}

// TestValidateChannelPackUnknownAPI checks that a channel may only send
// through an apis entry the configuration itself declares: a scenario's own
// apis are not visible to a channel (section 12).
func TestValidateChannelPackUnknownAPI(t *testing.T) {
	cfg := &Config{Notify: map[string]Channel{
		"x": {API: "telegram", Target: "-100123"},
	}}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "not declared in apis") {
		t.Errorf("Validate() error = %v, want mention of not declared in apis", err)
	}

	cfg.APIs = map[string]scenario.API{"telegram": {Pack: "telegram", From: "./apis/"}}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil once the entry exists", err)
	}
}
