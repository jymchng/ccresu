package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// legacyAliases maps the plain environment variables documented for Telegram
// onto their canonical CCRESU_ names.
var legacyAliases = map[string]string{
	"TELEGRAM_ENABLED":   EnvPrefix + "TELEGRAM__ENABLED",
	"TELEGRAM_BOT_TOKEN": EnvPrefix + "TELEGRAM__BOT_TOKEN",
	"TELEGRAM_CHAT_ID":   EnvPrefix + "TELEGRAM__CHAT_ID",
}

func envLookup(key string) (string, bool) { return os.LookupEnv(key) }

// withLegacyAliases lets the canonical keys win while still honouring the
// plain TELEGRAM_* variables documented in the README.
func withLegacyAliases(lookup func(string) (string, bool)) func(string) (string, bool) {
	if lookup == nil {
		lookup = envLookup
	}
	return func(key string) (string, bool) {
		if v, ok := lookup(key); ok {
			return v, true
		}
		for legacy, canonical := range legacyAliases {
			if canonical == key {
				if v, ok := lookup(legacy); ok {
					return v, true
				}
			}
		}
		return "", false
	}
}

type envSetter struct {
	key string
	set func(*Config, string) error
}

func envSetters() []envSetter {
	return []envSetter{
		{EnvPrefix + "APP__NAME", setString(func(c *Config) *string { return &c.App.Name })},
		{EnvPrefix + "APP__LOG_LEVEL", setString(func(c *Config) *string { return &c.App.LogLevel })},
		{EnvPrefix + "APP__LOG_FORMAT", setString(func(c *Config) *string { return &c.App.LogFormat })},
		{EnvPrefix + "APP__TIMEZONE", setString(func(c *Config) *string { return &c.App.Timezone })},

		{EnvPrefix + "CLAUDE__EXECUTABLE", setString(func(c *Config) *string { return &c.Claude.Executable })},
		{EnvPrefix + "CLAUDE__DISCOVERY_INTERVAL", setDuration(func(c *Config) *Duration { return &c.Claude.DiscoveryInterval })},
		{EnvPrefix + "CLAUDE__MONITORED_SESSIONS", setCSV(func(c *Config) *[]string { return &c.Claude.MonitoredSessions })},
		{EnvPrefix + "CLAUDE__DISCOVER_ALL_SESSIONS", setBool(func(c *Config) *bool { return &c.Claude.DiscoverAllSessions })},
		{EnvPrefix + "CLAUDE__RESUME_ENABLED", setBool(func(c *Config) *bool { return &c.Claude.ResumeEnabled })},
		{EnvPrefix + "CLAUDE__DEFAULT_MODEL", setString(func(c *Config) *string { return &c.Claude.DefaultModel })},
		{EnvPrefix + "CLAUDE__DEFAULT_EFFORT", setString(func(c *Config) *string { return &c.Claude.DefaultEffort })},
		{EnvPrefix + "CLAUDE__CONTINUATION_MESSAGE", setString(func(c *Config) *string { return &c.Claude.ContinuationMessage })},

		{EnvPrefix + "MONITORING__ENABLED", setBool(func(c *Config) *bool { return &c.Monitoring.Enabled })},
		{EnvPrefix + "MONITORING__DETECT_USAGE_LIMITS", setBool(func(c *Config) *bool { return &c.Monitoring.DetectUsageLimits })},
		{EnvPrefix + "MONITORING__DETECT_SESSION_COMPLETION", setBool(func(c *Config) *bool { return &c.Monitoring.DetectSessionCompletion })},

		// Custom patterns are a structured list and stay file-only; the
		// threshold is a single number and can be overridden here.
		{EnvPrefix + "DETECTOR__THRESHOLD", setFloat(func(c *Config) *float64 { return &c.Detector.Threshold })},

		{EnvPrefix + "SCHEDULER__POLL_INTERVAL", setDuration(func(c *Config) *Duration { return &c.Scheduler.PollInterval })},
		{EnvPrefix + "SCHEDULER__UNKNOWN_RESET_INITIAL_INTERVAL", setDuration(func(c *Config) *Duration { return &c.Scheduler.UnknownResetInitialInterval })},
		{EnvPrefix + "SCHEDULER__UNKNOWN_RESET_MAX_INTERVAL", setDuration(func(c *Config) *Duration { return &c.Scheduler.UnknownResetMaxInterval })},
		{EnvPrefix + "SCHEDULER__RECHECK_BEFORE_RESUME", setBool(func(c *Config) *bool { return &c.Scheduler.RecheckBeforeResume })},

		{EnvPrefix + "RECOVERY__ENABLED", setBool(func(c *Config) *bool { return &c.Recovery.Enabled })},
		{EnvPrefix + "RECOVERY__MAX_ATTEMPTS", setInt(func(c *Config) *int { return &c.Recovery.MaxAttempts })},
		{EnvPrefix + "RECOVERY__INITIAL_DELAY", setDuration(func(c *Config) *Duration { return &c.Recovery.InitialDelay })},
		{EnvPrefix + "RECOVERY__MAX_DELAY", setDuration(func(c *Config) *Duration { return &c.Recovery.MaxDelay })},
		{EnvPrefix + "RECOVERY__MULTIPLIER", setFloat(func(c *Config) *float64 { return &c.Recovery.Multiplier })},
		{EnvPrefix + "RECOVERY__JITTER", setFloat(func(c *Config) *float64 { return &c.Recovery.Jitter })},
		{EnvPrefix + "RECOVERY__STARTUP_TIMEOUT", setDuration(func(c *Config) *Duration { return &c.Recovery.StartupTimeout })},
		{EnvPrefix + "RECOVERY__VERIFICATION_TIMEOUT", setDuration(func(c *Config) *Duration { return &c.Recovery.VerificationTimeout })},
		{EnvPrefix + "RECOVERY__HEALTH_CHECK_INTERVAL", setDuration(func(c *Config) *Duration { return &c.Recovery.HealthCheckInterval })},
		{EnvPrefix + "RECOVERY__MAX_CONCURRENT_RECOVERIES", setInt(func(c *Config) *int { return &c.Recovery.MaxConcurrentRecoveries })},

		{EnvPrefix + "TELEGRAM__ENABLED", setBool(func(c *Config) *bool { return &c.Telegram.Enabled })},
		{EnvPrefix + "TELEGRAM__BOT_TOKEN", setString(func(c *Config) *string { return &c.Telegram.BotToken })},
		{EnvPrefix + "TELEGRAM__CHAT_ID", setString(func(c *Config) *string { return &c.Telegram.ChatID })},
		{EnvPrefix + "TELEGRAM__PARSE_MODE", setString(func(c *Config) *string { return &c.Telegram.ParseMode })},
		{EnvPrefix + "TELEGRAM__NOTIFY_LIMIT_DETECTED", setBool(func(c *Config) *bool { return &c.Telegram.NotifyLimitDetected })},
		{EnvPrefix + "TELEGRAM__NOTIFY_WAITING", setBool(func(c *Config) *bool { return &c.Telegram.NotifyWaiting })},
		{EnvPrefix + "TELEGRAM__NOTIFY_RECOVERY_STARTED", setBool(func(c *Config) *bool { return &c.Telegram.NotifyRecoveryStarted })},
		{EnvPrefix + "TELEGRAM__NOTIFY_RECOVERY_SUCCESS", setBool(func(c *Config) *bool { return &c.Telegram.NotifyRecoverySuccess })},
		{EnvPrefix + "TELEGRAM__NOTIFY_RECOVERY_FAILURE", setBool(func(c *Config) *bool { return &c.Telegram.NotifyRecoveryFailure })},
		{EnvPrefix + "TELEGRAM__NOTIFY_PERMANENT_FAILURE", setBool(func(c *Config) *bool { return &c.Telegram.NotifyPermanentFailure })},
		{EnvPrefix + "TELEGRAM__NOTIFY_SESSION_COMPLETED", setBool(func(c *Config) *bool { return &c.Telegram.NotifySessionCompleted })},

		{EnvPrefix + "SERVER__ENABLED", setBool(func(c *Config) *bool { return &c.Server.Enabled })},
		{EnvPrefix + "SERVER__HOST", setString(func(c *Config) *string { return &c.Server.Host })},
		{EnvPrefix + "SERVER__PORT", setInt(func(c *Config) *int { return &c.Server.Port })},
		{EnvPrefix + "SERVER__ALLOWED_ORIGINS", setCSV(func(c *Config) *[]string { return &c.Server.AllowedOrigins })},
		{EnvPrefix + "SERVER__TOKEN_PATH", setString(func(c *Config) *string { return &c.Server.TokenPath })},

		{EnvPrefix + "STORAGE__DRIVER", setString(func(c *Config) *string { return &c.Storage.Driver })},
		{EnvPrefix + "STORAGE__PATH", setString(func(c *Config) *string { return &c.Storage.Path })},
		{EnvPrefix + "STORAGE__BUSY_TIMEOUT", setDuration(func(c *Config) *Duration { return &c.Storage.BusyTimeout })},
	}
}

func applyEnv(cfg *Config, lookup func(string) (string, bool)) error {
	if cfg == nil {
		return fmt.Errorf("nil config")
	}
	for _, setter := range envSetters() {
		value, ok := lookup(setter.key)
		if !ok {
			continue
		}
		if err := setter.set(cfg, value); err != nil {
			return fmt.Errorf("%s: %v", setter.key, err)
		}
	}
	return nil
}

func setString(target func(*Config) *string) func(*Config, string) error {
	return func(c *Config, value string) error {
		*target(c) = strings.TrimSpace(value)
		return nil
	}
}

func setBool(target func(*Config) *bool) func(*Config, string) error {
	return func(c *Config, value string) error {
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("expected a boolean, got %q", value)
		}
		*target(c) = parsed
		return nil
	}
}

func setInt(target func(*Config) *int) func(*Config, string) error {
	return func(c *Config, value string) error {
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("expected an integer, got %q", value)
		}
		*target(c) = parsed
		return nil
	}
}

func setFloat(target func(*Config) *float64) func(*Config, string) error {
	return func(c *Config, value string) error {
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return fmt.Errorf("expected a number, got %q", value)
		}
		*target(c) = parsed
		return nil
	}
}

func setDuration(target func(*Config) *Duration) func(*Config, string) error {
	return func(c *Config, value string) error {
		parsed, err := ParseDuration(value)
		if err != nil {
			return err
		}
		*target(c) = Duration{Duration: parsed}
		return nil
	}
}

func setCSV(target func(*Config) *[]string) func(*Config, string) error {
	return func(c *Config, value string) error {
		parts := strings.Split(value, ",")
		result := make([]string, 0, len(parts))
		for _, part := range parts {
			trimmed := strings.TrimSpace(part)
			if trimmed == "" {
				continue
			}
			result = append(result, trimmed)
		}
		*target(c) = result
		return nil
	}
}
