package packs

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Load reads the pack named name from source. Source is a local directory,
// absolute or relative to baseDir, which is normally the directory of the
// scenario. Git sources arrive in a later milestone and are rejected here.
func Load(source, name, baseDir string) (*Pack, error) {
	if err := checkLocalSource(source); err != nil {
		return nil, err
	}
	dir := source
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(baseDir, dir)
	}
	path, err := findPackFile(dir, name)
	if err != nil {
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
	if pack.Pack != name {
		return nil, fmt.Errorf("%s: declares pack %q but was loaded as %q", path, pack.Pack, name)
	}
	return pack, nil
}

// checkLocalSource rejects the source forms v1 cannot load.
func checkLocalSource(source string) error {
	switch {
	case source == "":
		return fmt.Errorf("from is required and must be a local directory")
	case strings.Contains(source, "@"):
		return fmt.Errorf("from %q: git pack sources are not supported yet, use a local directory", source)
	case strings.Contains(source, "://"), strings.HasPrefix(source, "github.com/"),
		strings.HasPrefix(source, "gitlab.com/"):
		return fmt.Errorf("from %q: remote pack sources are not supported yet, use a local directory", source)
	}
	return nil
}

// findPackFile looks for the pack file of name in dir, accepting both
// <name>.yaml next to other packs and <name>/pack.yaml for packs that ship
// GraphQL queries or examples alongside.
func findPackFile(dir, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("pack is required")
	}
	if name != filepath.Base(name) || name == "." || name == ".." {
		return "", fmt.Errorf("pack %q must be a plain name", name)
	}
	candidates := []string{
		filepath.Join(dir, name+".yaml"),
		filepath.Join(dir, name+".yml"),
		filepath.Join(dir, name, "pack.yaml"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("pack %q not found in %s", name, dir)
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
