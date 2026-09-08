package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/foxzi/baton/internal/exitcode"
	"github.com/foxzi/baton/internal/packs"
	"github.com/foxzi/baton/internal/packs/openapi"
)

const apisUsage = `Usage:
  baton apis import --openapi <spec> --ops <id,...> [--interface forge/v1] [--name NAME] > pack.yaml
  baton apis validate <pack.yaml|pack-dir>...   # check a pack and its examples/

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
response. Interface conformance is not checked yet: the interface registry
does not exist, so implements is only checked for shape.
`

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
	default:
		fmt.Fprintf(os.Stderr, "baton: unknown apis subcommand %q\n\n%s", args[0], apisUsage)
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
		return exitcode.Config
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
		return exitcode.Config
	}
	if len(positional) == 0 {
		fmt.Fprint(os.Stderr, apisUsage)
		return exitcode.Config
	}

	failed := false
	sawImplements := false
	for _, path := range positional {
		ok, implements := validatePack(path)
		if !ok {
			failed = true
		}
		if implements {
			sawImplements = true
		}
	}
	if sawImplements {
		fmt.Fprintln(os.Stderr, "baton: note: implements is only checked for shape; interface conformance needs the interface registry")
	}
	if failed {
		return exitcode.Config
	}
	return exitcode.OK
}

// validatePack validates one pack path. It reports whether the pack and all
// of its examples checked out, and whether any operation declares implements.
func validatePack(path string) (ok, implements bool) {
	packPath, examplesDir, err := resolvePackPath(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return false, false
	}
	data, err := os.ReadFile(packPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return false, false
	}
	pack, err := packs.Parse(data, packPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baton: %v\n", err)
		return false, false
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
		if op.Implements != "" {
			implements = true
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
		result, err := pack.Envelope.Unwrapped(body)
		if err == nil {
			_, err = op.Transformed(result)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "baton: %s: %s: %v\n", packPath, name, err)
			ok = false
			continue
		}
		checked++
		fmt.Printf("ok  %s.%s  %s\n", pack.Pack, name, examplePath)
	}
	if ok {
		fmt.Printf("%s: ok (%d ops, %d examples checked, %d without examples)\n",
			packPath, len(names), checked, missing)
	}
	return ok, implements
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
