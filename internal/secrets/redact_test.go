package secrets

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/values"
)

// TestRedactorString checks that a plaintext secret is replaced by "***",
// including when it occurs more than once in the same text.
func TestRedactorString(t *testing.T) {
	cases := []struct {
		name   string
		secret string
		text   string
		want   string
	}{
		{
			name:   "single occurrence in a longer string",
			secret: "hunter2",
			text:   "please use hunter2 as the password",
			want:   "please use *** as the password",
		},
		{
			name:   "multiple occurrences all replaced",
			secret: "hunter2",
			text:   "a=hunter2 b=hunter2 c=hunter2",
			want:   "a=*** b=*** c=***",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRedactor(values.NewSecret("s", tc.secret))
			if got := r.String(tc.text); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRedactorEncodedForms checks that a secret is redacted when it appears
// base64- or percent-encoded, as it would in a URL or an HTTP header.
func TestRedactorEncodedForms(t *testing.T) {
	secret := "p@ss/word+value="

	cases := []struct {
		name   string
		encode func(string) string
	}{
		{name: "base64 std", encode: func(v string) string {
			return base64.StdEncoding.EncodeToString([]byte(v))
		}},
		{name: "base64 raw std", encode: func(v string) string {
			return base64.RawStdEncoding.EncodeToString([]byte(v))
		}},
		{name: "base64 url", encode: func(v string) string {
			return base64.URLEncoding.EncodeToString([]byte(v))
		}},
		{name: "percent-encoded", encode: url.QueryEscape},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded := tc.encode(secret)
			r := NewRedactor(values.NewSecret("s", secret))
			text := "value=" + encoded + " end"
			got := r.String(text)
			want := "value=*** end"
			if got != want {
				t.Errorf("String() = %q, want %q", got, want)
			}
		})
	}
}

// TestRedactorSubstringSecrets checks that when one secret's plaintext is a
// substring of another, the longer secret is fully redacted and no fragment
// of either plaintext leaks through.
func TestRedactorSubstringSecrets(t *testing.T) {
	short := "secret"
	long := "secretlong"

	r := NewRedactor(values.NewSecret("short", short), values.NewSecret("long", long))
	text := "token=" + long + " other=" + short
	got := r.String(text)
	want := "token=*** other=***"
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if strings.Contains(got, "secret") || strings.Contains(got, "long") {
		t.Errorf("String() = %q, still contains a fragment of a secret", got)
	}
}

// TestRedactorIgnoresEmptyAndWhitespaceSecrets checks that a secret whose
// value is empty or made only of whitespace is ignored, so that text passes
// through unchanged instead of having whitespace destroyed.
func TestRedactorIgnoresEmptyAndWhitespaceSecrets(t *testing.T) {
	r := NewRedactor(
		values.NewSecret("empty", ""),
		values.NewSecret("blank", "   \t\n"),
	)
	text := "hello   world\n"
	if got := r.String(text); got != text {
		t.Errorf("String() = %q, want unchanged %q", got, text)
	}
	if got := r.Bytes([]byte(text)); string(got) != text {
		t.Errorf("Bytes() = %q, want unchanged %q", got, text)
	}
}

// TestRedactorNilAndEmpty checks that a redactor built with no secrets, and a
// nil *Redactor, both pass text through unchanged.
func TestRedactorNilAndEmpty(t *testing.T) {
	cases := []struct {
		name string
		r    *Redactor
	}{
		{name: "no secrets", r: NewRedactor()},
		{name: "nil redactor", r: nil},
	}

	text := "nothing sensitive here"
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.r.String(text); got != text {
				t.Errorf("String() = %q, want unchanged %q", got, text)
			}
			if got := tc.r.Bytes([]byte(text)); string(got) != text {
				t.Errorf("Bytes() = %q, want unchanged %q", got, text)
			}
		})
	}
}

// TestRedactorBytes checks that Bytes returns the input slice unchanged when
// nothing matched, and redacted content when it did.
func TestRedactorBytes(t *testing.T) {
	r := NewRedactor(values.NewSecret("s", "hunter2"))

	t.Run("no match returns the same slice", func(t *testing.T) {
		data := []byte("nothing to see here")
		got := r.Bytes(data)
		if string(got) != string(data) {
			t.Fatalf("Bytes() = %q, want unchanged %q", got, data)
		}
		if len(data) > 0 && &got[0] != &data[0] {
			t.Errorf("Bytes() returned a different slice, want the same underlying array")
		}
	})

	t.Run("match returns redacted content", func(t *testing.T) {
		data := []byte("password is hunter2")
		got := r.Bytes(data)
		want := "password is ***"
		if string(got) != want {
			t.Errorf("Bytes() = %q, want %q", got, want)
		}
	})
}
