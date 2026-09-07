package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/foxzi/baton/internal/exitcode"
	"github.com/foxzi/baton/internal/scenario"
)

const schemaUsage = `Usage: baton schema

Prints the JSON Schema of the scenario format on stdout. Point an editor at
it to get completion and inline errors:

  baton schema > scenario.schema.json
`

// schemaCmd implements `baton schema` (spec section 11).
func schemaCmd(args []string) int {
	flags := flag.NewFlagSet("schema", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { fmt.Fprint(os.Stderr, schemaUsage) }
	if err := flags.Parse(args); err != nil {
		return exitcode.Config
	}
	if flags.NArg() != 0 {
		fmt.Fprint(os.Stderr, schemaUsage)
		return exitcode.Config
	}

	if _, err := os.Stdout.Write(scenario.Schema()); err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}
	return exitcode.OK
}
