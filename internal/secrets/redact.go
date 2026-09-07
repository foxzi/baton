package secrets

import (
	"encoding/base64"
	"net/url"
	"sort"
	"strings"

	"github.com/foxzi/baton/internal/values"
)

// Redactor replaces secret values with values.Redacted.
//
// Encoded forms are covered as well, because a secret that travels through a
// URL or an HTTP header often arrives in the log base64- or percent-encoded
// (docs/ru/spec.md, section 6). No minimum length applies: a short secret is
// still a secret, so noisy redaction is preferred over a leak.
type Redactor struct {
	replacer *strings.Replacer
}

// NewRedactor returns a redactor covering the given secrets. Secrets with an
// empty or whitespace-only value are ignored, since redacting whitespace
// would destroy unrelated output.
func NewRedactor(secretList ...values.Secret) *Redactor {
	seen := make(map[string]bool)
	var patterns []string
	for _, secret := range secretList {
		for _, form := range encodedForms(secret.Reveal()) {
			if strings.TrimSpace(form) == "" || seen[form] {
				continue
			}
			seen[form] = true
			patterns = append(patterns, form)
		}
	}
	if len(patterns) == 0 {
		return &Redactor{}
	}

	// Longest first, so that a short encoding cannot shadow a longer form
	// starting at the same position: strings.Replacer prefers the pattern
	// given earliest at a given offset.
	sort.Slice(patterns, func(i, j int) bool {
		if len(patterns[i]) != len(patterns[j]) {
			return len(patterns[i]) > len(patterns[j])
		}
		return patterns[i] < patterns[j]
	})

	pairs := make([]string, 0, len(patterns)*2)
	for _, pattern := range patterns {
		pairs = append(pairs, pattern, values.Redacted)
	}
	return &Redactor{replacer: strings.NewReplacer(pairs...)}
}

// encodedForms returns the plaintext value and the encodings a secret is
// likely to appear in.
func encodedForms(value string) []string {
	if value == "" {
		return nil
	}
	return []string{
		value,
		base64.StdEncoding.EncodeToString([]byte(value)),
		base64.RawStdEncoding.EncodeToString([]byte(value)),
		base64.URLEncoding.EncodeToString([]byte(value)),
		base64.RawURLEncoding.EncodeToString([]byte(value)),
		url.QueryEscape(value),
		url.PathEscape(value),
	}
}

// String redacts every known secret form in text.
func (r *Redactor) String(text string) string {
	if r == nil || r.replacer == nil {
		return text
	}
	return r.replacer.Replace(text)
}

// Bytes redacts every known secret form in data, returning a new slice when
// anything changed.
func (r *Redactor) Bytes(data []byte) []byte {
	if r == nil || r.replacer == nil {
		return data
	}
	redacted := r.replacer.Replace(string(data))
	if redacted == string(data) {
		return data
	}
	return []byte(redacted)
}
