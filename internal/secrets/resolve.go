// Package secrets resolves declared secrets and redacts them from output.
//
// The rules are specified in docs/ru/spec.md, section 6: every secret is
// resolved before the first step runs, a missing one aborts the run with exit
// code 3, and every secret value is replaced with values.Redacted in step
// output, logs, run state and notifications.
package secrets

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/foxzi/baton/internal/scenario"
	"github.com/foxzi/baton/internal/values"
)

// Store holds the resolved secrets of a run together with the redactor that
// covers them.
type Store struct {
	byName   map[string]values.Secret
	redactor *Redactor
}

// Resolve reads every declared secret from its source. All secrets are
// resolved before the first step runs, and every failure is reported at once
// so that a run is not restarted per missing variable.
//
// Relative file paths are resolved against baseDir, the directory of the
// scenario file.
func Resolve(declared map[string]scenario.Secret, baseDir string) (*Store, error) {
	names := make([]string, 0, len(declared))
	for name := range declared {
		names = append(names, name)
	}
	sort.Strings(names)

	byName := make(map[string]values.Secret, len(declared))
	var problems []error
	for _, name := range names {
		secret, err := ResolveOne(name, declared[name], baseDir)
		if err != nil {
			problems = append(problems, fmt.Errorf("secrets.%s: %w", name, err))
			continue
		}
		byName[name] = secret
	}
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}

	all := make([]values.Secret, 0, len(byName))
	for _, secret := range byName {
		all = append(all, secret)
	}
	return &Store{byName: byName, redactor: NewRedactor(all...)}, nil
}

// ResolveOne reads a single declared secret and labels it with name. It is
// what Resolve does per entry, exported for the secrets that live outside the
// scenario's own secrets: block, such as provider api keys (section 8.3).
func ResolveOne(name string, declared scenario.Secret, baseDir string) (values.Secret, error) {
	value, err := read(declared, baseDir)
	if err != nil {
		return values.Secret{}, err
	}
	return values.NewSecret(name, value), nil
}

// WithHidden returns a store whose redactor also covers extra, while none of
// extra is reachable by Lookup. A provider api key must never be readable
// from a scenario, but it must still be masked everywhere (section 13).
func (s *Store) WithHidden(extra ...values.Secret) *Store {
	if len(extra) == 0 {
		return s
	}
	byName := map[string]values.Secret{}
	if s != nil {
		byName = s.byName
	}
	all := make([]values.Secret, 0, len(byName)+len(extra))
	for _, secret := range byName {
		all = append(all, secret)
	}
	all = append(all, extra...)
	return &Store{byName: byName, redactor: NewRedactor(all...)}
}

// read returns the plaintext of one declared secret.
func read(declared scenario.Secret, baseDir string) (string, error) {
	var value string
	switch declared.From {
	case scenario.SecretFromEnv:
		found, ok := os.LookupEnv(declared.Key)
		if !ok {
			return "", fmt.Errorf("environment variable %s is not set", declared.Key)
		}
		value = found
	case scenario.SecretFromFile:
		path := declared.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(baseDir, path)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", declared.Path, err)
		}
		value = string(content)
	default:
		return "", fmt.Errorf("unknown source %q", declared.From)
	}

	if declared.Trim {
		value = strings.TrimSpace(value)
	}
	// An empty secret would be sent to an API as an empty credential and fail
	// there with a far less obvious error, so it counts as missing.
	if value == "" {
		return "", errors.New("value is empty")
	}
	return value, nil
}

// Lookup returns the secret declared under name.
func (s *Store) Lookup(name string) (values.Secret, bool) {
	if s == nil {
		return values.Secret{}, false
	}
	secret, ok := s.byName[name]
	return secret, ok
}

// Names returns the declared secret names in sorted order.
func (s *Store) Names() []string {
	if s == nil {
		return nil
	}
	names := make([]string, 0, len(s.byName))
	for name := range s.byName {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Redactor returns the redactor covering the resolved secrets. It is safe to
// call on a nil store, in which case the redactor passes text through.
func (s *Store) Redactor() *Redactor {
	if s == nil {
		return nil
	}
	return s.redactor
}
