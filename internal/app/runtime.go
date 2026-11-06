package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/clock"
	"github.com/jymchng/ccresu/internal/config"
	"github.com/jymchng/ccresu/internal/detector"
	"github.com/jymchng/ccresu/internal/recovery"
	"github.com/jymchng/ccresu/internal/scheduler"
	"github.com/jymchng/ccresu/internal/storage"
	"github.com/jymchng/ccresu/internal/telegram"
)

// ErrNoServer reports that the HTTP API is not available in this build.
var ErrNoServer = errors.New("the local HTTP API is not available in this build of ccresu")

// Notifier is the notification queue the commands and the daemon use.
// *telegram.Notifier satisfies it; a test substitutes a recorder.
type Notifier interface {
	Notify(ctx context.Context, req telegram.Request) (storage.Notification, bool, error)
	SendOnce(ctx context.Context) (telegram.Result, error)
	Run(ctx context.Context) error
	Enabled(event storage.NotificationEvent) bool
	MaxAttempts() int
}

// Server is the HTTP API `serve` runs. internal/server supplies the real one
// through Deps.NewServer.
type Server interface {
	// Run serves until ctx is cancelled and then shuts down gracefully.
	Run(ctx context.Context) error
}

// Transcripts reads transcripts: the tail for detection, and the whole-file
// measurement the rediscovery stage compares between two looks. *claude.Store
// satisfies it.
type Transcripts interface {
	TailEntries(path string, n int) ([]claude.Entry, error)
	// ScanTranscript measures a transcript, so a row can be compared against the
	// line count its last proved resume recorded.
	ScanTranscript(path string) (claude.TranscriptInfo, error)
}

// VersionReporter reports the Claude Code version in use. *claude.Provider
// satisfies it.
type VersionReporter interface {
	Version(ctx context.Context) (claude.VersionReport, error)
}

// Runtime is everything the long-lived commands operate on.
//
// Every field is either a value a test can build in memory or an interface it can
// fake, so `daemon`, `status`, `recover` and the rest are testable without a
// database file, a Claude Code installation or a Telegram bot.
type Runtime struct {
	// Config is the loaded configuration.
	Config *config.Config
	// ConfigPath is where the configuration came from.
	ConfigPath string
	// Clock is the shared time source.
	Clock clock.Clock
	// Log receives structured diagnostics.
	Log *slog.Logger
	// Store is the SQLite repository bundle.
	Store *storage.Store
	// Provider discovers and resumes sessions.
	Provider claude.SessionProvider
	// Transcripts reads transcript tails for detection. Optional.
	Transcripts Transcripts
	// Recovery is the attempt engine.
	Recovery *recovery.Manager
	// Scheduler resolves reset times and persists waits.
	Scheduler *scheduler.Scheduler
	// Detector recognises usage-limit messages. Optional.
	Detector *detector.Detector
	// Notifier is the Telegram queue. Nil when notifications are disabled.
	Notifier Notifier
	// Shutdown releases resources; nil is a no-op.
	Shutdown func() error
}

// Close releases the runtime. It is safe to call more than once.
func (r *Runtime) Close() error {
	if r == nil || r.Shutdown == nil {
		return nil
	}
	err := r.Shutdown()
	r.Shutdown = nil
	return err
}

// BuildRuntime assembles the production runtime: a migrated SQLite store, the
// real Claude Code provider, the recovery manager, the scheduler and — when it is
// enabled and configured — the Telegram notifier.
//
// Everything that can fail is attempted here, before a command does any work, so
// a broken configuration or an unreadable database is reported once, clearly,
// instead of halfway through a recovery.
func BuildRuntime(ctx context.Context, cfg *config.Config, clk clock.Clock, log *slog.Logger) (*Runtime, error) {
	if cfg == nil {
		return nil, errors.New("app: nil configuration")
	}
	if clk == nil {
		clk = clock.System()
	}
	if log == nil {
		log = slog.Default()
	}

	dbPath, err := config.ExpandPath(cfg.Storage.Path)
	if err != nil {
		return nil, fmt.Errorf("storage.path %q: %w", cfg.Storage.Path, err)
	}
	store, err := storage.Open(ctx, storage.Options{
		Path:        dbPath,
		BusyTimeout: cfg.Storage.BusyTimeout.Duration,
		Clock:       clk.Now,
	})
	if err != nil {
		return nil, err
	}
	if _, err := store.Migrate(ctx); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("migrate %s: %w", dbPath, err)
	}

	provider, err := claude.New(claude.Options{
		Executable: cfg.Claude.Executable,
		Clock:      clk,
		Logger:     log,
		All:        cfg.Claude.DiscoverAllSessions,
	})
	if err != nil {
		_ = store.Close()
		return nil, err
	}

	manager, err := recoveryFromConfig(cfg, store, provider, clk, log)
	if err != nil {
		_ = store.Close()
		return nil, err
	}

	sched, err := scheduler.FromConfig(cfg, clk, scheduler.NewStorageStore(store))
	if err != nil {
		_ = store.Close()
		return nil, err
	}

	limitDetector, err := detectorFromConfig(cfg, clk)
	if err != nil {
		_ = store.Close()
		return nil, err
	}

	notifier, err := telegram.NewFromConfig(cfg, store, clk, log)
	if err != nil {
		_ = store.Close()
		return nil, err
	}

	runtime := &Runtime{
		Config:      cfg,
		ConfigPath:  cfg.Source,
		Clock:       clk,
		Log:         log,
		Store:       store,
		Provider:    provider,
		Transcripts: provider.Store(),
		Recovery:    manager,
		Scheduler:   sched,
		Detector:    limitDetector,
		Shutdown: func() error {
			return store.Close()
		},
	}
	// A typed nil must not be stored in the interface: a disabled notifier has to
	// read as "no notifier", not as a non-nil interface over a nil pointer.
	if notifier != nil {
		runtime.Notifier = notifier
	}
	return runtime, nil
}

// recoveryFromConfig mirrors recovery.FromConfig and additionally installs the
// structured logger, which FromConfig cannot take without changing its verified
// signature.
//
// The mapping is kept identical on purpose; TestRecoveryFromConfigMatches is the
// guard against the two drifting apart.
func recoveryFromConfig(cfg *config.Config, store *storage.Store, provider claude.SessionProvider, clk clock.Clock, log *slog.Logger) (*recovery.Manager, error) {
	rec := cfg.Recovery
	return recovery.New(recovery.Config{
		Store:               store,
		Provider:            provider,
		Clock:               clk,
		Logger:              log,
		Enabled:             rec.Enabled,
		MaxAttempts:         rec.MaxAttempts,
		Backoff:             recovery.FromConfigValues(rec.InitialDelay.Duration, rec.MaxDelay.Duration, rec.Multiplier, rec.Jitter),
		MaxConcurrent:       rec.MaxConcurrentRecoveries,
		ContinuationMessage: cfg.Claude.ContinuationMessage,
		VerificationTimeout: rec.VerificationTimeout.Duration,
		HealthCheckInterval: rec.HealthCheckInterval.Duration,
	})
}

// detectorFromConfig builds the usage-limit detector, including the custom
// patterns from the configuration file.
func detectorFromConfig(cfg *config.Config, clk clock.Clock) (*detector.Detector, error) {
	patterns := make([]detector.Pattern, 0, len(cfg.Detector.CustomPatterns))
	for _, custom := range cfg.Detector.CustomPatterns {
		patterns = append(patterns, detector.Pattern{
			ID:            custom.ID,
			Expression:    custom.Pattern,
			Kind:          detector.LimitKind(custom.Kind),
			Confidence:    custom.Confidence,
			RequiresReset: custom.RequiresReset,
			Description:   custom.Description,
		})
	}
	return detector.New(detector.Options{
		Threshold: cfg.Detector.Threshold,
		Patterns:  patterns,
		Clock:     clk,
	})
}
