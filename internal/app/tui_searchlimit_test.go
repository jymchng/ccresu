package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/storage"
)

// gatedTranscripts holds every tail read until its gate is closed, so a test can
// look at the screen while a search is still in flight.
//
// Without the gate the search finishes in microseconds and the busy line is only
// ever a frame the test may or may not catch: "the keyboard never blocks" would
// then be asserted against a race rather than against the browser.
type gatedTranscripts struct {
	inner Transcripts
	gate  chan struct{}
}

// TailEntries implements Transcripts.
func (g *gatedTranscripts) TailEntries(path string, n int) ([]claude.Entry, error) {
	<-g.gate
	return g.inner.TailEntries(path, n)
}

// TestSessionsBrowserSearchesTheSelectedSessionOnL drives a real 'L' keystroke
// through the browser's list handler.
//
// The claim is threefold: the keystroke starts the search on the row the cursor is
// on, the busy line says "searching <id>…" while the read is in flight (so the
// keyboard is not blocked by it), and the status line distinguishes a search that
// found a banner from one that did not. The manager is a concrete type, so the
// proof that SearchLimit ran is its effect: a banner parks the session in
// WAITING_FOR_RESET with the two transitions, the usage-limit row and the wait that
// a pass would have written.
func TestSessionsBrowserSearchesTheSelectedSessionOnL(t *testing.T) {
	ctx := context.Background()

	t.Run("a banner is found and the session is parked", func(t *testing.T) {
		h := newHarness(t)
		session := h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
			s.State = storage.StateResumed
			s.TranscriptLines = 2
		})
		h.seedTranscript(session.TranscriptPath,
			claude.Entry{Text: "hello"}, claude.Entry{Text: limitBanner})
		gate := make(chan struct{})
		h.rt.Transcripts = &gatedTranscripts{inner: h.transcripts, gate: gate}

		paced := h.startBrowser(t, "sessions", "--all")
		paced.waitForScreen(t, "enter detail")
		paced.send(t, "L")
		// The read is still blocked, so this frame is stable: the busy line has to
		// be painted before the action finishes, or an operator sees a frozen
		// screen with no idea what it is waiting for.
		paced.waitForScreen(t, "busy: searching "+shortID(canonicalSessionID)+"\u2026")

		close(gate)
		paced.waitForScreen(t, "usage limit found, state WAITING_FOR_RESET")
		if code := paced.quit(t); code != ExitOK {
			t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
		}

		stored, err := h.store.GetSession(ctx, canonicalSessionID)
		if err != nil {
			t.Fatalf("read session: %v", err)
		}
		if stored.State != storage.StateWaitingForReset {
			t.Errorf("state = %s, want %s", stored.State, storage.StateWaitingForReset)
		}
		limit, err := h.store.LatestUsageLimit(ctx, canonicalSessionID)
		if err != nil {
			t.Fatalf("latest usage limit: %v (the search must record the banner it found)", err)
		}
		if !strings.Contains(limit.RawMessage, "5-hour limit") {
			t.Errorf("recorded banner = %q, want the CLI's own words", limit.RawMessage)
		}
		schedules, err := h.rt.Scheduler.Restore(ctx)
		if err != nil {
			t.Fatalf("restore schedules: %v", err)
		}
		if len(schedules) != 1 || schedules[0].SessionID != canonicalSessionID {
			t.Errorf("schedules = %+v, want exactly one wait for the session", schedules)
		}

		// The audit trail is what proves the manager walked the state machine
		// rather than the browser writing WAITING_FOR_RESET itself.
		transitions, err := h.store.ListTransitions(ctx, canonicalSessionID, 0)
		if err != nil {
			t.Fatalf("list transitions: %v", err)
		}
		if len(transitions) != 2 {
			t.Fatalf("transitions = %d, want 2 (DISCOVERED then WAITING_FOR_RESET): %+v",
				len(transitions), transitions)
		}
		// A RESUMED session goes back through DISCOVERED rather than straight to
		// the park, because the machine has no RESUMED -> WAITING_FOR_RESET edge:
		// the operator's search is the re-observation that earns the park.
		if transitions[0].ToState != storage.StateDiscovered {
			t.Errorf("first transition goes to %s, want %s: the re-observation must be recorded",
				transitions[0].ToState, storage.StateDiscovered)
		}
		if transitions[1].ToState != storage.StateWaitingForReset {
			t.Errorf("second transition goes to %s, want %s",
				transitions[1].ToState, storage.StateWaitingForReset)
		}
	})

	t.Run("a transcript with no banner leaves the session alone", func(t *testing.T) {
		h := newHarness(t)
		session := h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
			s.State = storage.StateResumed
			s.TranscriptLines = 1
		})
		h.seedTranscript(session.TranscriptPath, claude.Entry{Text: "just some output"})
		gate := make(chan struct{})
		h.rt.Transcripts = &gatedTranscripts{inner: h.transcripts, gate: gate}

		paced := h.startBrowser(t, "sessions", "--all")
		paced.waitForScreen(t, "enter detail")
		paced.send(t, "L")
		paced.waitForScreen(t, "busy: searching "+shortID(canonicalSessionID)+"\u2026")
		close(gate)

		// "not found" is a different answer from "found", and the status line has
		// to say which one the operator got.
		paced.waitForScreen(t, "no usage limit found, state unchanged (RESUMED)")
		if code := paced.quit(t); code != ExitOK {
			t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
		}

		stored, err := h.store.GetSession(ctx, canonicalSessionID)
		if err != nil {
			t.Fatalf("read session: %v", err)
		}
		if stored.State != storage.StateResumed {
			t.Errorf("state = %s, want it unchanged at %s", stored.State, storage.StateResumed)
		}
		if _, err := h.store.LatestUsageLimit(ctx, canonicalSessionID); err == nil {
			t.Errorf("a usage limit was recorded for a transcript with no banner")
		}
		if transitions, err := h.store.ListTransitions(ctx, canonicalSessionID, 0); err != nil {
			t.Fatalf("list transitions: %v", err)
		} else if len(transitions) != 0 {
			t.Errorf("transitions = %+v, want none: a search that finds nothing moves nothing", transitions)
		}
	})

	t.Run("the search follows the cursor, not the first row", func(t *testing.T) {
		const (
			firstID  = "aaaaaaaa-1111-4111-8111-111111111111"
			secondID = "bbbbbbbb-2222-4222-8222-222222222222"
		)
		h := newHarness(t)
		for _, id := range []string{firstID, secondID} {
			session := h.seedSession(t, id, func(s *storage.Session) {
				s.State = storage.StateResumed
				s.TranscriptLines = 1
			})
			h.seedTranscript(session.TranscriptPath, claude.Entry{Text: limitBanner})
		}
		gate := make(chan struct{})
		h.rt.Transcripts = &gatedTranscripts{inner: h.transcripts, gate: gate}

		paced := h.startBrowser(t, "sessions", "--all", "--limit", "0")
		paced.waitForScreen(t, "enter detail")

		// The drawn order is read off the screen rather than assumed, so a change
		// to the ordering cannot quietly make this test vacuous.
		order := listedIDs(lastScreen(paced.out.String()))
		if len(order) != 2 {
			t.Fatalf("listed %d rows, want 2:\n%s", len(order), lastScreen(paced.out.String()))
		}
		if order[0] != shortID(firstID) || order[1] != shortID(secondID) {
			t.Fatalf("drawn order = %v, want [%s %s]", order, shortID(firstID), shortID(secondID))
		}

		paced.send(t, "j")
		paced.waitForScreen(t, "> "+shortID(secondID))
		paced.send(t, "L")
		// The busy line names the row the cursor is on: that is the whole claim
		// that the keystroke acts on the selection.
		paced.waitForScreen(t, "busy: searching "+shortID(secondID)+"\u2026")
		close(gate)
		paced.waitForScreen(t, "usage limit found, state WAITING_FOR_RESET")
		if code := paced.quit(t); code != ExitOK {
			t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
		}

		searched, err := h.store.GetSession(ctx, secondID)
		if err != nil {
			t.Fatalf("read searched session: %v", err)
		}
		if searched.State != storage.StateWaitingForReset {
			t.Errorf("the cursor row (%s) is %s, want %s", shortID(secondID), searched.State, storage.StateWaitingForReset)
		}
		untouched, err := h.store.GetSession(ctx, firstID)
		if err != nil {
			t.Fatalf("read first session: %v", err)
		}
		if untouched.State != storage.StateResumed {
			t.Errorf("the row above the cursor (%s) is %s, want it untouched at %s",
				shortID(firstID), untouched.State, storage.StateResumed)
		}
		if _, err := h.store.LatestUsageLimit(ctx, firstID); err == nil {
			t.Errorf("the row above the cursor was searched too")
		}
	})

	t.Run("a parked session is re-parsed instead of refused", func(t *testing.T) {
		// The regression: 'L' on a WAITING_FOR_RESET row answered
		// "session <id> is WAITING_FOR_RESET, and only an idle session ... can be
		// searched", which refused the one state whose stored reset an operator
		// most needs re-parsed.
		h := newHarness(t)
		session := h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
			s.State = storage.StateWaitingForReset
			s.TranscriptLines = 2
		})
		h.seedTranscript(session.TranscriptPath,
			claude.Entry{Text: "hello"}, claude.Entry{Text: limitBanner})
		stale := h.clock.Now().UTC().Add(19*time.Hour + 27*time.Minute)
		if _, err := h.store.SaveSchedule(ctx, storage.Schedule{
			SessionID: canonicalSessionID, ResetAt: stale, ResetRaw: "the stale phrase",
		}); err != nil {
			t.Fatalf("save schedule: %v", err)
		}

		paced := h.startBrowser(t, "sessions", "--all")
		paced.waitForScreen(t, "enter detail")
		paced.send(t, "L")
		paced.waitForScreen(t, "the reset time was re-parsed")
		if code := paced.quit(t); code != ExitOK {
			t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
		}

		stored, err := h.store.GetSession(ctx, canonicalSessionID)
		if err != nil {
			t.Fatalf("read session: %v", err)
		}
		if stored.State != storage.StateWaitingForReset {
			t.Errorf("state = %s, want %s", stored.State, storage.StateWaitingForReset)
		}
		sched, err := h.store.GetSchedule(ctx, canonicalSessionID)
		if err != nil {
			t.Fatalf("read schedule: %v", err)
		}
		if sched.ResetRaw == "the stale phrase" {
			t.Errorf("schedule = %+v, want the stale wait replaced by the one the banner planned", sched)
		}
		if sched.UsageLimitID == 0 {
			t.Error("schedule is not linked to the usage limit the search recorded")
		}

		// The re-parse is an event even though the state cannot move, so it is
		// recorded as the machine's self-edge.
		transitions, err := h.store.ListTransitions(ctx, canonicalSessionID, 0)
		if err != nil {
			t.Fatalf("list transitions: %v", err)
		}
		sawReparse := false
		for _, transition := range transitions {
			if transition.FromState == storage.StateWaitingForReset &&
				transition.ToState == storage.StateWaitingForReset {
				sawReparse = true
			}
		}
		if !sawReparse {
			t.Errorf("transitions = %+v, want a WAITING_FOR_RESET -> WAITING_FOR_RESET row", transitions)
		}
	})
}

// TestTheLKeyIsAdvertisedOnEverySurface keeps the three places an operator can
// learn about the key in step: the key map the help screen renders, the list
// footer, and therefore the documentation that quotes the key map.
//
// A key that works but is not advertised is a key nobody presses, and a footer that
// names a key the handler does not have is worse: it is an instruction that fails.
func TestTheLKeyIsAdvertisedOnEverySurface(t *testing.T) {
	var binding keyBinding
	found := false
	for _, candidate := range tuiKeyMap() {
		if candidate.keys == "L" {
			binding, found = candidate, true
			break
		}
	}
	if !found {
		t.Fatalf("tuiKeyMap has no 'L' binding: %+v", tuiKeyMap())
	}
	if !strings.Contains(binding.meaning, "usage limit") {
		t.Errorf("the 'L' binding does not say what it searches for: %q", binding.meaning)
	}
	if !strings.Contains(binding.meaning, "search-limit") {
		t.Errorf("the 'L' binding does not name the command it is the call of: %q", binding.meaning)
	}

	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)

	// The list footer.
	if code := h.runWithKeys("q", "sessions"); code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	footer := lastScreen(h.stdout.String())
	if !strings.Contains(footer, "L search limit") {
		t.Errorf("the list footer does not advertise the key:\n%s", footer)
	}

	// The help screen, which renders tuiKeyMap row by row.
	if code := h.runWithKeys("?xq", "sessions"); code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	help := lastScreenBetween(h.stdout.String(), "keys")
	if help == "" {
		t.Fatalf("no help screen was drawn:\n%s", h.stdout.String())
	}
	if !strings.Contains(help, binding.keys) {
		t.Errorf("the help screen does not list the key:\n%s", help)
	}
	if !strings.Contains(help, binding.meaning) {
		t.Errorf("the help screen does not carry the binding's description:\n%s", help)
	}
}
