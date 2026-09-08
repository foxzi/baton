package config

import (
	"errors"
	"fmt"
	"sort"

	"github.com/foxzi/baton/internal/secrets"
	"github.com/foxzi/baton/internal/values"
)

// ResolveProviderKeys reads the api key of every configured provider. A
// provider whose key is declared optional and missing is simply left out:
// section 8.3 shows a local openai_compatible backend that needs none.
//
// Relative file paths are resolved against baseDir. Every failure is
// reported at once, as in internal/secrets.Resolve, so that a run is not
// restarted per missing variable.
func (c *Config) ResolveProviderKeys(baseDir string) (map[string]values.Secret, error) {
	if c == nil {
		return nil, nil
	}
	names := make([]string, 0, len(c.Providers))
	for name := range c.Providers {
		names = append(names, name)
	}
	sort.Strings(names)

	keys := make(map[string]values.Secret, len(names))
	var problems []error
	for _, name := range names {
		ref := c.Providers[name].APIKey
		if ref == nil {
			continue
		}
		secret, err := secrets.ResolveOne("providers."+name+".api_key", ref.Secret(), baseDir)
		if err != nil {
			if ref.Optional {
				continue
			}
			problems = append(problems, fmt.Errorf("providers.%s.api_key: %w", name, err))
			continue
		}
		keys[name] = secret
	}
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	return keys, nil
}

// ResolveAPISecrets reads the secrets the global apis: entries authorise
// with (spec sections 7.4.1 and 12). They are declared in the config's own
// secrets: block, which a scenario cannot see, so they are resolved here and
// handed to the engine separately from the scenario's secrets.
//
// The result is keyed by secret name, the name an apis entry refers to.
// Relative file paths are resolved against baseDir, as for provider keys.
func (c *Config) ResolveAPISecrets(baseDir string) (map[string]values.Secret, error) {
	if c == nil {
		return nil, nil
	}
	names := make([]string, 0, len(c.APIs))
	for name := range c.APIs {
		names = append(names, name)
	}
	sort.Strings(names)

	resolved := make(map[string]values.Secret, len(names))
	var problems []error
	for _, name := range names {
		secretName := c.APIs[name].Auth.Secret
		if secretName == "" {
			continue
		}
		if _, done := resolved[secretName]; done {
			continue
		}
		ref, declared := c.Secrets[secretName]
		if !declared {
			problems = append(problems, fmt.Errorf("apis.%s.auth.secret: undeclared secret %q", name, secretName))
			continue
		}
		secret, err := secrets.ResolveOne(secretName, ref.Secret(), baseDir)
		if err != nil {
			if ref.Optional {
				continue
			}
			problems = append(problems, fmt.Errorf("secrets.%s: %w", secretName, err))
			continue
		}
		resolved[secretName] = secret
	}
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	return resolved, nil
}
