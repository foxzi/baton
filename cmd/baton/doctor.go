package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/foxzi/baton/internal/config"
	"github.com/foxzi/baton/internal/exitcode"
	"github.com/foxzi/baton/internal/scenario"
)

const doctorUsage = `Usage: baton doctor <scenario.yaml>

Checks a scenario and its environment without running any step or making a
network call:

  - loads and validates the scenario, the same checks as ` + "`baton validate`" + `
  - lists the global configuration file(s) baton would search for (per
    BATON_CONFIG or the default search path) and whether each exists
  - for every llm step's model and fallback_models, whether the provider is
    declared in the configuration and whether its api_key environment
    variable is set (the value itself is never read or printed); a step
    with no model falls back to the scenario's then the configuration's
    defaults.model, the same order the engine itself uses
  - for every llm step's schema, whether the file exists
  - for every llm/agent step's prompt and system, whether it resolves to an
    existing local file, for information only, since a value that is not a
    file is a valid inline template

Not checked, because they need the network: whether an api key is actually
valid, whether a provider or a pack's base_url is reachable, and whether a
notify channel accepts a request.
`

// doctorCmd implements `baton doctor`.
func doctorCmd(args []string) int {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { fmt.Fprint(os.Stderr, doctorUsage) }
	if err := flags.Parse(args); err != nil {
		return flagsExitCode(err)
	}
	if flags.NArg() != 1 {
		fmt.Fprint(os.Stderr, doctorUsage)
		return exitcode.Config
	}

	path := flags.Arg(0)
	ok := true

	fmt.Printf("scenario: %s\n", path)
	scn, err := scenario.Load(path)
	if err != nil {
		fmt.Printf("  FAIL load: %v\n", err)
		fmt.Println()
		fmt.Println(doctorNotChecked)
		return exitcode.Config
	}
	fmt.Println("  OK load")

	result := scenario.Validate(scn)
	if result.OK() {
		packResult := scenario.CheckPacks(scn)
		result.Errors = append(result.Errors, packResult.Errors...)
		result.Warnings = append(result.Warnings, packResult.Warnings...)
	}
	for _, w := range result.Warnings {
		fmt.Printf("  WARN validate: %s\n", w)
	}
	if result.OK() {
		fmt.Println("  OK validate")
	} else {
		ok = false
		for _, e := range result.Errors {
			fmt.Printf("  FAIL validate: %s\n", e)
		}
	}

	cfg, cfgOK := doctorConfig()
	if !cfgOK {
		ok = false
	}

	baseDir := filepath.Dir(path)
	if !doctorSteps(scn, baseDir, cfg) {
		ok = false
	}

	fmt.Println()
	fmt.Println(doctorNotChecked)

	if !ok {
		return exitcode.Config
	}
	return exitcode.OK
}

const doctorNotChecked = "not checked: api key validity, network reachability of any provider or pack, notify channel delivery."

// doctorConfig reports the configuration files `baton run` would load, in
// the same order and with the same BATON_CONFIG override that
// config.Load() uses, then loads and validates the merged result. A missing
// file is not itself a failure — config.Load treats it the same way — but a
// parse or validation error is.
func doctorConfig() (*config.Config, bool) {
	fmt.Println("config:")
	ok := true

	if override := os.Getenv(config.EnvConfigOverride); override != "" {
		fmt.Printf("  %s=%s\n", config.EnvConfigOverride, override)
		printPathStatus(override)
	} else {
		for _, p := range config.DefaultPaths() {
			printPathStatus(p)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Printf("  FAIL load: %v\n", err)
		return nil, false
	}
	if err := cfg.Validate(); err != nil {
		fmt.Printf("  FAIL validate: %v\n", err)
		ok = false
	}
	return cfg, ok
}

func printPathStatus(path string) {
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		fmt.Printf("  FOUND %s\n", path)
	} else {
		fmt.Printf("  MISSING %s\n", path)
	}
}

// doctorSteps walks every llm and agent step and reports its provider and
// local file references. It returns false when a required check fails: an
// undeclared provider, an unset api key, or a missing schema file.
func doctorSteps(scn *scenario.Scenario, baseDir string, cfg *config.Config) bool {
	ok := true
	scenario.WalkSteps(scn, func(step *scenario.Step) {
		switch {
		case step.LLM != nil:
			fmt.Printf("step %s (llm):\n", step.ID)
			model, source := resolveModel(step.LLM.Model, scn, cfg)
			if model == "" {
				fmt.Println("  FAIL model: llm.model is not set and defaults.model is empty")
				ok = false
			} else {
				if source != "" {
					fmt.Printf("  OK model: %s (from %s)\n", model, source)
				}
				if !doctorProvider(model, cfg) {
					ok = false
				}
			}
			for _, fallback := range step.LLM.FallbackModels {
				if !doctorProvider(fallback, cfg) {
					ok = false
				}
			}
			doctorPromptField("system", step.LLM.System, baseDir)
			doctorPromptField("prompt", step.LLM.Prompt, baseDir)
			if !doctorSchemaField("schema", step.LLM.Schema, baseDir) {
				ok = false
			}
		case step.Agent != nil:
			fmt.Printf("step %s (agent, engine %s):\n", step.ID, step.Agent.Engine)
			doctorPromptField("system", step.Agent.System, baseDir)
			doctorPromptField("prompt", step.Agent.Prompt, baseDir)
		}
	})
	return ok
}

// resolveModel mirrors internal/engine.prepareLLM's model resolution: a
// step's own llm.model wins, then the scenario's defaults.model, then the
// configuration's defaults.model. It returns the empty string, the same as
// the engine's own error case, when none of the three is set, and reports
// which one supplied the value doctor is about to check.
func resolveModel(stepModel string, scn *scenario.Scenario, cfg *config.Config) (model, source string) {
	if stepModel != "" {
		return stepModel, ""
	}
	if scn.Defaults.Model != "" {
		return scn.Defaults.Model, "defaults.model"
	}
	if cfg != nil && cfg.Defaults.Model != "" {
		return cfg.Defaults.Model, "config defaults.model"
	}
	return "", ""
}

// doctorProvider checks that model, a "<provider>/<model>" reference, names
// a provider declared in cfg and, when that provider reads its api key from
// an environment variable, that the variable is set. It never reads the
// variable's value.
func doctorProvider(model string, cfg *config.Config) bool {
	name, _, found := strings.Cut(model, "/")
	if !found {
		fmt.Printf("  FAIL model %q: expected <provider>/<model>\n", model)
		return false
	}
	if cfg == nil {
		fmt.Printf("  FAIL provider %s: no configuration loaded\n", name)
		return false
	}
	p, declared := cfg.Providers[name]
	if !declared {
		fmt.Printf("  FAIL provider %s: not declared in configuration\n", name)
		return false
	}
	if p.APIKey == nil {
		fmt.Printf("  OK provider %s: kind %s, no api_key configured\n", name, p.Kind)
		return true
	}
	if p.APIKey.From != scenario.SecretFromEnv {
		fmt.Printf("  OK provider %s: kind %s, api_key from %s (presence not checked)\n", name, p.Kind, p.APIKey.From)
		return true
	}
	if _, set := os.LookupEnv(p.APIKey.Key); set {
		fmt.Printf("  OK provider %s: kind %s, %s is set\n", name, p.Kind, p.APIKey.Key)
		return true
	}
	if p.APIKey.Optional {
		fmt.Printf("  WARN provider %s: kind %s, %s is not set (optional)\n", name, p.Kind, p.APIKey.Key)
		return true
	}
	fmt.Printf("  FAIL provider %s: kind %s, %s is not set\n", name, p.Kind, p.APIKey.Key)
	return false
}

// doctorPromptField reports whether value, a prompt or system field, points
// at an existing local file. This is informational only: engine.promptText
// treats any value naming an existing file as that file and anything else
// as an inline template, so there is no failing case here.
func doctorPromptField(field, value, baseDir string) {
	if value == "" {
		return
	}
	if strings.ContainsAny(value, "\n{") {
		fmt.Printf("  OK %s: inline template\n", field)
		return
	}
	resolved := value
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(baseDir, resolved)
	}
	if info, err := os.Stat(resolved); err == nil && !info.IsDir() {
		fmt.Printf("  OK %s: file %s\n", field, resolved)
		return
	}
	fmt.Printf("  OK %s: inline template (no file at %s)\n", field, resolved)
}

// doctorSchemaField reports whether an llm step's schema, always a file
// path per spec section 3.5, resolves to an existing, readable file. An
// empty value is scenario.Validate's problem, not doctor's, so it is not
// reported again here.
func doctorSchemaField(field, value, baseDir string) bool {
	if value == "" {
		return true
	}
	resolved := value
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(baseDir, resolved)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		fmt.Printf("  FAIL %s: %s not found\n", field, resolved)
		return false
	}
	if info.IsDir() {
		fmt.Printf("  FAIL %s: %s is a directory\n", field, resolved)
		return false
	}
	fmt.Printf("  OK %s: %s\n", field, resolved)
	return true
}
