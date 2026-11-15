package app

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/storage"
	"github.com/jymchng/ccresu/internal/telegram"
)

// hookSession is the conversation the hook tests are about.
const hookSession = "22222222-2222-4222-8222-222222222222"

// hookUnknownSession is an id ccresu has never discovered, which the hook must
// leave alone (R11).
const hookUnknownSession = "33333333-3333-4333-8333-333333333333"

// hookJSON builds one Notification payload in the shape Claude Code writes it:
// the fields ccresu reads plus the ones it merely records, so a test that
// depends on the parser being tolerant is exercising a realistic document.
func hookJSON(notificationType, sessionID string) string {
	return `{"session_id":"` + sessionID + `","notification_type":"` + notificationType +
		`","hook_event_name":"Notification","cwd":"/work/project","message":"Claude needs your permission"}`
}

// publishBlocked scripts the supervisor listing as one blocked session, which is
// the only observation a hook may announce.
func publishBlocked(t *testing.T, h *harness, sessionID, waitingFor string) {
	t.Helper()
	h.provider.Published = claude.PublishedListing{
		ReadAt: harnessBaseTime,
		Entries: []claude.AgentEntry{{
			StateID:    "state-" + sessionID,
			SessionID:  sessionID,
			State:      "blocked",
			Status:     "waiting",
			WaitingFor: waitingFor,
		}},
	}
}

// TestHookAlwaysExitsZeroAndSaysNothing is R1's whole matrix: every input a hook
// can be handed leaves the process quiet and successful, because the alternative
// is a line of ccresu's output in somebody else's transcript or a non-zero exit
// the CLI reports as a broken hook.
func TestHookAlwaysExitsZeroAndSaysNothing(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, hookSession, nil)
	publishBlocked(t, h, hookSession, "permission to edit .env")

	payloads := []struct {
		name  string
		stdin string
	}{
		{"no payload at all", ""},
		{"not JSON", "please resume the session"},
		{"truncated JSON", `{"notification_type":"agent_needs_input",`},
		{"larger than the cap", strings.Repeat("x", hookMaxPayloadBytes+1)},
		{"no session id", `{"notification_type":"agent_needs_input"}`},
		{"no notification type", `{"session_id":"` + hookSession + `"}`},
		{"an interactive-only type", hookJSON("permission_prompt", hookSession)},
		{"a type from a newer CLI", hookJSON("some_future_notification", hookSession)},
		{"a session ccresu does not know", hookJSON(hookNeedsInputType, hookUnknownSession)},
		{"the type the hook exists for", hookJSON(hookNeedsInputType, hookSession)},
		{"the completed type", hookJSON(hookCompletedType, hookSession)},
	}
	for _, payload := range payloads {
		t.Run(payload.name, func(t *testing.T) {
			h.stdin = payload.stdin
			code, stdout, stderr := h.exec(t, "hook")
			if code != ExitOK {
				t.Fatalf("exit = %d, want %d: a hook's exit code is not a verdict", code, ExitOK)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want nothing: a hook may not write to the transcript", stdout)
			}
			if stderr != "" {
				t.Errorf("stderr = %q, want nothing: a hook may not write to the transcript", stderr)
			}
		})
	}
}

// TestHookAnnouncesABlockedSessionOnce proves the hook reports the observation it
// was given and nothing else, and that the message carries the episode identity
// both producers share (PRD 4 R4, R14 and §6.3).
func TestHookAnnouncesABlockedSessionOnce(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, hookSession, func(s *storage.Session) { s.ProjectPath = "/work/hooked" })
	publishBlocked(t, h, hookSession, "permission to edit .env")
	h.stdin = hookJSON(hookNeedsInputType, hookSession)

	if code, stdout, stderr := h.exec(t, "hook"); code != ExitOK || stdout != "" || stderr != "" {
		t.Fatalf("hook = exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	requests := h.notifier.queued()
	if len(requests) != 1 {
		t.Fatalf("queued %d notification(s), want exactly one", len(requests))
	}
	request := requests[0]
	if request.Event != storage.EventNeedsInput {
		t.Errorf("event = %q, want %q", request.Event, storage.EventNeedsInput)
	}
	if request.SessionID != hookSession {
		t.Errorf("session = %q, want %q", request.SessionID, hookSession)
	}
	if request.Data.AttentionStart.IsZero() {
		t.Error("the message carries no attention episode, so it has no identity to deduplicate on")
	}
	if request.Data.ProjectPath != "/work/hooked" {
		t.Errorf("project = %q, want the persisted row's /work/hooked rather than the payload's cwd",
			request.Data.ProjectPath)
	}
	if !strings.Contains(request.Data.Reason, "permission to edit .env") {
		t.Errorf("reason = %q, want the supervisor's own words", request.Data.Reason)
	}

	first := telegram.DedupeKey(request.Event, request.Data)
	want := string(storage.EventNeedsInput) + ":" + hookSession + ":" +
		strconv.FormatInt(request.Data.AttentionStart.UTC().Unix(), 10)
	if first != want {
		t.Errorf("dedupe key = %q, want the episode key %q", first, want)
	}

	// The same block seen again by the other documented type: a second hook, or
	// the daemon's own pass a minute later, must compute the very same key, which
	// is what makes the store's unique constraint the only dedupe mechanism.
	h.stdin = hookJSON(hookCompletedType, hookSession)
	if code, _, _ := h.exec(t, "hook"); code != ExitOK {
		t.Fatalf("second hook exit = %d, want %d", code, ExitOK)
	}
	again := h.notifier.queued()
	if len(again) != 2 {
		t.Fatalf("queued %d notification(s) after the second hook, want 2 observations of one episode", len(again))
	}
	if got := telegram.DedupeKey(again[1].Event, again[1].Data); got != first {
		t.Errorf("the second observation keyed %q, want the episode key %q: one block must not queue two messages", got, first)
	}
}

// TestHookAnnouncesNothingWhenTheObservationIsNotBlocked covers the silence that
// makes the hook safe: it reports a past moment, the observation is the present,
// and only the present may be announced (PRD 4 §6.1).
func TestHookAnnouncesNothingWhenTheObservationIsNotBlocked(t *testing.T) {
	cases := []struct {
		name    string
		entry   *claude.AgentEntry
		readErr error
	}{
		{"the session finished", &claude.AgentEntry{SessionID: hookSession, State: "done"}, nil},
		{"the session is working", &claude.AgentEntry{SessionID: hookSession, State: "working"}, nil},
		{"the session failed", &claude.AgentEntry{SessionID: hookSession, State: "failed"}, nil},
		{"the listing does not mention it", nil, nil},
		{"the listing could not be read", nil, io.ErrUnexpectedEOF},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.seedSession(t, hookSession, nil)
			if tc.readErr != nil {
				h.provider.PublishedErr = tc.readErr
			} else if tc.entry != nil {
				h.provider.Published = claude.PublishedListing{
					ReadAt:  harnessBaseTime,
					Entries: []claude.AgentEntry{*tc.entry},
				}
			}
			h.stdin = hookJSON(hookNeedsInputType, hookSession)

			if code, stdout, stderr := h.exec(t, "hook"); code != ExitOK || stdout != "" || stderr != "" {
				t.Fatalf("hook = exit %d, stdout %q, stderr %q", code, stdout, stderr)
			}
			if requests := h.notifier.queued(); len(requests) != 0 {
				t.Errorf("queued %d notification(s), want none: the session is not blocked now", len(requests))
			}
		})
	}
}

// TestHookLeavesAnUnknownSessionAlone is R11: discovery owns discovery, so an id
// ccresu has never seen ends the invocation instead of creating a row.
func TestHookLeavesAnUnknownSessionAlone(t *testing.T) {
	h := newHarness(t)
	publishBlocked(t, h, hookUnknownSession, "permission to edit .env")
	h.stdin = hookJSON(hookNeedsInputType, hookUnknownSession)

	if code, stdout, stderr := h.exec(t, "hook"); code != ExitOK || stdout != "" || stderr != "" {
		t.Fatalf("hook = exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if requests := h.notifier.queued(); len(requests) != 0 {
		t.Errorf("queued %d notification(s) for an undiscovered session, want none", len(requests))
	}
	sessions, err := h.store.ListSessions(context.Background(), storage.SessionFilter{})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("the hook created %d session(s), want none", len(sessions))
	}
}

// TestHookOnlyObserves is the positive form of "the hook may not act": after one
// successful invocation the provider was read, and the session's state, attempts
// and attempts budget are exactly what they were.
func TestHookOnlyObserves(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, hookSession, nil)
	publishBlocked(t, h, hookSession, "permission to edit .env")
	h.stdin = hookJSON(hookNeedsInputType, hookSession)

	ctx := context.Background()
	before, err := h.store.GetSession(ctx, hookSession)
	if err != nil {
		t.Fatalf("get session before: %v", err)
	}
	if code, _, _ := h.exec(t, "hook"); code != ExitOK {
		t.Fatalf("hook exit = %d, want %d", code, ExitOK)
	}

	for _, call := range h.provider.Calls() {
		if call.Name != "PublishedListing" {
			t.Errorf("the hook called %q, want observations only", call.Name)
		}
	}
	after, err := h.store.GetSession(ctx, hookSession)
	if err != nil {
		t.Fatalf("get session after: %v", err)
	}
	if after.State != before.State {
		t.Errorf("state = %s after the hook, want %s: a hook observes, it does not act", after.State, before.State)
	}
	if after.Enabled != before.Enabled || after.Recoverable != before.Recoverable {
		t.Errorf("flags changed: enabled %v/%v, recoverable %v/%v",
			before.Enabled, after.Enabled, before.Recoverable, after.Recoverable)
	}
	attempts, err := h.store.ListAttempts(ctx, hookSession, 0)
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 0 {
		t.Errorf("the hook wrote %d attempt(s), want none", len(attempts))
	}
}

// TestReadHookPayloadIsTolerantAndBounded covers R2 directly: the read is
// tolerant of what the CLI may add, and bounded in both size and time.
func TestReadHookPayloadIsTolerantAndBounded(t *testing.T) {
	t.Run("a documented payload with an unknown field", func(t *testing.T) {
		payload, ok := readHookPayload(strings.NewReader(
			`{"session_id":"`+hookSession+`","notification_type":"agent_needs_input",`+
				`"future_key":{"nested":true},"hook_event_name":"Notification"}`), time.Second)
		if !ok {
			t.Fatal("readHookPayload rejected a documented payload")
		}
		if payload.SessionID != hookSession {
			t.Errorf("session = %q, want %q", payload.SessionID, hookSession)
		}
		if payload.NotificationType != hookNeedsInputType {
			t.Errorf("type = %q, want %q", payload.NotificationType, hookNeedsInputType)
		}
	})

	t.Run("no reader", func(t *testing.T) {
		if _, ok := readHookPayload(nil, time.Second); ok {
			t.Error("a nil reader must not be read")
		}
	})

	t.Run("larger than the cap", func(t *testing.T) {
		// A JSON document that is valid only if the whole thing is read: the cap
		// must reject it before the parser ever sees a complete value.
		oversized := `{"session_id":"` + strings.Repeat("a", hookMaxPayloadBytes) + `"}`
		if _, ok := readHookPayload(strings.NewReader(oversized), time.Second); ok {
			t.Error("a payload above the cap must be ignored")
		}
	})

	t.Run("a writer that never closes the pipe", func(t *testing.T) {
		reader, writer := io.Pipe()
		defer func() { _ = writer.Close() }()
		started := time.Now()
		if _, ok := readHookPayload(reader, 20*time.Millisecond); ok {
			t.Error("an unterminated payload must be abandoned, not waited for")
		}
		if elapsed := time.Since(started); elapsed > 5*time.Second {
			t.Errorf("readHookPayload blocked for %s, want the deadline to end it", elapsed)
		}
	})
}

// TestHookIgnoresEveryTypeItDoesNotModel is R3 in its strong form: a type ccresu
// does not model is not merely silent, it is not even observed, because mapping
// an interactive notification would make ccresu a second notifier for events it
// does not own.
func TestHookIgnoresEveryTypeItDoesNotModel(t *testing.T) {
	types := []string{
		"permission_prompt",
		"idle_prompt",
		"auth_success",
		"auth_storage_failure",
		"elicitation_dialog",
		"elicitation_url_dialog",
		"elicitation_complete",
		"elicitation_response",
		"quota_auto_resume_fired",
		"quota_auto_resume_stale",
		"quota_auto_resume_disabled",
		"plugin_notification",
	}
	for _, notificationType := range types {
		t.Run(notificationType, func(t *testing.T) {
			h := newHarness(t)
			h.seedSession(t, hookSession, nil)
			publishBlocked(t, h, hookSession, "permission to edit .env")
			h.stdin = hookJSON(notificationType, hookSession)

			if code, stdout, stderr := h.exec(t, "hook"); code != ExitOK || stdout != "" || stderr != "" {
				t.Fatalf("hook = exit %d, stdout %q, stderr %q", code, stdout, stderr)
			}
			if requests := h.notifier.queued(); len(requests) != 0 {
				t.Errorf("type %q queued %d notification(s), want none: ccresu does not model it", notificationType, len(requests))
			}
			if calls := h.provider.Calls(); len(calls) != 0 {
				t.Errorf("type %q made %d provider call(s), want none: the type is decided first", notificationType, len(calls))
			}
		})
	}
}

// TestHookDegradesSilentlyWhenItCannotWork is R9: a hook is an optimisation over
// the poll, so a broken configuration or a database it cannot open leaves the
// poll as the only path and nothing else changed (R10).
func TestHookDegradesSilentlyWhenItCannotWork(t *testing.T) {
	cases := []struct {
		name string
		arm  func(*harness)
	}{
		{"the configuration cannot be read", func(h *harness) {
			h.loadErr = errors.New("config: permission denied")
		}},
		{"the database is locked", func(h *harness) {
			h.buildErr = errors.New("storage: database is locked")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.seedSession(t, hookSession, nil)
			publishBlocked(t, h, hookSession, "permission to edit .env")
			tc.arm(h)
			h.stdin = hookJSON(hookNeedsInputType, hookSession)

			if code, stdout, stderr := h.exec(t, "hook"); code != ExitOK || stdout != "" || stderr != "" {
				t.Fatalf("hook = exit %d, stdout %q, stderr %q: a hook that cannot work must still say nothing", code, stdout, stderr)
			}
			if requests := h.notifier.queued(); len(requests) != 0 {
				t.Errorf("queued %d notification(s), want none", len(requests))
			}
		})
	}
}
