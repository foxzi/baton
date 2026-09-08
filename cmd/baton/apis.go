package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/foxzi/baton/internal/exitcode"
	"github.com/foxzi/baton/internal/packs/openapi"
)

const apisUsage = `Usage:
  baton apis import --openapi <spec> --ops <id,...> [--interface forge/v1] [--name NAME] > pack.yaml

Options:
  --openapi FILE     OpenAPI 3 document, JSON or YAML ("-" reads stdin)
  --ops IDS          Comma-separated operationIds to import (repeatable)
  --interface NAME   Pack interface the author intends to implement
  --name NAME        Pack name (default: a slug of the document title)

The generated pack is a skeleton: transforms, descriptions, readonly and
pagination are left as TODO markers for the author. The document itself is
not used at run time.
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
