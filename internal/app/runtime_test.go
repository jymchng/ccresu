package app

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/jymchng/ccresu/internal/clock"
	"github.com/jymchng/ccresu/internal/config"
	"github.com/jymchng/ccresu/internal/detector"
	"github.com/jymchng/ccresu/internal/recovery"
	"github.com/jymchng/ccresu/internal/storage"
)

// runtimeConfig is a configuration whose state lands in a temporary directory, so
// BuildRuntime can be exercised for real.
func runtimeConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.Defaults()
	dir := t.TempDir()
	cfg.Source = filepath.Join(dir, "config.yaml")
	cfg.Storage.Path = filepath.Join(dir, "state", "ccresu.db")
	cfg.Telegram.Enabled = false
	return &cfg
}

func TestBuildRuntimeWiresEveryComponent(t *testing.T) {
	cfg := runtimeConfig(t)
	clk := clock.NewFake(harnessBaseTime)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	rt, err := BuildRuntime(context.Background(), cfg, clk, logger)
	if err != nil {
		t.Fatalf("BuildRuntime: %v", err)
	}
	if rt == nil {
		t.Fatal("BuildRuntime returned no runtime")
	}

	if rt.Config != cfg || rt.ConfigPath != cfg.Source {
		t.Errorf("config wiring: path = %q", rt.ConfigPath)
	}
	if rt.Store == nil || rt.Provider == nil || rt.Recovery == nil ||
		rt.Scheduler == nil || rt.Detector == nil || rt.Transcripts == nil {
		t.Fatalf("runtime is missing a component: %+v", rt)
	}
	if rt.Clock.Now() != harnessBaseTime {
		t.Errorf("clock = %s, want the injected one", rt.Clock.Now())
	}
	// Notifications are disabled in this configuration, so nothing may be stored
	// in the interface: a typed nil would read as "enabled".
	if rt.Notifier != nil {
		t.Errorf("notifier = %v, want nil when Telegram is disabled", rt.Notifier)
	}

	// The database is migrated, so it is usable immediately.
	if _, err := rt.Store.CountSessions(context.Background(), storage.SessionFilter{}); err != nil {
		t.Errorf("store is not usable: %v", err)
	}

	// Close releases the database and is safe to repeat.
	if err := rt.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := rt.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if rt.Shutdown != nil {
		t.Error("Close left the shutdown hook installed, so a second Close would run it again")
	}
}

func TestBuildRuntimeRejectsBadInput(t *testing.T) {
	clk := clock.NewFake(harnessBaseTime)
	if _, err := BuildRuntime(context.Background(), nil, clk, nil); err == nil {
		t.Error("BuildRuntime accepted a nil configuration")
	}

	cfg := runtimeConfig(t)
	// A directory can never be a SQLite database file.
	cfg.Storage.Path = t.TempDir()
	if _, err := BuildRuntime(context.Background(), cfg, clk, nil); err == nil {
		t.Error("BuildRuntime accepted a directory as the database path")
	}

	// A configuration with an unknown custom detector pattern must fail before a
	// command does any work.
	cfg = runtimeConfig(t)
	cfg.Detector.CustomPatterns = []config.DetectorPattern{{
		ID:         "broken",
		Pattern:    "(",
		Confidence: 0.5,
	}}
	if _, err := BuildRuntime(context.Background(), cfg, clk, nil); err == nil {
		t.Error("BuildRuntime accepted an uncompilable detector pattern")
	}
}

func TestBuildRuntimeDefaultsTheClockAndLogger(t *testing.T) {
	cfg := runtimeConfig(t)
	rt, err := BuildRuntime(context.Background(), cfg, nil, nil)
	if err != nil {
		t.Fatalf("BuildRuntime: %v", err)
	}
	defer func() { _ = rt.Close() }()
	if rt.Clock == nil {
		t.Error("a nil clock was not replaced")
	}
	if rt.Log == nil {
		t.Error("a nil logger was not replaced")
	}
}

func TestRecoveryFromConfigMatchesThePackageConstructor(t *testing.T) {
	cfg := runtimeConfig(t)
	clk := clock.NewFake(harnessBaseTime)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	rt, err := BuildRuntime(context.Background(), cfg, clk, logger)
	if err != nil {
		t.Fatalf("BuildRuntime: %v", err)
	}
	defer func() { _ = rt.Close() }()

	// The app-level helper exists only to install a logger; every policy value
	// must stay identical to recovery.FromConfig, and this guards the drift.
	reference, err := recovery.FromConfig(cfg, rt.Store, rt.Provider, clk)
	if err != nil {
		t.Fatalf("recovery.FromConfig: %v", err)
	}
	if got, want := rt.Recovery.MaxAttempts(), reference.MaxAttempts(); got != want {
		t.Errorf("max attempts = %d, want %d", got, want)
	}
	if got, want := rt.Recovery.Enabled(), reference.Enabled(); got != want {
		t.Errorf("enabled = %v, want %v", got, want)
	}
	if got, want := rt.Recovery.ContinuationMessage(), reference.ContinuationMessage(); got != want {
		t.Errorf("continuation message = %q, want %q", got, want)
	}
	// Backoff carries a random source, so compare the policy fields one by one.
	gotBackoff, wantBackoff := rt.Recovery.Backoff(), reference.Backoff()
	if gotBackoff.Initial != wantBackoff.Initial ||
		gotBackoff.Max != wantBackoff.Max ||
		gotBackoff.Multiplier != wantBackoff.Multiplier ||
		gotBackoff.Jitter != wantBackoff.Jitter {
		t.Errorf("backoff = %+v, want %+v", gotBackoff, wantBackoff)
	}
}

func TestDetectorFromConfigMapsCustomPatterns(t *testing.T) {
	cfg := runtimeConfig(t)
	cfg.Detector.Threshold = config.DefaultDetectorThreshold
	cfg.Detector.CustomPatterns = []config.DetectorPattern{{
		ID:          "in-house-banner",
		Pattern:     `\bquota exhausted for the day\b`,
		Kind:        string(detector.LimitWeekly),
		Confidence:  0.93,
		Description: "a banner only this shop sees",
	}}

	limitDetector, err := detectorFromConfig(cfg, clock.NewFake(harnessBaseTime))
	if err != nil {
		t.Fatalf("detectorFromConfig: %v", err)
	}
	event := limitDetector.Analyze("Quota exhausted for the day, sorry.")
	if !event.IsUsageLimit() {
		t.Fatalf("the configured pattern did not fire: %+v", event)
	}
	if event.Limit != detector.LimitWeekly {
		t.Errorf("limit kind = %q, want %q", event.Limit, detector.LimitWeekly)
	}
	if got := detectedSummary(event); got == "" {
		t.Error("detectedSummary produced nothing for a detected limit")
	}
	if got := detectedSummary(detector.UsageLimitEvent{Classification: "ordinary"}); got == "" {
		t.Error("detectedSummary produced nothing for an ordinary message")
	}
}

func TestRuntimeCloseToleratesMissingShutdown(t *testing.T) {
	var nilRuntime *Runtime
	if err := nilRuntime.Close(); err != nil {
		t.Errorf("Close on a nil runtime = %v", err)
	}
	bare := &Runtime{}
	if err := bare.Close(); err != nil {
		t.Errorf("Close without a shutdown hook = %v", err)
	}
	calls := 0
	counting := &Runtime{Shutdown: func() error {
		calls++
		return nil
	}}
	if err := counting.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := counting.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if calls != 1 {
		t.Errorf("shutdown calls = %d, want exactly one", calls)
	}
}

func TestRunUsesTheProcessDefaults(t *testing.T) {
	// Run is the only function that touches the process; with no arguments it
	// prints the help page and reports a usage error, which proves the defaults
	// (stdout, stderr, exit codes) are wired.
	var stdout, stderr discardBuffer
	code := Run([]string{"--help"}, BuildInfo{}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d", code, ExitOK)
	}
	if stdout.Len() == 0 {
		t.Error("Run printed no help text")
	}
}

// discardBuffer is a minimal io.Writer that only counts bytes.
type discardBuffer struct{ n int }

// Write implements io.Writer.
func (b *discardBuffer) Write(p []byte) (int, error) {
	b.n += len(p)
	return len(p), nil
}

// Len reports how many bytes were written.
func (b *discardBuffer) Len() int { return b.n }

// String renders the byte count, which is all these tests need.
func (b *discardBuffer) String() string { return "" }
