package main

import (
	"fmt"

	"github.com/foxzi/baton/internal/config"
	"github.com/foxzi/baton/internal/scenario"
)

// pricingWarnings reports the models the scenario can call that carry no
// entry in the pricing table of the configuration. Such a model runs, but
// its cost stays null and the dollar budget is not checked for it (spec
// section 8.3).
func pricingWarnings(scn *scenario.Scenario, cfg *config.Config) []scenario.Diagnostic {
	if cfg == nil {
		return nil
	}
	seen := make(map[string]bool)
	var warnings []scenario.Diagnostic

	warn := func(step *scenario.Step, field, ref string) {
		if ref == "" || seen[ref] {
			return
		}
		seen[ref] = true
		if _, ok := cfg.Pricing[ref]; ok {
			return
		}
		warnings = append(warnings, scenario.Diagnostic{
			Path: "llm." + field,
			Line: step.Line,
			Message: fmt.Sprintf("model %s is not in pricing: cost_usd stays null and the dollar budget is not checked for it",
				ref),
		})
	}

	scenario.WalkSteps(scn, func(step *scenario.Step) {
		if step.LLM == nil {
			return
		}
		model := step.LLM.Model
		if model == "" {
			// An unset model falls back to defaults.model (section 12).
			model = cfg.Defaults.Model
		}
		warn(step, "model", model)
		for _, ref := range step.LLM.FallbackModels {
			warn(step, "fallback_models", ref)
		}
	})
	return warnings
}
