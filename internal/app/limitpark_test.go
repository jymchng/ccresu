package app

import (
	"context"
	"testing"
	"time"
)

// TestARefusalWithNoReadableResetStillPlansAPoll covers the degraded half of the
// parker.
//
// `claude` can say the limit is in force without saying when it lifts — "You've
// hit your 5-hour limit" is a refusal the shipped detector recognises but cannot
// turn into an instant. The wait is still real, so the refusal must leave the
// same two rows behind as a banner that does name one: a usage_limits row
// carrying the CLI's words, and a schedule that polls. Anything else either
// resumes again immediately (the bug) or parks the session with no wake-up at
// all (a wait nothing can end).
func TestARefusalWithNoReadableResetStillPlansAPoll(t *testing.T) {
	h := newHarness(t)
	session := h.seedSession(t, canonicalSessionID, nil)

	const banner = "You've hit your 5-hour limit"
	park := limitParker(h.store, h.rt.Scheduler, h.rt.Detector, h.clock, nil)
	wait, err := park(context.Background(), session.ID, banner)
	if err != nil {
		t.Fatalf("park: %v", err)
	}

	if wait.Known {
		t.Errorf("wait known = true, want a poll: the banner named no instant")
	}
	if wait.At.IsZero() {
		t.Fatal("wait at = zero, want the first poll: an unknown reset still wakes up")
	}
	if want := h.clock.Now().Add(wait.Delay); !wait.At.Equal(want) {
		t.Errorf("wait at = %s, want %s: At is the instant ccresu next acts", wait.At, want)
	}
	if wait.Delay <= 0 {
		t.Errorf("wait delay = %s, want the first poll delay", wait.Delay)
	}

	limit, err := h.store.LatestUsageLimit(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("latest usage limit: %v (the refusal must be recorded as a limit)", err)
	}
	if limit.RawMessage != banner {
		t.Errorf("recorded banner = %q, want the CLI's own words %q", limit.RawMessage, banner)
	}
	if !limit.ResetAt.IsZero() {
		t.Errorf("recorded reset = %s, want none: the banner named no instant", limit.ResetAt)
	}

	schedules, err := h.rt.Scheduler.Restore(context.Background())
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("schedules = %d, want exactly one wait\n%+v", len(schedules), schedules)
	}
	got := schedules[0]
	if got.SessionID != session.ID {
		t.Errorf("schedule session = %q, want %q", got.SessionID, session.ID)
	}
	if !got.Unknown {
		t.Errorf("schedule = %+v, want an unknown reset that polls", got)
	}
	if !got.ResetAt.IsZero() {
		t.Errorf("schedule reset = %s, want none", got.ResetAt)
	}
	if got.NextPollAt.IsZero() {
		t.Fatal("next poll = zero, want the first poll instant")
	}
	if !got.NextPollAt.After(h.clock.Now()) {
		t.Errorf("next poll = %s, want after now %s: a due poll is a resume on the next pass",
			got.NextPollAt, h.clock.Now())
	}
	if want := h.clock.Now().Add(wait.Delay); !got.NextPollAt.Equal(want) {
		t.Errorf("next poll = %s, want %s: the wait the manager reports and the saved schedule must agree",
			got.NextPollAt, want)
	}
	if wait.Delay > time.Duration(24)*time.Hour {
		t.Errorf("wait delay = %s, want a poll interval rather than a day", wait.Delay)
	}
}
