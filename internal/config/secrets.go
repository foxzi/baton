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
