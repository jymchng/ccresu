package config

import (
	"errors"
	"strings"
	"testing"
)

// validDetectorConfig returns a config that passes validation so each test can
// break exactly one thing.
func validDetectorConfig(t *testing.T) Config {
	t.Helper()
	cfg := Defaults()
	// The API token path has no default; validation requires it to be set.
	cfg.Server.TokenPath = "/tmp/ccresu-test-token"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
	return cfg
}

func TestDetectorDefaults(t *testing.T) {
	t.Parallel()

	cfg := Defaults()
	if cfg.Detector.Threshold != DefaultDetectorThreshold {
		t.Fatalf("threshold = %v, want %v", cfg.Detector.Threshold, DefaultDetectorThreshold)
	}
	if cfg.Detector.CustomPatterns == nil {
		t.Fatal("custom patterns must be an empty slice, not nil")
	}
	if len(cfg.Detector.CustomPatterns) != 0 {
		t.Fatalf("custom patterns = %v, want none", cfg.Detector.CustomPatterns)
	}
}

func TestDetectorValidationAcceptsAGoodRule(t *testing.T) {
	t.Parallel()

	cfg := validDetectorConfig(t)
	cfg.Detector.CustomPatterns = []DetectorPattern{{
		ID:            "proxy-limit",
		Kind:          "five_hour",
		Pattern:       `proxy quota exhausted`,
		Confidence:    0.9,
		RequiresReset: true,
		Description:   "a corporate proxy notice",
	}, {
		ID:         "bare-rule",
		Pattern:    `\bparked\b`,
		Confidence: 0.5,
	}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestDetectorValidationRejectsBadRules(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{
			name:   "threshold of zero",
			mutate: func(c *Config) { c.Detector.Threshold = 0 },
			want:   "detector.threshold must be in the range (0, 1]",
		},
		{
			name:   "threshold above one",
			mutate: func(c *Config) { c.Detector.Threshold = 1.5 },
			want:   "detector.threshold must be in the range (0, 1]",
		},
		{
			name: "empty id",
			mutate: func(c *Config) {
				c.Detector.CustomPatterns = []DetectorPattern{{ID: "  ", Pattern: "x", Confidence: 0.5}}
			},
			want: "id must not be empty",
		},
		{
			name: "duplicate id",
			mutate: func(c *Config) {
				c.Detector.CustomPatterns = []DetectorPattern{
					{ID: "same", Pattern: "a", Confidence: 0.5},
					{ID: "same", Pattern: "b", Confidence: 0.5},
				}
			},
			want: "is duplicated",
		},
		{
			name: "empty pattern",
			mutate: func(c *Config) {
				c.Detector.CustomPatterns = []DetectorPattern{{ID: "x", Pattern: " ", Confidence: 0.5}}
			},
			want: "pattern must not be empty",
		},
		{
			name: "invalid regular expression",
			mutate: func(c *Config) {
				c.Detector.CustomPatterns = []DetectorPattern{{ID: "x", Pattern: "[unclosed", Confidence: 0.5}}
			},
			want: "not a valid regular expression",
		},
		{
			name: "zero confidence",
			mutate: func(c *Config) {
				c.Detector.CustomPatterns = []DetectorPattern{{ID: "x", Pattern: "x", Confidence: 0}}
			},
			want: "confidence must be in the range (0, 1]",
		},
		{
			name: "confidence above one",
			mutate: func(c *Config) {
				c.Detector.CustomPatterns = []DetectorPattern{{ID: "x", Pattern: "x", Confidence: 2}}
			},
			want: "confidence must be in the range (0, 1]",
		},
		{
			name: "unknown kind",
			mutate: func(c *Config) {
				c.Detector.CustomPatterns = []DetectorPattern{{ID: "x", Pattern: "x", Confidence: 0.5, Kind: "hourly"}}
			},
			want: "kind must be empty, five_hour, weekly or other",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := Defaults()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("expected a validation error")
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want it to wrap ErrInvalid", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestDetectorValidationAcceptsEveryKind(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"", "five_hour", "weekly", "other", "WEEKLY"} {
		cfg := validDetectorConfig(t)
		cfg.Detector.CustomPatterns = []DetectorPattern{{ID: "x", Pattern: "x", Confidence: 0.5, Kind: kind}}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("kind %q must be accepted: %v", kind, err)
		}
	}
}

func TestDetectorThresholdEnvironmentOverride(t *testing.T) {
	t.Parallel()

	cfg := Defaults()
	lookup := func(key string) (string, bool) {
		if key == EnvPrefix+"DETECTOR__THRESHOLD" {
			return "0.75", true
		}
		return "", false
	}
	if err := applyEnv(&cfg, lookup); err != nil {
		t.Fatalf("applyEnv: %v", err)
	}
	if cfg.Detector.Threshold != 0.75 {
		t.Fatalf("threshold = %v, want 0.75", cfg.Detector.Threshold)
	}
}
