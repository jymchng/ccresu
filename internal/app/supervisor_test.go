package app

// PRD 3 / R10 at the operator surface: the supervisor holds the session's live
// state and its recent output, so `ccresu inspect` reads them with the documented
// commands — `claude daemon status` and `claude logs <state-id>` — instead of a
// second copy in ccresu's own tables or a tail of a file the supervisor owns.
//
// "On demand" is the other half of the requirement, and it is the half a test can
// falsify: nothing may read the supervisor on a path that is not an explicit
// inspection, because polling it would make ccresu a second, slower client of the
// same socket.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/storage"
)

const supervisorSessionID = "55555555-5555-4555-8555-555555555555"

// supervisorProvider is the harness provider plus the supervisor's read-only
// surface, so one fake answers both what the session is and what the supervisor
// says about it.
type supervisorProvider struct {
	*claude.FakeProvider
	status    claude.SupervisorStatus
	statusErr error
	logs      claude.LogsResult
	logsErr   error
	daemonLog claude.LogsResult
	daemonErr error

	statusCalls    int
	logCalls       int
	daemonLogCalls int
	logTargets     []string
}

func (p *supervisorProvider) DaemonStatus(context.Context) (claude.SupervisorStatus, error) {
	p.statusCalls++
	return p.status, p.statusErr
}

func (p *supervisorProvider) SessionLogs(_ context.Context, stateID string) (claude.LogsResult, error) {
	p.logCalls++
	p.logTargets = append(p.logTargets, stateID)
	return p.logs, p.logsErr
}

func (p *supervisorProvider) DaemonLogs(context.Context) (claude.LogsResult, error) {
	p.daemonLogCalls++
	return p.daemonLog, p.daemonErr
}

// seedHandedOffAttempt records the attempt a handoff left behind, including the
// state id the acceptance block printed — the identifier `claude logs` takes.
func seedHandedOffAttempt(t *testing.T, h *harness) {
	t.Helper()
	attempt, err := h.store.StartAttempt(context.Background(), supervisorSessionID, 0,
		"claude --bg --resume "+supervisorSessionID)
	if err != nil {
		t.Fatalf("StartAttempt: %v", err)
	}
	if _, err := h.store.FinishAttempt(context.Background(), attempt.ID, storage.AttemptResult{
		State:          storage.StateResumed,
		Success:        storage.BoolPtr(true),
		Output:         "backgrounded",
		HandoffStateID: "ab12cd34",
		HandoffName:    "ccresu-55555555",
		HandoffAt:      time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC),
		FinishedAt:     time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("FinishAttempt: %v", err)
	}
}

func TestInspectReadsTheSupervisorOnDemand(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, supervisorSessionID, nil)
	seedHandedOffAttempt(t, h)

	version, err := claude.ParseVersion("2.1.300")
	if err != nil {
		t.Fatalf("ParseVersion: %v", err)
	}
	provider := &supervisorProvider{
		FakeProvider: h.provider,
		status: claude.SupervisorStatus{
			Running:      true,
			State:        "running",
			Version:      version,
			Workers:      3,
			WorkersKnown: true,
			Argv:         claude.DaemonStatusArgs(),
		},
		logs: claude.LogsResult{Argv: claude.SessionLogsArgs("ab12cd34"), Text: "line one\nline two"},
		daemonLog: claude.LogsResult{
			Argv: claude.DaemonLogsArgs(),
			Text: "supervisor started\nrespawned worker 1",
		},
	}
	h.rt.Provider = provider

	code, out, stderr := h.exec(t, "inspect", supervisorSessionID)
	if code != ExitOK {
		t.Fatalf("inspect exit = %d (stderr %q)", code, stderr)
	}

	for _, want := range []string{
		"Supervisor",
		"running",
		"2.1.300",
		"claude daemon status",
		"claude daemon logs",
		"supervisor started",
		"respawned worker 1",
		"claude logs ab12cd34",
		"line one",
		"line two",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("inspect output is missing %q:\n%s", want, out)
		}
	}
	if got := provider.logTargets; len(got) != 1 || got[0] != "ab12cd34" {
		t.Errorf("`claude logs` targets = %v, want the handoff's state id ab12cd34", got)
	}
	if provider.daemonLogCalls != 1 {
		t.Errorf("`claude daemon logs` reads = %d, want 1: the supervisor's own log is evidence (R10)",
			provider.daemonLogCalls)
	}
}

// TestNothingButInspectReadsTheSupervisor is the "not polled" half of R10: a
// listing, a report or a history read must not touch the supervisor's socket.
func TestNothingButInspectReadsTheSupervisor(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, supervisorSessionID, nil)
	seedHandedOffAttempt(t, h)

	provider := &supervisorProvider{
		FakeProvider: h.provider,
		status:       claude.SupervisorStatus{Running: true, State: "running"},
	}
	h.rt.Provider = provider

	for _, args := range [][]string{
		{"sessions"},
		{"sessions", "--json"},
		{"history", supervisorSessionID},
		{"status"},
	} {
		code, _, stderr := h.exec(t, args...)
		if code != ExitOK {
			t.Fatalf("%v exit = %d (stderr %q)", args, code, stderr)
		}
	}
	if provider.statusCalls != 0 || provider.logCalls != 0 || provider.daemonLogCalls != 0 {
		t.Errorf("supervisor reads = %d status, %d logs, %d daemon logs, want 0: it is read on demand, never polled",
			provider.statusCalls, provider.logCalls, provider.daemonLogCalls)
	}
}

// TestInspectSaysWhenThereIsNoSupervisor keeps the documented exit-1 answer from
// reading as a failure: a machine that has never run `claude --bg` has no
// supervisor, and the next handoff starts one.
func TestInspectSaysWhenThereIsNoSupervisor(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, supervisorSessionID, nil)
	provider := &supervisorProvider{
		FakeProvider: h.provider,
		// Exit 1: `claude daemon status` answered, and the answer is "not running".
		status: claude.SupervisorStatus{ExitCode: 1, Argv: claude.DaemonStatusArgs()},
	}
	h.rt.Provider = provider

	code, out, stderr := h.exec(t, "inspect", supervisorSessionID)
	if code != ExitOK {
		t.Fatalf("inspect exit = %d, want %d: no supervisor is not a failure (stderr %q)", code, ExitOK, stderr)
	}
	if !strings.Contains(out, "not running") {
		t.Errorf("inspect output does not say the supervisor is not running:\n%s", out)
	}
	if provider.logCalls != 0 {
		t.Error("`claude logs` was read for a session the supervisor does not host")
	}
	if provider.daemonLogCalls != 0 {
		t.Error("`claude daemon logs` was read with no supervisor running: there is no log to read")
	}
}

// TestInspectReportsAFailedSupervisorLog keeps a failed daemon-log read a named
// fact rather than an empty log: the nonexistent and the unreadable are different
// answers, and only one of them means "the supervisor has nothing to say".
func TestInspectReportsAFailedSupervisorLog(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, supervisorSessionID, nil)
	provider := &supervisorProvider{
		FakeProvider: h.provider,
		status:       claude.SupervisorStatus{Running: true, State: "running"},
		daemonErr:    errors.New("claude daemon logs: exit 2: permission denied"),
	}
	h.rt.Provider = provider

	code, out, stderr := h.exec(t, "inspect", supervisorSessionID)
	if code != ExitOK {
		t.Fatalf("inspect exit = %d, want %d: an unreadable log is not a failed inspection (stderr %q)",
			code, ExitOK, stderr)
	}
	if !strings.Contains(out, "permission denied") {
		t.Errorf("inspect does not report why the supervisor's log could not be read:\n%s", out)
	}
}
