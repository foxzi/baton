// Command baton runs YAML scenarios that interleave deterministic steps with
// agentic ones.
//
// The command surface is specified in docs/ru/spec.md, section 11. Commands
// arrive with the milestones of that document; the rest of M1 follows.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/foxzi/baton/internal/exitcode"
	"github.com/foxzi/baton/internal/version"
)

const usage = `baton - headless runner for YAML scenarios

Usage:
  baton <command> [arguments]

Commands:
  run        Execute a scenario
  validate   Check a scenario file and report every problem
  schema     Print the JSON Schema of the scenario format
  version    Print the build identity
  help       Print this message
`

// parseFlags parses args allowing flags on either side of the positional
// arguments, which the command lines of section 11 use.
func parseFlags(flags *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	rest := args
	for {
		if err := flags.Parse(rest); err != nil {
			return nil, err
		}
		rest = flags.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		rest = rest[1:]
	}
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return exitcode.Config
	}

	switch args[0] {
	case "run":
		return runCmd(args[1:])
	case "validate":
		return validateCmd(args[1:])
	case "schema":
		return schemaCmd(args[1:])
	case "version", "--version", "-v":
		fmt.Println(version.String())
		return exitcode.OK
	case "help", "--help", "-h":
		fmt.Print(usage)
		return exitcode.OK
	default:
		fmt.Fprintf(os.Stderr, "baton: unknown command %q\n\n%s", args[0], usage)
		return exitcode.Config
	}
}
