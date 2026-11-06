package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// ValidationError reports every problem found in a configuration document.
//
// All problems are collected before returning so that a user fixing a config
// sees the complete list instead of one error per run.
type ValidationError struct {
	Problems []string
}

// Error renders the problems as a single, stable message.
func (e *ValidationError) Error() string {
	if len(e.Problems) == 1 {
		return fmt.Sprintf("%v: %s", ErrInvalid, e.Problems[0])
	}
	return fmt.Sprintf("%v: %d problems: %s", ErrInvalid, len(e.Problems), strings.Join(e.Problems, "; "))
}

// Is makes errors.Is(err, ErrInvalid) succeed for validation failures.
func (e *ValidationError) Is(target error) bool { return target == ErrInvalid }

// Validate checks every field of cfg and returns a *ValidationError listing all
// problems. It never mutates cfg.
func (c *Config) Validate() error {
	if c == nil {
		return &ValidationError{Problems: []string{"config is nil"}}
	}

	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	c.validateApp(add)
	c.validateClaude(add)
	c.validateMonitoring(add)
	c.validateDetector(add)
	c.validateScheduler(add)
	c.validateRecovery(add)
	c.validateTelegram(add)
	c.validateServer(add)
	c.validateStorage(add)

	if len(problems) == 0 {
		return nil
	}
	return &ValidationError{Problems: problems}
}

type problemAdder func(format string, args ...any)

func (c *Config) validateApp(add problemAdder) {
	if strings.TrimSpace(c.App.Name) == "" {
		add("app.name must not be empty")
	}
	switch strings.ToLower(strings.TrimSpace(c.App.LogLevel)) {
	case "debug", "info", "warn", "error":
	default:
		add("app.log_level must be one of debug, info, warn, error (got %q)", c.App.LogLevel)
	}
	switch strings.ToLower(strings.TrimSpace(c.App.LogFormat)) {
	case "json", "text":
	default:
		add("app.log_format must be one of json, text (got %q)", c.App.LogFormat)
	}
	if _, err := time.LoadLocation(strings.TrimSpace(c.App.Timezone)); err != nil {
		add("app.timezone %q is not a valid IANA timezone", c.App.Timezone)
	}
}

func (c *Config) validateClaude(add problemAdder) {
	if strings.TrimSpace(c.Claude.Executable) == "" {
		add("claude.executable must not be empty")
	}
	if c.Claude.DiscoveryInterval.Duration <= 0 {
		add("claude.discovery_interval must be greater than zero")
	}
	for i, id := range c.Claude.MonitoredSessions {
		if strings.TrimSpace(id) == "" {
			add("claude.monitored_sessions[%d] must not be empty", i)
		}
	}
	switch strings.ToLower(strings.TrimSpace(c.Claude.DefaultEffort)) {
	case "", "low", "medium", "high", "xhigh", "max":
	default:
		add("claude.default_effort must be empty, low, medium, high, xhigh or max (got %q)", c.Claude.DefaultEffort)
	}
	if c.Claude.ResumeEnabled && strings.TrimSpace(c.Claude.ContinuationMessage) == "" {
		add("claude.continuation_message must not be empty when resume is enabled")
	}
}

func (c *Config) validateMonitoring(add problemAdder) {
	if !c.Monitoring.Enabled {
		return
	}
	if !c.Monitoring.DetectUsageLimits && !c.Monitoring.DetectSessionCompletion {
		add("monitoring is enabled but nothing is being detected: set detect_usage_limits or detect_session_completion")
	}
}

// validateDetector checks the usage-limit detection rules.
//
// A custom rule is rejected unless its expression compiles, because a rule that
// cannot run would silently fail to detect the very phrasing the user added it
// for.
func (c *Config) validateDetector(add problemAdder) {
	if c.Detector.Threshold <= 0 || c.Detector.Threshold > 1 {
		add("detector.threshold must be in the range (0, 1] (got %v)", c.Detector.Threshold)
	}
	seen := make(map[string]struct{}, len(c.Detector.CustomPatterns))
	for i, pattern := range c.Detector.CustomPatterns {
		id := strings.TrimSpace(pattern.ID)
		if id == "" {
			add("detector.custom_patterns[%d].id must not be empty", i)
		} else if _, duplicate := seen[id]; duplicate {
			add("detector.custom_patterns[%d].id %q is duplicated", i, id)
		}
		seen[id] = struct{}{}

		if strings.TrimSpace(pattern.Pattern) == "" {
			add("detector.custom_patterns[%d].pattern must not be empty", i)
		} else if _, err := regexp.Compile(pattern.Pattern); err != nil {
			add("detector.custom_patterns[%d].pattern is not a valid regular expression: %v", i, err)
		}
		if pattern.Confidence <= 0 || pattern.Confidence > 1 {
			add("detector.custom_patterns[%d].confidence must be in the range (0, 1] (got %v)", i, pattern.Confidence)
		}
		switch strings.ToLower(strings.TrimSpace(pattern.Kind)) {
		case "", "five_hour", "weekly", "other":
		default:
			add("detector.custom_patterns[%d].kind must be empty, five_hour, weekly or other (got %q)", i, pattern.Kind)
		}
	}
}

func (c *Config) validateScheduler(add problemAdder) {
	if c.Scheduler.PollInterval.Duration <= 0 {
		add("scheduler.poll_interval must be greater than zero")
	}
	if c.Scheduler.UnknownResetInitialInterval.Duration <= 0 {
		add("scheduler.unknown_reset_initial_interval must be greater than zero")
	}
	if c.Scheduler.UnknownResetMaxInterval.Duration <= 0 {
		add("scheduler.unknown_reset_max_interval must be greater than zero")
	}
	if c.Scheduler.UnknownResetInitialInterval.Duration > 0 &&
		c.Scheduler.UnknownResetMaxInterval.Duration > 0 &&
		c.Scheduler.UnknownResetMaxInterval.Duration < c.Scheduler.UnknownResetInitialInterval.Duration {
		add("scheduler.unknown_reset_max_interval must be greater than or equal to unknown_reset_initial_interval")
	}
}

func (c *Config) validateRecovery(add problemAdder) {
	if c.Recovery.MaxAttempts < 1 {
		add("recovery.max_attempts must be at least 1")
	}
	if c.Recovery.InitialDelay.Duration <= 0 {
		add("recovery.initial_delay must be greater than zero")
	}
	if c.Recovery.MaxDelay.Duration <= 0 {
		add("recovery.max_delay must be greater than zero")
	}
	if c.Recovery.InitialDelay.Duration > 0 &&
		c.Recovery.MaxDelay.Duration > 0 &&
		c.Recovery.MaxDelay.Duration < c.Recovery.InitialDelay.Duration {
		add("recovery.max_delay must be greater than or equal to initial_delay")
	}
	if c.Recovery.Multiplier < 1 {
		add("recovery.multiplier must be at least 1")
	}
	if c.Recovery.Jitter < 0 || c.Recovery.Jitter >= 1 {
		add("recovery.jitter must be in the range [0, 1)")
	}
	if c.Recovery.StartupTimeout.Duration <= 0 {
		add("recovery.startup_timeout must be greater than zero")
	}
	if c.Recovery.VerificationTimeout.Duration <= 0 {
		add("recovery.verification_timeout must be greater than zero")
	}
	if c.Recovery.HealthCheckInterval.Duration <= 0 {
		add("recovery.health_check_interval must be greater than zero")
	}
	if c.Recovery.MaxConcurrentRecoveries < 1 {
		add("recovery.max_concurrent_recoveries must be at least 1")
	}
	if c.Recovery.RediscoveryWindow.Duration <= 0 {
		add("recovery.rediscovery_window must be greater than zero")
	}
}

func (c *Config) validateTelegram(add problemAdder) {
	if mode := normalizeParseMode(c.Telegram.ParseMode); mode != "HTML" {
		add("telegram.parse_mode must be HTML, the only mode ccresu sends (got %q)", c.Telegram.ParseMode)
	}
	if !c.Telegram.Enabled {
		return
	}
	if strings.TrimSpace(c.Telegram.BotToken) == "" {
		add("telegram.bot_token must be set when telegram.enabled is true")
	} else if !validBotToken(c.Telegram.BotToken) {
		add("telegram.bot_token is not shaped like a Telegram bot token (expected <digits>:<token>)")
	}
	if strings.TrimSpace(c.Telegram.ChatID) == "" {
		add("telegram.chat_id must be set when telegram.enabled is true")
	}
}

func (c *Config) validateServer(add problemAdder) {
	if !c.Server.Enabled {
		return
	}
	if strings.TrimSpace(c.Server.Host) == "" {
		add("server.host must not be empty")
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		add("server.port must be between 1 and 65535 (got %d)", c.Server.Port)
	}
	for i, origin := range c.Server.AllowedOrigins {
		parsed, err := url.Parse(strings.TrimSpace(origin))
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			add("server.allowed_origins[%d] must be an absolute http(s) origin (got %q)", i, origin)
			continue
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			add("server.allowed_origins[%d] must use http or https (got %q)", i, origin)
		}
		if parsed.Path != "" && parsed.Path != "/" {
			add("server.allowed_origins[%d] must not include a path (got %q)", i, origin)
		}
	}
	if strings.TrimSpace(c.Server.TokenPath) == "" {
		add("server.token_path must resolve to a file path")
	}
}

func (c *Config) validateStorage(add problemAdder) {
	if driver := strings.ToLower(strings.TrimSpace(c.Storage.Driver)); driver != "sqlite" {
		add("storage.driver must be sqlite (got %q)", c.Storage.Driver)
	}
	if strings.TrimSpace(c.Storage.Path) == "" {
		add("storage.path must not be empty")
	}
	if c.Storage.BusyTimeout.Duration <= 0 {
		add("storage.busy_timeout must be greater than zero")
	}
}

// normalizeParseMode maps a configured value onto the Telegram API spelling,
// treating an empty value as the documented HTML default.
//
// This only normalises spelling for display and comparison; it does not make a
// mode usable. HTML is the only mode ccresu renders (its templates emit
// <b>/<code> spans and escape for the HTML parser), so validateTelegram rejects
// everything else and NewClient refuses it too.
func normalizeParseMode(mode string) string {
	trimmed := strings.TrimSpace(mode)
	if trimmed == "" {
		return "HTML"
	}
	switch strings.ToLower(trimmed) {
	case "html":
		return "HTML"
	case "markdownv2", "markdown_v2":
		return "MarkdownV2"
	case "markdown", "markdownv1":
		return "Markdown"
	default:
		return trimmed
	}
}

// NormalizedParseMode exposes the Telegram API spelling of telegram.parse_mode.
func (c Config) NormalizedParseMode() string { return normalizeParseMode(c.Telegram.ParseMode) }

// validBotToken reports whether token looks like "<numeric id>:<secret>".
func validBotToken(token string) bool {
	trimmed := strings.TrimSpace(token)
	id, secret, found := strings.Cut(trimmed, ":")
	if !found || id == "" || secret == "" {
		return false
	}
	for _, r := range id {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	if len(secret) < 20 {
		return false
	}
	for _, r := range secret {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

// MaskSecret renders a secret for display without revealing it: short values
// are fully masked and longer ones keep only a short suffix.
func MaskSecret(secret string) string {
	trimmed := strings.TrimSpace(secret)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) <= 8 {
		return strings.Repeat("*", len(trimmed))
	}
	return strings.Repeat("*", 8) + trimmed[len(trimmed)-4:]
}
