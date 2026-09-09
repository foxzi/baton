package units_test

import (
	"strings"
	"testing"
	"time"

	"github.com/foxzi/baton/internal/units"
	"gopkg.in/yaml.v3"
)

// doc is the shape both types are parsed through, so the tests exercise the
// same path a scenario field goes through.
type doc struct {
	Timeout units.Duration `yaml:"timeout"`
	Size    units.ByteSize `yaml:"size"`
}

func TestDurationUnmarshal(t *testing.T) {
	tests := []struct {
		yaml string
		want time.Duration
	}{
		{"timeout: 10m", 10 * time.Minute},
		{"timeout: 90s", 90 * time.Second},
		{"timeout: 1h30m", 90 * time.Minute},
		{"timeout: 250ms", 250 * time.Millisecond},
		// A bare number is a count of seconds, whole or fractional.
		{"timeout: 30", 30 * time.Second},
		{"timeout: 1.5", 1500 * time.Millisecond},
		{"timeout: 0", 0},
		{`timeout: "10m"`, 10 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.yaml, func(t *testing.T) {
			var got doc
			if err := yaml.Unmarshal([]byte(tt.yaml), &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got.Timeout.Duration() != tt.want {
				t.Errorf("Duration() = %v, want %v", got.Timeout.Duration(), tt.want)
			}
		})
	}
}

func TestDurationUnmarshalErrors(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{"not a duration", "timeout: soon"},
		{"unknown unit", "timeout: 10y"},
		{"empty", `timeout: ""`},
		{"a list is not a scalar", "timeout: [10m]"},
		{"a mapping is not a scalar", "timeout: {value: 10m}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got doc
			err := yaml.Unmarshal([]byte(tt.yaml), &got)
			if err == nil {
				t.Fatalf("Unmarshal(%s) error = nil, want an error", tt.yaml)
			}
			if !strings.Contains(err.Error(), "duration") {
				t.Errorf("error = %q, want it to say what a duration looks like", err)
			}
		})
	}
}

func TestDurationString(t *testing.T) {
	if got := units.Duration(90 * time.Second).String(); got != "1m30s" {
		t.Errorf("String() = %q, want 1m30s", got)
	}
}

func TestByteSizeUnmarshal(t *testing.T) {
	tests := []struct {
		yaml string
		want int64
	}{
		{"size: 1024", 1024},
		{"size: 0", 0},
		{"size: 1k", 1 << 10},
		{"size: 1m", 1 << 20},
		{"size: 1g", 1 << 30},
		// The two-letter forms must win over the one-letter ones, otherwise
		// 1kb would be read as the unparsable "1k".
		{"size: 1kb", 1 << 10},
		{"size: 1mb", 1 << 20},
		{"size: 1gb", 1 << 30},
		{"size: 512b", 512},
		{"size: 2M", 2 << 20},
		{`size: "1 m"`, 1 << 20},
		{`size: " 4k "`, 4 << 10},
	}
	for _, tt := range tests {
		t.Run(tt.yaml, func(t *testing.T) {
			var got doc
			if err := yaml.Unmarshal([]byte(tt.yaml), &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got.Size.Bytes() != tt.want {
				t.Errorf("Bytes() = %d, want %d", got.Size.Bytes(), tt.want)
			}
		})
	}
}

func TestByteSizeUnmarshalErrors(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{"not a number", "size: big"},
		{"unknown unit", "size: 1t"},
		{"fractional", "size: 1.5m"},
		{"empty", `size: ""`},
		{"suffix without a count", "size: m"},
		{"a list is not a scalar", "size: [1m]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got doc
			err := yaml.Unmarshal([]byte(tt.yaml), &got)
			if err == nil {
				t.Fatalf("Unmarshal(%s) error = nil, want an error", tt.yaml)
			}
			if !strings.Contains(err.Error(), "size") {
				t.Errorf("error = %q, want it to say what a size looks like", err)
			}
		})
	}
}
