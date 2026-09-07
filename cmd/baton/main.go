// Command baton runs YAML scenarios that interleave deterministic steps with
// agentic ones.
//
// The command surface is specified in docs/ru/spec.md, section 11. Commands
// arrive with the milestones of that document; the rest of M1 follows.
package main

import (
	"fmt"
	"os"

	"github.com/foxzi/baton/internal/exitcode"
	"github.com/foxzi/baton/internal/version"
)

const usage = `baton - headless runner for YAML scenarios

Usage:
  baton <command> [arguments]

Commands:
  validate   Check a scenario file and report every problem
  version    Print the build identity
  help       Print this message
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return exitcode.Config
	}

	switch args[0] {
	case "validate":
		return validateCmd(args[1:])
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
