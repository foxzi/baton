package main

import (
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/config"
	"github.com/foxzi/baton/internal/scenario"
)

// parseScenario parses a scenario literal, failing the test on a bad one.
func parseScenario(t *testing.T, yaml string) *scenario.Scenario {
	t.Helper()
	scn, err := scenario.Parse([]byte(yaml), "test.yaml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	return scn
}

const pricedScenario = `
version: 1
name: priced
steps:
  - id: ask
    llm:
      model: anthropic/claude-sonnet-4-6
      fallback_models: ["openai/gpt-5"]
      prompt: "hi"
      schema: s.json
  - id: each
    foreach:
      items: "{{ .inputs.repos }}"
      as: repo
      step:
        llm:
          model: openrouter/some/model
          prompt: "hi"
          schema: s.json
`

// TestPricingWarningsListsUnpricedModels checks that every model reference
// the scenario can call is reported once, nested bodies included, and that a
// priced model is not.
func TestPricingWarningsListsUnpricedModels(t *testing.T) {
	scn := parseScenario(t, pricedScenario)
	cfg := &config.Config{Pricing: map[string]config.Price{
		"anthropic/claude-sonnet-4-6": {InputPerMTok: 3, OutputPerMTok: 15},
	}}

	warnings := pricingWarnings(scn, cfg)
	if len(warnings) != 2 {
		t.Fatalf("warnings = %v, want 2", warnings)
	}
	for _, want := range []string{"openai/gpt-5", "openrouter/some/model"} {
		found := false
		for _, w := range warnings {
			if strings.Contains(w.Message, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("warnings = %v, want one about %s", warnings, want)
		}
	}
	if warnings[0].Line == 0 {
		t.Errorf("warnings[0].Line = 0, want the line of the step")
	}
}

// TestPricingWarningsUsesDefaultModel checks that a step without a model of
// its own is reported against defaults.model.
func TestPricingWarningsUsesDefaultModel(t *testing.T) {
	scn := parseScenario(t, `
version: 1
name: defaulted
steps:
  - id: ask
    llm:
      prompt: "hi"
      schema: s.json
`)
	cfg := &config.Config{Defaults: config.Defaults{Model: "openai/gpt-5"}}

	warnings := pricingWarnings(scn, cfg)
	if len(warnings) != 1 || !strings.Contains(warnings[0].Message, "openai/gpt-5") {
		t.Fatalf("warnings = %v, want one about openai/gpt-5", warnings)
	}
}

// TestPricingWarningsWithoutModel checks that a scenario with no model at all
// stays quiet: there is nothing to price.
func TestPricingWarningsWithoutModel(t *testing.T) {
	scn := parseScenario(t, `
version: 1
name: quiet
steps:
  - id: ask
    llm:
      prompt: "hi"
      schema: s.json
  - id: shell
    run:
      argv: ["true"]
`)
	if warnings := pricingWarnings(scn, &config.Config{}); len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	if warnings := pricingWarnings(scn, nil); len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
}
