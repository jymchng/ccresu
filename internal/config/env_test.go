package config

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// everyEnvSetterSamples maps each documented environment variable to a value
// that is valid for it. TestEveryEnvSetterHasCoverage fails when a setter exists
// without a sample, so adding a variable without testing it is caught.
var everyEnvSetterSamples = map[string]string{
	"CCRESU_APP__NAME":                                 "renamed",
	"CCRESU_APP__LOG_LEVEL":                            "debug",
	"CCRESU_APP__LOG_FORMAT":                           "text",
	"CCRESU_APP__TIMEZONE":                             "Europe/Berlin",
	"CCRESU_CLAUDE__EXECUTABLE":                        "claude-nightly",
	"CCRESU_CLAUDE__DISCOVERY_INTERVAL":                "45s",
	"CCRESU_CLAUDE__MONITORED_SESSIONS":                "session-a,session-b",
	"CCRESU_CLAUDE__DISCOVER_ALL_SESSIONS":             "false",
	"CCRESU_CLAUDE__RESUME_ENABLED":                    "false",
	"CCRESU_CLAUDE__DEFAULT_MODEL":                     "claude-sonnet-4",
	"CCRESU_CLAUDE__DEFAULT_EFFORT":                    "high",
	"CCRESU_CLAUDE__CONTINUATION_MESSAGE":              "keep going",
	"CCRESU_MONITORING__ENABLED":                       "false",
	"CCRESU_MONITORING__DETECT_USAGE_LIMITS":           "false",
	"CCRESU_MONITORING__DETECT_SESSION_COMPLETION":     "false",
	"CCRESU_DETECTOR__THRESHOLD":                       "0.7",
	"CCRESU_SCHEDULER__POLL_INTERVAL":                  "1m",
	"CCRESU_SCHEDULER__UNKNOWN_RESET_INITIAL_INTERVAL": "2m",
	"CCRESU_SCHEDULER__UNKNOWN_RESET_MAX_INTERVAL":     "40m",
	"CCRESU_SCHEDULER__RECHECK_BEFORE_RESUME":          "false",
	"CCRESU_RECOVERY__ENABLED":                         "false",
	"CCRESU_RECOVERY__MAX_ATTEMPTS":                    "4",
	"CCRESU_RECOVERY__INITIAL_DELAY":                   "10s",
	"CCRESU_RECOVERY__MAX_DELAY":                       "20m",
	"CCRESU_RECOVERY__MULTIPLIER":                      "3",
	"CCRESU_RECOVERY__JITTER":                          "0.2",
	"CCRESU_RECOVERY__STARTUP_TIMEOUT":                 "90s",
	"CCRESU_RECOVERY__VERIFICATION_TIMEOUT":            "180s",
	"CCRESU_RECOVERY__HEALTH_CHECK_INTERVAL":           "15s",
	"CCRESU_RECOVERY__MAX_CONCURRENT_RECOVERIES":       "5",
	"CCRESU_TELEGRAM__ENABLED":                         "true",
	"CCRESU_TELEGRAM__BOT_TOKEN":                       "123456789:abcdefghijklmnopqrstuvwxyz",
	"CCRESU_TELEGRAM__CHAT_ID":                         "-100200300",
	"CCRESU_TELEGRAM__PARSE_MODE":                      "MarkdownV2",
	"CCRESU_TELEGRAM__NOTIFY_LIMIT_DETECTED":           "false",
	"CCRESU_TELEGRAM__NOTIFY_WAITING":                  "true",
	"CCRESU_TELEGRAM__NOTIFY_RECOVERY_STARTED":         "false",
	"CCRESU_TELEGRAM__NOTIFY_RECOVERY_SUCCESS":         "false",
	"CCRESU_TELEGRAM__NOTIFY_RECOVERY_FAILURE":         "false",
	"CCRESU_TELEGRAM__NOTIFY_PERMANENT_FAILURE":        "false",
	"CCRESU_TELEGRAM__NOTIFY_SESSION_COMPLETED":        "false",
	"CCRESU_SERVER__ENABLED":                           "false",
	"CCRESU_SERVER__HOST":                              "127.0.0.1",
	"CCRESU_SERVER__PORT":                              "9000",
	"CCRESU_SERVER__ALLOWED_ORIGINS":                   "https://a.example,https://b.example",
	"CCRESU_SERVER__TOKEN_PATH":                        "/tmp/ccresu-token",
	"CCRESU_STORAGE__DRIVER":                           "sqlite",
	"CCRESU_STORAGE__PATH":                             "/tmp/ccresu.db",
	"CCRESU_STORAGE__BUSY_TIMEOUT":                     "10s",
}

// TestEveryEnvSetterHasCoverage applies every registered environment setter with
// a valid sample value, so no override can silently stop working.
func TestEveryEnvSetterHasCoverage(t *testing.T) {
	t.Parallel()

	setters := envSetters()
	if len(setters) == 0 {
		t.Fatal("no environment setters registered")
	}

	seen := make(map[string]struct{}, len(setters))
	for _, setter := range setters {
		if _, duplicate := seen[setter.key]; duplicate {
			t.Errorf("environment key %s is registered twice", setter.key)
		}
		seen[setter.key] = struct{}{}

		sample, ok := everyEnvSetterSamples[setter.key]
		if !ok {
			t.Errorf("no sample value for %s; add one to everyEnvSetterSamples", setter.key)
			continue
		}

		cfg := Defaults()
		if err := setter.set(&cfg, sample); err != nil {
			t.Errorf("%s rejected valid sample %q: %v", setter.key, sample, err)
		}
	}

	for key := range everyEnvSetterSamples {
		if _, ok := seen[key]; !ok {
			t.Errorf("sample defined for unregistered key %s", key)
		}
	}
}

func TestEnvSettersRejectInvalidValuesDirectly(t *testing.T) {
	t.Parallel()

	for _, setter := range envSetters() {
		cfg := Defaults()
		// Every non-string setter must reject a clearly invalid value.
		switch setter.key {
		case "CCRESU_APP__NAME", "CCRESU_APP__LOG_LEVEL", "CCRESU_APP__LOG_FORMAT",
			"CCRESU_APP__TIMEZONE", "CCRESU_CLAUDE__EXECUTABLE", "CCRESU_CLAUDE__DEFAULT_MODEL",
			"CCRESU_CLAUDE__DEFAULT_EFFORT", "CCRESU_CLAUDE__CONTINUATION_MESSAGE",
			"CCRESU_TELEGRAM__BOT_TOKEN", "CCRESU_TELEGRAM__CHAT_ID", "CCRESU_TELEGRAM__PARSE_MODE",
			"CCRESU_SERVER__HOST", "CCRESU_SERVER__TOKEN_PATH", "CCRESU_STORAGE__DRIVER",
			"CCRESU_STORAGE__PATH", "CCRESU_CLAUDE__MONITORED_SESSIONS",
			"CCRESU_SERVER__ALLOWED_ORIGINS":
			continue
		}
		if err := setter.set(&cfg, "definitely-not-a-value"); err == nil {
			t.Errorf("%s accepted an invalid value", setter.key)
		}
	}
}

func TestApplyEnvNilConfig(t *testing.T) {
	t.Parallel()

	if err := applyEnv(nil, noEnv); err == nil {
		t.Fatal("expected an error for a nil config")
	}
}

func TestParseWithNilLookupUsesProcessEnvironment(t *testing.T) {
	t.Setenv("CCRESU_APP__NAME", "from-process-env")

	cfg, err := parse(nil, nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.App.Name != "from-process-env" {
		t.Errorf("app.name = %q, want from-process-env", cfg.App.Name)
	}
}

func TestDurationUnmarshalRejectsNonString(t *testing.T) {
	t.Parallel()

	_, err := parse([]byte("claude:\n  discovery_interval:\n    nested: value\n"), noEnv)
	if err == nil {
		t.Fatal("expected an error for a non-string duration")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("error should wrap ErrInvalid, got %v", err)
	}
}

func TestParseRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	// A document that parses but fails validation must be rejected.
	_, err := parse([]byte("app:\n  log_level: scream\n"), noEnv)
	if err == nil {
		t.Fatal("expected a validation error")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("error should wrap ErrInvalid, got %v", err)
	}
}

func TestParseReportsExpandPathFailure(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	_, err := parse([]byte("storage:\n  path: ~/data/ccresu.db\n"), noEnv)
	if err == nil {
		t.Fatal("expected an error when the home directory is unavailable")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("error should wrap ErrInvalid, got %v", err)
	}
}

func TestLoadWithEnvMissingFile(t *testing.T) {
	t.Parallel()

	_, err := LoadWithEnv(t.TempDir()+"/absent.yaml", noEnv)
	if err == nil {
		t.Fatal("expected an error for a missing file")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("error should wrap ErrInvalid, got %v", err)
	}
}

func TestLoadWithEnvPropagatesParseErrors(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.yaml"
	if err := os.WriteFile(path, []byte("app:\n  log_level: scream\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadWithEnv(path, noEnv)
	if err == nil {
		t.Fatal("expected an error for an invalid config file")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error should name the config file, got %v", err)
	}
}

func TestParseReaderPropagatesReadError(t *testing.T) {
	t.Parallel()

	_, err := ParseReader(failingReader{})
	if err == nil {
		t.Fatal("expected a read error")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("error should wrap ErrInvalid, got %v", err)
	}
}

func TestDefaultDirectoriesWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	if _, err := DefaultPath(); err == nil {
		t.Error("DefaultPath should fail without a home directory")
	}
	if _, err := DefaultDataDir(); err == nil {
		t.Error("DefaultDataDir should fail without a home directory")
	}
	if _, err := DefaultStateDir(); err == nil {
		t.Error("DefaultStateDir should fail without a home directory")
	}
	if _, err := ExpandPath("~/x"); err == nil {
		t.Error("ExpandPath should fail without a home directory")
	}
}

func TestExpandPathsFailsWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	cfg := Defaults()
	cfg.Storage.Path = ""
	cfg.Server.TokenPath = ""
	if err := ExpandPaths(&cfg); err == nil {
		t.Error("ExpandPaths should fail when the token path needs a config dir and none exists")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("boom") }
