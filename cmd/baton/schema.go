package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/foxzi/baton/internal/exitcode"
	"github.com/foxzi/baton/internal/scenario"
	"github.com/foxzi/baton/internal/schemadoc"
)

const schemaUsage = `Usage: baton schema [--markdown LANG]

Prints the JSON Schema of the scenario format on stdout. Point an editor at
it to get completion and inline errors:

  baton schema > scenario.schema.json

Options:
  --markdown LANG   print the schema as a Markdown reference in LANG (en, ru)
                    instead of JSON; this is how docs/{en,ru}/schema.md are
                    generated
`

// schemaCmd implements `baton schema` (spec section 11).
func schemaCmd(args []string) int {
	flags := flag.NewFlagSet("schema", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { fmt.Fprint(os.Stderr, schemaUsage) }
	markdown := flags.String("markdown", "", "print a Markdown reference in this language")
	if err := flags.Parse(args); err != nil {
		return exitcode.Config
	}
	if flags.NArg() != 0 {
		fmt.Fprint(os.Stderr, schemaUsage)
		return exitcode.Config
	}

	out := scenario.Schema()
	if *markdown != "" {
		reference, err := schemadoc.Render(out, *markdown)
		if err != nil {
			fmt.Fprintf(os.Stderr, "baton: %v\n", err)
			return exitcode.Config
		}
		out = []byte(reference)
	}

	if _, err := os.Stdout.Write(out); err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}
	return exitcode.OK
}
