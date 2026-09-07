// Package units parses the durations and byte sizes used in scenarios and
// API packs (spec sections 3.2, 3.3 and 7.4.2).
package units

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time span written as a Go duration string, such as 10m or 90s.
type Duration time.Duration

// UnmarshalYAML accepts a duration string or a plain number of seconds.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: duration must be a string like 10m", node.Line)
	}
	if secs, err := strconv.ParseFloat(node.Value, 64); err == nil {
		*d = Duration(float64(time.Second) * secs)
		return nil
	}
	parsed, err := time.ParseDuration(node.Value)
	if err != nil {
		return fmt.Errorf("line %d: %q is not a duration like 10m", node.Line, node.Value)
	}
	*d = Duration(parsed)
	return nil
}

// Duration returns the span as a time.Duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// String renders the span the way it is written in scenarios.
func (d Duration) String() string { return time.Duration(d).String() }

// ByteSize is a byte count written with an optional k, m or g suffix, as in
// max_output_bytes: 1m. Suffixes are powers of 1024.
type ByteSize int64

var byteUnits = []struct {
	suffix string
	factor int64
}{
	{"kb", 1 << 10}, {"mb", 1 << 20}, {"gb", 1 << 30},
	{"k", 1 << 10}, {"m", 1 << 20}, {"g", 1 << 30},
	{"b", 1},
}

// UnmarshalYAML accepts a plain byte count or a suffixed size.
func (b *ByteSize) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: size must be a number or a string like 1m", node.Line)
	}
	text := strings.ToLower(strings.TrimSpace(node.Value))
	factor := int64(1)
	for _, unit := range byteUnits {
		if rest, ok := strings.CutSuffix(text, unit.suffix); ok {
			text, factor = strings.TrimSpace(rest), unit.factor
			break
		}
	}
	count, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return fmt.Errorf("line %d: %q is not a size like 1m", node.Line, node.Value)
	}
	*b = ByteSize(count * factor)
	return nil
}

// Bytes returns the size in bytes.
func (b ByteSize) Bytes() int64 { return int64(b) }
