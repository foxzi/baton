package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/foxzi/baton/internal/config"
	"github.com/foxzi/baton/internal/engine"
	"github.com/foxzi/baton/internal/exitcode"
	"github.com/foxzi/baton/internal/packs"
	"github.com/foxzi/baton/internal/packs/openapi"
	"github.com/foxzi/baton/internal/runstore"
	"github.com/foxzi/baton/internal/scenario"
	"github.com/foxzi/baton/internal/secrets"
)

const apisUsage = `Usage:
  baton apis import --openapi <spec> --ops <id,...> [--interface forge/v1] [--name NAME] > pack.yaml
  baton apis validate <pack.yaml|pack-dir>...   # check a pack and its examples/
  baton apis call <scenario.yaml> <api>.<op> [-a k=v] [-a k:=<json>] [-i k=v] [--config FILE]

Options:
  --openapi FILE     OpenAPI 3 document, JSON or YAML ("-" reads stdin)
  --ops IDS          Comma-separated operationIds to import (repeatable)
  --interface NAME   Pack interface the author intends to implement
  --name NAME        Pack name (default: a slug of the document title)

The generated pack is a skeleton: transforms, descriptions, readonly and
pagination are left as TODO markers for the author. The document itself is
not used at run time.

apis validate loads each pack and, for every operation with a matching
examples/<op>.json, replays the envelope and transform on that recorded
response. An operation that declares implements is additionally checked
against the interface registry: argument names and the shape of the
transformed example.

apis call runs one operation of one apis entry of the given scenario, so that
a pack can be exercised without writing a step for it. base_url, auth secret
and timeout all come from the scenario's apis entry, exactly as a run would
resolve them. -a k=v sets a string argument; -a k:=<json> sets an argument
parsed as JSON, for numbers, booleans, lists and objects.
`

// apisSubcommands lists apis's subcommands, for suggesting a close match on
// an unknown one.
var apisSubcommands = []string{"import", "validate", "call"}

// apisCmd implements `baton apis` (spec section 7.4.7).
func apisCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, apisUsage)
		return exitcode.Config
	}
	switch args[0] {
	case "import":
		return apisImportCmd(args[1:])
	case "validate":
		return apisValidateCmd(args[1:])
	case "call":
		return apisCallCmd(args[1:])
	case "-h", "--help", "help":
		fmt.Print(apisUsage)
		return exitcode.OK
	default:
		fmt.Fprintf(os.Stderr, "%s\n\n%s", unknownCommandError("apis subcommand", args[0], apisSubcommands), apisUsage)
		return exitcode.Config
	}
}

// apisImportCmd generates the skeleton of a pack from an OpenAPI document.
func apisImportCmd(args []string) int {
	flags := flag.NewFlagSet("apis import", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { fmt.Fprint(os.Stderr, apisUsage) }
	spec := flags.String("openapi", "", "OpenAPI document")
	iface := flags.String("interface", "", "pack interface")
	name := flags.String("name", "", "pack name")
	var ops opsFlag
	flags.Var(&ops, "ops", "operationIds to import")

	positional, err := parseFlags(flags, args)
	if err != nil {
		return flagsExitCode(err)
	}
	if len(positional) != 0 {
		fmt.Fprint(os.Stderr, apisUsage)
		return exitcode.Config
	}
	if *spec == "" {
		fmt.Fprintf(os.Stderr, "baton: --openapi is required\n\n%s", apisUsage)
		return exitcode.Config
	}

	data, err := readSpec(*spec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}
	out, err := openapi.Import(data, openapi.Options{Name: *name, Ops: ops, Interface: *iface})
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}
	if _, err := os.Stdout.Write(out); err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}
	return exitcode.OK
}

// readSpec reads the document from a file, or from stdin when the path is "-"
// so that a spec can be piped in from a downloader.
func readSpec(path string) ([]byte, error) {
	if path == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("stdin: %w", err)
		}
		return data, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// opsFlag collects operationIds from comma-separated lists, so that --ops can
// be given once with a list or several times.
type opsFlag []string

func (o *opsFlag) String() string { return strings.Join(*o, ",") }

func (o *opsFlag) Set(v string) error {
	for _, id := range strings.Split(v, ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		*o = append(*o, id)
	}
	return nil
}

// apisValidateCmd loads one or more packs and replays their recorded
// examples through the envelope and the operation transform, which is the
// only part of a pack that can silently break without a live API to call.
func apisValidateCmd(args []string) int {
	flags := flag.NewFlagSet("apis validate", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { fmt.Fprint(os.Stderr, apisUsage) }

	positional, err := parseFlags(flags, args)
	if err != nil {
		return flagsExitCode(err)
	}
	if len(positional) == 0 {
		fmt.Fprint(os.Stderr, apisUsage)
		return exitcode.Config
	}

	failed := false
	for _, path := range positional {
		if !validatePack(path) {
			failed = true
		}
	}
	if failed {
		return exitcode.Config
	}
	return exitcode.OK
}

// validatePack validates one pack path. It reports whether the pack and all
// of its examples checked out.
func validatePack(path string) (ok bool) {
	packPath, examplesDir, err := resolvePackPath(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return false
	}
	data, err := os.ReadFile(packPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return false
	}
	pack, err := packs.Parse(data, packPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return false
	}

	ok = true
	checked, missing := 0, 0
	names := pack.OpNames()
	for _, name := range names {
		op, err := pack.Op(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "baton: %s: %s: %v\n", packPath, name, err)
			ok = false
			continue
		}
		examplePath := filepath.Join(examplesDir, name+".json")
		raw, err := os.ReadFile(examplePath)
		if errors.Is(err, fs.ErrNotExist) {
			missing++
			continue
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "baton: %v\n", err)
			ok = false
			continue
		}
		var body any
		if err := json.Unmarshal(raw, &body); err != nil {
			fmt.Fprintf(os.Stderr, "baton: %s: %s: %v\n", packPath, name, err)
			ok = false
			continue
		}
		if _, err := pack.ReplayExample(op, body); err != nil {
			fmt.Fprintf(os.Stderr, "baton: %s: %s: %v\n", packPath, name, err)
			ok = false
			continue
		}
		checked++
		fmt.Printf("ok  %s.%s  %s\n", pack.Pack, name, examplePath)
	}
	if err := pack.CheckImplements(); err != nil {
		fmt.Fprintf(os.Stderr, "baton: %s: %v\n", packPath, err)
		ok = false
	}
	if ok {
		fmt.Printf("%s: ok (%d ops, %d examples checked, %d without examples)\n",
			packPath, len(names), checked, missing)
	}
	return ok
}

// resolvePackPath resolves a positional apis-validate argument to the pack
// file to parse and the examples directory that sits beside it.
func resolvePackPath(path string) (packPath, examplesDir string, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", "", err
	}
	if !info.IsDir() {
		return path, filepath.Join(filepath.Dir(path), "examples"), nil
	}
	packPath = filepath.Join(path, "pack.yaml")
	if _, err := os.Stat(packPath); err != nil {
		return "", "", fmt.Errorf("no pack.yaml in %s", path)
	}
	return packPath, filepath.Join(path, "examples"), nil
}

// argsFlag collects the -a arguments of `apis call`: k=v sets a string, and
// k:=<json> sets a value parsed as JSON, for arguments that are not strings.
type argsFlag map[string]any

func (a *argsFlag) String() string {
	if *a == nil {
		return ""
	}
	keys := make([]string, 0, len(*a))
	for k := range *a {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%v", k, (*a)[k])
	}
	return strings.Join(parts, ",")
}

func (a *argsFlag) Set(v string) error {
	// k:= is checked first: it contains "=", so a plain split on "=" would
	// otherwise treat the JSON marker as part of the value.
	if idx := strings.Index(v, ":="); idx >= 0 {
		key, raw := v[:idx], v[idx+2:]
		var value any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return fmt.Errorf("-a %s: invalid JSON value: %w", v, err)
		}
		if *a == nil {
			*a = argsFlag{}
		}
		(*a)[key] = value
		return nil
	}
	if idx := strings.Index(v, "="); idx >= 0 {
		key, value := v[:idx], v[idx+1:]
		if *a == nil {
			*a = argsFlag{}
		}
		(*a)[key] = value
		return nil
	}
	return fmt.Errorf("-a %s: expected k=v or k:=<json>", v)
}

// apisCallCmd calls one operation of one apis entry of a scenario, for
// debugging a pack without writing a step to hold the call (spec section
// 7.4.7, docs/ru/spec.md line 788).
func apisCallCmd(args []string) int {
	var (
		opArgs    argsFlag
		inputs    stringList
		inputFile string
		configs   stringList
	)

	flags := flag.NewFlagSet("apis call", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { fmt.Fprint(os.Stderr, apisUsage) }
	flags.Var(&opArgs, "a", "operation argument as k=v or k:=<json>")
	flags.Var(&inputs, "i", "scenario input as key=value")
	flags.StringVar(&inputFile, "input-file", "", "JSON file with inputs")
	flags.Var(&configs, "config", "global configuration file")

	positional, err := parseFlags(flags, args)
	if err != nil {
		return flagsExitCode(err)
	}
	if len(positional) != 2 {
		fmt.Fprint(os.Stderr, apisUsage)
		return exitcode.Config
	}

	scenarioPath, apiOp := positional[0], positional[1]
	dot := strings.Index(apiOp, ".")
	if dot <= 0 || dot == len(apiOp)-1 {
		fmt.Fprintf(os.Stderr, "baton: expected <api>.<op>, got %q\n", apiOp)
		return exitcode.Config
	}
	apiName, opName := apiOp[:dot], apiOp[dot+1:]

	scn, err := scenario.Load(scenarioPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}
	if result := scenario.Validate(scn); !result.OK() {
		for _, problem := range result.Errors {
			fmt.Fprintf(os.Stderr, "error: %s\n", problem)
		}
		fmt.Fprintf(os.Stderr, "%s: %s\n", scenarioPath, plural(len(result.Errors), "error"))
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

	baseDir := filepath.Dir(scenarioPath)
	secretStore, err := secrets.Resolve(scn.Secrets, baseDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}

	// A call may name an api the configuration declares globally, so the
	// secrets those entries authorise with are resolved too, and masked.
	apiSecrets, err := cfg.ResolveAPISecrets(baseDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}
	secretStore = secretStore.WithHidden(secretValues(apiSecrets)...)

	// The call writes nothing a real run would, but the engine still records
	// runs in a store, so it gets a throwaway one instead of a directory
	// under runs/.
	storeDir, err := os.MkdirTemp("", "baton-apis-call-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}
	defer os.RemoveAll(storeDir)
	store, err := runstore.Create(storeDir, "apis-call", secretStore.Redactor())
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}
	defer store.Close()

	eng, err := engine.New(engine.Options{
		Scenario:   scn,
		Inputs:     bound,
		Secrets:    secretStore,
		Store:      store,
		Config:     cfg,
		APISecrets: apiSecrets,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Config
	}

	result, err := eng.CallOp(context.Background(), apiName, opName, opArgs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		var engErr *engine.Error
		if errors.As(err, &engErr) && engErr.Class == engine.ClassConfig {
			return exitcode.Config
		}
		return exitcode.Failure
	}

	out, err := json.MarshalIndent(result.Result, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Failure
	}
	out = secretStore.Redactor().Bytes(out)
	if _, err := fmt.Printf("%s\n", out); err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return exitcode.Failure
	}

	if result.Pages > 1 || result.TruncatedPages || result.Truncated {
		note := fmt.Sprintf("baton: %s", plural(result.Pages, "page"))
		if result.TruncatedPages {
			note += ", truncated at max_pages"
		}
		if result.Truncated {
			note += ", truncated at max_bytes"
		}
		fmt.Fprintln(os.Stderr, note)
	}

	return exitcode.OK
}
