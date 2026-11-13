package recovery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/storage"
)

// refusalBannerText is the wording `claude -p --resume` prints when the usage
// window has not reset yet, taken verbatim from the defect report.
const refusalBannerText = "Claude usage limit reached. Your limit will reset at 10am (Europe/Berlin)."

// refusedResume is the failure the CLI adapter returns for that banner: the
// process ran, exited 1, and said why.
func refusedResume() error {
	return &claude.CLIError{
		What:     "claude -p --resume",
		ExitCode: 1,
		Stdout:   []byte(refusalBannerText),
		Err:      errors.New("exit status 1"),
		Kind:     claude.ErrUsageLimitInForce,
	}
}

// TestARefusedResumeIsParkedUntilTheReset is the manager's half of the contract:
// a refusal is a wait. The session is parked in WAITING_FOR_RESET, the banner is
// what it records, the wait the parker planned is what the caller is told, and
// nothing about the failure is retryable.
func TestARefusedResumeIsParkedUntilTheReset(t *testing.T) {
	store := newTestStore(t)
	provider := &providerStub{
		resumeFn: func(string, string) (claude.ResumeResult, error) {
			return claude.ResumeResult{}, refusedResume()
		},
	}
	reset := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
	var parked struct {
		sessionID string
		banner    string
		calls     int
	}
	manager := testManager(t, store, provider, func(cfg *Config) {
		cfg.LimitPark = func(_ context.Context, sessionID, banner string) (Wait, error) {
			parked.sessionID, parked.banner = sessionID, banner
			parked.calls++
			return Wait{At: reset, Known: true, Delay: 21 * time.Hour, LimitID: 7}, nil
		}
	})
	session := seedSession(t, store, recoverableSession("1347e5db-a9fb-42eb-b92a-82256bd5aa68"))

	outcome, err := manager.Recover(context.Background(), session.ID)
	if err == nil {
		t.Fatalf("Recover err = nil, want the refusal; outcome = %+v", outcome)
	}

	// The refusal stays recognisable through the new type: the class is decided
	// from the adapter's sentinel, and the wait is reported as the same fact.
	if !errors.Is(err, claude.ErrUsageLimitInForce) {
		t.Errorf("error = %v, want claude.ErrUsageLimitInForce", err)
	}
	if !errors.Is(err, ErrLimitNotCleared) {
		t.Errorf("error = %v, want ErrLimitNotCleared so a caller can tell it is a wait", err)
	}
	if class := Classify(err); class != ClassLimitInForce {
		t.Errorf("class = %s, want %s", class, ClassLimitInForce)
	}
	if Retryable(err) {
		t.Errorf("Retryable = true, want false: the reset ends this wait, not a backoff")
	}
	var retry *RetryError
	if errors.As(err, &retry) {
		t.Errorf("error = %v, a refusal must never ask for a retry", err)
	}

	if parked.calls != 1 {
		t.Fatalf("parker calls = %d, want 1", parked.calls)
	}
	if parked.sessionID != session.ID {
		t.Errorf("parker session = %q, want %q", parked.sessionID, session.ID)
	}
	if parked.banner != refusalBannerText {
		t.Errorf("parker banner = %q, want the CLI's own stdout %q", parked.banner, refusalBannerText)
	}

	if outcome.State != StateWaitingForReset {
		t.Errorf("outcome state = %s, want %s", outcome.State, StateWaitingForReset)
	}
	if outcome.Resumed {
		t.Error("outcome.Resumed = true, a refused resume is not a success")
	}
	if outcome.Wait != 21*time.Hour {
		t.Errorf("outcome.Wait = %s, want 21h", outcome.Wait)
	}
	if !outcome.WaitUntil.Equal(reset) {
		t.Errorf("outcome.WaitUntil = %s, want %s", outcome.WaitUntil, reset)
	}

	stored, err := store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	if stored.State != storage.StateWaitingForReset {
		t.Errorf("stored state = %s, want %s", stored.State, storage.StateWaitingForReset)
	}
	if stored.LastError != refusalBannerText {
		t.Errorf("last error = %q, want the banner %q", stored.LastError, refusalBannerText)
	}

	// The attempt the CLI answered must be closed by the pass that saw the
	// refusal. An open row would be rewritten by the next Recover as "interrupted
	// by a process restart", which is not what happened.
	attempts, err := store.ListAttempts(context.Background(), session.ID, 0)
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
	if attempts[0].FinishedAt.IsZero() {
		t.Error("the refused attempt was left open; it must be finished by the refusal")
	}
	if attempts[0].Success == nil || *attempts[0].Success {
		t.Errorf("attempt success = %v, want a recorded failure", attempts[0].Success)
	}
	if attempts[0].State != StateWaitingForReset {
		t.Errorf("attempt state = %s, want %s", attempts[0].State, StateWaitingForReset)
	}
	if attempts[0].Error != refusalBannerText {
		t.Errorf("attempt error = %q, want the banner %q", attempts[0].Error, refusalBannerText)
	}
}

// TestAParkerThatCannotPlanStillParksTheSession pins the ordering: the wait is
// the decision, the schedule is the detail. A parker that fails must not send
// the session back to the retry loop, because the CLI would only refuse again.
func TestAParkerThatCannotPlanStillParksTheSession(t *testing.T) {
	store := newTestStore(t)
	provider := &providerStub{
		resumeFn: func(string, string) (claude.ResumeResult, error) {
			return claude.ResumeResult{}, refusedResume()
		},
	}
	manager := testManager(t, store, provider, func(cfg *Config) {
		cfg.LimitPark = func(context.Context, string, string) (Wait, error) {
			return Wait{}, errors.New("no schedule could be planned")
		}
	})
	session := seedSession(t, store, recoverableSession("1347e5db-a9fb-42eb-b92a-82256bd5aa68"))

	outcome, err := manager.Recover(context.Background(), session.ID)
	if err == nil {
		t.Fatalf("Recover err = nil, want the refusal")
	}
	if !errors.Is(err, claude.ErrUsageLimitInForce) {
		t.Errorf("error = %v, want the refusal to stay recognisable", err)
	}
	if outcome.State != StateWaitingForReset {
		t.Errorf("outcome state = %s, want %s", outcome.State, StateWaitingForReset)
	}
	if outcome.Wait != 0 || !outcome.WaitUntil.IsZero() {
		t.Errorf("outcome wait = %s until %s, want no wait planned", outcome.Wait, outcome.WaitUntil)
	}
	stored, err := store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	if stored.LastError != refusalBannerText {
		t.Errorf("last error = %q, want the banner %q", stored.LastError, refusalBannerText)
	}
}

// TestRefusalBannerPrefersTheCLIsOwnOutput covers the extraction the parker
// depends on: the banner is what the CLI printed, not the wrapper's rendering of
// it, because that is what the detector can parse.
func TestRefusalBannerPrefersTheCLIsOwnOutput(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "banner on stdout",
			err:  refusedResume(),
			want: refusalBannerText,
		},
		{
			name: "banner on stderr",
			err: &claude.CLIError{What: "claude -p --resume", ExitCode: 1,
				Stderr: []byte(refusalBannerText), Kind: claude.ErrUsageLimitInForce},
			want: refusalBannerText,
		},
		{
			name: "no captured output falls back to the error",
			err:  errors.New("recovery: usage limit is still in force"),
			want: "recovery: usage limit is still in force",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := refusalBanner(tc.err); got != tc.want {
				t.Errorf("refusalBanner = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestARefusedAttemptsOwnWritesDoNotProveAResume pins the baseline against the
// other half of the misreport. `claude -p --resume` appends its refusal to the
// transcript before exiting, so the file grows even though nothing was resumed.
// If that growth were measured against the pre-attempt snapshot, the next status
// check would take the "the session had already resumed after the limit reset"
// shortcut and report a refused resume as a success.
//
// The failed attempt's own lines are therefore absorbed into the stored
// baseline, and only growth beyond them can prove the session continued by
// itself.
func TestARefusedAttemptsOwnWritesDoNotProveAResume(t *testing.T) {
	store := newTestStore(t)
	provider := &providerStub{}
	// The transcript carries the base conversation plus one line for every
	// refused attempt, because the CLI appends its refusal before exiting. Once
	// a resume has been invoked the session also still looks running. That is
	// exactly the shape that used to be read as "it resumed on its own": growth
	// since the pre-attempt snapshot, with nothing else changed.
	provider.statusFn = func(_ int, sessionID string) (claude.SessionStatus, error) {
		resumes := provider.resumeCount()
		lines := int64(100 + resumes)
		status := statusFor(sessionID, lines, lines*100)
		status.Running = resumes > 0
		return status, nil
	}
	provider.resumeFn = func(string, string) (claude.ResumeResult, error) {
		return claude.ResumeResult{}, refusedResume()
	}
	manager := testManager(t, store, provider, func(cfg *Config) {
		cfg.LimitPark = func(context.Context, string, string) (Wait, error) {
			return Wait{}, nil
		}
	})
	session := seedSession(t, store, recoverableSession("1347e5db-a9fb-42eb-b92a-82256bd5aa68"))

	if _, err := manager.Recover(context.Background(), session.ID); err == nil {
		t.Fatalf("first Recover err = nil, want the refusal")
	}
	stored, err := store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	if stored.TranscriptLines != 101 {
		t.Errorf("baseline lines = %d, want 101: the refused attempt's own write must be absorbed",
			stored.TranscriptLines)
	}

	// The refusal closed the attempt itself, so the row the second pass finds is
	// already finished. Snapshotting it here is what makes the comparison below
	// mean something: a rewrite by closeStaleAttempt would change these fields.
	before, err := store.ListAttempts(context.Background(), session.ID, 0)
	if err != nil {
		t.Fatalf("list attempts after the first pass: %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("attempts after the first pass = %d, want 1", len(before))
	}
	first := before[0]

	// The reset arrives, exactly as the daemon's schedule would announce it.
	if _, err := store.TransitionSession(context.Background(), session.ID, StateResetTimeReached,
		"the scheduled reset time has arrived", 0); err != nil {
		t.Fatalf("move to RESET_TIME_REACHED: %v", err)
	}

	outcome, err := manager.Recover(context.Background(), session.ID)
	if err == nil {
		t.Fatalf("second Recover err = nil, want the second refusal; outcome = %+v", outcome)
	}
	if outcome.Resumed {
		t.Errorf("outcome.Resumed = true; the only growth was the refused attempt's own output")
	}
	stored, err = store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	if stored.State != StateWaitingForReset {
		t.Errorf("state = %s, want %s", stored.State, StateWaitingForReset)
	}

	transitions, err := store.ListTransitions(context.Background(), session.ID, 0)
	if err != nil {
		t.Fatalf("list transitions: %v", err)
	}
	for _, transition := range transitions {
		if strings.Contains(transition.Reason, "already resumed") {
			t.Errorf("a refused resume was recorded as an independent resumption: %s -> %s (%s)",
				transition.FromState, transition.ToState, transition.Reason)
		}
		// The attempt was answered, so nothing about it may be rewritten as a
		// restart, and a session waiting for a reset may not be sent back into the
		// retry loop behind the park's back.
		if strings.Contains(transition.Reason, "interrupted by a process restart") {
			t.Errorf("the refused attempt was rewritten as a restart: %s -> %s (%s)",
				transition.FromState, transition.ToState, transition.Reason)
		}
		if transition.ToState == StateRetryWait {
			t.Errorf("a RETRY_WAIT was logged over a parked session: %s -> %s (%s)",
				transition.FromState, transition.ToState, transition.Reason)
		}
	}

	// The second pass must leave the answered attempt exactly as the refusal
	// recorded it. This is the assertion the park's FinishAttempt exists for: with
	// the row left open, closeStaleAttempt closes it here as "interrupted by a
	// process restart" and the error stops being the CLI's banner.
	after, err := store.ListAttempts(context.Background(), session.ID, 0)
	if err != nil {
		t.Fatalf("list attempts after the second pass: %v", err)
	}
	var kept *storage.RecoveryAttempt
	for i := range after {
		if after[i].ID == first.ID {
			kept = &after[i]
		}
	}
	if kept == nil {
		t.Fatalf("attempt %d disappeared from the history", first.ID)
	}
	if !kept.FinishedAt.Equal(first.FinishedAt) {
		t.Errorf("finished at = %s, want the original %s: the refused attempt was rewritten",
			kept.FinishedAt, first.FinishedAt)
	}
	if kept.Error != first.Error {
		t.Errorf("attempt error = %q, want the original banner %q", kept.Error, first.Error)
	}
	if kept.State != first.State {
		t.Errorf("attempt state = %s, want the original %s", kept.State, first.State)
	}

	provider.mu.Lock()
	resumes := len(provider.resumes)
	provider.mu.Unlock()
	if resumes != 2 {
		t.Errorf("resumes = %d, want 2: the pass after the reset must ask again", resumes)
	}
}
