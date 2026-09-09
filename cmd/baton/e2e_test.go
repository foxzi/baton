package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/exitcode"
)

// The tests in this file are the only ones that leave the machine. Every
// other test in the tree answers "does the code do what it says" against a
// stub; these answer "is the thing on the other side still what the stub
// pretends it is" - a provider that changed its wire format, a model name
// that was retired, an agent CLI whose flags moved. That question cannot be
// answered offline, and its answer costs money, so both tests stay switched
// off until an environment variable turns them on:
//
//	BATON_E2E_PROVIDERS=1  one completion per configured llm provider
//	BATON_E2E=1            one real run of the claude-code agent
//
// A provider whose key is not in the environment is skipped rather than
// failed: a contributor with one key should still be able to check that one.
const (
	e2eProvidersVar = "BATON_E2E_PROVIDERS"
	e2eAgentVar     = "BATON_E2E"
)

// requireE2E skips unless the named variable is set to 1.
func requireE2E(t *testing.T, name string) {
	t.Helper()
	if os.Getenv(name) != "1" {
		t.Skipf("live test: set %s=1 to run it", name)
	}
}

// e2eProvider is one backend to check, with the model the check asks for.
// The model is deliberately the cheapest of each family: the point is the
// round trip, not the answer.
type e2eProvider struct {
	// name is both the provider's key in the configuration and the prefix
	// of the model reference, so the two cannot drift apart.
	name string
	kind string

	// keyVar is where the API key comes from, and modelVar overrides the
	// default model for an account that has no access to it.
	keyVar   string
	modelVar string
	model    string
}

var e2eProviders = []e2eProvider{
	{
		name:     "anthropic",
		kind:     "anthropic",
		keyVar:   "ANTHROPIC_API_KEY",
		modelVar: "BATON_E2E_ANTHROPIC_MODEL",
		model:    "claude-haiku-4-5-20251001",
	},
	{
		name:     "openai",
		kind:     "openai",
		keyVar:   "OPENAI_API_KEY",
		modelVar: "BATON_E2E_OPENAI_MODEL",
		model:    "gpt-4.1-nano",
	},
	{
		name:     "openrouter",
		kind:     "openrouter",
		keyVar:   "OPENROUTER_API_KEY",
		modelVar: "BATON_E2E_OPENROUTER_MODEL",
		model:    "openai/gpt-4.1-nano",
	},
}

// TestE2E_Providers asks every configured provider for one structured
// answer. It covers what a stub cannot: that the key works, that the model
// name still exists, that the structured-output mode the provider advertises
// is the one it honours, and that usage comes back priced.
func TestE2E_Providers(t *testing.T) {
	requireE2E(t, e2eProvidersVar)

	for _, backend := range e2eProviders {
		t.Run(backend.name, func(t *testing.T) {
			key := os.Getenv(backend.keyVar)
			if key == "" {
				t.Skipf("%s is not set", backend.keyVar)
			}
			model := backend.model
			if override := os.Getenv(backend.modelVar); override != "" {
				model = override
			}

			dir := t.TempDir()
			schemaDir := filepath.Join(dir, "schemas")
			if err := os.MkdirAll(schemaDir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			write := func(path, text string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
					t.Fatalf("write %s: %v", path, err)
				}
			}

			write(filepath.Join(schemaDir, "answer.json"), `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "required": ["capital"],
  "properties": { "capital": { "type": "string" } }
}`)

			write(filepath.Join(dir, "config.yaml"), fmt.Sprintf(`providers:
  %s:
    kind: %s
    api_key: { from: env, key: %s }
`, backend.name, backend.kind, backend.keyVar))

			write(filepath.Join(dir, "scenario.yaml"), fmt.Sprintf(`version: 1
name: e2e-provider
defaults:
  model: %s/%s
  timeout: 60s
budget:
  usd: 0.05
  time: 3m
steps:
  - id: ask
    llm:
      system: Answer with JSON only.
      prompt: What is the capital of France?
      schema: schemas/answer.json
      max_tokens: 64
`, backend.name, model))

			runsDir := filepath.Join(dir, "runs")
			cacheDir := filepath.Join(dir, "cache")

			var code int
			_, stderr := captureOutput(t, func() {
				code = run([]string{"run", filepath.Join(dir, "scenario.yaml"),
					"--config", filepath.Join(dir, "config.yaml"),
					"--runs-dir", runsDir,
					"--cache-dir", cacheDir,
					"--run-id", "e2e",
				})
			})
			if code != exitcode.OK {
				t.Fatalf("run() = %d, want %d; stderr:\n%s", code, exitcode.OK, stderr)
			}

			output := readStepOutput(t, runsDir, "e2e", "ask")
			result, ok := output["result"].(map[string]any)
			if !ok {
				t.Fatalf("step output carries no result object: %v", output)
			}
			capital, _ := result["capital"].(string)
			// Only the shape is asserted. A model that answers "Paris,
			// France" is not a regression, and a test that fails on the
			// wording would fail on a model upgrade instead of a bug.
			if capital == "" {
				t.Errorf("capital is empty: %v", result)
			}
			if !strings.Contains(strings.ToLower(capital), "paris") {
				t.Logf("unexpected answer %q; the round trip still worked", capital)
			}

			assertNoTokenOnDisk(t, runsDir, key)
			assertNoTokenOnDisk(t, cacheDir, key)
		})
	}
}

// TestE2E_ClaudeCode runs testdata/e2e/agent.yaml against the installed CLI.
// It is the only check that the adapter's flags, its MCP configuration and
// its settings file are still understood by the agent, and that a step can
// end through submit_result served by the gateway.
func TestE2E_ClaudeCode(t *testing.T) {
	requireE2E(t, e2eAgentVar)

	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("ANTHROPIC_API_KEY is not set")
	}
	// The adapter gives the CLI an empty environment and a home directory of
	// its own, so a subscription login on this machine would not be visible
	// to it; the key above is the only credential the step gets.
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skipf("the claude CLI is not in PATH: %v", err)
	}

	dir := t.TempDir()
	workspace := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	question := "Which planet is known as the Red Planet?"
	if err := os.WriteFile(filepath.Join(workspace, "question.txt"), []byte(question+"\n"), 0o600); err != nil {
		t.Fatalf("write question: %v", err)
	}

	runsDir := filepath.Join(dir, "runs")

	var code int
	_, stderr := captureOutput(t, func() {
		code = run([]string{"run", "testdata/e2e/agent.yaml",
			"--workspace", workspace,
			"--runs-dir", runsDir,
			"--cache-dir", filepath.Join(dir, "cache"),
			"--run-id", "e2e",
		})
	})
	if code != exitcode.OK {
		t.Fatalf("run() = %d, want %d; stderr:\n%s", code, exitcode.OK, stderr)
	}

	output := readStepOutput(t, runsDir, "e2e", "answer")
	result, ok := output["result"].(map[string]any)
	if !ok {
		t.Fatalf("step output carries no result object: %v", output)
	}
	answer, _ := result["answer"].(string)
	if answer == "" {
		t.Fatalf("the agent submitted an empty answer: %v", result)
	}
	if !strings.Contains(strings.ToLower(answer), "mars") {
		// The agent had to read the workspace file to answer at all, so a
		// wrong answer here means the read tool did not work.
		t.Errorf("answer = %q, want it to name Mars; the agent may not have read question.txt", answer)
	}

	assertNoTokenOnDisk(t, runsDir, key)
}
