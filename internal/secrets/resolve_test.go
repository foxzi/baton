package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/scenario"
)

// TestResolveFromEnv checks reading a secret from an environment variable:
// the happy path, an unset variable and a variable set to the empty string.
func TestResolveFromEnv(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		t.Setenv("BATON_TEST_SECRET", "s3cr3t-value")
		declared := map[string]scenario.Secret{
			"api_token": {From: scenario.SecretFromEnv, Key: "BATON_TEST_SECRET"},
		}
		store, err := Resolve(declared, ".")
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		secret, ok := store.Lookup("api_token")
		if !ok {
			t.Fatalf("Lookup(%q) ok = false, want true", "api_token")
		}
		if secret.Name() != "api_token" {
			t.Errorf("Name() = %q, want %q", secret.Name(), "api_token")
		}
		if secret.Reveal() != "s3cr3t-value" {
			t.Errorf("Reveal() = %q, want %q", secret.Reveal(), "s3cr3t-value")
		}
	})

	t.Run("not set", func(t *testing.T) {
		key := "BATON_TEST_SECRET_UNSET"
		os.Unsetenv(key)
		declared := map[string]scenario.Secret{
			"api_token": {From: scenario.SecretFromEnv, Key: key},
		}
		store, err := Resolve(declared, ".")
		if err == nil {
			t.Fatalf("Resolve() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "api_token") {
			t.Errorf("Resolve() error = %q, want mention of %q", err, "api_token")
		}
		if store != nil {
			t.Errorf("Resolve() store = %v, want nil", store)
		}
	})

	t.Run("set to empty string", func(t *testing.T) {
		t.Setenv("BATON_TEST_SECRET_EMPTY", "")
		declared := map[string]scenario.Secret{
			"api_token": {From: scenario.SecretFromEnv, Key: "BATON_TEST_SECRET_EMPTY"},
		}
		store, err := Resolve(declared, ".")
		if err == nil {
			t.Fatalf("Resolve() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "api_token") {
			t.Errorf("Resolve() error = %q, want mention of %q", err, "api_token")
		}
		if store != nil {
			t.Errorf("Resolve() store = %v, want nil", store)
		}
	})
}

// TestResolveFromFile checks reading a secret from a file: absolute paths
// with and without trimming, a relative path resolved against baseDir, and a
// missing file.
func TestResolveFromFile(t *testing.T) {
	t.Run("absolute path, no trim keeps trailing newline", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "secret.txt")
		if err := os.WriteFile(path, []byte("value\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		declared := map[string]scenario.Secret{
			"file_secret": {From: scenario.SecretFromFile, Path: path, Trim: false},
		}
		store, err := Resolve(declared, ".")
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		secret, ok := store.Lookup("file_secret")
		if !ok {
			t.Fatalf("Lookup(%q) ok = false, want true", "file_secret")
		}
		if want := "value\n"; secret.Reveal() != want {
			t.Errorf("Reveal() = %q, want %q", secret.Reveal(), want)
		}
	})

	t.Run("absolute path, trim strips surrounding whitespace", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "secret.txt")
		if err := os.WriteFile(path, []byte("  value \n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		declared := map[string]scenario.Secret{
			"file_secret": {From: scenario.SecretFromFile, Path: path, Trim: true},
		}
		store, err := Resolve(declared, ".")
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		secret, ok := store.Lookup("file_secret")
		if !ok {
			t.Fatalf("Lookup(%q) ok = false, want true", "file_secret")
		}
		if want := "value"; secret.Reveal() != want {
			t.Errorf("Reveal() = %q, want %q", secret.Reveal(), want)
		}
	})

	t.Run("relative path resolved against baseDir", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("relative-value"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		declared := map[string]scenario.Secret{
			"file_secret": {From: scenario.SecretFromFile, Path: "secret.txt"},
		}
		store, err := Resolve(declared, dir)
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		secret, ok := store.Lookup("file_secret")
		if !ok {
			t.Fatalf("Lookup(%q) ok = false, want true", "file_secret")
		}
		if want := "relative-value"; secret.Reveal() != want {
			t.Errorf("Reveal() = %q, want %q", secret.Reveal(), want)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		dir := t.TempDir()
		declared := map[string]scenario.Secret{
			"file_secret": {From: scenario.SecretFromFile, Path: filepath.Join(dir, "does-not-exist.txt")},
		}
		store, err := Resolve(declared, ".")
		if err == nil {
			t.Fatalf("Resolve() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "file_secret") {
			t.Errorf("Resolve() error = %q, want mention of %q", err, "file_secret")
		}
		if store != nil {
			t.Errorf("Resolve() store = %v, want nil", store)
		}
	})
}

// TestResolveMultipleFailures checks that when several secrets cannot be
// resolved, the single returned error mentions every one of them.
func TestResolveMultipleFailures(t *testing.T) {
	os.Unsetenv("BATON_TEST_SECRET_MISSING_A")
	declared := map[string]scenario.Secret{
		"secret_a": {From: scenario.SecretFromEnv, Key: "BATON_TEST_SECRET_MISSING_A"},
		"secret_b": {From: scenario.SecretFromFile, Path: "/no/such/file/for/baton/tests"},
	}
	_, err := Resolve(declared, ".")
	if err == nil {
		t.Fatalf("Resolve() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "secret_a") {
		t.Errorf("Resolve() error = %q, want mention of %q", err, "secret_a")
	}
	if !strings.Contains(err.Error(), "secret_b") {
		t.Errorf("Resolve() error = %q, want mention of %q", err, "secret_b")
	}
}

// TestStoreNamesAndLookup checks that Names returns the declared names
// sorted, and that Lookup of an undeclared name reports ok = false.
func TestStoreNamesAndLookup(t *testing.T) {
	t.Setenv("BATON_TEST_SECRET_ONE", "one-value")
	t.Setenv("BATON_TEST_SECRET_TWO", "two-value")
	declared := map[string]scenario.Secret{
		"zebra": {From: scenario.SecretFromEnv, Key: "BATON_TEST_SECRET_ONE"},
		"alpha": {From: scenario.SecretFromEnv, Key: "BATON_TEST_SECRET_TWO"},
	}
	store, err := Resolve(declared, ".")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	names := store.Names()
	want := []string{"alpha", "zebra"}
	if len(names) != len(want) {
		t.Fatalf("Names() = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("Names()[%d] = %q, want %q", i, names[i], want[i])
		}
	}

	if _, ok := store.Lookup("does_not_exist"); ok {
		t.Errorf("Lookup(%q) ok = true, want false", "does_not_exist")
	}
}

// TestNilStore checks that every Store method is safe to call on a nil
// *Store and behaves as if the store were empty.
func TestNilStore(t *testing.T) {
	var store *Store

	if _, ok := store.Lookup("anything"); ok {
		t.Errorf("Lookup() ok = true, want false")
	}
	if names := store.Names(); names != nil {
		t.Errorf("Names() = %v, want nil", names)
	}
	if r := store.Redactor(); r != nil {
		t.Errorf("Redactor() = %v, want nil", r)
	}

	text := "unaffected text"
	if got := store.Redactor().String(text); got != text {
		t.Errorf("Redactor().String() = %q, want unchanged %q", got, text)
	}
}

// TestStoreRedactor checks that the redactor returned by a resolved store
// redacts the secret's value.
func TestStoreRedactor(t *testing.T) {
	t.Setenv("BATON_TEST_SECRET_REDACT", "top-secret-value")
	declared := map[string]scenario.Secret{
		"api_token": {From: scenario.SecretFromEnv, Key: "BATON_TEST_SECRET_REDACT"},
	}
	store, err := Resolve(declared, ".")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	text := "authorization: top-secret-value"
	got := store.Redactor().String(text)
	want := "authorization: ***"
	if got != want {
		t.Errorf("Redactor().String() = %q, want %q", got, want)
	}
}
