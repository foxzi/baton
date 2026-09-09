package engine

import (
	"testing"

	"github.com/foxzi/baton/internal/runstore"
)

// A step that pushes the run over budget.tokens fails with the budget class.
func TestEngine_SpendTokenBudgetExceeded(t *testing.T) {
	yamlText := `
version: 1
name: tokens
budget:
  tokens: 100
steps:
  - id: one
    run:
      argv: ["echo", "hi"]
      parse: text
`
	eng, _, _ := newTestEngine(t, yamlText, nil)

	if err := eng.spend(runstore.CostEntry{Step: "one", InputTokens: 40, OutputTokens: 40}); err != nil {
		t.Fatalf("first spend: %+v, want nil", err)
	}
	err := eng.spend(runstore.CostEntry{Step: "two", InputTokens: 60, OutputTokens: 0})
	if err == nil {
		t.Fatal("spend over budget.tokens: want error, got nil")
	}
	if err.Class != ClassBudget {
		t.Fatalf("Class = %s, want %s", err.Class, ClassBudget)
	}
	want := "run token budget exceeded: spent 140 of 100 tokens"
	if err.Msg != want {
		t.Errorf("Msg = %q, want %q", err.Msg, want)
	}
}

// budget.tokens == 0 (unset) means no token limit is enforced.
func TestEngine_SpendNoTokenBudget(t *testing.T) {
	yamlText := `
version: 1
name: tokens
steps:
  - id: one
    run:
      argv: ["echo", "hi"]
      parse: text
`
	eng, _, _ := newTestEngine(t, yamlText, nil)

	if err := eng.spend(runstore.CostEntry{Step: "one", InputTokens: 1_000_000, OutputTokens: 1_000_000}); err != nil {
		t.Fatalf("spend: %+v, want nil", err)
	}
}
