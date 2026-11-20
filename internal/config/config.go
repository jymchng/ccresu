package config

import (
	"net"
	"strconv"
	"strings"
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
	Detector   Detector   `yaml:"detector"`
	Scheduler  Scheduler  `yaml:"scheduler"`
	Recovery   Recovery   `yaml:"recovery"`
	Telegram   Telegram   `yaml:"telegram"`
	Server     Server     `yaml:"server"`
	Storage    Storage    `yaml:"storage"`

	// Source is the path the configuration was loaded from. It is not part of
	// the YAML document and is empty for programmatically built configs.
	Source string `yaml:"-"`

	// retired holds the keys the document carried that this release no longer
	// has; the loader drops them so the file still loads. It is not part of the
	// document, and RetiredKeys is the read access.
	retired []string
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
	// RescanResumed extends the transcript scan to the states the daemon would
	// otherwise never read again.
	//
	// The scan is normally limited to DISCOVERED, because every other state either
	// has a pending pass that will read the transcript anyway (WAITING_FOR_RESET
	// has its schedule, RESUMING is a resume in flight) or is a decision ccresu
	// must not walk back (DISABLED, COMPLETED, UNRECOVERABLE). RESUMED falls
	// between those two cases: it is Settled, so no pass revisits it, and a
	// resumed session that hits a new usage limit is invisible for ever. With this
	// on it is scanned too, and a limit found there parks the session in
	// WAITING_FOR_RESET instead of being missed.
	//
	// The name says RESUMED because that is the one state it adds.
	// The scan can only ever *park* a session, never resume one: it writes
	// WAITING_FOR_RESET, reaching it through DISCOVERED because the machine has no
	// RESUMED -> WAITING_FOR_RESET edge, and it has no edge from either beat to
	// RESUMING.
	//
	// Default true. Turning it off restores the pre-0.8.0 behaviour, in which a
	// limit that landed in a resumed session's transcript was only ever found by
	// an operator running `ccresu search-limit`.
	RescanResumed bool `yaml:"rescan_resumed"`
}

// Detector holds usage-limit detection tuning.
type Detector struct {
	// Threshold is the minimum confidence for a detection, in (0, 1]. It
	// matches DefaultDetectorThreshold.
	Threshold float64 `yaml:"threshold"`
	// CustomPatterns are extra rules evaluated alongside the built-in ones, so
	// a new Claude Code phrasing can be taught without a new release.
	CustomPatterns []DetectorPattern `yaml:"custom_patterns"`
}

// DetectorPattern is one user-defined detection rule.
type DetectorPattern struct {
	// ID names the rule and must be unique.
	ID string `yaml:"id"`
	// Kind overrides the detected limit kind: five_hour, weekly or other.
	// Empty keeps the inference from the message text.
	Kind string `yaml:"kind"`
	// Pattern is a Go regular expression matched against normalised text
	// (lowercase, single-spaced, ASCII punctuation). It must compile.
	Pattern string `yaml:"pattern"`
	// Confidence is the score for a match, in (0, 1].
	Confidence float64 `yaml:"confidence"`
	// RequiresReset discards the match unless a reset time was present.
	RequiresReset bool `yaml:"requires_reset"`
	// Description documents the phrasing the rule covers.
	Description string `yaml:"description"`
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
	// Guardrails bounds what one resume may cost and which CLI flags the
	// handoff may carry (PRD 5). Every knob is optional, and an unset knob
	// means today's behaviour exactly: no flag added, no check performed.
	Guardrails Guardrails `yaml:"guardrails"`
}

// Warnings reports configuration that is valid but likely to disappoint.
//
// They are advisory, not errors: refusing to start over a value the CLI may
// ignore would be worse than saying so once. The caller prints them where the
// operator will see them — at load, not at resume time, so a mistake is learned
// once rather than once per attempt.
func (c *Config) Warnings() []string {
	if c == nil {
		return nil
	}
	var warnings []string
	if len(c.retired) > 0 {
		warnings = append(warnings, strings.Join(c.retired, ", ")+
			" is no longer a configuration key: it is ignored, not rejected, so a"+
			" file written for an older release still starts. Nothing enables it and"+
			" nothing reads it — delete the line to keep the file honest")
	}
	if strings.EqualFold(strings.TrimSpace(c.Recovery.Guardrails.PermissionMode), "bypassPermissions") {
		warnings = append(warnings, "recovery.guardrails.permission_mode is bypassPermissions, "+
			"which a background session may silently ignore: Claude Code reports that a background "+
			"session ignores the bypass request made at launch unless the acceptance is in your user "+
			"or managed settings. recovery.guardrails.permission_mode dontAsk denies the prompts "+
			"nobody can answer instead")
	}
	return warnings
}

// Guardrails is PRD 5's config surface: two spend caps ccresu enforces itself,
// and the four CLI flags that survive a `--bg` handoff.
//
// The caps exist because the flag that used to bound a run's cost,
// `--max-budget-usd`, is documented as "only works with --print" and therefore
// cannot ride a handoff. The replacement bounds the series of attempts rather
// than a single turn, and §6.3 of the PRD states that hole rather than hiding it.
//
// Both caps default to zero, which means off: a non-zero default would stop
// recovery on an upgrade nobody asked to change (R12).
type Guardrails struct {
	// MaxCostUSDPerSession refuses a new attempt once the sum of the session's
	// recorded attempt costs reaches it (R5). Zero disables the check.
	MaxCostUSDPerSession float64 `yaml:"max_cost_usd_per_session"`
	// MaxCostUSDPerAttempt stops automatic recovery for the session when one
	// attempt reports more than this (R6). Zero disables the check.
	MaxCostUSDPerAttempt float64 `yaml:"max_cost_usd_per_attempt"`
	// Model becomes `--model <value>` on the handoff; empty omits the flag.
	Model string `yaml:"model"`
	// Effort becomes `--effort <value>`; empty omits the flag.
	Effort string `yaml:"effort"`
	// FallbackModel becomes `--fallback-model <value>`; empty omits the flag.
	FallbackModel string `yaml:"fallback_model"`
	// PermissionMode becomes `--permission-mode <value>`; empty omits the flag.
	// The recommended value for an unattended resume is `dontAsk`, which denies
	// what nobody can answer. `bypassPermissions` is accepted but warned about at
	// load time, because a background session may silently ignore it (§6.1).
	PermissionMode string `yaml:"permission_mode"`
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
