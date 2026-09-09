// Command baton runs YAML scenarios that interleave deterministic steps with
// agentic ones.
//
// The command surface is specified in docs/ru/spec.md, section 11. Commands
// arrive with the milestones of that document; the rest of M1 follows.
package main

import (
	"errors"
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
  init       Write a self-contained scenario template into a directory
  run        Execute a scenario
  validate   Check a scenario file and report every problem
  resume     Continue a failed run at the step that failed
  runs       List runs, show one, print step logs
  tools      Print the tools an agent step would be given
  apis       Generate a pack skeleton from an OpenAPI document
  schema     Print the JSON Schema of the scenario format
  version    Print the build identity
  help       Print this message, or "help <command>" for one command's usage
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

// flagsExitCode turns a flag.FlagSet parse error into an exit code.
// -h/--help must exit 0 without performing the command's action; any other
// parse error (unknown flag, bad value) is a configuration error. The
// FlagSet's own Usage already printed the text in both cases.
func flagsExitCode(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return exitcode.OK
	}
	return exitcode.Config
}

// commandNames lists the top-level commands, for suggesting a close match
// on an unknown one.
var commandNames = []string{
	"init", "run", "validate", "resume", "runs", "tools", "apis", "schema", "version", "help",
}

// unknownCommandError formats the "unknown command" message, with a
// suggestion when name is close to one of candidates.
func unknownCommandError(kind, name string, candidates []string) string {
	msg := fmt.Sprintf("baton: unknown %s %q", kind, name)
	if hint := suggest(name, candidates); hint != "" {
		msg += fmt.Sprintf(", did you mean %q?", hint)
	}
	return msg
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
	case "init":
		return initCmd(args[1:])
	case "run":
		return runCmd(args[1:])
	case "validate":
		return validateCmd(args[1:])
	case "resume":
		return resumeCmd(args[1:])
	case "runs":
		return runsCmd(args[1:])
	case "tools":
		return toolsCmd(args[1:])
	case "apis":
		return apisCmd(args[1:])
	case "schema":
		return schemaCmd(args[1:])
	case "version", "--version", "-v":
		fmt.Println(version.String())
		return exitcode.OK
	case "help", "--help", "-h":
		return helpCmd(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "%s\n\n%s", unknownCommandError("command", args[0], commandNames), usage)
		return exitcode.Config
	}
}

// helpCmd implements `baton help [command]`. With no argument it prints the
// top-level usage; with one, the usage of that command, which for apis and
// runs already documents their subcommands (import/validate/call,
// list/show/logs) in one block, so a further "help apis import" is not
// worth a separate case. A second argument is rejected instead of being
// silently ignored.
func helpCmd(args []string) int {
	if len(args) == 0 {
		fmt.Print(usage)
		return exitcode.OK
	}
	if len(args) > 1 {
		fmt.Fprintf(os.Stderr, "baton: help takes at most one command name, got %q\n\n%s", args, usage)
		return exitcode.Config
	}
	text, ok := commandUsage(args[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "%s\n\n%s", unknownCommandError("command", args[0], commandNames), usage)
		return exitcode.Config
	}
	fmt.Print(text)
	return exitcode.OK
}

// commandUsage returns the usage text of one top-level command, the same
// text that command prints on -h/--help or on a missing/invalid argument.
func commandUsage(name string) (string, bool) {
	switch name {
	case "init":
		return initUsage, true
	case "run":
		return runUsage, true
	case "validate":
		return validateUsage, true
	case "resume":
		return resumeUsage, true
	case "runs":
		return runsUsage, true
	case "tools":
		return toolsUsage, true
	case "apis":
		return apisUsage, true
	case "schema":
		return schemaUsage, true
	case "version":
		return "Usage: baton version\n\nPrints the build identity.\n", true
	case "help":
		return usage, true
	default:
		return "", false
	}
}
