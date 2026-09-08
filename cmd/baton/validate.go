package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/foxzi/baton/internal/config"
	"github.com/foxzi/baton/internal/exitcode"
	"github.com/foxzi/baton/internal/scenario"
)

const validateUsage = `Usage: baton validate <scenario.yaml>

Loads a scenario and reports every problem it finds. Exits with 0 when the
scenario is valid and 3 when it is not; warnings do not change the code.
`

// validateCmd implements `baton validate` (spec section 11).
func validateCmd(args []string) int {
	flags := flag.NewFlagSet("validate", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { fmt.Fprint(os.Stderr, validateUsage) }
	if err := flags.Parse(args); err != nil {
		return exitcode.Config
	}
	if flags.NArg() != 1 {
		fmt.Fprint(os.Stderr, validateUsage)
		return exitcode.Config
	}

	path := flags.Arg(0)
	scn, err := scenario.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}

	result := scenario.Validate(scn)
	// The pricing table lives in the global configuration, so a model with
	// no price can only be reported when that configuration loads. A broken
	// one is the business of `baton run`, not of a scenario check.
	if cfg, err := config.Load(); err == nil {
		result.Warnings = append(result.Warnings, pricingWarnings(scn, cfg)...)
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", warning)
	}
	for _, problem := range result.Errors {
		fmt.Fprintf(os.Stderr, "error: %s\n", problem)
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

func plural(count int, noun string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, noun)
	}
	return fmt.Sprintf("%d %ss", count, noun)
}
