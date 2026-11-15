package recovery

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jymchng/ccresu/internal/claude"
)

// supervisorStub is a SupervisorReader driven by functions, so each test states
// exactly what `claude daemon status` answered and when.
type supervisorStub struct {
	mu        sync.Mutex
	statusFn  func(call int) (claude.SupervisorStatus, error)
	cliFn     func(call int) (claude.Version, error)
	minimum   claude.Version
	statuses  int
	versioned int
}

func (s *supervisorStub) DaemonStatus(context.Context) (claude.SupervisorStatus, error) {
	s.mu.Lock()
	s.statuses++
	call := s.statuses
	fn := s.statusFn
	s.mu.Unlock()
	if fn == nil {
		return claude.SupervisorStatus{}, nil
	}
	return fn(call)
}

func (s *supervisorStub) CLIVersion(context.Context) (claude.Version, error) {
	s.mu.Lock()
	s.versioned++
	call := s.versioned
	fn := s.cliFn
	s.mu.Unlock()
	if fn == nil {
		return claude.Version{}, nil
	}
	return fn(call)
}

func (s *supervisorStub) MinimumVersion() claude.Version { return s.minimum }

func (s *supervisorStub) statusCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statuses
}

// runningSupervisor is a supervisor that answered, with a version.
func runningSupervisor(version string) claude.SupervisorStatus {
	parsed, err := claude.ParseVersion(version)
	if err != nil {
		panic(err)
	}
	return claude.SupervisorStatus{
		Running:      true,
		State:        "running",
		Version:      parsed,
		Workers:      2,
		WorkersKnown: true,
		Raw:          "state: running\nversion: " + version + "\nworkers: 2",
		ExitCode:     0,
		Argv:         claude.DaemonStatusArgs(),
	}
}

func parsedVersion(t *testing.T, raw string) claude.Version {
	t.Helper()
	version, err := claude.ParseVersion(raw)
	if err != nil {
		t.Fatalf("ParseVersion(%q): %v", raw, err)
	}
	return version
}

// TestOneDaemonStatusReadServesEverySessionInAPass is R1's acceptance test at the
// engine: the supervisor is read once per pass, and every session in the pass is
// answered from that one reading.
func TestOneDaemonStatusReadServesEverySessionInAPass(t *testing.T) {
	store := newTestStore(t)
	for _, id := range []string{sessionA, sessionB, sessionC} {
		seedSession(t, store, recoverableSession(id))
	}
	supervisor := &supervisorStub{
		statusFn: func(int) (claude.SupervisorStatus, error) {
			return runningSupervisor(claude.MinimumVersion.String()), nil
		},
		cliFn:   func(int) (claude.Version, error) { return claude.MinimumVersion, nil },
		minimum: claude.MinimumVersion,
	}
	provider := growthProvider(10, 12)
	manager := testManager(t, store, provider, func(cfg *Config) { cfg.Supervisor = supervisor })

	for _, id := range []string{sessionA, sessionB, sessionC} {
		if _, err := manager.Recover(context.Background(), id); err != nil {
			t.Fatalf("Recover(%s) error = %v", id, err)
		}
	}

	if got := supervisor.statusCount(); got != 1 {
		t.Errorf("`claude daemon status` invocations = %d, want exactly 1 for the pass", got)
	}
}

// TestNoSupervisorIsANormalAnswer is R1's other half: exit 1 means there is no
// supervisor, which is the normal state of a machine that has never run
// `claude --bg`. It must not fail the pass, and the pass must still recover.
func TestNoSupervisorIsANormalAnswer(t *testing.T) {
	store := newTestStore(t)
	seedSession(t, store, recoverableSession(sessionA))
	supervisor := &supervisorStub{
		statusFn: func(int) (claude.SupervisorStatus, error) {
			return claude.SupervisorStatus{ExitCode: 1, Argv: claude.DaemonStatusArgs()}, nil
		},
		minimum: claude.MinimumVersion,
	}
	manager := testManager(t, store, growthProvider(10, 12), func(cfg *Config) { cfg.Supervisor = supervisor })

	outcome, err := manager.Recover(context.Background(), sessionA)
	if err != nil {
		t.Fatalf("Recover error = %v, want no supervisor to be a normal answer", err)
	}
	if outcome.Supervisor.Running {
		t.Error("Outcome.Supervisor.Running = true, want false")
	}
	if outcome.State != StateResumed {
		t.Errorf("state = %s, want RESUMED: an absent supervisor does not block a recovery", outcome.State)
	}
}

// TestAnUnreadableSupervisorStatusIsItsOwnFailure is R1's third answer: anything
// but exit 0 or 1 is a named failure, and nothing is handed off while the
// supervisor cannot answer — a handoff could start a second one.
func TestAnUnreadableSupervisorStatusIsItsOwnFailure(t *testing.T) {
	store := newTestStore(t)
	seedSession(t, store, recoverableSession(sessionA))
	supervisor := &supervisorStub{
		statusFn: func(int) (claude.SupervisorStatus, error) {
			return claude.SupervisorStatus{ExitCode: 2, Stderr: "unknown command: daemon"},
				claude.ErrDaemonStatusFailed
		},
		minimum: claude.MinimumVersion,
	}
	provider := growthProvider(10, 12)
	manager := testManager(t, store, provider, func(cfg *Config) { cfg.Supervisor = supervisor })

	outcome, err := manager.Recover(context.Background(), sessionA)
	if !errors.Is(err, ErrSupervisorReadFailed) {
		t.Fatalf("Recover error = %v, want ErrSupervisorReadFailed", err)
	}
	if !errors.Is(err, claude.ErrDaemonStatusFailed) {
		t.Errorf("Recover error = %v, want it to keep the CLI's own condition", err)
	}
	if provider.resumeCount() != 0 {
		t.Error("a resume was issued while the supervisor could not be read")
	}
	if outcome.State != StateRetryWait {
		t.Errorf("state = %s, want RETRY_WAIT: the failure is retried, not fatal", outcome.State)
	}
}

// TestAVersionMismatchRunsNothing is R2's acceptance test at the engine: a
// supervisor on a different version is a named condition carrying both versions
// and the documented remedy, and the pass runs nothing — no handoff, and no
// `daemon stop` on its own initiative (R3).
func TestAVersionMismatchRunsNothing(t *testing.T) {
	store := newTestStore(t)
	seedSession(t, store, recoverableSession(sessionA))
	supervisor := &supervisorStub{
		statusFn: func(int) (claude.SupervisorStatus, error) {
			return runningSupervisor("2.1.223"), nil
		},
		cliFn:   func(int) (claude.Version, error) { return parsedVersion(t, "2.1.300"), nil },
		minimum: parsedVersion(t, "2.1.257"),
	}
	provider := growthProvider(10, 12)
	manager := testManager(t, store, provider, func(cfg *Config) { cfg.Supervisor = supervisor })

	_, err := manager.Recover(context.Background(), sessionA)
	if !errors.Is(err, claude.ErrSupervisorVersionMismatch) {
		t.Fatalf("Recover error = %v, want ErrSupervisorVersionMismatch", err)
	}
	for _, want := range []string{"2.1.223", "2.1.300", claude.RemedySupervisorVersionMismatch} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
	if provider.resumeCount() != 0 {
		t.Error("a resume was issued while the supervisor was a different version")
	}

	// The condition is recorded, not swallowed: the operator reads it from the
	// session rather than having to reproduce the pass.
	stored, getErr := store.GetSession(context.Background(), sessionA)
	if getErr != nil {
		t.Fatalf("GetSession: %v", getErr)
	}
	if !strings.Contains(stored.LastError, claude.RemedySupervisorVersionMismatch) {
		t.Errorf("LastError = %q, want the documented remedy", stored.LastError)
	}
}

// TestSupervisorStoppedMidHandoffIsRetryable is R7's acceptance test.
//
// The handoff is accepted, and then the supervisor loses the session — it was
// stopped, or the supervisor itself went away, which the docs say is normal. The
// pass must not report that as a recovery: the continuation is not there, so the
// attempt is closed unresolved, the session is parked in RETRY_WAIT with a
// scheduled retry that re-observes before handing off again, and nothing is
// announced as a permanent failure.
func TestSupervisorStoppedMidHandoffIsRetryable(t *testing.T) {
	store := newTestStore(t)
	seedSession(t, store, recoverableSession(sessionA))

	// The session is listed (and working) before the handoff, and gone after it.
	provider := &providerStub{
		statusFn: func(_ int, sessionID string) (claude.SessionStatus, error) {
			return statusFor(sessionID, 10, 200), nil
		},
		listingFn: func(call int) (claude.PublishedListing, error) {
			if call == 1 {
				// Listed, and stopped outside ccresu: the mapping still hands the
				// continuation off (an idle stop is expected and the conversation is
				// on disk), which is what makes the second read meaningful.
				entry := claude.AgentEntry{SessionID: sessionA, Kind: "background", State: "stopped"}
				return claude.PublishedListing{
					Entries:     []claude.AgentEntry{entry},
					BySessionID: map[string]claude.AgentEntry{sessionA: entry},
					ReadAt:      observedAt,
				}, nil
			}
			return claude.PublishedListing{ReadAt: observedAt}, nil
		},
	}
	var milestones []MilestoneKind
	manager := testManager(t, store, provider, func(cfg *Config) {
		cfg.Observer = func(_ context.Context, m Milestone) { milestones = append(milestones, m.Kind) }
	})

	outcome, err := manager.Recover(context.Background(), sessionA)
	var retry *RetryError
	if !errors.As(err, &retry) {
		t.Fatalf("Recover error = %v, want a RetryError with a scheduled retry", err)
	}
	if !errors.Is(err, ErrHandoffUnobserved) {
		t.Errorf("Recover error = %v, want ErrHandoffUnobserved", err)
	}
	if outcome.State != StateRetryWait {
		t.Errorf("state = %s, want RETRY_WAIT", outcome.State)
	}
	if outcome.Delay <= 0 {
		t.Error("Delay = 0, want a schedule: the next pass re-observes and hands off again")
	}

	// The retry is on disk as an instant, so `ccresu sessions` can report when the
	// session will be re-observed instead of leaving the operator to guess.
	stored, err := store.GetSession(context.Background(), sessionA)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if stored.RetryAt == nil || stored.RetryAt.IsZero() {
		t.Error("no retry instant was recorded: the session would never be re-observed")
	}

	// Never a verdict about the work: no permanent-failure milestone.
	for _, kind := range milestones {
		if kind == MilestonePermanent {
			t.Fatalf("milestones = %v, want no permanent failure: an idle stop is expected", milestones)
		}
	}

	// And the attempt itself is unresolved rather than failed: the handoff was
	// accepted, and what became of it is unknown.
	attempts, err := store.ListAttempts(context.Background(), sessionA, 0)
	if err != nil {
		t.Fatalf("ListAttempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
	if attempts[0].Success != nil {
		t.Errorf("attempt success = %v, want nil: the outcome is unknown, not failed", *attempts[0].Success)
	}
	if attempts[0].FinishedAt.IsZero() {
		t.Error("the attempt was left open, so the next pass would close it as interrupted")
	}
}
