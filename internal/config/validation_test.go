package config

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func validConfig(t *testing.T) Config {
	t.Helper()

	cfg := Defaults()
	if err := ExpandPaths(&cfg); err != nil {
		t.Fatalf("ExpandPaths: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("baseline config must be valid, got: %v", err)
	}
	return cfg
}

func TestValidateAcceptsDefaults(t *testing.T) {
	t.Parallel()

	_ = validConfig(t)
}

func TestValidateReportsEveryProblem(t *testing.T) {
	t.Parallel()

	cfg := validConfig(t)
	cfg.App.Name = ""
	cfg.App.LogLevel = "verbose"
	cfg.Server.Port = 0

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation errors")
	}

	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("error should be a *ValidationError, got %T", err)
	}
	if len(invalid.Problems) != 3 {
		t.Fatalf("expected 3 problems, got %d: %v", len(invalid.Problems), invalid.Problems)
	}
	if !errors.Is(err, ErrInvalid) {
		t.Error("ValidationError should match ErrInvalid")
	}
	if !strings.Contains(err.Error(), "3 problems") {
		t.Errorf("aggregate message should state the count, got %q", err.Error())
	}
}

func TestValidateSingleProblemMessage(t *testing.T) {
	t.Parallel()

	cfg := validConfig(t)
	cfg.Recovery.MaxAttempts = 0

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "problems") {
		t.Errorf("single problem should not mention a count: %q", err.Error())
	}
}

func TestValidateFieldRules(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"app name", func(c *Config) { c.App.Name = "  " }, "app.name"},
		{"log level", func(c *Config) { c.App.LogLevel = "trace" }, "app.log_level"},
		{"log format", func(c *Config) { c.App.LogFormat = "xml" }, "app.log_format"},
		{"timezone", func(c *Config) { c.App.Timezone = "Mars/Olympus" }, "app.timezone"},

		{"claude executable", func(c *Config) { c.Claude.Executable = "" }, "claude.executable"},
		{"discovery interval", func(c *Config) { c.Claude.DiscoveryInterval = NewDuration(0) }, "claude.discovery_interval"},
		{"monitored session empty", func(c *Config) {
			c.Claude.MonitoredSessions = []string{"  "}
		}, "claude.monitored_sessions[0]"},
		{"default effort", func(c *Config) { c.Claude.DefaultEffort = "extreme" }, "claude.default_effort"},
		{"continuation message", func(c *Config) { c.Claude.ContinuationMessage = "" }, "claude.continuation_message"},

		{"monitoring nothing enabled", func(c *Config) {
			c.Monitoring.DetectUsageLimits = false
			c.Monitoring.DetectSessionCompletion = false
		}, "nothing is being detected"},

		{"poll interval", func(c *Config) { c.Scheduler.PollInterval = NewDuration(0) }, "scheduler.poll_interval"},
		{"unknown initial", func(c *Config) {
			c.Scheduler.UnknownResetInitialInterval = NewDuration(0)
		}, "scheduler.unknown_reset_initial_interval"},
		{"unknown max", func(c *Config) {
			c.Scheduler.UnknownResetMaxInterval = NewDuration(0)
		}, "scheduler.unknown_reset_max_interval"},
		{"unknown max below initial", func(c *Config) {
			c.Scheduler.UnknownResetInitialInterval = NewDuration(10 * time.Minute)
			c.Scheduler.UnknownResetMaxInterval = NewDuration(time.Minute)
		}, "unknown_reset_max_interval must be greater"},

		{"max attempts", func(c *Config) { c.Recovery.MaxAttempts = 0 }, "recovery.max_attempts"},
		{"initial delay", func(c *Config) { c.Recovery.InitialDelay = NewDuration(0) }, "recovery.initial_delay"},
		{"max delay", func(c *Config) { c.Recovery.MaxDelay = NewDuration(0) }, "recovery.max_delay"},
		{"max delay below initial", func(c *Config) {
			c.Recovery.InitialDelay = NewDuration(time.Hour)
			c.Recovery.MaxDelay = NewDuration(time.Minute)
		}, "recovery.max_delay must be greater"},
		{"multiplier", func(c *Config) { c.Recovery.Multiplier = 0.5 }, "recovery.multiplier"},
		{"negative jitter", func(c *Config) { c.Recovery.Jitter = -0.1 }, "recovery.jitter"},
		{"jitter too large", func(c *Config) { c.Recovery.Jitter = 1 }, "recovery.jitter"},
		{"startup timeout", func(c *Config) { c.Recovery.StartupTimeout = NewDuration(0) }, "recovery.startup_timeout"},
		{"verification timeout", func(c *Config) {
			c.Recovery.VerificationTimeout = NewDuration(0)
		}, "recovery.verification_timeout"},
		{"health check interval", func(c *Config) {
			c.Recovery.HealthCheckInterval = NewDuration(0)
		}, "recovery.health_check_interval"},
		{"max concurrent", func(c *Config) { c.Recovery.MaxConcurrentRecoveries = 0 }, "recovery.max_concurrent_recoveries"},
		{"rediscovery window", func(c *Config) { c.Recovery.RediscoveryWindow = NewDuration(0) }, "recovery.rediscovery_window"},
		{"negative rediscovery window", func(c *Config) {
			c.Recovery.RediscoveryWindow = NewDuration(-time.Hour)
		}, "recovery.rediscovery_window must be greater than zero"},

		{"parse mode", func(c *Config) { c.Telegram.ParseMode = "Richtext" }, "telegram.parse_mode"},
		{"parse mode markdownv2", func(c *Config) { c.Telegram.ParseMode = "MarkdownV2" }, "telegram.parse_mode must be HTML"},
		{"parse mode markdown", func(c *Config) { c.Telegram.ParseMode = "Markdown" }, "telegram.parse_mode must be HTML"},
		{"telegram token missing", func(c *Config) {
			c.Telegram.Enabled = true
			c.Telegram.BotToken = ""
			c.Telegram.ChatID = "1"
		}, "telegram.bot_token must be set"},
		{"telegram token malformed", func(c *Config) {
			c.Telegram.Enabled = true
			c.Telegram.BotToken = "not-a-token"
			c.Telegram.ChatID = "1"
		}, "not shaped like a Telegram bot token"},
		{"telegram chat missing", func(c *Config) {
			c.Telegram.Enabled = true
			c.Telegram.BotToken = "123456789:abcdefghijklmnopqrstuvwxyz"
			c.Telegram.ChatID = ""
		}, "telegram.chat_id must be set"},

		{"server host", func(c *Config) { c.Server.Host = " " }, "server.host"},
		{"server port low", func(c *Config) { c.Server.Port = 0 }, "server.port"},
		{"server port high", func(c *Config) { c.Server.Port = 70000 }, "server.port"},
		{"origin malformed", func(c *Config) {
			c.Server.AllowedOrigins = []string{"not-a-url"}
		}, "server.allowed_origins[0] must be an absolute"},
		{"origin scheme", func(c *Config) {
			c.Server.AllowedOrigins = []string{"ftp://example.com"}
		}, "must use http or https"},
		{"origin path", func(c *Config) {
			c.Server.AllowedOrigins = []string{"https://example.com/ccresu"}
		}, "must not include a path"},
		{"token path", func(c *Config) { c.Server.TokenPath = "" }, "server.token_path"},

		{"storage driver", func(c *Config) { c.Storage.Driver = "postgres" }, "storage.driver"},
		{"storage path", func(c *Config) { c.Storage.Path = "" }, "storage.path"},
		{"busy timeout", func(c *Config) { c.Storage.BusyTimeout = NewDuration(0) }, "storage.busy_timeout"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := validConfig(t)
			tc.mutate(&cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected a validation error mentioning %q", tc.want)
			}
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("error should wrap ErrInvalid, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should mention %q", err, tc.want)
			}
		})
	}
}

func TestValidateDisabledSectionsSkipTheirRules(t *testing.T) {
	t.Parallel()

	cfg := validConfig(t)
	cfg.Telegram.Enabled = false
	cfg.Telegram.BotToken = ""
	cfg.Telegram.ChatID = ""
	if err := cfg.Validate(); err != nil {
		t.Errorf("disabled telegram must not require credentials: %v", err)
	}

	cfg = validConfig(t)
	cfg.Monitoring.Enabled = false
	cfg.Monitoring.DetectUsageLimits = false
	cfg.Monitoring.DetectSessionCompletion = false
	if err := cfg.Validate(); err != nil {
		t.Errorf("disabled monitoring must not require detectors: %v", err)
	}

	cfg = validConfig(t)
	cfg.Server.Enabled = false
	cfg.Server.Port = 0
	cfg.Server.AllowedOrigins = nil
	if err := cfg.Validate(); err != nil {
		t.Errorf("disabled server must not require bind settings: %v", err)
	}

	cfg = validConfig(t)
	cfg.Claude.ResumeEnabled = false
	cfg.Claude.ContinuationMessage = ""
	if err := cfg.Validate(); err != nil {
		t.Errorf("disabled resume must not require a continuation message: %v", err)
	}
}

func TestValidateNilConfig(t *testing.T) {
	t.Parallel()

	var cfg *Config
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected an error for a nil config")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("error should wrap ErrInvalid, got %v", err)
	}
}

func TestValidBotToken(t *testing.T) {
	t.Parallel()

	valid := []string{
		"123456789:abcdefghijklmnopqrstuvwxyz",
		"1:abcdefghijklmnopqrstuvwxyz0123456789",
	}
	for _, token := range valid {
		if !validBotToken(token) {
			t.Errorf("validBotToken(%q) = false, want true", token)
		}
	}

	invalid := []string{
		"",
		"no-colon",
		":abcdefghijklmnopqrstuvwxyz",
		"123:",
		"abc:abcdefghijklmnopqrstuvwxyz",
		"123:short",
		"123:abcdefghijklmnopqrstuvwxyz!",
	}
	for _, token := range invalid {
		if validBotToken(token) {
			t.Errorf("validBotToken(%q) = true, want false", token)
		}
	}
}

func TestNormalizeParseMode(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"":            "HTML",
		"html":        "HTML",
		"HTML":        "HTML",
		"markdownv2":  "MarkdownV2",
		"markdown_v2": "MarkdownV2",
		"markdown":    "Markdown",
		"Richtext":    "Richtext",
	}
	for input, want := range tests {
		if got := normalizeParseMode(input); got != want {
			t.Errorf("normalizeParseMode(%q) = %q, want %q", input, got, want)
		}
	}
}
