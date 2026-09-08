package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/foxzi/baton/internal/config"
	"github.com/foxzi/baton/internal/engine"
	"github.com/foxzi/baton/internal/exitcode"
	"github.com/foxzi/baton/internal/runstore"
	"github.com/foxzi/baton/internal/scenario"
	"github.com/foxzi/baton/internal/secrets"
)

const toolsUsage = `Usage: baton tools <scenario.yaml> --step ID [options]

Prints the tools the agent of one step would be given: names, descriptions
and argument schemas. Nothing is executed and no run directory is written.

Options:
  --step ID          Agent step to inspect; required
  -i key=value       Set a scenario input; repeatable
  --input-file FILE  Read inputs from a JSON file
  --config FILE      Global configuration file; repeatable, later files win
  --workspace DIR    Working directory of the step (default: the scenario directory)
  --json             Print the tools as JSON instead of a human readable list

Exit codes: 0 success, 3 configuration.
`

// toolsCmd implements `baton tools` (spec section 11).
func toolsCmd(args []string) int {
	var (
		stepID    string
		inputs    stringList
		inputFile string
		configs   stringList
		workspace string
		asJSON    bool
	)

	flags := flag.NewFlagSet("tools", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { fmt.Fprint(os.Stderr, toolsUsage) }
	flags.StringVar(&stepID, "step", "", "agent step to inspect")
	flags.Var(&inputs, "i", "scenario input as key=value")
	flags.StringVar(&inputFile, "input-file", "", "JSON file with inputs")
	flags.Var(&configs, "config", "global configuration file")
	flags.StringVar(&workspace, "workspace", "", "working directory of the step")
	flags.BoolVar(&asJSON, "json", false, "print the tools as JSON")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return exitcode.Config
	}
	if len(positional) != 1 || stepID == "" {
		fmt.Fprint(os.Stderr, toolsUsage)
		return exitcode.Config
	}

	path := positional[0]
	scn, err := scenario.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}
	if result := scenario.Validate(scn); !result.OK() {
		for _, problem := range result.Errors {
			fmt.Fprintf(os.Stderr, "error: %s\n", problem)
		}
		fmt.Fprintf(os.Stderr, "%s: %s\n", path, plural(len(result.Errors), "error"))
		return exitcode.Config
	}

	fileInputs, err := readInputFile(inputFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}
	bound, err := scenario.BindInputs(scn.Inputs, inputs, fileInputs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}

	cfg, err := config.Load(configs...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}

	// Secrets are resolved because an api tool needs its credential to be
	// built at all; none of them is printed (section 13).
	baseDir := filepath.Dir(path)
	secretStore, err := secrets.Resolve(scn.Secrets, baseDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}

	// The listing writes nothing, but the engine records runs in a store, so
	// it gets a throwaway one instead of a directory under runs/.
	storeDir, err := os.MkdirTemp("", "baton-tools-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}
	defer os.RemoveAll(storeDir)
	store, err := runstore.Create(storeDir, "tools", secretStore.Redactor())
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}
	defer store.Close()

	eng, err := engine.New(engine.Options{
		Scenario:  scn,
		Inputs:    bound,
		Secrets:   secretStore,
		Store:     store,
		Config:    cfg,
		Workspace: workspace,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}

	list, err := eng.StepTools(stepID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}

	if asJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(list); err != nil {
			fmt.Fprintf(os.Stderr, "baton: %v\n", err)
			return exitcode.Config
		}
		return exitcode.OK
	}

	printTools(stepID, list)
	return exitcode.OK
}

// printTools writes the human readable listing: one block per tool, with the
// argument schema indented under it.
func printTools(stepID string, list []engine.ToolInfo) {
	fmt.Printf("step %s: %s\n", stepID, plural(len(list), "tool"))
	for _, tool := range list {
		fmt.Printf("\n%s", tool.Name)
		if tool.MaxCalls > 0 {
			fmt.Printf("  (up to %s)", plural(tool.MaxCalls, "call"))
		}
		fmt.Println()
		if tool.Description != "" {
			fmt.Printf("  %s\n", tool.Description)
		}
		if schema := indentJSON(tool.InputSchema, "  "); schema != "" {
			fmt.Printf("%s\n", schema)
		}
	}
}

// indentJSON reformats a schema for reading. An unparsable schema is printed
// as it is: this is a listing, not a check.
func indentJSON(raw json.RawMessage, prefix string) string {
	if len(raw) == 0 {
		return ""
	}
	var buf []byte
	if pretty, err := json.MarshalIndent(json.RawMessage(raw), prefix, "  "); err == nil {
		buf = pretty
	} else {
		buf = raw
	}
	return prefix + string(buf)
}
