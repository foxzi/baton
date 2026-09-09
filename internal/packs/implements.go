package packs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/foxzi/baton/internal/ifaces"
)

// ExamplesDir is the directory recorded examples live in: examples/ beside
// the pack file. Both pack layouts put it in the same place, since a
// nested pack is <dir>/<name>/pack.yaml.
func (p *Pack) ExamplesDir() string {
	return filepath.Join(filepath.Dir(p.Path), "examples")
}

// CheckImplements verifies every implements declaration of the pack
// against the interface registry: that the pack's parameters cover the
// interface's arguments by name, and that a recorded example of the
// operation transforms into a value the interface's result schema accepts.
func (p *Pack) CheckImplements() error {
	var problems []error
	for _, name := range p.OpNames() {
		op := p.Ops[name]
		if op.Implements == "" {
			continue
		}
		if err := p.checkOpImplements(op); err != nil {
			problems = append(problems, err)
		}
	}
	return errors.Join(problems...)
}

// checkOpImplements checks one operation's implements declaration.
func (p *Pack) checkOpImplements(op *Op) error {
	var problems []error
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Errorf("ops.%s: %s", op.name, fmt.Sprintf(format, args...)))
	}
	addParam := func(arg, format string, args ...any) {
		problems = append(problems, fmt.Errorf("ops.%s.params.%s: %s", op.name, arg, fmt.Sprintf(format, args...)))
	}

	_, ifaceOp, err := ifaces.Lookup(op.Implements)
	if err != nil {
		add("implements: %v", err)
		return errors.Join(problems...)
	}

	for _, argName := range ifaceOp.ArgNames() {
		arg := ifaceOp.Args[argName]
		param, ok := op.Params[argName]
		if arg.Required {
			if !ok {
				add("implements %s: interface argument %q is missing from params", op.Implements, argName)
			}
			continue
		}
		if ok && param.IsRequired() {
			add("implements %s: interface argument %q is optional but the pack requires it", op.Implements, argName)
		}
	}
	for _, paramName := range sortedParamNames(op.Params) {
		param := op.Params[paramName]
		if !param.IsRequired() {
			continue
		}
		if _, ok := ifaceOp.Args[paramName]; !ok {
			addParam(paramName, "implements %s: params.%s is required but is not an argument of the interface", op.Implements, paramName)
		}
	}

	examplePath := filepath.Join(p.ExamplesDir(), op.name+".json")
	raw, err := os.ReadFile(examplePath)
	if errors.Is(err, fs.ErrNotExist) {
		add("implements %s: examples/%s.json is required to check the interface result", op.Implements, op.name)
		return errors.Join(problems...)
	}
	if err != nil {
		problems = append(problems, err)
		return errors.Join(problems...)
	}
	var body any
	if err := json.Unmarshal(raw, &body); err != nil {
		problems = append(problems, fmt.Errorf("%s: %v", examplePath, err))
		return errors.Join(problems...)
	}

	result, err := p.ReplayExample(op, body)
	if err != nil {
		add("examples/%s.json: %v", op.name, err)
		return errors.Join(problems...)
	}
	if err := ifaceOp.ValidateResult(result); err != nil {
		add("implements %s: examples/%s.json: the transform result does not match the interface: %v", op.Implements, op.name, err)
	}
	return errors.Join(problems...)
}

// ReplayExample puts a recorded response through the same steps a real call
// would: the envelope, then the pagination, then the transform. A paginated
// operation is the reason this is not one line. At runtime its transform sees
// the items of every page concatenated, never a response body, so the
// recorded example is the body of a single page and the walk over it is
// replayed here - one page long, which is enough to catch an items path that
// does not match the body it is pointed at.
func (p *Pack) ReplayExample(op *Op, body any) (any, error) {
	unwrapped, err := p.Envelope.Unwrapped(body)
	if err != nil {
		return nil, err
	}
	if !op.Paginate {
		return op.Transformed(unwrapped)
	}

	strategy := p.PageStrategy(op)
	if strategy == nil {
		return nil, fmt.Errorf("paginate is set but no pagination strategy is declared")
	}
	items, err := strategy.PageItems(unwrapped)
	if err != nil {
		return nil, err
	}
	page, ok := items.([]any)
	if !ok {
		return nil, fmt.Errorf("the example is replayed as one page of the walk, which must be a list, got %T; record the body of a single page and set pagination.items", items)
	}
	return op.Transformed(page)
}

// sortedParamNames returns the parameter names of an operation, sorted, so
// the checks below report in a stable order.
func sortedParamNames(params map[string]*Param) []string {
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// CheckInterface reports whether the pack implements every operation of
// the named interface: every operation of the interface must have a pack
// operation whose implements field names it.
func (p *Pack) CheckInterface(name string) error {
	iface, ok := ifaces.Get(name)
	if !ok {
		return fmt.Errorf("unknown interface %q: known interfaces are %s", name, strings.Join(ifaces.Names(), ", "))
	}

	implemented := make(map[string]bool, len(p.Ops))
	for _, op := range p.Ops {
		implemented[op.Implements] = true
	}

	var missing []string
	for _, opName := range iface.OpNames() {
		if !implemented[name+"."+opName] {
			missing = append(missing, opName)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return fmt.Errorf("pack %s does not implement %s: missing %s", p.Pack, name, strings.Join(missing, ", "))
	}
	return nil
}
