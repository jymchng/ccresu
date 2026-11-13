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
			fmt.Errorf("%s %s: %w", call.Name, strings.Join(call.Args, " "), errors.New("exit status 1"))
	default:
		return claude.Output{ExitCode: 1},
			fmt.Errorf("%s %s: %w", call.Name, strings.Join(call.Args, " "), errors.New("exit status 1"))
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
		"state:        WAITING_FOR_RESET",
		"Latest usage limit",
		"10am (Europe/Berlin)",
		"waiting for the usage limit to reset",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("inspect output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "already resumed") {
		t.Errorf("inspect still claims the session resumed:\n%s", out)
	}
}

// TestTheRefusedResumeIsCurrentlyMisreported records today's behaviour, exactly
// as the operator reported it, so the defect is reproduced rather than
// described. It is the ledger the fix must invert: once a refused resume parks
// the session, this test is rewritten to assert WAITING_FOR_RESET.
func TestTheRefusedResumeIsCurrentlyMisreported(t *testing.T) {
	f := newUsageLimitFixture(t, true)
	f.steps(t, 2)

	got := f.describe(t)
	session := f.session(t)

	// 1. The refusal is recorded as a missing binary.
	if !strings.Contains(session.LastError, "claude: claude executable unavailable") {
		t.Fatalf("last error = %q, want today's misreport\n%s", session.LastError, got)
	}
	// 2. The failed attempt's own transcript write is read as progress, so the
	//    session is declared resumed while the limit is still in force.
	if session.State != storage.StateResumed {
		t.Fatalf("state = %s, want today's %s\n%s", session.State, storage.StateResumed, got)
	}
	// 3. The operator is told there is nothing to wait for.
	out := f.inspect(t)
	for _, want := range []string{"why:           already resumed", "at:            none"} {
		if !strings.Contains(out, want) {
			t.Errorf("inspect output missing %q:\n%s", want, out)
		}
	}
	// 4. No usage limit covers the current window, because the refusal never
	//    reached the detector.
	if _, err := f.h.store.LatestUsageLimit(context.Background(), f.id); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("latest usage limit err = %v, want %v (nothing recorded today)", err, storage.ErrNotFound)
	}
}
