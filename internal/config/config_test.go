package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// noEnv is an environment lookup that returns nothing.
func noEnv(string) (string, bool) { return "", false }

func envFrom(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := values[key]
		return v, ok
	}
}

func mustParse(t *testing.T, yaml string, lookup func(string) (string, bool)) *Config {
	t.Helper()
	cfg, err := parse([]byte(yaml), lookup)
	if err != nil {
		t.Fatalf("parse(%q): unexpected error: %v", yaml, err)
	}
	return cfg
}

func TestParseEmptyDocumentYieldsDefaults(t *testing.T) {
	t.Parallel()

	cfg := mustParse(t, "", noEnv)

	if cfg.App.Name != "ccresu" {
		t.Errorf("app.name = %q, want ccresu", cfg.App.Name)
	}
	if got, want := cfg.Claude.DiscoveryInterval.Duration, 30*time.Second; got != want {
		t.Errorf("discovery_interval = %v, want %v", got, want)
	}
	if !cfg.Claude.ResumeEnabled {
		t.Error("resume_enabled should default to true")
	}
	if got, want := cfg.Recovery.MaxAttempts, 10; got != want {
		t.Errorf("max_attempts = %d, want %d", got, want)
	}
	// The rediscovery window is the Claude quota window: it is what stops the
	// daemon from deferring a still-working continuation for ever.
	if got, want := cfg.Recovery.RediscoveryWindow.Duration, 5*time.Hour; got != want {
		t.Errorf("rediscovery_window = %v, want %v", got, want)
	}
	if cfg.Telegram.Enabled {
		t.Error("telegram should default to disabled")
	}
	if cfg.Source != "" {
		t.Errorf("Source = %q, want empty for parse", cfg.Source)
	}
}

func TestParseAppliesDefaultsBeforeOverrides(t *testing.T) {
	t.Parallel()

	cfg := mustParse(t, "app:\n  log_level: debug\n", noEnv)

	if cfg.App.LogLevel != "debug" {
		t.Errorf("log_level = %q, want debug", cfg.App.LogLevel)
	}
	// Untouched fields keep their default.
	if cfg.App.Name != "ccresu" {
		t.Errorf("app.name = %q, want default ccresu", cfg.App.Name)
	}
	if !cfg.Monitoring.Enabled {
		t.Error("monitoring.enabled should keep its default of true")
	}
}

func TestParseExampleConfigFile(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(filepath.Join("..", "..", "configs", "config.example.yaml"))
	if err != nil {
		t.Fatalf("read example config: %v", err)
	}

	cfg, err := parse(data, noEnv)
	if err != nil {
		t.Fatalf("example config must be valid: %v", err)
	}

	if cfg.App.Timezone != "Asia/Singapore" {
		t.Errorf("app.timezone = %q, want Asia/Singapore", cfg.App.Timezone)
	}
	if cfg.Server.Port != 8787 {
		t.Errorf("server.port = %d, want 8787", cfg.Server.Port)
	}
	if cfg.Telegram.Enabled {
		t.Error("example config must ship with telegram disabled")
	}
	if !strings.HasPrefix(cfg.Storage.Path, "/") {
		t.Errorf("storage.path should be expanded to an absolute path, got %q", cfg.Storage.Path)
	}
	if !strings.Contains(strings.Join(cfg.Server.AllowedOrigins, ","), "https://jymchng.github.io") {
		t.Errorf("example config should allow the GitHub Pages origin, got %v", cfg.Server.AllowedOrigins)
	}
	if cfg.Claude.ContinuationMessage != DefaultContinuationMessage {
		t.Errorf("continuation message mismatch: %q", cfg.Claude.ContinuationMessage)
	}
}

func TestParseRejectsUnknownField(t *testing.T) {
	t.Parallel()

	_, err := parse([]byte("recovery:\n  max_attempt: 3\n"), noEnv)
	if err == nil {
		t.Fatal("expected an error for an unknown field")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("error should wrap ErrInvalid, got %v", err)
	}
	if !strings.Contains(err.Error(), "max_attempt") {
		t.Errorf("error should name the offending field, got %v", err)
	}
}

func TestParseRejectsMalformedYAML(t *testing.T) {
	t.Parallel()

	_, err := parse([]byte("app: [1, 2\n"), noEnv)
	if err == nil {
		t.Fatal("expected an error for malformed YAML")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("error should wrap ErrInvalid, got %v", err)
	}
}

func TestParseRejectsInvalidDuration(t *testing.T) {
	t.Parallel()

	_, err := parse([]byte("claude:\n  discovery_interval: soon\n"), noEnv)
	if err == nil {
		t.Fatal("expected an error for an invalid duration")
	}
	if !strings.Contains(err.Error(), "invalid duration") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestParseAppliesEnvironmentOverrides(t *testing.T) {
	t.Parallel()

	cfg := mustParse(t, "", envFrom(map[string]string{
		"CCRESU_APP__LOG_LEVEL":                  "debug",
		"CCRESU_APP__TIMEZONE":                   "Europe/Berlin",
		"CCRESU_RECOVERY__MAX_ATTEMPTS":          "3",
		"CCRESU_RECOVERY__MULTIPLIER":            "1.5",
		"CCRESU_RECOVERY__INITIAL_DELAY":         "45s",
		"CCRESU_CLAUDE__RESUME_ENABLED":          "false",
		"CCRESU_CLAUDE__MONITORED_SESSIONS":      " a , b ,, c ",
		"CCRESU_SERVER__PORT":                    "9999",
		"CCRESU_TELEGRAM__ENABLED":               "true",
		"CCRESU_TELEGRAM__BOT_TOKEN":             "123456789:abcdefghijklmnopqrstuvwxyz",
		"CCRESU_TELEGRAM__CHAT_ID":               "-100123",
		"CCRESU_SERVER__ALLOWED_ORIGINS":         "https://example.com",
		"CCRESU_MONITORING__DETECT_USAGE_LIMITS": "false",
	}))

	if cfg.App.LogLevel != "debug" {
		t.Errorf("log_level = %q", cfg.App.LogLevel)
	}
	if cfg.App.Timezone != "Europe/Berlin" {
		t.Errorf("timezone = %q", cfg.App.Timezone)
	}
	if cfg.Recovery.MaxAttempts != 3 {
		t.Errorf("max_attempts = %d", cfg.Recovery.MaxAttempts)
	}
	if cfg.Recovery.Multiplier != 1.5 {
		t.Errorf("multiplier = %v", cfg.Recovery.Multiplier)
	}
	if cfg.Recovery.InitialDelay.Duration != 45*time.Second {
		t.Errorf("initial_delay = %v", cfg.Recovery.InitialDelay)
	}
	if cfg.Claude.ResumeEnabled {
		t.Error("resume_enabled should be false")
	}
	want := []string{"a", "b", "c"}
	if len(cfg.Claude.MonitoredSessions) != len(want) {
		t.Fatalf("monitored_sessions = %v, want %v", cfg.Claude.MonitoredSessions, want)
	}
	for i := range want {
		if cfg.Claude.MonitoredSessions[i] != want[i] {
			t.Errorf("monitored_sessions[%d] = %q, want %q", i, cfg.Claude.MonitoredSessions[i], want[i])
		}
	}
	if cfg.Server.Port != 9999 {
		t.Errorf("port = %d", cfg.Server.Port)
	}
	if len(cfg.Server.AllowedOrigins) != 1 || cfg.Server.AllowedOrigins[0] != "https://example.com" {
		t.Errorf("allowed_origins = %v", cfg.Server.AllowedOrigins)
	}
	if !cfg.Telegram.Enabled || cfg.Telegram.ChatID != "-100123" {
		t.Errorf("telegram overrides not applied: %+v", cfg.Telegram)
	}
	if cfg.Monitoring.DetectUsageLimits {
		t.Error("detect_usage_limits override not applied")
	}
}

func TestEnvironmentOverridesInvalidValuesAreRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		env   map[string]string
		wants string
	}{
		{"bool", map[string]string{"CCRESU_MONITORING__ENABLED": "maybe"}, "expected a boolean"},
		{"int", map[string]string{"CCRESU_RECOVERY__MAX_ATTEMPTS": "lots"}, "expected an integer"},
		{"float", map[string]string{"CCRESU_RECOVERY__JITTER": "some"}, "expected a number"},
		{"duration", map[string]string{"CCRESU_SCHEDULER__POLL_INTERVAL": "often"}, "invalid duration"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := parse(nil, envFrom(tc.env))
			if err == nil {
				t.Fatalf("expected error for env %v", tc.env)
			}
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("error should wrap ErrInvalid, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("error %q should contain %q", err, tc.wants)
			}
		})
	}
}

func TestLegacyTelegramEnvironmentVariables(t *testing.T) {
	t.Parallel()

	cfg := mustParse(t, "", envFrom(map[string]string{
		"TELEGRAM_ENABLED":   "true",
		"TELEGRAM_BOT_TOKEN": "123456789:abcdefghijklmnopqrstuvwxyz",
		"TELEGRAM_CHAT_ID":   "42",
	}))

	if !cfg.Telegram.Enabled {
		t.Error("TELEGRAM_ENABLED should enable telegram")
	}
	if cfg.Telegram.ChatID != "42" {
		t.Errorf("chat_id = %q", cfg.Telegram.ChatID)
	}

	// The prefixed variable must win over the legacy alias.
	cfg = mustParse(t, "", envFrom(map[string]string{
		"TELEGRAM_ENABLED":         "true",
		"CCRESU_TELEGRAM__ENABLED": "false",
	}))
	if cfg.Telegram.Enabled {
		t.Error("CCRESU_TELEGRAM__ENABLED should take precedence over TELEGRAM_ENABLED")
	}
}

func TestParseDurationTable(t *testing.T) {
	t.Parallel()

	valid := map[string]time.Duration{
		"30s":    30 * time.Second,
		"5m":     5 * time.Minute,
		"1h30m":  90 * time.Minute,
		" 250ms": 250 * time.Millisecond,
	}
	for input, want := range valid {
		got, err := ParseDuration(input)
		if err != nil {
			t.Errorf("ParseDuration(%q) unexpected error: %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("ParseDuration(%q) = %v, want %v", input, got, want)
		}
	}

	invalid := []string{"", "   ", "soon", "-5s", "5", "1y"}
	for _, input := range invalid {
		if _, err := ParseDuration(input); err == nil {
			t.Errorf("ParseDuration(%q) should have failed", input)
		}
	}
}

func TestDurationMarshalYAML(t *testing.T) {
	t.Parallel()

	value, err := NewDuration(90 * time.Second).MarshalYAML()
	if err != nil {
		t.Fatalf("MarshalYAML: %v", err)
	}
	if value != "1m30s" {
		t.Errorf("MarshalYAML = %v, want 1m30s", value)
	}
}

func TestExpandPath(t *testing.T) {
	t.Parallel()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home dir: %v", err)
	}

	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"~", home},
		{"~/.config/ccresu/config.yaml", filepath.Join(home, ".config", "ccresu", "config.yaml")},
		{"/absolute/path", "/absolute/path"},
		{"relative/path/../other", "relative/other"},
	}
	for _, tc := range tests {
		got, err := ExpandPath(tc.in)
		if err != nil {
			t.Errorf("ExpandPath(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ExpandPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDefaultPathHonoursXDGConfigHome(t *testing.T) {
	// `ccresu config init` writes to DefaultPath, so "honouring XDG" is this
	// function's promise; the CLI tests stub it out and would not notice if it
	// stopped reading the variable.
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CCRESU_CONFIG", "")

	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if want := filepath.Join(xdg, "ccresu", "config.yaml"); got != want {
		t.Errorf("DefaultPath with XDG_CONFIG_HOME=%s = %q, want %q", xdg, got, want)
	}
}

func TestDefaultDirectories(t *testing.T) {
	t.Parallel()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home dir: %v", err)
	}

	configPath, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if !strings.HasSuffix(configPath, filepath.Join("ccresu", "config.yaml")) {
		t.Errorf("DefaultPath = %q", configPath)
	}

	dataDir, err := DefaultDataDir()
	if err != nil {
		t.Fatalf("DefaultDataDir: %v", err)
	}
	if want := filepath.Join(home, ".local", "share", "ccresu"); dataDir != want {
		t.Errorf("DefaultDataDir = %q, want %q", dataDir, want)
	}

	stateDir, err := DefaultStateDir()
	if err != nil {
		t.Fatalf("DefaultStateDir: %v", err)
	}
	if want := filepath.Join(home, ".local", "state", "ccresu"); stateDir != want {
		t.Errorf("DefaultStateDir = %q, want %q", stateDir, want)
	}
}

func TestResolvePathPrecedence(t *testing.T) {
	dir := t.TempDir()
	explicit := filepath.Join(dir, "explicit.yaml")
	fromEnv := filepath.Join(dir, "env.yaml")

	t.Setenv(EnvConfigPath, fromEnv)

	got, err := ResolvePath(explicit)
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if got != explicit {
		t.Errorf("explicit path should win, got %q", got)
	}

	got, err = ResolvePath("")
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if got != fromEnv {
		t.Errorf("env path should be used, got %q", got)
	}

	t.Setenv(EnvConfigPath, "")
	got, err = ResolvePath("")
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if !strings.HasSuffix(got, "config.yaml") {
		t.Errorf("default path expected, got %q", got)
	}
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := "app:\n  name: ccresu-test\n  log_level: warn\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.App.Name != "ccresu-test" {
		t.Errorf("app.name = %q", cfg.App.Name)
	}
	if cfg.Source != path {
		t.Errorf("Source = %q, want %q", cfg.Source, path)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err == nil {
		t.Fatal("expected an error for a missing file")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("error should wrap ErrInvalid, got %v", err)
	}
}

func TestLoadWithEnvUsesInjectedLookup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("app:\n  log_level: info\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := LoadWithEnv(path, envFrom(map[string]string{"CCRESU_APP__LOG_LEVEL": "error"}))
	if err != nil {
		t.Fatalf("LoadWithEnv: %v", err)
	}
	if cfg.App.LogLevel != "error" {
		t.Errorf("log_level = %q, want error", cfg.App.LogLevel)
	}
}

func TestParseReader(t *testing.T) {
	t.Parallel()

	cfg, err := ParseReader(strings.NewReader("app:\n  name: from-reader\n"))
	if err != nil {
		t.Fatalf("ParseReader: %v", err)
	}
	if cfg.App.Name != "from-reader" {
		t.Errorf("app.name = %q", cfg.App.Name)
	}
}

func TestExpandPathsRejectsNil(t *testing.T) {
	t.Parallel()

	if err := ExpandPaths(nil); err == nil {
		t.Fatal("expected an error for a nil config")
	}
}

func TestServerAddr(t *testing.T) {
	t.Parallel()

	srv := Server{Host: "127.0.0.1", Port: 8787}
	if got, want := srv.Addr(), "127.0.0.1:8787"; got != want {
		t.Errorf("Addr() = %q, want %q", got, want)
	}
}

func TestDisplayLocationAndParseMode(t *testing.T) {
	t.Parallel()

	cfg := Defaults()
	loc, err := cfg.DisplayLocation()
	if err != nil {
		t.Fatalf("DisplayLocation: %v", err)
	}
	if loc.String() != "UTC" {
		t.Errorf("location = %q", loc)
	}

	cfg.Telegram.ParseMode = "html"
	if got := cfg.NormalizedParseMode(); got != "HTML" {
		t.Errorf("NormalizedParseMode = %q, want HTML", got)
	}
}

func TestMaskSecretConfig(t *testing.T) {
	t.Parallel()

	if got := MaskSecret(""); got != "" {
		t.Errorf("MaskSecret(\"\") = %q", got)
	}
	if got := MaskSecret("short"); got != "*****" {
		t.Errorf("MaskSecret(short) = %q", got)
	}
	masked := MaskSecret("123456789:abcdefghijklmnopqrstuvwxyz")
	if strings.Contains(masked, "abcdefghij") {
		t.Errorf("MaskSecret leaked the secret: %q", masked)
	}
	if !strings.HasSuffix(masked, "wxyz") {
		t.Errorf("MaskSecret should keep a short suffix, got %q", masked)
	}
}
