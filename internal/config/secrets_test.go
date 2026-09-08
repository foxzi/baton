package config

import (
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/scenario"
)

// TestResolveAPISecrets checks that the secret a global apis entry
// authorises with is read from the configuration's own secrets block, and
// that an entry without auth needs none (sections 7.4.1 and 12).
func TestResolveAPISecrets(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "bot-token-42")
	cfg := &Config{
		APIs: map[string]scenario.API{
			"telegram": {Pack: "telegram", Auth: scenario.APIAuth{Secret: "tg_bot"}},
			"public":   {Pack: "statuspage"},
		},
		Secrets: map[string]SecretRef{
			"tg_bot": {From: scenario.SecretFromEnv, Key: "TELEGRAM_BOT_TOKEN"},
		},
	}

	secrets, err := cfg.ResolveAPISecrets("")
	if err != nil {
		t.Fatalf("ResolveAPISecrets: %v", err)
	}
	if len(secrets) != 1 {
		t.Fatalf("resolved %d secrets, want 1: %v", len(secrets), secrets)
	}
	if got := secrets["tg_bot"].Reveal(); got != "bot-token-42" {
		t.Errorf("tg_bot = %q, want the value of the environment variable", got)
	}
}

// TestResolveAPISecretsUndeclared checks that an entry naming a secret the
// configuration does not declare is reported instead of silently sending an
// unauthorised request.
func TestResolveAPISecretsUndeclared(t *testing.T) {
	cfg := &Config{APIs: map[string]scenario.API{
		"telegram": {Pack: "telegram", Auth: scenario.APIAuth{Secret: "tg_bot"}},
	}}
	_, err := cfg.ResolveAPISecrets("")
	if err == nil || !strings.Contains(err.Error(), "undeclared secret") {
		t.Errorf("ResolveAPISecrets error = %v, want mention of undeclared secret", err)
	}
}

// TestResolveAPISecretsMissingValue checks that a declared but unreadable
// secret fails, unless the entry marked it optional.
func TestResolveAPISecretsMissingValue(t *testing.T) {
	cfg := &Config{
		APIs:    map[string]scenario.API{"telegram": {Pack: "telegram", Auth: scenario.APIAuth{Secret: "tg_bot"}}},
		Secrets: map[string]SecretRef{"tg_bot": {From: scenario.SecretFromEnv, Key: "BATON_TEST_MISSING_TOKEN"}},
	}
	if _, err := cfg.ResolveAPISecrets(""); err == nil {
		t.Errorf("ResolveAPISecrets error = nil, want the unset variable reported")
	}

	cfg.Secrets["tg_bot"] = SecretRef{From: scenario.SecretFromEnv, Key: "BATON_TEST_MISSING_TOKEN", Optional: true}
	secrets, err := cfg.ResolveAPISecrets("")
	if err != nil {
		t.Fatalf("ResolveAPISecrets with optional: %v", err)
	}
	if _, ok := secrets["tg_bot"]; ok {
		t.Errorf("optional secret is present, want it left out")
	}
}
