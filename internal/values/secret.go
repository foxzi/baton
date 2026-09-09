// Package values holds the value types of the scenario language
// (docs/ru/spec.md, section 5.3).
package values

import "fmt"

// Redacted is what a secret renders as everywhere except the few packages
// allowed to read its contents.
const Redacted = "***"

// Secret is an opaque secret value. It renders as Redacted through every
// standard formatting and marshalling path, so a secret that reaches a log
// line, run.json or a notification cannot leak by accident.
//
// The only way to read the contents is Reveal, which is restricted to the
// packages listed in docs/ru/spec.md, section 2: secrets, httpx, gateway,
// notify, engine and provider. The restriction is enforced by
// TestRevealCallersAreWhitelisted in this package.
type Secret struct {
	name  string
	value string
}

// NewSecret returns the named secret holding value.
func NewSecret(name, value string) Secret {
	return Secret{name: name, value: value}
}

// Name returns the name the scenario declared the secret under. Names are not
// sensitive: they appear in diagnostics.
func (s Secret) Name() string { return s.name }

// IsZero reports whether the secret holds no value.
func (s Secret) IsZero() bool { return s.value == "" }

// String implements fmt.Stringer for callers that render the value without
// fmt, such as a direct .String() call.
func (s Secret) String() string { return Redacted }

// Format covers every verb, including the ones fmt does not route through
// String: %d and %#v would otherwise fall back to printing the struct fields.
func (s Secret) Format(f fmt.State, verb rune) {
	switch verb {
	case 'q':
		fmt.Fprintf(f, "%q", Redacted)
	default:
		fmt.Fprint(f, Redacted)
	}
}

// MarshalText covers encoding.TextMarshaler and, through it, most encoders.
func (s Secret) MarshalText() ([]byte, error) { return []byte(Redacted), nil }

// MarshalJSON keeps secrets out of run.json and event streams.
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + Redacted + `"`), nil }

// MarshalYAML keeps secrets out of rendered YAML.
func (s Secret) MarshalYAML() (any, error) { return Redacted, nil }

// Reveal returns the secret contents.
//
// Callers outside the whitelist of the package documentation are a security
// defect; add the package to the whitelist only together with a review of why
// it needs plaintext secrets.
func (s Secret) Reveal() string { return s.value }
