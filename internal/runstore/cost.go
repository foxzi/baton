package runstore

import (
	"encoding/json"
	"fmt"
	"path/filepath"
)

// CostEntry is what one step spent (docs/ru/spec.md, section 10.1). CostUSD
// is a pointer because a model missing from the pricing table has no known
// cost, which is not the same as costing nothing (section 8.3).
type CostEntry struct {
	Step         string   `json:"step"`
	Model        string   `json:"model,omitempty"`
	InputTokens  int      `json:"input_tokens"`
	OutputTokens int      `json:"output_tokens"`
	CachedTokens int      `json:"cached_tokens,omitempty"`
	CostUSD      *float64 `json:"cost_usd"`
	Duration     string   `json:"duration"`
}

// CostReport is cost.json: the per-step spending of a run plus its totals.
type CostReport struct {
	SchemaVersion     int         `json:"schema_version"`
	TotalUSD          *float64    `json:"total_usd"`
	TotalInputTokens  int         `json:"total_input_tokens"`
	TotalOutputTokens int         `json:"total_output_tokens"`
	Steps             []CostEntry `json:"steps"`
}

// WriteCost writes cost.json atomically.
func (s *Store) WriteCost(report *CostReport) error {
	if report.Steps == nil {
		report.Steps = []CostEntry{}
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("runstore: marshal cost.json: %w", err)
	}
	data = s.redact(data)
	if err := atomicWrite(filepath.Join(s.dir, "cost.json"), data); err != nil {
		return fmt.Errorf("runstore: write cost.json: %w", err)
	}
	return nil
}
