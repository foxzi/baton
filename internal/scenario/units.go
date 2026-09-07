package scenario

import "github.com/foxzi/baton/internal/units"

// Duration is a time span written as a Go duration string, such as 10m.
type Duration = units.Duration

// ByteSize is a byte count written with an optional k, m or g suffix.
type ByteSize = units.ByteSize
