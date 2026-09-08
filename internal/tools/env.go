package tools

import (
	"fmt"
	"os"
	"sort"

	"github.com/foxzi/baton/internal/scenario"
)

// keptVars are the variables a child process inherits from the runner:
// enough to find an executable and reuse a build cache, and nothing that
// carries a credential (spec section 7.5).
var keptVars = []string{"PATH", "HOME", "GOCACHE", "GOPATH", "GOFLAGS"}

// processEnv builds the environment of a process the runner starts for an
// agent: the minimal allow list plus what the scenario or the configuration
// declares, secrets included. Secrets reach the child process and nothing
// else: they are not written to the run directory.
func processEnv(secrets SecretSource, declared map[string]scenario.EnvValue) ([]string, error) {
	env := make([]string, 0, len(keptVars)+len(declared))
	for _, name := range keptVars {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}

	names := make([]string, 0, len(declared))
	for name := range declared {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		entry := declared[name]
		if entry.Secret == "" {
			env = append(env, name+"="+entry.Value)
			continue
		}
		if secrets == nil {
			return nil, fmt.Errorf("env %s refers to the secret %q, and no secrets are resolved", name, entry.Secret)
		}
		secret, ok := secrets.Lookup(entry.Secret)
		if !ok {
			return nil, fmt.Errorf("env %s refers to the unknown secret %q", name, entry.Secret)
		}
		env = append(env, name+"="+secret.Reveal())
	}
	return env, nil
}
