package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/storage"
)

// seedDeletedSession puts a session in a stuck-looking state with an attempt and
// a transition behind it, i.e. the shape an operator reaches for
// `sessions delete` to clear.
func seedDeletableSession(t *testing.T, h *harness, id string) {
	t.Helper()
	ctx := context.Background()
	h.seedSession(t, id, func(s *storage.Session) {
		s.State = storage.StateCheckingSession
		s.LastError = "resume failed"
	})
	attempt, err := h.store.StartAttempt(ctx, id, 0, "claude -p --resume "+id)
	if err != nil {
		t.Fatalf("seed attempt: %v", err)
	}
	if _, err := h.store.FinishAttempt(ctx, attempt.ID, storage.AttemptResult{
		State: storage.StateRetryWait,
		Error: "resume failed",
	}); err != nil {
		t.Fatalf("finish attempt: %v", err)
	}
	if _, err := h.store.TransitionSession(ctx, id, storage.StateRetryWait, "seed transition", 0); err != nil {
		t.Fatalf("seed transition: %v", err)
	}
}

// seedFailedSession puts a session in the state a failed resume leaves behind:
// the resume error recorded, one finished attempt, and a state the history can
// still show a transition out of.
func seedFailedSession(t *testing.T, h *harness, id string) {
	t.Helper()
	ctx := context.Background()
	h.seedSession(t, id, func(s *storage.Session) {
		s.State = storage.StateCheckingSession
		s.LastError = "resume failed"
	})
	attempt, err := h.store.StartAttempt(ctx, id, 0, "claude -p --resume "+id)
	if err != nil {
		t.Fatalf("seed attempt: %v", err)
	}
	if _, err := h.store.FinishAttempt(ctx, attempt.ID, storage.AttemptResult{
		State: storage.StateRetryWait,
		Error: "resume failed",
	}); err != nil {
		t.Fatalf("finish attempt: %v", err)
	}
}

// TestSessionsStatusShowsEverythingKnown is the new spelling of `inspect`: the
// two must show the same fields, because they are the same code.
func TestSessionsStatusShowsEverythingKnown(t *testing.T) {
	h := newHarness(t)
	seedFailedSession(t, h, canonicalSessionID)
	if _, err := h.store.TransitionSession(context.Background(), canonicalSessionID,
		storage.StateRetryWait, "seeded so the history section has a row", 0); err != nil {
		t.Fatalf("seed transition: %v", err)
	}

	code, statusOut, stderr := h.exec(t, "sessions", "status", canonicalSessionID)
	if code != ExitOK {
		t.Fatalf("sessions status exit = %d, want %d (stderr %q)", code, ExitOK, stderr)
	}
	_, inspectOut, _ := h.exec(t, "inspect", canonicalSessionID)

	if statusOut != inspectOut {
		t.Errorf("sessions status and inspect disagree:\n status = %q\ninspect = %q", statusOut, inspectOut)
	}
	for _, want := range []string{
		"session", canonicalSessionID,
		"state", "RETRY_WAIT",
		"recoverable",
		"transcript",
		"Next attempt",
		"Latest attempt",
		"Recent transitions",
	} {
		if !strings.Contains(statusOut, want) {
			t.Errorf("sessions status output %q does not mention %q", statusOut, want)
		}
	}
}

func TestSessionsStatusUsage(t *testing.T) {
	h := newHarness(t)

	if code, _, _ := h.exec(t, "sessions", "status"); code != ExitUsage {
		t.Errorf("sessions status without an id = %d, want %d", code, ExitUsage)
	}
	if code, _, _ := h.exec(t, "sessions", "status", "a", "b"); code != ExitUsage {
		t.Errorf("sessions status with two ids = %d, want %d", code, ExitUsage)
	}
	if code, _, stderr := h.exec(t, "sessions", "status", "99999999-9999-4999-8999-999999999999"); code != ExitNotFound {
		t.Errorf("sessions status of an unknown session = %d, want %d", code, ExitNotFound)
	} else if !strings.Contains(stderr, "not found") {
		t.Errorf("stderr = %q, want it to say the session was not found", stderr)
	}
}

// TestSessionsStatusAcceptsGlobalFlagsAfterTheSubcommand keeps `--json` usable in
// the position people actually type it.
func TestSessionsStatusJSON(t *testing.T) {
	h := newHarness(t)
	seedFailedSession(t, h, canonicalSessionID)

	code, stdout, stderr := h.exec(t, "sessions", "--json", "status", canonicalSessionID)
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, ExitOK, stderr)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, stdout)
	}
	session, ok := report["session"].(map[string]any)
	if !ok {
		t.Fatalf("report has no session object: %v", report)
	}
	// The session object is the storage struct, so its JSON keys are the Go field
	// names; the report is rendered from `inspect`, which has always done this.
	if session["ID"] != canonicalSessionID {
		t.Errorf("session.ID = %v, want %q", session["ID"], canonicalSessionID)
	}
	if _, ok := report["next_attempt"]; !ok {
		t.Errorf("report has no next_attempt: %v", report)
	}
}

func TestSessionsDeleteRemovesTheSession(t *testing.T) {
	h := newHarness(t)
	seedDeletableSession(t, h, canonicalSessionID)
	ctx := context.Background()

	code, stdout, stderr := h.exec(t, "sessions", "delete", canonicalSessionID)
	if code != ExitOK {
		t.Fatalf("sessions delete exit = %d, want %d (stderr %q)", code, ExitOK, stderr)
	}
	for _, want := range []string{
		"session " + canonicalSessionID,
		"deleted",
		"attempts removed",
		"conversation",
		"nothing under ~/.claude is touched",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout %q does not mention %q", stdout, want)
		}
	}

	// The record is gone, its history went with it, and the tombstone keeps
	// discovery from bringing it back.
	if _, err := h.store.GetSession(ctx, canonicalSessionID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("GetSession after delete = %v, want ErrNotFound", err)
	}
	if attempts, err := h.store.ListAttempts(ctx, canonicalSessionID, 0); err != nil || len(attempts) != 0 {
		t.Errorf("attempts survived the delete: %v, %v", attempts, err)
	}
	deleted, err := h.store.DeletedSessionIDs(ctx)
	if err != nil {
		t.Fatalf("DeletedSessionIDs: %v", err)
	}
	if _, gone := deleted[canonicalSessionID]; !gone {
		t.Error("the session was deleted without a tombstone: discovery would re-create it")
	}

	// And it is no longer listed.
	if code, _, _ := h.exec(t, "sessions", "--all"); code != ExitOK {
		t.Fatalf("sessions --all exit = %d", code)
	}
	if strings.Contains(h.stdout.String(), canonicalSessionID) {
		t.Errorf("the deleted session is still listed: %q", h.stdout.String())
	}
}

func TestSessionsDeleteJSON(t *testing.T) {
	h := newHarness(t)
	seedDeletableSession(t, h, canonicalSessionID)

	code, stdout, stderr := h.exec(t, "--json", "sessions", "delete", canonicalSessionID)
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, ExitOK, stderr)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, stdout)
	}
	if report["session_id"] != canonicalSessionID {
		t.Errorf("session_id = %v, want %q", report["session_id"], canonicalSessionID)
	}
	if report["deleted"] != true {
		t.Errorf("deleted = %v, want true", report["deleted"])
	}
	if report["transcript_kept"] != true {
		t.Errorf("transcript_kept = %v, want true: the conversation is never touched", report["transcript_kept"])
	}
	removed, ok := report["removed"].(map[string]any)
	if !ok {
		t.Fatalf("removed is not an object: %v", report)
	}
	if removed["attempts"] != float64(1) {
		t.Errorf("removed.attempts = %v, want 1", removed["attempts"])
	}
	if restore, _ := report["restore"].(string); !strings.Contains(restore, "ccresu enable") {
		t.Errorf("restore = %q, want the documented undo", restore)
	}
}

// TestSessionsDeleteIsIdempotent: deleting twice is not an error, because the
// operator asked for the session to be gone and it is.
func TestSessionsDeleteIsIdempotent(t *testing.T) {
	h := newHarness(t)
	seedDeletableSession(t, h, canonicalSessionID)

	if code, _, stderr := h.exec(t, "sessions", "delete", canonicalSessionID); code != ExitOK {
		t.Fatalf("first delete exit = %d (stderr %q)", code, stderr)
	}
	code, stdout, stderr := h.exec(t, "sessions", "delete", canonicalSessionID)
	if code != ExitOK {
		t.Fatalf("second delete exit = %d, want %d (stderr %q)", code, ExitOK, stderr)
	}
	if !strings.Contains(stdout, "already deleted") {
		t.Errorf("stdout = %q, want it to say the session was already deleted", stdout)
	}
	if !strings.Contains(stdout, "ccresu enable") {
		t.Errorf("stdout = %q, want the documented undo", stdout)
	}
}

func TestSessionsDeleteUsageAndUnknown(t *testing.T) {
	h := newHarness(t)

	if code, _, _ := h.exec(t, "sessions", "delete"); code != ExitUsage {
		t.Errorf("sessions delete without an id = %d, want %d", code, ExitUsage)
	}
	code, _, stderr := h.exec(t, "sessions", "delete", "88888888-8888-4888-8888-888888888888")
	if code != ExitNotFound {
		t.Errorf("sessions delete of an unknown session = %d, want %d", code, ExitNotFound)
	}
	if !strings.Contains(stderr, "not found") {
		t.Errorf("stderr = %q, want it to say the session was not found", stderr)
	}
}

// TestEnableRestoresADeletedSession closes the loop the delete message promises:
// the documented undo has to work, and it has to bring the session back enabled.
func TestEnableRestoresADeletedSession(t *testing.T) {
	h := newHarness(t)
	seedDeletableSession(t, h, canonicalSessionID)
	ctx := context.Background()

	if code, _, stderr := h.exec(t, "sessions", "delete", canonicalSessionID); code != ExitOK {
		t.Fatalf("delete exit = %d (stderr %q)", code, stderr)
	}

	code, stdout, stderr := h.exec(t, "enable", canonicalSessionID)
	if code != ExitOK {
		t.Fatalf("enable after delete exit = %d, want %d (stderr %q)", code, ExitOK, stderr)
	}
	if !strings.Contains(stdout, "restored") {
		t.Errorf("stdout = %q, want it to report the restore", stdout)
	}

	session, err := h.store.GetSession(ctx, canonicalSessionID)
	if err != nil {
		t.Fatalf("session was not restored: %v", err)
	}
	if !session.Enabled {
		t.Error("restored session is disabled, want enabled")
	}
	if session.State != storage.StateDiscovered {
		t.Errorf("restored state = %q, want %q", session.State, storage.StateDiscovered)
	}
	if _, err := h.store.GetDeletedSession(ctx, canonicalSessionID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("the tombstone survived the restore: %v", err)
	}
}

// TestSessionsUnknownSubcommandIsAUsageError keeps a typo a usage error rather
// than a silent listing.
func TestSessionsRejectsAnUnknownSubcommand(t *testing.T) {
	h := newHarness(t)

	code, _, stderr := h.exec(t, "sessions", "deletee", canonicalSessionID)
	if code != ExitUsage {
		t.Fatalf("sessions deletee exit = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "sessions takes no arguments") {
		t.Errorf("stderr = %q, want the positional-argument usage error", stderr)
	}
}

// TestSessionsSubcommandScan covers the argument shapes the dispatcher must
// accept, including the ones that put a global flag before the subcommand.
func TestSessionsSubcommandScan(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
		rest []string
		ok   bool
	}{
		{name: "plain", args: []string{"status", "id"}, want: "status", rest: []string{"id"}, ok: true},
		{name: "delete", args: []string{"delete", "id"}, want: "delete", rest: []string{"id"}, ok: true},
		// Flags written before the subcommand are forwarded, not dropped: that is
		// what makes `sessions --json status <id>` produce JSON.
		{name: "json first", args: []string{"--json", "status", "id"}, want: "status", rest: []string{"--json", "id"}, ok: true},
		{name: "limit first", args: []string{"--limit", "5", "status", "id"}, want: "status", rest: []string{"--limit", "5", "id"}, ok: true},
		{name: "limit equals", args: []string{"--limit=5", "status", "id"}, want: "status", rest: []string{"--limit=5", "id"}, ok: true},
		{name: "state then delete", args: []string{"--state", "RUNNING", "delete", "id"}, want: "delete", rest: []string{"--state", "RUNNING", "id"}, ok: true},
		{name: "after separator", args: []string{"--", "status", "id"}, want: "status", rest: []string{"--", "id"}, ok: true},
		{name: "no subcommand", args: []string{"--all"}, ok: false},
		{name: "no arguments", args: nil, ok: false},
		{name: "unknown word", args: []string{"bogus", "id"}, ok: false},
		{name: "separator then flag", args: []string{"--", "--all"}, ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, rest, ok := sessionsSubcommand(tc.args)
			if ok != tc.ok {
				t.Fatalf("sessionsSubcommand(%q) ok = %v, want %v", tc.args, ok, tc.ok)
			}
			if !ok {
				return
			}
			if name != tc.want {
				t.Errorf("subcommand = %q, want %q", name, tc.want)
			}
			if strings.Join(rest, ",") != strings.Join(tc.rest, ",") {
				t.Errorf("rest = %q, want %q", rest, tc.rest)
			}
		})
	}
}

// TestDiscoverySkipsADeletedSession is the reason delete needs a tombstone at
// all: the daemon must not re-create what the operator removed.
func TestDiscoverySkipsADeletedSession(t *testing.T) {
	h := newHarness(t)
	seedDeletableSession(t, h, canonicalSessionID)
	ctx := context.Background()

	if _, _, err := h.store.ForgetSession(ctx, canonicalSessionID); err != nil {
		t.Fatalf("ForgetSession: %v", err)
	}

	// The provider still sees the session: its transcript is on disk, and ccresu
	// never deletes the conversation.
	h.provider.SetSession(discoveredSession(canonicalSessionID, "/tmp/a.jsonl", claude.SessionTypeBackground))
	if err := NewDaemon(h.rt, time.Minute).discover(ctx); err != nil {
		t.Fatalf("discover: %v", err)
	}
	if _, err := h.store.GetSession(ctx, canonicalSessionID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("discovery resurrected the deleted session: %v", err)
	}
}

// clearFixture seeds three sessions: two that gave up and one that is merely
// disabled, so a clear that ignores its state filter is caught.
func clearFixture(t *testing.T, h *harness) (failed [2]string, other string) {
	t.Helper()
	failed = [2]string{
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
	}
	for _, id := range failed {
		seedFailedSession(t, h, id)
	}
	other = "44444444-4444-4444-8444-444444444444"
	h.seedSession(t, other, func(s *storage.Session) {
		s.State = storage.StateDisabled
		s.Enabled = false
	})
	return failed, other
}

// TestSessionsClearRemovesEveryPermanentlyFailedSession is the point of the
// command: the sessions that gave up stop appearing, and only those.
