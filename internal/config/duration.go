package config

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that unmarshals from Go duration strings such as
// "30s", "5m" or "1h30m". It also accepts a bare integer as seconds so that
// hand-written configs cannot silently produce a zero duration by mistake.
type Duration struct {
	time.Duration
}

// NewDuration returns a Duration wrapping d.
func NewDuration(d time.Duration) Duration { return Duration{Duration: d} }

// MarshalYAML renders the duration in its canonical Go form ("30s").
func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }

// UnmarshalYAML parses a duration from a string or integer node.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var raw string
	if err := value.Decode(&raw); err != nil {
		return fmt.Errorf("duration must be a string such as \"30s\": %w", err)
	}
	parsed, err := ParseDuration(raw)
	if err != nil {
		return err
	}
	d.Duration = parsed
	return nil
}

// ParseDuration parses a Go duration string and rejects empty input.
func ParseDuration(s string) (time.Duration, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0, fmt.Errorf("duration is empty")
	}
	d, err := time.ParseDuration(trimmed)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: expected a value such as \"30s\" or \"5m\"", s)
	}
	if d < 0 {
		return 0, fmt.Errorf("invalid duration %q: must not be negative", s)
	}
	return d, nil
}
