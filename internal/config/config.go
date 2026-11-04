package config

import (
	"net"
	"strconv"
	"time"

	// Embed the IANA timezone database so reset-time parsing works on hosts
	// without zoneinfo (notably Windows release binaries).
	_ "time/tzdata"
)

// Config is the complete ccresu configuration.
type Config struct {
	App        App        `yaml:"app"`
	Claude     Claude     `yaml:"claude"`
	Monitoring Monitoring `yaml:"monitoring"`
	Scheduler  Scheduler  `yaml:"scheduler"`
	Recovery   Recovery   `yaml:"recovery"`
	Telegram   Telegram   `yaml:"telegram"`
	Server     Server     `yaml:"server"`
	Storage    Storage    `yaml:"storage"`

	// Source is the path the configuration was loaded from. It is not part of
	// the YAML document and is empty for programmatically built configs.
	Source string `yaml:"-"`
}

// App holds process-wide settings.
type App struct {
	Name      string `yaml:"name"`
	LogLevel  string `yaml:"log_level"`
	LogFormat string `yaml:"log_format"`
	Timezone  string `yaml:"timezone"`
}

// Claude holds Claude Code discovery and resumption settings.
type Claude struct {
	Executable          string   `yaml:"executable"`
	DiscoveryInterval   Duration `yaml:"discovery_interval"`
	MonitoredSessions   []string `yaml:"monitored_sessions"`
	DiscoverAllSessions bool     `yaml:"discover_all_sessions"`
	ResumeEnabled       bool     `yaml:"resume_enabled"`
	DefaultModel        string   `yaml:"default_model"`
	DefaultEffort       string   `yaml:"default_effort"`
	ContinuationMessage string   `yaml:"continuation_message"`
}

// Monitoring holds detection switches.
type Monitoring struct {
	Enabled                 bool `yaml:"enabled"`
	DetectUsageLimits       bool `yaml:"detect_usage_limits"`
	DetectSessionCompletion bool `yaml:"detect_session_completion"`
}

// Scheduler holds reset-polling settings.
type Scheduler struct {
	PollInterval                Duration `yaml:"poll_interval"`
	UnknownResetInitialInterval Duration `yaml:"unknown_reset_initial_interval"`
	UnknownResetMaxInterval     Duration `yaml:"unknown_reset_max_interval"`
	RecheckBeforeResume         bool     `yaml:"recheck_before_resume"`
}

// Recovery holds retry and concurrency settings.
type Recovery struct {
	Enabled                 bool     `yaml:"enabled"`
	MaxAttempts             int      `yaml:"max_attempts"`
	InitialDelay            Duration `yaml:"initial_delay"`
	MaxDelay                Duration `yaml:"max_delay"`
	Multiplier              float64  `yaml:"multiplier"`
	Jitter                  float64  `yaml:"jitter"`
	StartupTimeout          Duration `yaml:"startup_timeout"`
	VerificationTimeout     Duration `yaml:"verification_timeout"`
	HealthCheckInterval     Duration `yaml:"health_check_interval"`
	MaxConcurrentRecoveries int      `yaml:"max_concurrent_recoveries"`
}

// Telegram holds optional notification settings.
type Telegram struct {
	Enabled                bool   `yaml:"enabled"`
	BotToken               string `yaml:"bot_token"`
	ChatID                 string `yaml:"chat_id"`
	ParseMode              string `yaml:"parse_mode"`
	NotifyLimitDetected    bool   `yaml:"notify_limit_detected"`
	NotifyWaiting          bool   `yaml:"notify_waiting"`
	NotifyRecoveryStarted  bool   `yaml:"notify_recovery_started"`
	NotifyRecoverySuccess  bool   `yaml:"notify_recovery_success"`
	NotifyRecoveryFailure  bool   `yaml:"notify_recovery_failure"`
	NotifyPermanentFailure bool   `yaml:"notify_permanent_failure"`
	NotifySessionCompleted bool   `yaml:"notify_session_completed"`
}

// Server holds local API settings.
type Server struct {
	Enabled        bool     `yaml:"enabled"`
	Host           string   `yaml:"host"`
	Port           int      `yaml:"port"`
	AllowedOrigins []string `yaml:"allowed_origins"`
	TokenPath      string   `yaml:"token_path"`
}

// Addr returns the host:port the API server binds to.
func (s Server) Addr() string {
	return net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
}

// Storage holds persistence settings.
type Storage struct {
	Driver      string   `yaml:"driver"`
	Path        string   `yaml:"path"`
	BusyTimeout Duration `yaml:"busy_timeout"`
}

// DisplayLocation resolves app.timezone. Validation guarantees the zone loads,
// so callers may treat an error here as an internal bug.
func (c Config) DisplayLocation() (*time.Location, error) {
	return time.LoadLocation(c.App.Timezone)
}
