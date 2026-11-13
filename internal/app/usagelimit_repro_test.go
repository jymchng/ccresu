package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/recovery"
	"github.com/jymchng/ccresu/internal/scheduler"
	"github.com/jymchng/ccresu/internal/storage"
)

// usageLimitBanner is the refusal `claude -p --resume` prints when the five-hour
// window has not reset yet. The wording is the canonical form the detector
// already understands ("Claude usage limit reached. Your limit will reset at
// <time>") with the reset phrase the live database recorded for session
// 1347e5db-a9fb-42eb-b92a-82256bd5aa68 ("10am (Europe/Berlin)").
const usageLimitBanner = "Claude usage limit reached. Your limit will reset at 10am (Europe/Berlin)."

// reproSessionID is the session the defect was reported against. Its persisted
// history is the fixture: 60 refused resumes, one every 30-60s, each recorded as
// "claude: claude executable unavailable", and a final transition into RESUMED
// after the last refusal.
const reproSessionID = "1347e5db-a9fb-42eb-b92a-82256bd5aa68"

// refusalScript answers the CLI adapter's invocations the way the real binary
// did: every `-p --resume` writes the usage-limit banner to stdout and exits 1.
//
// grow models the second half of the defect: the refused attempt itself appends
// to the transcript, so the next status check sees more lines than the stored
// baseline even though nothing was resumed.
type refusalScript struct {
	grow bool

	mu      sync.Mutex
	resumes int
}

// handle implements the execer contract, including OSExecer's error wording, so
// the error text under test is the one production builds.
//
// `--version` answers like a healthy installation because the CLI adapter probes
// it before every resume; only the resume itself is refused.
func (s *refusalScript) handle(call claude.Call) (claude.Output, error) {
	switch {
	case isVersionCall(call.Args):
		return claude.Output{Stdout: []byte("2.1.223 (Claude Code)")}, nil
	case isResumeCall(call.Args):
		s.mu.Lock()
		s.resumes++
		s.mu.Unlock()
		return claude.Output{Stdout: []byte(usageLimitBanner), ExitCode: 1},
			fmt.Errorf("%s: %w", call.Name, errors.New("exit status 1"))
	default:
		return claude.Output{ExitCode: 1},
			fmt.Errorf("%s: %w", call.Name, errors.New("exit status 1"))
	}
}

// isVersionCall reports whether args is the version probe.
func isVersionCall(args []string) bool {
	return len(args) == 1 && args[0] == "--version"
}

// resumeCount reports how many resume invocations were issued.
func (s *refusalScript) resumeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resumes
}

// isResumeCall reports whether args is the verified resume vector.
func isResumeCall(args []string) bool {
	for _, arg := range args {
		if arg == "--resume" {
			return true
		}
	}
	return false
}

// refusingProvider serves discovery and status from the in-memory fake and
// routes resumes through the real CLI adapter, so a refusal travels the
// production path (`CLI.Resume` -> `describeRunError` -> `recovery.Classify`)
// instead of being simulated in the test.
type refusingProvider struct {
	*claude.FakeProvider
	cli    *claude.CLI
	script *refusalScript
	now    func() time.Time
	path   string
	lines  int64
	size   int64
}

// ResumeSession routes the resume through the real CLI adapter, so a refusal
// travels the production path instead of being simulated here.
func (p *refusingProvider) ResumeSession(ctx context.Context, sessionID, message string) (claude.ResumeResult, error) {
	return p.cli.Resume(ctx, sessionID, message)
}

// GetSessionStatus reports the transcript as the failed attempts left it: the
// line count grows with every refusal when the script says so.
func (p *refusingProvider) GetSessionStatus(ctx context.Context, sessionID string) (claude.SessionStatus, error) {
	status, err := p.FakeProvider.GetSessionStatus(ctx, sessionID)
	if err != nil {
		return status, err
	}
	lines, size := p.lines, p.size
	if p.script.grow {
		lines += int64(p.script.resumeCount())
		size += int64(p.script.resumeCount()) * 512
	}
	status.Transcript = claude.TranscriptInfo{Path: p.path, SessionID: sessionID, Lines: lines, Size: size}
	status.CheckedAt = p.now()
	return status, nil
}

// usageLimitFixture is the reproduction: one paused session, a schedule whose
// reset time has already passed, and a CLI that keeps refusing the resume.
type usageLimitFixture struct {
	h      *harness
	script *refusalScript
	id     string
	path   string
}

// newUsageLimitFixture seeds the reported situation.
//
// The transcript deliberately carries no banner: the evidence is that the
// refusal only ever existed on the CLI's stdout, never in the file the daemon
// scans, which is why re-detection did not happen between attempts.
func newUsageLimitFixture(t *testing.T, grow bool) *usageLimitFixture {
	t.Helper()
	h := newHarness(t)
	script := &refusalScript{grow: grow}
	execer := claude.NewFakeExecer(script.handle)
	cli := claude.NewCLI(claude.CLIOptions{Executable: "claude", Execer: execer})

	path := "/home/ai-slave/.claude/projects/-root-python-projects-agenthicc/" + reproSessionID + ".jsonl"
	provider := &refusingProvider{
		FakeProvider: h.provider,
		cli:          cli,
		script:       script,
		now:          h.clock.Now,
		path:         path,
		lines:        8470,
		size:         27472239,
	}
	manager, err := recovery.New(recovery.Config{
		Store:    h.store,
		Provider: provider,
		Clock:    h.clock,
		Enabled:  true,
		// A fake clock never advances, so verification must be "check once".
		MaxAttempts:         3,
		VerificationTimeout: -1,
		HealthCheckInterval: -1,
		// The production seam: the refusal is planned by the detector and the
		// scheduler, exactly as the daemon does it.
		LimitPark: limitParker(h.store, h.rt.Scheduler, h.rt.Detector, h.clock, nil),
	})
	if err != nil {
		t.Fatalf("recovery manager: %v", err)
	}
	h.rt.Recovery = manager

	h.seedSession(t, reproSessionID, func(s *storage.Session) {
		s.ProjectPath = "/root/python_projects/agenthicc"
		s.TranscriptPath = path
		s.Type = storage.SessionTypeHeadless
		s.State = storage.StateWaitingForReset
		s.TranscriptLines = 8470
		s.TranscriptSize = 27472239
	})
	// The session is not running, and the newest transcript lines say nothing
	// about a limit: exactly the state the daemon faced between attempts.
	h.transcripts.entries[path] = []claude.Entry{
		{Type: "assistant", Text: "Working on the existing active goal."},
	}
	status := claude.SessionStatus{
		Session: claude.Session{
			ID:             reproSessionID,
			ProjectPath:    "/root/python_projects/agenthicc",
			TranscriptPath: path,
			Type:           claude.SessionTypeBackground,
			Recoverable:    true,
		},
		Running: grow,
	}
	h.provider.Statuses[reproSessionID] = status

	// A reset instant that has already passed, and that Advance cannot move:
	// for a known-reset schedule Advance is a no-op, so every reconcile pass
	// finds the wait due again and issues another resume.
	sched := scheduler.Schedule{
		SessionID:     reproSessionID,
		ResetAt:       harnessBaseTime.Add(-time.Hour),
		ResetTimezone: "Europe/Berlin",
		ResetRaw:      "9am (Europe/Berlin)",
	}
	if _, err := h.rt.Scheduler.Save(context.Background(), sched); err != nil {
		t.Fatalf("save schedule: %v", err)
	}

	return &usageLimitFixture{h: h, script: script, id: reproSessionID, path: path}
}

// steps runs n reconcile passes on the fixed clock.
func (f *usageLimitFixture) steps(t *testing.T, n int) {
	t.Helper()
	daemon := NewDaemon(f.h.rt, time.Minute)
	for i := 0; i < n; i++ {
		if err := daemon.Step(context.Background()); err != nil {
			t.Fatalf("step %d: %v", i+1, err)
		}
	}
}

// session reads the persisted session back.
func (f *usageLimitFixture) session(t *testing.T) storage.Session {
	t.Helper()
	session, err := f.h.store.GetSession(context.Background(), f.id)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	return session
}

// attempts reports how many attempt rows the fixture produced.
func (f *usageLimitFixture) attempts(t *testing.T) []storage.RecoveryAttempt {
	t.Helper()
	attempts, err := f.h.store.ListAttempts(context.Background(), f.id, 0)
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	return attempts
}

// inspect renders `ccresu sessions status <id>`, the page an operator sees.
func (f *usageLimitFixture) inspect(t *testing.T) string {
	t.Helper()
	code, stdout, stderr := f.h.exec(t, "sessions", "status", f.id)
	if code != ExitOK {
		t.Fatalf("sessions status exit = %d (stderr %q)", code, stderr)
	}
	return stdout
}

// describe renders everything the defect report quoted, so a failing assertion
// shows the whole wrong picture rather than one field.
func (f *usageLimitFixture) describe(t *testing.T) string {
	t.Helper()
	session := f.session(t)
	return fmt.Sprintf("state=%s attempts=%d resumes=%d last_error=%q\n%s",
		session.State, len(f.attempts(t)), f.script.resumeCount(), session.LastError, f.inspect(t))
}

// TestARefusedResumeParksTheSessionUntilTheNextReset is the target behaviour:
// a resume refused by a usage limit is a wait, not a failure to retry and not a
// success to celebrate.
func TestARefusedResumeParksTheSessionUntilTheNextReset(t *testing.T) {
	f := newUsageLimitFixture(t, false)
	f.steps(t, 5)

	got := f.describe(t)
	session := f.session(t)

	if session.State != storage.StateWaitingForReset {
		t.Errorf("state = %s, want %s\n%s", session.State, storage.StateWaitingForReset, got)
	}
	if resumes := f.script.resumeCount(); resumes != 1 {
		t.Errorf("resume invocations = %d, want exactly 1 for one reset window\n%s", resumes, got)
	}
	if !strings.Contains(session.LastError, "usage limit") {
		t.Errorf("last error = %q, want the CLI's usage-limit refusal\n%s", session.LastError, got)
	}
	if strings.Contains(session.LastError, "claude executable unavailable") {
		t.Errorf("last error still claims the executable was unavailable: %q", session.LastError)
	}

	limit, err := f.h.store.LatestUsageLimit(context.Background(), f.id)
	if err != nil {
		t.Fatalf("latest usage limit: %v (the refusal must be recorded as a limit)\n%s", err, got)
	}
	if limit.ResetRaw != "10am (Europe/Berlin)" {
		t.Errorf("recorded reset phrase = %q, want %q", limit.ResetRaw, "10am (Europe/Berlin)")
	}

	out := f.inspect(t)
	for _, want := range []string{
		// The padding is `field`'s: "state:" is padded to 14 columns, then one
		// space, so the value starts in column 16.
		"state:         WAITING_FOR_RESET",
		"Latest usage limit",
		"10am (Europe/Berlin)",
		"waiting for the usage limit to reset",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("inspect output missing %q:\n%s", want, out)
		}
	}
	// The refused attempt is finished by the pass that saw the refusal, with the
	// banner as its error: an open row would be rewritten by the next Recover as
	// "interrupted by a process restart", which is not what happened.
	attempts := f.attempts(t)
	if len(attempts) == 0 {
		t.Fatalf("no attempt rows:\n%s", out)
	}
	if attempts[0].FinishedAt.IsZero() {
		t.Errorf("the refused attempt was left open:\n%s", out)
	}
	if attempts[0].Success == nil || *attempts[0].Success {
		t.Errorf("attempt success = %v, want a recorded failure:\n%s", attempts[0].Success, out)
	}
	if !strings.Contains(attempts[0].Error, "usage limit reached") {
		t.Errorf("attempt error = %q, want the banner:\n%s", attempts[0].Error, out)
	}
	if strings.Contains(out, "interrupted by a process restart") {
		t.Errorf("the refusal was rewritten as a restart:\n%s", out)
	}
	if strings.Contains(out, "already resumed") {
		t.Errorf("inspect still claims the session resumed:\n%s", out)
	}
}

// TestARefusedResumeIsRecordedAsAWaitAtTheManager pins the same defect one layer
// down, at the manager, so the fix is not mistaken for a daemon-loop patch: a
// single Recover call on a rediscovered session used to turn the CLI's
// usage-limit refusal into "claude executable unavailable", which the retry
// policy then classified as permanent.
//
// The refusal is now a wait: the error names the condition, the banner is what
// the session records, and the retry policy may repeat the attempt.
func TestARefusedResumeIsRecordedAsAWaitAtTheManager(t *testing.T) {
	f := newUsageLimitFixture(t, false)
	ctx := context.Background()

	// DISCOVERED is where a refused attempt leaves a session today, and where the
	// daemon rediscovers it; Recover accepts it directly.
	if _, err := f.h.store.TransitionSession(ctx, f.id, storage.StateDiscovered,
		"returned to discovery for the reproduction", 0); err != nil {
		t.Fatalf("move to DISCOVERED: %v", err)
	}

	outcome, err := f.h.rt.Recovery.Recover(ctx, f.id)
	if err == nil {
		t.Fatalf("Recover err = nil, want the refusal; outcome = %+v", outcome)
	}
	got := f.describe(t)

	if resumes := f.script.resumeCount(); resumes != 1 {
		t.Fatalf("resume invocations = %d, want 1 for one Recover call\n%s", resumes, got)
	}
	session := f.session(t)
	if !errors.Is(err, claude.ErrUsageLimitInForce) {
		t.Errorf("error = %v, want claude.ErrUsageLimitInForce\n%s", err, got)
	}
	if errors.Is(err, claude.ErrCLIUnavailable) {
		t.Errorf("error = %v, must not claim the executable was unavailable\n%s", err, got)
	}
	if !strings.Contains(session.LastError, "usage limit reached") {
		t.Errorf("last error = %q, want the refusal banner\n%s", session.LastError, got)
	}
	if strings.Contains(session.LastError, "executable unavailable") {
		t.Errorf("last error still claims a missing binary: %q\n%s", session.LastError, got)
	}
	// A refusal is its own class: not permanent (the conversation is fine), not
	// retryable (repeating it now is the hot loop), but a wait until the reset.
	if class := recovery.Classify(err); class != recovery.ClassLimitInForce {
		t.Errorf("class = %s, want %s for a refusal\n%s", class, recovery.ClassLimitInForce, got)
	}
	if recovery.Retryable(err) {
		t.Errorf("Retryable = true, want false: a refusal waits for the reset, it is not retried\n%s", got)
	}
	if class := recovery.Classify(err); class == recovery.ClassPermanent {
		t.Errorf("class = %s, a refusal must never be permanent\n%s", class, got)
	}
	// A refused resume is a wait, not a rediscovery: the manager must not hand
	// the session back to the discovery loop while the limit is in force.
	if session.State == storage.StateDiscovered {
		t.Errorf("state = %s, want a waiting state (WAITING_FOR_RESET)\n%s", session.State, got)
	}
}

// TestARefusedAttemptsOwnWritesAreNotProofOfResumption drives the second half of
// the misreport through the daemon. A refused `claude -p --resume` still appends
// its refusal to the transcript, so the file has grown by the time the reset
// arrives. That growth is the attempt's own: the pass after the reset must ask
// again, not declare the session RESUMED / "already resumed" and stop.
func TestARefusedAttemptsOwnWritesAreNotProofOfResumption(t *testing.T) {
	// grow makes every refused attempt append a line to the transcript, which is
	// what the real CLI does when it prints its banner into the conversation.
	f := newUsageLimitFixture(t, true)
	f.steps(t, 5)

	before := f.session(t)
	if before.State != storage.StateWaitingForReset {
		t.Fatalf("state = %s, want %s before the reset\n%s",
			before.State, storage.StateWaitingForReset, f.describe(t))
	}
	if lines := before.TranscriptLines; lines != 8471 {
		t.Errorf("baseline lines = %d, want 8471: the refused attempt's own write must be absorbed", lines)
	}

	// The reset instant the banner named arrives.
	f.h.clock.Advance(22 * time.Hour)
	f.steps(t, 1)

	got := f.describe(t)
	session := f.session(t)
	if session.State == storage.StateResumed {
		t.Errorf("state = RESUMED, want the wait: the only growth is the refused attempt's own output\n%s", got)
	}
	if session.State != storage.StateWaitingForReset {
		t.Errorf("state = %s, want %s\n%s", session.State, storage.StateWaitingForReset, got)
	}
	if out := f.inspect(t); strings.Contains(out, "already resumed") {
		t.Errorf("inspect claims the session resumed on its own:\n%s", out)
	}
	if resumes := f.script.resumeCount(); resumes != 2 {
		t.Errorf("resume invocations = %d, want 2: the pass after the reset must ask again\n%s", resumes, got)
	}
	if attempts := len(f.attempts(t)); attempts != 2 {
		t.Errorf("attempts = %d, want 2\n%s", attempts, got)
	}
}
