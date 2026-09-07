package provider

import (
	"github.com/foxzi/baton/internal/config"
	"github.com/foxzi/baton/internal/values"
)

// New builds the provider named by a config entry (spec section 8.3). The API
// key is already resolved; a provider whose key is optional may be given a
// zero secret.
func New(name string, cfg config.Provider, apiKey values.Secret) (Provider, error) {
	switch cfg.Kind {
	case config.ProviderAnthropic:
		return newAnthropic(cfg, apiKey)
	case config.ProviderOpenAI:
		return newOpenAI(cfg, apiKey)
	case config.ProviderOpenRouter, config.ProviderOpenAICompatible:
		// One implementation serves both: openrouter is openai_compatible
		// with a preset base URL, required headers and a cost parser.
		return newCompat(cfg, apiKey)
	case "":
		return nil, errorf(ClassConfig, "provider %q has no kind", name)
	default:
		return nil, errorf(ClassConfig, "provider %q has unknown kind %q", name, cfg.Kind)
	}
}
