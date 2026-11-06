package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/detector"
	"github.com/jymchng/ccresu/internal/recovery"
	"github.com/jymchng/ccresu/internal/storage"
)

// canonicalSessionID is a realistic Claude Code session UUID.
const canonicalSessionID = "11111111-1111-4111-8111-111111111111"

func TestHelpListsEveryCommand(t *testing.T) {
	h := newHarness(t)
	code, stdout, stderr := h.exec(t, "--help")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, ExitOK, stderr)
	}
	for _, name := range []string{
		"daemon", "status", "sessions", "inspect", "recover", "retry", "enable",
		"reset", "disable", "history", "config", "doctor", "serve", "version",
	} {
		if !strings.Contains(stdout, name) {
			t.Errorf("help does not mention %q", name)
		}
	}
	if !strings.Contains(stdout, "Exit codes:") {
		t.Error("help does not document the exit codes")
	}
}

func TestHelpWithoutArgumentsIsAUsageError(t *testing.T) {
	h := newHarness(t)
	code, stdout, stderr := h.exec(t)
	if code != ExitUsage {
		t.Fatalf("exit = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stdout, "Commands:") {
		t.Error("no help was printed")
	}
	if !strings.Contains(stderr, "no command given") {
		t.Errorf("stderr = %q, want the missing-command message", stderr)
	}
	if !strings.Contains(stderr, "ccresu --help") {
		t.Errorf("stderr = %q, want the usage hint", stderr)
	}
}

func TestUnknownCommandIsAUsageError(t *testing.T) {
	h := newHarness(t)
	code, _, stderr := h.exec(t, "frobnicate")
	if code != ExitUsage {
		t.Fatalf("exit = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, `unknown command "frobnicate"`) {
		t.Errorf("stderr = %q, want the unknown-command message", stderr)
	}
	if !errors.Is(ErrUnknownCommand, ErrUnknownCommand) {
		t.Fatal("sanity")
	}
}

func TestUnknownGlobalFlagIsAUsageError(t *testing.T) {
	h := newHarness(t)
	code, _, stderr := h.exec(t, "--nope", "status")
	if code != ExitUsage {
		t.Fatalf("exit = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, `unknown flag "--nope"`) {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestGlobalFlagNeedingAValue(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{{"--config"}, {"--log-level"}, {"--log-format"}} {
		code, _, stderr := h.exec(t, args...)
		if code != ExitUsage {
			t.Errorf("%v exit = %d, want %d", args, code, ExitUsage)
		}
		if !strings.Contains(stderr, "needs a") {
			t.Errorf("%v stderr = %q, want a missing-value message", args, stderr)
		}
	}
}

func TestVersionCommand(t *testing.T) {
	h := newHarness(t)
	code, stdout, _ := h.exec(t, "version")
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	for _, want := range []string{"1.2.3", "deadbeef", "2026-01-05", "go"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("version output %q missing %q", stdout, want)
		}
	}

	code, stdout, _ = h.exec(t, "--version")
	if code != ExitOK || !strings.Contains(stdout, "1.2.3") {
		t.Errorf("--version = %d %q", code, stdout)
	}

	code, stdout, _ = h.exec(t, "--json", "version")
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	var report map[string]string
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("version --json is not JSON: %v (%q)", err, stdout)
	}
	if report["version"] != "1.2.3" || report["commit"] != "deadbeef" {
		t.Errorf("report = %+v", report)
	}
}

func TestVersionRejectsArguments(t *testing.T) {
	h := newHarness(t)
	code, _, stderr := h.exec(t, "version", "extra")
	if code != ExitUsage {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, ExitUsage, stderr)
	}
}

func TestStatusReportsPersistedState(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)
	h.seedSession(t, "22222222-2222-4222-8222-222222222222", func(s *storage.Session) {
		disabled := false
		s.Enabled = disabled
		s.State = storage.StateDisabled
	})

	code, stdout, stderr := h.exec(t, "status")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, stderr)
	}
	for _, want := range []string{
		"sessions:", "2 total, 1 enabled", "ccresu sessions --all shows all 2", "telegram:", "recovery:", "RUNNING", "DISABLED",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status output missing %q:\n%s", want, stdout)
		}
	}

	code, stdout, _ = h.exec(t, "--json", "status")
	if code != ExitOK {
		t.Fatalf("json exit = %d", code)
	}
	var report statusReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("status --json: %v (%q)", err, stdout)
	}
	if report.Sessions != 2 || report.EnabledSessions != 1 {
		t.Errorf("report = %+v", report)
	}
	if report.States["RUNNING"] != 1 || report.States["DISABLED"] != 1 {
		t.Errorf("state histogram = %+v", report.States)
	}
	if !report.RecoveryEnabled || report.MaxAttempts != 3 {
		t.Errorf("recovery fields = %+v", report)
	}
	if !report.TelegramEnabled {
		t.Error("the harness notifier should report enabled")
	}
}

func TestStatusRejectsArguments(t *testing.T) {
	h := newHarness(t)
	if code, _, _ := h.exec(t, "status", "now"); code != ExitUsage {
		t.Fatalf("exit = %d, want %d", code, ExitUsage)
	}
}

func TestStatusReportsAScheduleAndQueuedNotification(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)
	now := h.clock.Now().UTC()
	if _, err := h.store.SaveSchedule(context.Background(), storage.Schedule{
		SessionID: canonicalSessionID,
		ResetAt:   now.Add(90 * 60 * 1e9),
	}); err != nil {
		t.Fatalf("save schedule: %v", err)
	}
	if _, _, err := h.store.EnqueueNotification(context.Background(), storage.Notification{
		SessionID: canonicalSessionID,
		Event:     storage.EventLimitDetected,
		DedupeKey: "limit:s1",
		Body:      "hello",
		Status:    storage.NotificationPending,
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	code, stdout, _ := h.exec(t, "--json", "status")
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	var report statusReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("status --json: %v", err)
	}
	if report.Schedules != 1 {
		t.Errorf("schedules = %d, want 1", report.Schedules)
	}
	if report.NextScheduleAt == "" {
		t.Error("next_schedule_at is empty, want the persisted reset instant")
	}
	if report.PendingNotifications != 1 {
		t.Errorf("pending notifications = %d, want 1", report.PendingNotifications)
	}
}

func TestSessionsFiltersAndFormats(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)
	h.seedSession(t, "33333333-3333-4333-8333-333333333333", func(s *storage.Session) {
		s.Enabled = false
		s.State = storage.StateDisabled
		s.Reason = "the user disabled it"
	})

	code, stdout, stderr := h.exec(t, "sessions")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, stderr)
	}
	if !strings.Contains(stdout, canonicalSessionID[:8]) {
		t.Errorf("sessions output missing the enabled session:\n%s", stdout)
	}
	if strings.Contains(stdout, "33333333") {
		t.Errorf("sessions listed a disabled session without --all:\n%s", stdout)
	}

	code, stdout, _ = h.exec(t, "sessions", "--all")
	if code != ExitOK {
		t.Fatalf("--all exit = %d", code)
	}
	if !strings.Contains(stdout, "33333333") || !strings.Contains(stdout, "no") {
		t.Errorf("--all output =\n%s", stdout)
	}

	code, stdout, _ = h.exec(t, "--json", "sessions", "--all")
	if code != ExitOK {
		t.Fatalf("json exit = %d", code)
	}
	var sessions []storage.Session
	if err := json.Unmarshal([]byte(stdout), &sessions); err != nil {
		t.Fatalf("sessions --json: %v", err)
	}
	if len(sessions) != 2 {
		t.Errorf("sessions = %d, want 2", len(sessions))
	}

	if code, _, _ := h.exec(t, "sessions", "--state", "bogus"); code != ExitUsage {
		t.Errorf("bogus state exit = %d, want %d", code, ExitUsage)
	}
	if code, _, _ := h.exec(t, "sessions", "--limit", "-1"); code != ExitUsage {
		t.Errorf("negative limit exit = %d, want %d", code, ExitUsage)
	}
	if code, _, _ := h.exec(t, "sessions", "--state", "disabled"); code != ExitOK {
		t.Errorf("lowercase state exit = %d, want it accepted", code)
	}
}

// continuationSessionID is the session a dispatched resume continued the
// conversation under, which is never the id ccresu tracks.
const continuationSessionID = "22222222-2222-4222-8222-222222222222"

func TestSessionsShowsAContinuationColumnOnlyWhenOneExists(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, continuationSessionID, nil)
	h.seedSession(t, canonicalSessionID, nil)

	code, stdout, stderr := h.exec(t, "sessions")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, stderr)
	}
	if strings.Contains(stdout, "CONTINUATION") {
		t.Errorf("the listing grew a CONTINUATION column with nothing to show:\n%s", stdout)
	}

	if err := h.store.SetSessionContinuation(context.Background(),
		canonicalSessionID, continuationSessionID,
		"/home/u/.claude/projects/-work-project/"+continuationSessionID+".jsonl"); err != nil {
		t.Fatalf("SetSessionContinuation: %v", err)
	}

	code, stdout, _ = h.exec(t, "sessions")
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stdout, "CONTINUATION") {
		t.Errorf("the listing does not name the continuation session:\n%s", stdout)
	}
	if !strings.Contains(stdout, continuationSessionID[:8]) {
		t.Errorf("the continuation id is missing from the listing:\n%s", stdout)
	}

	code, stdout, _ = h.exec(t, "--json", "sessions")
	if code != ExitOK {
		t.Fatalf("json exit = %d", code)
	}
	var sessions []storage.Session
	if err := json.Unmarshal([]byte(stdout), &sessions); err != nil {
		t.Fatalf("sessions --json: %v", err)
	}
	found := false
	for _, session := range sessions {
		if session.ID == canonicalSessionID {
			found = true
			if session.ContinuationSessionID != continuationSessionID {
				t.Errorf("json continuation = %q, want %q",
					session.ContinuationSessionID, continuationSessionID)
			}
		}
	}
	if !found {
		t.Errorf("the resumed session is missing from the json listing: %s", stdout)
	}
}

func TestInspectNamesTheContinuationSession(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)

	code, stdout, stderr := h.exec(t, "inspect", canonicalSessionID)
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, stderr)
	}
	if strings.Contains(stdout, "continuation") {
		t.Errorf("inspect claims a continuation before one was recorded:\n%s", stdout)
	}

	if err := h.store.SetSessionContinuation(context.Background(),
		canonicalSessionID, continuationSessionID,
		"/home/u/.claude/projects/-work-project/"+continuationSessionID+".jsonl"); err != nil {
		t.Fatalf("SetSessionContinuation: %v", err)
	}

	code, stdout, _ = h.exec(t, "inspect", canonicalSessionID)
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stdout, "continuation") || !strings.Contains(stdout, continuationSessionID) {
		t.Errorf("inspect does not say where the conversation went:\n%s", stdout)
	}
}

func TestSessionsReportsAnEmptyStore(t *testing.T) {
	h := newHarness(t)
	code, stdout, _ := h.exec(t, "sessions")
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stdout, "no sessions match") {
		t.Errorf("stdout = %q, want the empty message", stdout)
	}
}

func TestInspectPrintsEverythingKnown(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)
	ctx := context.Background()
	if _, err := h.store.RecordUsageLimit(ctx, storage.UsageLimit{
		SessionID:  canonicalSessionID,
		LimitType:  storage.LimitTypeFiveHour,
		ResetRaw:   "12pm (Europe/Berlin)",
		ResetAt:    harnessBaseTime,
		Confidence: 0.95,
	}); err != nil {
		t.Fatalf("record usage limit: %v", err)
	}
	if _, err := h.store.SaveSchedule(ctx, storage.Schedule{
		SessionID: canonicalSessionID,
		ResetAt:   harnessBaseTime.Add(3600e9),
		Unknown:   false,
	}); err != nil {
		t.Fatalf("save schedule: %v", err)
	}
	if _, err := h.store.TransitionSession(ctx, canonicalSessionID, storage.StateRunning, "started", 0); err != nil {
		t.Fatalf("transition: %v", err)
	}

	code, stdout, stderr := h.exec(t, "inspect", canonicalSessionID)
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, stderr)
	}
	for _, want := range []string{
		canonicalSessionID, "RUNNING", "Latest usage limit", "five_hour",
		"12pm (Europe/Berlin)", "Schedule", "Recent transitions",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("inspect output missing %q:\n%s", want, stdout)
		}
	}

	code, stdout, _ = h.exec(t, "--json", "inspect", canonicalSessionID)
	if code != ExitOK {
		t.Fatalf("json exit = %d", code)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("inspect --json: %v", err)
	}
	if report["session"] == nil || report["usage_limit"] == nil || report["schedule"] == nil {
		t.Errorf("inspect report is missing sections: %+v", report)
	}
}

func TestInspectReportsAMissingSession(t *testing.T) {
	h := newHarness(t)
	code, _, stderr := h.exec(t, "inspect", canonicalSessionID)
	if code != ExitNotFound {
		t.Fatalf("exit = %d, want %d", code, ExitNotFound)
	}
	if !strings.Contains(stderr, canonicalSessionID) {
		t.Errorf("stderr = %q, want it to name the session", stderr)
	}
}

func TestInspectRequiresASessionID(t *testing.T) {
	h := newHarness(t)
	if code, _, _ := h.exec(t, "inspect"); code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
	if code, _, _ := h.exec(t, "inspect", "a", "b"); code != ExitUsage {
		t.Errorf("two ids exit = %d, want %d", code, ExitUsage)
	}
}

func TestHistoryShowsAttemptsAndTransitions(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.State = storage.StateDiscovered
	})
	ctx := context.Background()
	if _, err := h.store.TransitionSession(ctx, canonicalSessionID, storage.StateRunning, "process is alive", 0); err != nil {
		t.Fatalf("transition: %v", err)
	}

	code, stdout, stderr := h.exec(t, "history", canonicalSessionID)
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, stderr)
	}
	if !strings.Contains(stdout, "DISCOVERED -> RUNNING") {
		t.Errorf("history output missing the transition:\n%s", stdout)
	}
	if !strings.Contains(stdout, "No recovery attempts recorded") {
		t.Errorf("history output missing the attempts note:\n%s", stdout)
	}

	code, stdout, _ = h.exec(t, "--json", "history", canonicalSessionID)
	if code != ExitOK {
		t.Fatalf("json exit = %d", code)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("history --json: %v", err)
	}
	if report["session_id"] != canonicalSessionID {
		t.Errorf("report session = %v", report["session_id"])
	}

	if code, _, _ := h.exec(t, "history", "99999999-9999-4999-8999-999999999999"); code != ExitNotFound {
		t.Errorf("unknown session exit = %d, want %d", code, ExitNotFound)
	}
}

func TestEnableAndDisable(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)

	code, stdout, stderr := h.exec(t, "disable", canonicalSessionID)
	if code != ExitOK {
		t.Fatalf("disable exit = %d (stderr %q)", code, stderr)
	}
	if !strings.Contains(stdout, "enabled=no") {
		t.Errorf("disable output = %q", stdout)
	}
	session, err := h.store.GetSession(context.Background(), canonicalSessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if session.Enabled {
		t.Error("the session is still enabled after disable")
	}

	code, stdout, _ = h.exec(t, "--json", "enable", canonicalSessionID)
	if code != ExitOK {
		t.Fatalf("enable exit = %d", code)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("enable --json: %v", err)
	}
	if report["enabled"] != true {
		t.Errorf("report = %+v", report)
	}
	session, _ = h.store.GetSession(context.Background(), canonicalSessionID)
	if !session.Enabled {
		t.Error("the session is not enabled after enable")
	}

	if code, _, _ := h.exec(t, "enable", "99999999-9999-4999-8999-999999999999"); code != ExitNotFound {
		t.Errorf("unknown session exit = %d, want %d", code, ExitNotFound)
	}
}

func TestRecoverRefusesADisabledSession(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.Enabled = false
		s.State = storage.StateDisabled
	})

	code, _, stderr := h.exec(t, "recover", canonicalSessionID)
	if code != ExitNotEligible {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, ExitNotEligible, stderr)
	}
	if !strings.Contains(stderr, canonicalSessionID) {
		t.Errorf("stderr = %q, want it to name the session", stderr)
	}
}

func TestRecoverResumesAWaitingSession(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.State = storage.StateResetTimeReached
		s.TranscriptSize = 10
		s.TranscriptLines = 1
	})
	ctx := context.Background()
	resumed := claude.Session{
		ID:          canonicalSessionID,
		ProjectPath: "/work/project",
		Recoverable: true,
	}
	h.provider.SetSession(resumed)
	h.provider.Statuses[canonicalSessionID] = claude.SessionStatus{
		Session:    resumed,
		Transcript: claude.TranscriptInfo{Path: "/tmp/t.jsonl", Size: 400, Lines: 9},
	}
	// A resume is a background dispatch, so the provider registers the job it
	// created; the command's verification follows that job to completion.
	h.provider.ResumeResults[canonicalSessionID] = claude.ResumeResult{RequestedSessionID: canonicalSessionID}
	// The resumed run appends to the transcript; verification must observe that
	// growth rather than a frozen measurement.
	h.provider.OnResume = func(context.Context, string, string) (claude.ResumeResult, error) {
		jobID := claude.FakeJobID(canonicalSessionID)
		h.provider.SetJob(claude.AgentEntry{
			StateID:   jobID,
			SessionID: canonicalSessionID,
			Kind:      "background",
			State:     "done",
		})
		h.provider.Statuses[canonicalSessionID] = claude.SessionStatus{
			Session:    resumed,
			Transcript: claude.TranscriptInfo{Path: "/tmp/t.jsonl", Size: 4096, Lines: 42},
		}
		return claude.ResumeResult{
			RequestedSessionID:    canonicalSessionID,
			JobID:                 jobID,
			ContinuationSessionID: canonicalSessionID,
		}, nil
	}

	code, stdout, stderr := h.exec(t, "recover", canonicalSessionID)
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %s)", code, stderr)
	}
	for _, want := range []string{"resumed:", "yes", "RESUMED", "proven"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("recover output missing %q:\n%s", want, stdout)
		}
	}
	attempts, err := h.store.ListAttempts(ctx, canonicalSessionID, 0)
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
}

func TestRecoverReportsAMissingSession(t *testing.T) {
	h := newHarness(t)
	code, _, stderr := h.exec(t, "recover", "99999999-9999-4999-8999-999999999999")
	if code != ExitNotFound {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, ExitNotFound, stderr)
	}
	if !strings.Contains(stderr, "recover") {
		t.Errorf("stderr = %q, want it to name the command", stderr)
	}
}

func TestRecoverWithoutARecoveryManager(t *testing.T) {
	h := newHarness(t)
	h.rt.Recovery = nil
	h.seedSession(t, canonicalSessionID, nil)
	code, _, stderr := h.exec(t, "recover", canonicalSessionID)
	if code != ExitNotEligible {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, ExitNotEligible, stderr)
	}
	if !strings.Contains(stderr, "recovery is not configured") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestRetryReportsAFailedSession(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.State = storage.StateUnrecoverable
		s.Recoverable = false
		s.LastError = "the limit never cleared"
	})
	code, _, stderr := h.exec(t, "retry", canonicalSessionID)
	if code == ExitOK {
		t.Fatalf("retry of an unknown session succeeded (stderr %q)", stderr)
	}
	if !strings.Contains(stderr, "retry") {
		t.Errorf("stderr = %q, want it to name the command", stderr)
	}
}

func TestConfigValidateAcceptsAValidFile(t *testing.T) {
	h := newHarness(t)
	code, stdout, stderr := h.exec(t, "config", "validate")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, stderr)
	}
	if !strings.Contains(stdout, "is valid") || !strings.Contains(stdout, h.cfg.Source) {
		t.Errorf("stdout = %q", stdout)
	}

	code, stdout, _ = h.exec(t, "--json", "config", "validate")
	if code != ExitOK {
		t.Fatalf("json exit = %d", code)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("config validate --json: %v", err)
	}
	if report["valid"] != true {
		t.Errorf("report = %+v", report)
	}
}

func TestConfigValidateReportsEveryProblem(t *testing.T) {
	h := newHarness(t)
	h.loadErr = fmt.Errorf("telegram.bot_token: is required\ntelegram.chat_id: is required")
	code, _, stderr := h.exec(t, "config", "validate")
	if code != ExitFailure {
		t.Fatalf("exit = %d, want %d", code, ExitFailure)
	}
	if !strings.Contains(stderr, "telegram.bot_token") || !strings.Contains(stderr, "telegram.chat_id") {
		t.Errorf("stderr = %q, want every problem listed", stderr)
	}
}

func TestConfigValidateRejectsExtraArguments(t *testing.T) {
	h := newHarness(t)
	if code, _, _ := h.exec(t, "config", "validate", "a", "b"); code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
	if code, _, _ := h.exec(t, "config"); code != ExitUsage {
		t.Errorf("bare config exit = %d, want %d", code, ExitUsage)
	}
	if code, _, _ := h.exec(t, "config", "explain"); code != ExitUsage {
		t.Errorf("unknown subcommand exit = %d, want %d", code, ExitUsage)
	}
}

func TestConfigShowMasksTheBotToken(t *testing.T) {
	h := newHarness(t)
	const secret = "123456:SUPER-SECRET-token-value"
	h.cfg.Telegram.Enabled = true
	h.cfg.Telegram.BotToken = secret
	h.cfg.Telegram.ChatID = "42"

	code, stdout, stderr := h.exec(t, "config", "show")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, stderr)
	}
	if strings.Contains(stdout, "SUPER-SECRET") {
		t.Fatalf("config show leaked the bot token:\n%s", stdout)
	}
	if !strings.Contains(stdout, "42") {
		t.Errorf("config show dropped the chat id:\n%s", stdout)
	}
}

// versionedProvider adds the doctor's version probe to the fake provider.
type versionedProvider struct {
	*claude.FakeProvider
	report claude.VersionReport
	err    error
}

// Version implements VersionReporter.
func (v versionedProvider) Version(context.Context) (claude.VersionReport, error) {
	return v.report, v.err
}

func TestDoctorReportsEveryCheck(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)
	h.rt.Provider = versionedProvider{
		FakeProvider: h.provider,
		report:       claude.VersionReport{Command: []string{"claude", "--version"}},
	}

	code, stdout, stderr := h.exec(t, "doctor")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q, stdout %q)", code, stderr, stdout)
	}
	for _, want := range []string{"config", "database", "claude", "telegram", "sessions", "data directory"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("doctor output missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "fail") {
		t.Errorf("doctor reported a failure:\n%s", stdout)
	}

	code, stdout, _ = h.exec(t, "--json", "doctor")
	if code != ExitOK {
		t.Fatalf("json exit = %d", code)
	}
	var report struct {
		Checks []check `json:"checks"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("doctor --json: %v", err)
	}
	names := make([]string, 0, len(report.Checks))
	for _, item := range report.Checks {
		names = append(names, item.Name)
	}
	sort.Strings(names)
	if len(names) < 6 {
		t.Errorf("checks = %v, want at least six", names)
	}
}

func TestDoctorFailsWhenTheDatabaseCannotBeOpened(t *testing.T) {
	h := newHarness(t)
	h.buildErr = errors.New("database is locked")
	code, stdout, stderr := h.exec(t, "doctor")
	if code != ExitFailure {
		t.Fatalf("exit = %d, want %d", code, ExitFailure)
	}
	if !strings.Contains(stdout, "database is locked") {
		t.Errorf("stdout = %q, want the failure detail", stdout)
	}
	if !strings.Contains(stderr, "check(s) failed") {
		t.Errorf("stderr = %q, want a summary", stderr)
	}
}

func TestDoctorFailsWhenTheClaudeProbeFails(t *testing.T) {
	h := newHarness(t)
	h.rt.Provider = versionedProvider{FakeProvider: h.provider, err: errors.New("claude: executable not found")}
	code, stdout, _ := h.exec(t, "doctor")
	if code != ExitFailure {
		t.Fatalf("exit = %d, want %d", code, ExitFailure)
	}
	if !strings.Contains(stdout, "executable not found") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestDoctorWarnsAboutAMissingConfiguration(t *testing.T) {
	h := newHarness(t)
	missing := h.cfg.Source + ".missing"
	h.cfg.Source = missing
	code, stdout, _ := h.exec(t, "doctor")
	if code != ExitFailure {
		t.Fatalf("exit = %d, want %d", code, ExitFailure)
	}
	if !strings.Contains(stdout, "not readable") {
		t.Errorf("stdout = %q, want the missing-file detail", stdout)
	}
}

func TestDoctorTelegramChecks(t *testing.T) {
	h := newHarness(t)
	h.rt.Provider = versionedProvider{FakeProvider: h.provider}
	h.cfg.Telegram.Enabled = true
	h.cfg.Telegram.BotToken = "123456:abcdefghijklmnop"
	h.cfg.Telegram.ChatID = "42"

	code, stdout, _ := h.exec(t, "doctor")
	if code != ExitOK {
		t.Fatalf("exit = %d (stdout %q)", code, stdout)
	}
	if !strings.Contains(stdout, "chat 42") {
		t.Errorf("stdout = %q, want the telegram line", stdout)
	}
	if strings.Contains(stdout, "abcdefghijklmnop") {
		t.Errorf("doctor leaked the bot token:\n%s", stdout)
	}

	h.rt.Notifier = nil
	code, stdout, _ = h.exec(t, "doctor")
	if code != ExitFailure {
		t.Fatalf("exit = %d, want a failure when the notifier is missing", code)
	}
	if !strings.Contains(stdout, "notifier not built") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestServeRequiresAServer(t *testing.T) {
	h := newHarness(t)
	h.server = nil
	code, _, stderr := h.exec(t, "serve")
	if code != ExitFailure {
		t.Fatalf("exit = %d, want %d", code, ExitFailure)
	}
	if !strings.Contains(stderr, "HTTP API") {
		t.Errorf("stderr = %q, want the not-available message", stderr)
	}
}

func TestServeRefusesADisabledServer(t *testing.T) {
	h := newHarness(t)
	h.server = &fakeServer{}
	h.cfg.Server.Enabled = false
	code, _, stderr := h.exec(t, "serve")
	if code != ExitFailure {
		t.Fatalf("exit = %d, want %d", code, ExitFailure)
	}
	if !strings.Contains(stderr, "server.enabled") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestServeRunsUntilTheSignalArrives(t *testing.T) {
	h := newHarness(t)
	server := &fakeServer{}
	h.server = server

	done := make(chan int, 1)
	go func() { done <- h.run("serve") }()

	waitForRun(t, func() bool {
		server.mu.Lock()
		defer server.mu.Unlock()
		return server.runs == 1
	})
	h.signals <- os.Interrupt

	select {
	case code := <-done:
		if code != ExitOK {
			t.Fatalf("exit = %d, want %d (stderr %q)", code, ExitOK, h.stderr.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not stop after the signal")
	}
	server.mu.Lock()
	stopped := server.stopped
	server.mu.Unlock()
	if !stopped {
		t.Error("serve returned before the server stopped")
	}
	if !strings.Contains(h.stdout.String(), "API stopped") {
		t.Errorf("stdout = %q", h.stdout.String())
	}
}

func TestServeReportsAServerFailure(t *testing.T) {
	h := newHarness(t)
	h.server = &fakeServer{err: errors.New("bind: address already in use")}
	code, _, stderr := h.exec(t, "serve")
	if code != ExitFailure {
		t.Fatalf("exit = %d, want %d", code, ExitFailure)
	}
	if !strings.Contains(stderr, "address already in use") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestServeReportsABuildFailure(t *testing.T) {
	h := newHarness(t)
	h.serverErr = errors.New("token file is not writable")
	if code, _, stderr := h.exec(t, "serve"); code != ExitFailure {
		t.Errorf("exit = %d, want %d (stderr %q)", code, ExitFailure, stderr)
	}
}

func TestExitCodesForKnownErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, ExitOK},
		{"cancelled", context.Canceled, ExitInterrupted},
		{"not found", storage.ErrNotFound, ExitNotFound},
		{"not eligible", recovery.ErrNotEligible, ExitNotEligible},
		{"already running", recovery.ErrAlreadyRunning, ExitNotEligible},
		{"disabled", recovery.ErrDisabled, ExitNotEligible},
		{"max attempts", recovery.ErrMaxAttempts, ExitNotEligible},
		{"plain", errors.New("boom"), ExitFailure},
		{"explicit", withCode(ExitUsage, errors.New("bad flags")), ExitUsage},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := exitCode(testCase.err); got != testCase.want {
				t.Errorf("exitCode(%v) = %d, want %d", testCase.err, got, testCase.want)
			}
		})
	}
}

func TestGlobalFlagParsing(t *testing.T) {
	h := newHarness(t)
	app := newApp(h.deps(), BuildInfo{}, &h.stdout, &h.stderr)

	rest, err := app.parseGlobal([]string{"--config=/tmp/x.yaml", "--json", "--verbose", "status"})
	if err != nil {
		t.Fatalf("parseGlobal: %v", err)
	}
	if len(rest) != 1 || rest[0] != "status" {
		t.Errorf("rest = %v", rest)
	}
	if app.configPath != "/tmp/x.yaml" || !app.json || app.logLevel != "debug" {
		t.Errorf("flags = %q %v %q", app.configPath, app.json, app.logLevel)
	}

	app2 := newApp(Deps{}, BuildInfo{}, &h.stdout, &h.stderr)
	if _, err := app2.parseGlobal([]string{"--", "status"}); err != nil {
		t.Errorf("-- handling: %v", err)
	}
	if rest, err := app2.parseGlobal([]string{"--log-level=warn", "--log-format=text", "status"}); err != nil || len(rest) != 1 {
		t.Errorf("log flags: %v %v", rest, err)
	}
	if rest, err := app2.parseGlobal([]string{"--version"}); err != nil || len(rest) != 1 || rest[0] != "version" {
		t.Errorf("--version: %v %v", rest, err)
	}
	if rest, err := app2.parseGlobal([]string{"-q", "status"}); err != nil || app2.logLevel != "error" {
		t.Errorf("-q: %v %v", rest, err)
	}
}

// waitForRun blocks until condition holds, failing the test after two seconds.
func waitForRun(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if condition() {
			return
		}
		select {
		case <-deadline:
			t.Fatal("condition was not met within two seconds")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestSessionArgValidation(t *testing.T) {
	h := newHarness(t)
	app := newApp(h.deps(), BuildInfo{}, &h.stdout, &h.stderr)
	fs := app.flags("x", "x <id>")
	fs.SetOutput(io.Discard)
	if err := fs.Parse([]string{"  abc  "}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	id, err := app.sessionArg(fs, "x")
	if err != nil || id != "abc" {
		t.Errorf("sessionArg = %q, %v", id, err)
	}
}

func TestParseFlagsWrapsErrorsAsUsage(t *testing.T) {
	h := newHarness(t)
	app := newApp(h.deps(), BuildInfo{}, &h.stdout, &h.stderr)
	fs := app.flags("x", "x")
	fs.SetOutput(io.Discard)
	err := app.parseFlags(fs, []string{"--nope"})
	if err == nil {
		t.Fatal("parseFlags accepted an unknown flag")
	}
	if !errors.Is(err, flag.ErrHelp) && !strings.Contains(err.Error(), "nope") {
		t.Errorf("err = %v", err)
	}
	if got := exitCode(err); got != ExitUsage {
		t.Errorf("exitCode = %d, want %d", got, ExitUsage)
	}
}

func TestFormattingHelpers(t *testing.T) {
	if got := shortID("abcdefghijkl"); got != "abcdefgh" {
		t.Errorf("shortID = %q", got)
	}
	if got := shortID("abc"); got != "abc" {
		t.Errorf("shortID(short) = %q", got)
	}
	// The limit is the total budget, ellipsis included, so a truncation can never
	// overflow the column it was sized for.
	if got := truncateText("  hello\nworld  ", 8); got != "hello..." {
		t.Errorf("truncateText = %q", got)
	}
	if got := truncateText("abc", 3); got != "abc" {
		t.Errorf("truncateText(short) = %q", got)
	}
	if got := truncateText("abcdef", 3); got != "abc" {
		t.Errorf("truncateText(tiny limit) = %q", got)
	}
	if got := yesNo(true) + yesNo(false); got != "yesno" {
		t.Errorf("yesNo = %q", got)
	}
	if got := formatTime(harnessBaseTime, nil); !strings.Contains(got, "UTC") {
		t.Errorf("formatTime = %q", got)
	}
	if got := formatTime(harnessBaseTime, time.UTC); got != "2026-01-05 12:00:00 UTC" {
		t.Errorf("formatTime = %q", got)
	}
	if got := formatTime(time.Time{}, time.UTC); got != "-" {
		t.Errorf("formatTime(zero) = %q", got)
	}
	if got := formatDuration(0); got != "-" {
		t.Errorf("formatDuration(0) = %q", got)
	}
	if got := formatDuration(90e9); got != "1m30s" {
		t.Errorf("formatDuration = %q", got)
	}
	if got := emptyDash("  "); got != "-" {
		t.Errorf("emptyDash = %q", got)
	}
	if got := emptyDash("x"); got != "x" {
		t.Errorf("emptyDash = %q", got)
	}
	if got := plural(1) + "|" + plural(2); got != "|s" {
		t.Errorf("plural = %q", got)
	}
	if got := limitType(detector.LimitFiveHour); got != storage.LimitTypeFiveHour {
		t.Errorf("limitType = %q", got)
	}
	if got := limitType(detector.LimitOther); got != storage.LimitTypeOther {
		t.Errorf("limitType(unknown) = %q", got)
	}
}

// TestSessionsShowsTheNameTheCLIReported is the display half of the loop: an
// operator watching a session needs to recognise it by name, and a listing of
// unnamed sessions must not grow a column that says nothing.
func TestSessionsShowsTheNameTheCLIReported(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)

	code, stdout, stderr := h.exec(t, "sessions")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, stderr)
	}
	if strings.Contains(stdout, "NAME") {
		t.Errorf("the listing grew a NAME column with nothing to show:\n%s", stdout)
	}

	if err := h.store.SetSessionName(context.Background(), canonicalSessionID, "nightly-audit"); err != nil {
		t.Fatalf("SetSessionName: %v", err)
	}

	code, stdout, _ = h.exec(t, "sessions")
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stdout, "NAME") || !strings.Contains(stdout, "nightly-audit") {
		t.Errorf("the listing does not show the session name:\n%s", stdout)
	}

	code, stdout, _ = h.exec(t, "inspect", canonicalSessionID)
	if code != ExitOK {
		t.Fatalf("inspect exit = %d", code)
	}
	if !strings.Contains(stdout, "nightly-audit") {
		t.Errorf("inspect does not report the name:\n%s", stdout)
	}
}
