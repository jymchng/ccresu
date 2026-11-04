package app

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/clock"
	"github.com/jymchng/ccresu/internal/config"
	"github.com/jymchng/ccresu/internal/detector"
	"github.com/jymchng/ccresu/internal/recovery"
	"github.com/jymchng/ccresu/internal/scheduler"
	"github.com/jymchng/ccresu/internal/storage"
	"github.com/jymchng/ccresu/internal/telegram"
)

// harnessBaseTime is the fixed instant every test starts from, so a report is
// byte-for-byte reproducible.
var harnessBaseTime = time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)

// syncBuffer is a bytes.Buffer guarded by a mutex. Several daemon tests start a
// CLI invocation on its own goroutine and poll the captured output from the test
// goroutine, so the buffer must tolerate a concurrent Write and String.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write appends p to the buffer.
func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String returns everything captured so far.
func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Reset discards the captured output.
func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

// fakeNotifier records queued notifications and can pretend to be a drain loop.
type fakeNotifier struct {
	mu        sync.Mutex
	requests  []telegram.Request
	sent      telegram.Result
	sendErr   error
	drainErr  error
	runs      int
	notifyErr error
}

// Notify implements Notifier.
func (n *fakeNotifier) Notify(_ context.Context, req telegram.Request) (storage.Notification, bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.notifyErr != nil {
		return storage.Notification{}, false, n.notifyErr
	}
	n.requests = append(n.requests, req)
	return storage.Notification{Event: req.Event, DedupeKey: req.DedupeKey}, true, nil
}

// SendOnce implements Notifier.
func (n *fakeNotifier) SendOnce(context.Context) (telegram.Result, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.sent, n.sendErr
}

// Run implements Notifier: it blocks until the context ends, like the real drain,
// and then reports drainErr as the loop's outcome.
func (n *fakeNotifier) Run(ctx context.Context) error {
	n.mu.Lock()
	n.runs++
	err := n.drainErr
	n.mu.Unlock()
	<-ctx.Done()
	if err != nil {
		return err
	}
	return ctx.Err()
}

// Enabled implements Notifier.
func (n *fakeNotifier) Enabled(storage.NotificationEvent) bool { return true }

// MaxAttempts implements Notifier.
func (n *fakeNotifier) MaxAttempts() int { return 3 }

// queued returns the queued events.
func (n *fakeNotifier) queued() []telegram.Request {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]telegram.Request(nil), n.requests...)
}

// runCount reports how often the drain loop was started.
func (n *fakeNotifier) runCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.runs
}

// fakeTranscripts serves transcript tails from memory.
type fakeTranscripts struct {
	entries map[string][]claude.Entry
	err     error
}

// TailEntries implements Transcripts.
func (f *fakeTranscripts) TailEntries(path string, limit int) ([]claude.Entry, error) {
	if f.err != nil {
		return nil, f.err
	}
	entries := f.entries[path]
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return append([]claude.Entry(nil), entries...), nil
}

// fakeServer records its lifecycle and stops when the context is cancelled.
type fakeServer struct {
	mu      sync.Mutex
	runs    int
	stopped bool
	err     error
}

// Run implements Server: a bind failure returns at once, exactly as the real
// server does, and a healthy server blocks until the context ends.
func (s *fakeServer) Run(ctx context.Context) error {
	s.mu.Lock()
	s.runs++
	early := s.err
	s.mu.Unlock()
	if early != nil {
		return early
	}
	<-ctx.Done()
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()
	return s.err
}

// harness is one CLI invocation's worth of in-memory state.
type harness struct {
	store       *storage.Store
	provider    *claude.FakeProvider
	notifier    *fakeNotifier
	transcripts *fakeTranscripts
	rt          *Runtime
	cfg         *config.Config
	clock       *clock.Fake
	signals     chan os.Signal

	stdout syncBuffer
	stderr syncBuffer

	buildErr  error
	loadErr   error
	server    Server
	serverErr error
}

// newHarness builds a runtime over an in-memory database, a fake provider and a
// recording notifier.
func newHarness(t *testing.T) *harness {
	t.Helper()
	clk := clock.NewFake(harnessBaseTime)

	store, err := storage.Open(context.Background(), storage.Options{
		Memory: true,
		Clock:  clk.Now,
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	provider := claude.NewFakeProvider()
	manager, err := recovery.New(recovery.Config{
		Store:       store,
		Provider:    provider,
		Clock:       clk,
		Enabled:     true,
		MaxAttempts: 3,
		// A fake clock never advances on its own, so the verification poll must
		// be "check once" instead of "wait for the transcript to grow".
		VerificationTimeout: -1,
		HealthCheckInterval: -1,
	})
	if err != nil {
		t.Fatalf("recovery manager: %v", err)
	}
	sched, err := scheduler.New(scheduler.Config{
		Clock: clk,
		Store: scheduler.NewStorageStore(store),
	})
	if err != nil {
		t.Fatalf("scheduler: %v", err)
	}
	limitDetector, err := detector.New(detector.Options{Clock: clk})
	if err != nil {
		t.Fatalf("detector: %v", err)
	}

	cfg := config.Defaults()
	// A real invocation always has a readable configuration file; writing one
	// keeps `doctor` and `config` honest instead of reporting a missing file.
	cfg.Source = filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfg.Source, []byte("app:\n  name: ccresu\n"), 0o600); err != nil {
		t.Fatalf("write harness config: %v", err)
	}
	cfg.Storage.Path = filepath.Join(t.TempDir(), "state", "ccresu.db")
	cfg.Telegram.Enabled = false

	notifier := &fakeNotifier{}
	h := &harness{
		store:       store,
		provider:    provider,
		notifier:    notifier,
		transcripts: &fakeTranscripts{entries: map[string][]claude.Entry{}},
		cfg:         &cfg,
		clock:       clk,
		signals:     make(chan os.Signal, 1),
	}
	h.rt = &Runtime{
		Config:      &cfg,
		ConfigPath:  cfg.Source,
		Clock:       clk,
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       store,
		Provider:    provider,
		Transcripts: h.transcripts,
		Recovery:    manager,
		Scheduler:   sched,
		Detector:    limitDetector,
		Notifier:    notifier,
	}
	return h
}

// deps returns the injected environment for this harness.
func (h *harness) deps() Deps {
	return Deps{
		Stdout:    &h.stdout,
		Stderr:    &h.stderr,
		Clock:     h.clock,
		Signals:   h.signals,
		LookupEnv: func(string) (string, bool) { return "", false },
		LoadConfig: func(string, func(string) (string, bool)) (*config.Config, error) {
			if h.loadErr != nil {
				return nil, h.loadErr
			}
			return h.cfg, nil
		},
		DefaultConfigPath: func() (string, error) { return h.cfg.Source, nil },
		BuildRuntime: func(context.Context, *config.Config, clock.Clock, *slog.Logger) (*Runtime, error) {
			if h.buildErr != nil {
				return nil, h.buildErr
			}
			return h.rt, nil
		},
		NewServer: func(*App, *Runtime) (Server, error) {
			if h.serverErr != nil {
				return nil, h.serverErr
			}
			if h.server == nil {
				return nil, ErrNoServer
			}
			return h.server, nil
		},
	}
}

// run executes one CLI invocation and returns its exit code.
func (h *harness) run(args ...string) int {
	h.stdout.Reset()
	h.stderr.Reset()
	return RunWith(h.deps(), args, BuildInfo{Version: "1.2.3", Commit: "deadbeef", BuildDate: "2026-01-05"}, &h.stdout, &h.stderr)
}

// seedSession persists a session, applying mutate first.
func (h *harness) seedSession(t *testing.T, id string, mutate func(*storage.Session)) storage.Session {
	t.Helper()
	session := storage.Session{
		ID:             id,
		ProjectPath:    "/work/project",
		TranscriptPath: "/home/u/.claude/projects/-work-project/" + id + ".jsonl",
		Type:           storage.SessionTypeInteractive,
		State:          storage.StateRunning,
		Enabled:        true,
		Recoverable:    true,
	}
	if mutate != nil {
		mutate(&session)
	}
	if _, err := h.store.UpsertSession(context.Background(), session); err != nil {
		t.Fatalf("seed session %s: %v", id, err)
	}
	stored, err := h.store.GetSession(context.Background(), id)
	if err != nil {
		t.Fatalf("read back session %s: %v", id, err)
	}
	return stored
}

// exec runs the CLI in the current goroutine; used by tests that need the exit
// code twice.
func (h *harness) exec(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	code := h.run(args...)
	return code, h.stdout.String(), h.stderr.String()
}
