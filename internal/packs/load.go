package packs

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Load reads the pack described by src. The source is a local directory or a
// git repository with a version pin; a git source is checked out into the
// pack cache and its checksum is verified on every load (docs/ru/spec.md,
// section 7.4.1).
func Load(src Source) (*Pack, error) {
	dir, pinned, err := src.resolve()
	if err != nil {
		return nil, err
	}
	path, nested, err := findPackFile(dir, src.Pack)
	if err != nil {
		return nil, err
	}
	// A pack laid out as a directory is checksummed whole: its queries and
	// examples are part of what the scenario pins.
	location := path
	if nested {
		location = filepath.Dir(path)
	}
	if err := checkDigest(location, src.SHA256, pinned); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pack, err := Parse(data, path)
	if err != nil {
		return nil, err
	}
	if pack.Pack != src.Pack {
		return nil, fmt.Errorf("%s: declares pack %q but was loaded as %q", path, pack.Pack, src.Pack)
	}
	if err := pack.CheckImplements(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return pack, nil
}

// findPackFile looks for the pack file of name in dir, accepting both
// <name>.yaml next to other packs and <name>/pack.yaml for packs that ship
// GraphQL queries or examples alongside. Nested reports the second layout.
func findPackFile(dir, name string) (path string, nested bool, err error) {
	if name == "" {
		return "", false, fmt.Errorf("pack is required")
	}
	if name != filepath.Base(name) || name == "." || name == ".." {
		return "", false, fmt.Errorf("pack %q must be a plain name", name)
	}
	candidates := []struct {
		path   string
		nested bool
	}{
		{filepath.Join(dir, name+".yaml"), false},
		{filepath.Join(dir, name+".yml"), false},
		{filepath.Join(dir, name, "pack.yaml"), true},
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate.path); err == nil && !info.IsDir() {
			return candidate.path, candidate.nested, nil
		}
	}
	return "", false, fmt.Errorf("pack %q not found in %s", name, dir)
}

// Parse parses, validates and compiles a pack. Path is recorded on the pack
// and used in diagnostics.
func Parse(data []byte, path string) (*Pack, error) {
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)

	var pack Pack
	if err := decoder.Decode(&pack); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	pack.Path = path
	for name, op := range pack.Ops {
		op.name = name
	}
	if err := pack.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &pack, nil
}

// Op returns the operation by name.
func (p *Pack) Op(name string) (*Op, error) {
	op, ok := p.Ops[name]
	if !ok {
		return nil, fmt.Errorf("pack %s has no operation %q", p.Pack, name)
	}
	return op, nil
}

// OpNames returns the operation names in alphabetical order.
func (p *Pack) OpNames() []string {
	names := make([]string, 0, len(p.Ops))
	for name := range p.Ops {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// PageStrategy returns the pagination settings in effect for an operation:
// its own, or the pack default.
func (p *Pack) PageStrategy(op *Op) *Pagination {
	if op.Pagination != nil {
		return op.Pagination
	}
	return p.Pagination
}
