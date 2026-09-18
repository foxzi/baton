package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/foxzi/baton/internal/config"
	"github.com/foxzi/baton/internal/exitcode"
	"github.com/foxzi/baton/internal/scenario"
)

const validateUsage = `Usage: baton validate <scenario.yaml> [--json]

Loads a scenario and reports every problem it finds. Exits with 0 when the
scenario is valid and 3 when it is not; warnings do not change the code.
`

// validateCmd implements `baton validate` (spec section 11).
func validateCmd(args []string) int {
	var asJSON bool
	flags := flag.NewFlagSet("validate", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { fmt.Fprint(os.Stderr, validateUsage) }
	flags.BoolVar(&asJSON, "json", false, "print the result as JSON on stdout")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return flagsExitCode(err)
	}
	if len(positional) != 1 {
		fmt.Fprint(os.Stderr, validateUsage)
		return exitcode.Config
	}

	path := positional[0]
	scn, err := scenario.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}

	result := scenario.Validate(scn)
	// Checks 12 and 13 of section 4 need the packs themselves, which
	// Validate does not read; only run them once the scenario itself holds
	// up, since a scenario with undeclared apis produces noise here.
	if result.OK() {
		packResult := scenario.CheckPacks(scn)
		result.Errors = append(result.Errors, packResult.Errors...)
		result.Warnings = append(result.Warnings, packResult.Warnings...)
	}
	// The pricing table lives in the global configuration, so a model with
	// no price can only be reported when that configuration loads. A broken
	// one is the business of `baton run`, not of a scenario check.
	if cfg, err := config.Load(); err == nil {
		result.Warnings = append(result.Warnings, pricingWarnings(scn, cfg)...)
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		err := enc.Encode(struct {
			Scenario string           `json:"scenario"`
			OK       bool             `json:"ok"`
			Errors   []diagnosticJSON `json:"errors"`
			Warnings []diagnosticJSON `json:"warnings"`
		}{
			Scenario: path,
			OK:       result.OK(),
			Errors:   diagnosticsJSON(result.Errors),
			Warnings: diagnosticsJSON(result.Warnings),
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "baton: %v\n", err)
			return exitcode.Config
		}
		if !result.OK() {
			return exitcode.Config
		}
		return exitcode.OK
	}

	for _, warning := range result.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", warning.Format(path))
	}
	for _, problem := range result.Errors {
		fmt.Fprintf(os.Stderr, "error: %s\n", problem.Format(path))
	}

	if !result.OK() {
		fmt.Fprintf(os.Stderr, "%s: %s\n", path, plural(len(result.Errors), "error"))
		return exitcode.Config
	}
	if len(result.Warnings) > 0 {
		fmt.Printf("%s: valid, %s\n", path, plural(len(result.Warnings), "warning"))
		return exitcode.OK
	}
	fmt.Printf("%s: valid\n", path)
	return exitcode.OK
}

// diagnosticJSON is the JSON shape of a scenario.Diagnostic for `--json`.
type diagnosticJSON struct {
	Path    string `json:"path"`
	Line    int    `json:"line,omitempty"`
	StepID  string `json:"step,omitempty"`
	Message string `json:"message"`
}

// diagnosticsJSON converts diagnostics for JSON output, returning an empty
// slice rather than nil so it encodes as [] instead of null.
func diagnosticsJSON(diags []scenario.Diagnostic) []diagnosticJSON {
	out := make([]diagnosticJSON, 0, len(diags))
	for _, d := range diags {
		out = append(out, diagnosticJSON{
			Path:    d.Path,
			Line:    d.Line,
			StepID:  d.StepID,
			Message: d.Message,
		})
	}
	return out
}

func plural(count int, noun string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, noun)
	}
	return fmt.Sprintf("%d %ss", count, noun)
}
