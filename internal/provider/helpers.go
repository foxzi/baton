package provider

import (
	"fmt"
	"strings"

	"github.com/foxzi/baton/internal/config"
	"github.com/foxzi/baton/internal/values"
)

// SplitModel splits a scenario model reference into its provider name and the
// provider-side model name. The separator is the first slash, so an
// OpenRouter model that itself contains a slash still works (spec section
// 3.5).
func SplitModel(ref string) (providerName, model string, err error) {
	slash := strings.Index(ref, "/")
	if slash <= 0 || slash == len(ref)-1 {
		return "", "", errorf(ClassConfig, "model %q must be <provider>/<model>", ref)
	}
	return ref[:slash], ref[slash+1:], nil
}

// applyCaps overlays the capability overrides of a config entry on what the
// implementation reports by itself (spec section 8.3).
func applyCaps(base Caps, override *config.Capabilities) Caps {
	if override == nil {
		return base
	}
	if override.StructuredOutput != nil {
		base.StructuredOutput = *override.StructuredOutput
	}
	if override.Tools != nil {
		base.Tools = *override.Tools
	}
	return base
}

// requireKey returns the plaintext API key, or a config error when a provider
// that needs one was given nothing.
func requireKey(kind config.ProviderKind, apiKey values.Secret) (string, error) {
	if apiKey.IsZero() {
		return "", errorf(ClassConfig, "provider kind %s needs an api_key", kind)
	}
	return apiKey.Reveal(), nil
}

// maxBodyTail is how much of a failed response body goes into an error
// message. Long provider errors are truncated, like everywhere else in the
// runner.
const maxBodyTail = 2048

// bodyTail trims a response body down to what an error message may carry.
func bodyTail(data []byte) string {
	text := strings.TrimSpace(string(data))
	if len(text) <= maxBodyTail {
		return text
	}
	return fmt.Sprintf("%s... (%d bytes)", text[:maxBodyTail], len(text))
}
