package config_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jymchng/ccresu/internal/config"
)

// TestVerifyUnknownKeysRejected proves a typo can never be silently ignored.
func TestVerifyUnknownKeysRejected(t *testing.T) {
	cases := map[string]string{
		"top-level typo":  "appp:\n  name: x\n",
		"nested typo":     "recovery:\n  max_attempt: 5\n",
		"deep typo":       "telegram:\n  notify_waitng: true\n",
		"deprecated path": "schedular:\n  poll_interval: 30s\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := config.Parse([]byte(doc))
			if err == nil {
				t.Fatalf("expected an error for %q, got nil (silently ignored!)", doc)
			}
			if !errors.Is(err, config.ErrInvalid) {
				t.Fatalf("error %v is not errors.Is(ErrInvalid)", err)
			}
			if !strings.Contains(err.Error(), "field") && !strings.Contains(err.Error(), "not found") {
				t.Fatalf("error does not identify the unknown field: %v", err)
			}
		})
	}
}

// TestVerifyBadEnvValuesRejected proves a malformed override names its variable.
func TestVerifyBadEnvValuesRejected(t *testing.T) {
	cases := map[string]string{
		"CCRESU_MONITORING__ENABLED":               "maybe",
		"CCRESU_SERVER__PORT":                      "eighty",
		"CCRESU_RECOVERY__MAX_ATTEMPTS":            "three",
		"CCRESU_RECOVERY__MULTIPLIER":              "1.2.3",
		"CCRESU_SCHEDULER__POLL_INTERVAL":          "30",
		"CCRESU_CLAUDE__DISCOVERY_INTERVAL":        "",
		"CCRESU_RECOVERY__INITIAL_DELAY":           "-5s",
		"CCRESU_TELEGRAM__NOTIFY_RECOVERY_SUCCESS": "yes please",
	}
	for key, value := range cases {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, value)
			_, err := config.Parse([]byte("app:\n  name: x\n"))
			if err == nil {
				t.Fatalf("%s=%q was silently ignored", key, value)
			}
			if !errors.Is(err, config.ErrInvalid) {
				t.Fatalf("error %v is not errors.Is(ErrInvalid)", err)
			}
			if !strings.Contains(err.Error(), key) {
				t.Fatalf("error does not name %s: %v", key, err)
			}
		})
	}
}

// TestVerifyDurationsRejected checks unit-less, empty and negative durations.
func TestVerifyDurationsRejected(t *testing.T) {
	for _, raw := range []string{"", "  ", "30", "abc", "-30s", "5 minutes"} {
		if _, err := config.ParseDuration(raw); err == nil {
			t.Fatalf("ParseDuration(%q) accepted an invalid duration", raw)
		}
	}
	for raw, want := range map[string]string{"30s": "30s", "5m": "5m0s", "1h30m": "1h30m0s"} {
		got, err := config.ParseDuration(raw)
		if err != nil {
			t.Fatalf("ParseDuration(%q): %v", raw, err)
		}
		if got.String() != want {
			t.Fatalf("ParseDuration(%q) = %s, want %s", raw, got, want)
		}
	}
}

// TestVerifyAllProblemsAggregated proves validation reports every problem.
func TestVerifyAllProblemsAggregated(t *testing.T) {
	_, err := config.Parse([]byte(`
app:
  name: ""
  log_level: verbose
  log_format: xml
  timezone: Mars/Olympus
recovery:
  max_attempts: 0
  multiplier: 0.5
  jitter: 2
server:
  enabled: true
  port: 99999
storage:
  driver: postgres
`))
	if err == nil {
		t.Fatal("expected validation failures")
	}
	var verr *config.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if len(verr.Problems) < 8 {
		t.Fatalf("expected at least 8 aggregated problems, got %d: %v", len(verr.Problems), verr.Problems)
	}
	joined := strings.Join(verr.Problems, "\n")
	for _, want := range []string{"app.name", "log_level", "log_format", "timezone", "max_attempts", "multiplier", "jitter", "port", "storage.driver"} {
		if !strings.Contains(joined, want) {
			t.Errorf("aggregated problems are missing %q: %s", want, joined)
		}
	}
}

// TestVerifyTelegramDisabledSkipsSectionSecrets proves disabled sections do not
// demand credentials but enabled ones do.
func TestVerifyTelegramDisabledSkipsSectionSecrets(t *testing.T) {
	if _, err := config.Parse([]byte("app:\n  name: ok\ntelegram:\n  enabled: false\n")); err != nil {
		t.Fatalf("disabled telegram should validate: %v", err)
	}
	_, err := config.Parse([]byte("app:\n  name: ok\ntelegram:\n  enabled: true\n"))
	if err == nil || !strings.Contains(err.Error(), "bot_token") {
		t.Fatalf("enabled telegram without token must fail on bot_token, got %v", err)
	}
}

// TestVerifyExampleConfigLoads is the real shipped example, so it must validate.
func TestVerifyExampleConfigLoads(t *testing.T) {
	data, err := os.ReadFile("../../configs/config.example.yaml")
	if err != nil {
		t.Fatalf("read example config: %v", err)
	}
	cfg, err := config.Parse(data)
	if err != nil {
		t.Fatalf("shipped example config does not validate: %v", err)
	}
	if cfg.App.Name == "" || cfg.Scheduler.PollInterval.Duration <= 0 {
		t.Fatalf("example config parsed but is empty: %+v", cfg.App)
	}
}

// TestVerifyEnvSetterCoverage proves every override has a sample (guard test).
func TestVerifyMaskSecretNeverReveals(t *testing.T) {
	secret := "123456789:AAHsecretvalue_that_is_long"
	masked := config.MaskSecret(secret)
	if strings.Contains(masked, "secretvalue") || strings.Contains(masked, "AAH") {
		t.Fatalf("MaskSecret leaked the secret: %q", masked)
	}
	if config.MaskSecret("short") != "*****" {
		t.Fatalf("short secrets must be fully masked")
	}
}
