// Package ifaces is the registry of API-pack interfaces (docs/ru/spec.md,
// section 7.4.5).
//
// An interface fixes the argument names and the result shape of a set of
// operations, so a scenario can declare `interface: forge/v1` and swap one
// pack implementing it for another (GitLab for GitHub, say) without
// touching the scenario itself. The registry is embedded in the binary and
// versioned: v1 ships forge/v1, tracker/v1 and notify/v1.
package ifaces

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/foxzi/baton/internal/jsonschema"
)

//go:embed data/*.json
var dataFS embed.FS

// Interface is one registry entry: a named set of operations with fixed
// argument names and result schemas.
type Interface struct {
	Name string

	ops map[string]*Op
}

// Op returns the named operation of the interface.
func (i *Interface) Op(name string) (*Op, bool) {
	op, ok := i.ops[name]
	return op, ok
}

// OpNames returns the operation names of the interface, sorted.
func (i *Interface) OpNames() []string {
	names := make([]string, 0, len(i.ops))
	for name := range i.ops {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Op is one operation of an interface: its argument contract and the JSON
// Schema its result must satisfy.
type Op struct {
	Name        string
	Description string
	Args        map[string]*Arg

	result *jsonschema.Schema
}

// ArgNames returns the argument names of the operation, sorted.
func (o *Op) ArgNames() []string {
	names := make([]string, 0, len(o.Args))
	for name := range o.Args {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// ValidateResult checks a decoded result value against the operation's
// result schema. It reports nil when the operation carries no schema.
func (o *Op) ValidateResult(result any) error {
	if o.result == nil {
		return nil
	}
	return o.result.Validate(result)
}

// Arg declares one argument an interface operation takes. Type is a
// documentation string ("string", "integer", "array", ...); it is not
// itself validated against.
type Arg struct {
	Type     string
	Required bool
}

// rawFile is the on-disk shape of one data/*.json file.
type rawFile struct {
	Interface string           `json:"interface"`
	Ops       map[string]rawOp `json:"ops"`
}

// rawOp is the on-disk shape of one operation entry.
type rawOp struct {
	Description string            `json:"description"`
	Args        map[string]rawArg `json:"args"`
	Result      json.RawMessage   `json:"result"`
}

// rawArg is the on-disk shape of one argument entry.
type rawArg struct {
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

// registry maps interface name to Interface, loaded once from the embedded
// data files.
var registry = sync.OnceValue(load)

// load parses and compiles every embedded interface file. The data is
// embedded and covered by tests, so a failure here is a programmer error
// (a typo in one of the data/*.json files), not something a caller can act
// on: it panics rather than threading an error through every accessor.
func load() map[string]*Interface {
	entries, err := dataFS.ReadDir("data")
	if err != nil {
		panic(fmt.Sprintf("ifaces: %v", err))
	}

	out := make(map[string]*Interface, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		data, err := dataFS.ReadFile("data/" + name)
		if err != nil {
			panic(fmt.Sprintf("ifaces: %s: %v", name, err))
		}

		iface, err := parseFile(name, data)
		if err != nil {
			panic(fmt.Sprintf("ifaces: %s: %v", name, err))
		}

		if iface.Name == "" {
			panic(fmt.Sprintf("ifaces: %s: empty interface name", name))
		}
		if _, dup := out[iface.Name]; dup {
			panic(fmt.Sprintf("ifaces: %s: duplicate interface name %q", name, iface.Name))
		}
		out[iface.Name] = iface
	}
	return out
}

// parseFile decodes one data file and compiles the result schema of each
// operation. Decoding is strict (unknown fields fail) so a typo in a data
// file is caught by the test suite rather than silently ignored.
func parseFile(fileName string, data []byte) (*Interface, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var raw rawFile
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	iface := &Interface{
		Name: raw.Interface,
		ops:  make(map[string]*Op, len(raw.Ops)),
	}

	for opName, rop := range raw.Ops {
		args := make(map[string]*Arg, len(rop.Args))
		for argName, ra := range rop.Args {
			args[argName] = &Arg{Type: ra.Type, Required: ra.Required}
		}

		schemaName := raw.Interface + "." + opName
		schema, err := jsonschema.Compile(schemaName, rop.Result)
		if err != nil {
			return nil, fmt.Errorf("op %s: %w", opName, err)
		}

		iface.ops[opName] = &Op{
			Name:        opName,
			Description: rop.Description,
			Args:        args,
			result:      schema,
		}
	}

	return iface, nil
}

// Get returns the interface registered under name, e.g. "forge/v1".
func Get(name string) (*Interface, bool) {
	iface, ok := registry()[name]
	return iface, ok
}

// Names returns the registered interface names, sorted.
func Names() []string {
	reg := registry()
	names := make([]string, 0, len(reg))
	for name := range reg {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Lookup resolves a "<interface>.<op>" reference, e.g. "forge/v1.get_change",
// to its Interface and Op. Errors list what interfaces or operations
// actually exist, so a typo is easy to fix from the message alone.
func Lookup(ref string) (*Interface, *Op, error) {
	ifaceName, opName, err := ParseRef(ref)
	if err != nil {
		return nil, nil, err
	}

	iface, ok := Get(ifaceName)
	if !ok {
		return nil, nil, fmt.Errorf("unknown interface %q: known interfaces are %s",
			ifaceName, strings.Join(Names(), ", "))
	}

	op, ok := iface.Op(opName)
	if !ok {
		return nil, nil, fmt.Errorf("interface %s has no operation %q: it has %s",
			ifaceName, opName, strings.Join(iface.OpNames(), ", "))
	}

	return iface, op, nil
}

// ParseRef splits a "<interface>.<op>" reference on the last ".", since the
// interface name itself contains a "/" but never a ".". Both halves must be
// non-empty.
func ParseRef(ref string) (iface, op string, err error) {
	i := strings.LastIndex(ref, ".")
	if i < 0 {
		return "", "", fmt.Errorf("%q is not an interface operation like forge/v1.get_change", ref)
	}

	iface, op = ref[:i], ref[i+1:]
	if iface == "" || op == "" {
		return "", "", fmt.Errorf("%q is not an interface operation like forge/v1.get_change", ref)
	}
	return iface, op, nil
}
