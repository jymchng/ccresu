package recovery

import (
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/storage"
)

// fixedNow is the reference instant every case reads as "now". The reset and
// retry instants below are expressed relative to it, so a countdown assertion
// cannot drift with the wall clock.
var fixedNow = time.Date(2026, 11, 4, 19, 30, 0, 0, time.UTC)

// at builds a pointer to a fixed instant, the shape storage.Session.RetryAt and
// the schedule helpers use.
func at(t time.Time) *time.Time { return &t }

func TestNextAttemptForEveryState(t *testing.T) {
	resetAt := fixedNow.Add(2*time.Hour + 14*time.Minute)
	pollAt := fixedNow.Add(5 * time.Minute)
	retryAt := fixedNow.Add(58 * time.Second)

	cases := []struct {
		name      string
		session   storage.Session
		schedule  *storage.Schedule
		wantAt    time.Time
		wantSched bool
		wantWhy   string
	}{
		{
			// A known reset time is taken from the persisted Schedule row, so the
			// printed instant is the one the daemon will wake at.
			name:      "waiting for a known reset reports the stored reset time",
			session:   storage.Session{State: storage.StateWaitingForReset, Enabled: true},
			schedule:  &storage.Schedule{SessionID: "s", ResetAt: resetAt},
			wantAt:    resetAt,
			wantSched: true,
			wantWhy:   "waiting for the usage limit to reset",
		},
		{
			// An unknown reset time means the session is polled, and the next poll
			// time is what the row says — not the reset time, which does not exist.
			name:      "waiting on an unknown reset reports the next poll",
			session:   storage.Session{State: storage.StateWaitingForReset, Enabled: true},
			schedule:  &storage.Schedule{SessionID: "s", Unknown: true, NextPollAt: pollAt},
			wantAt:    pollAt,
			wantSched: true,
			wantWhy:   "polling until the usage limit clears",
		},
		{
			// A missing Schedule row is a store defect, and it is reported as one
			// rather than as a time nobody agreed to.
			name:      "waiting without a stored schedule says so",
			session:   storage.Session{State: storage.StateWaitingForReset, Enabled: true},
			wantWhy:   ReasonNoSchedule,
			wantAt:    time.Time{},
			wantSched: false,
		},
		{
			name:     "waiting with an unschedulable schedule says so",
			session:  storage.Session{State: storage.StateWaitingForReset, Enabled: true},
			schedule: &storage.Schedule{SessionID: "s"},
			wantWhy:  ReasonNoSchedule,
		},
		{
			// RETRY_WAIT is the state with no Schedule row: the instant is the
			// backoff computed when the attempt failed and persisted on the session.
			name:      "retry wait reports the persisted retry instant",
			session:   storage.Session{State: storage.StateRetryWait, Enabled: true, RetryAt: at(retryAt)},
			wantAt:    retryAt,
			wantSched: true,
			wantWhy:   "retrying after a failed attempt",
		},
		{
			name:    "retry wait without a stored instant says so",
			session: storage.Session{State: storage.StateRetryWait, Enabled: true},
			wantWhy: ReasonNoRetryInstant,
		},
		{
			// The wait is over: the work is happening as this is read, so the
			// answer is "now" rather than a countdown.
			name:      "a recovery in flight reports now",
			session:   storage.Session{State: storage.StateResuming, Enabled: true},
			wantAt:    fixedNow,
			wantSched: true,
			wantWhy:   ReasonInFlight,
		},
		{
			name:      "reset time reached reports now",
			session:   storage.Session{State: storage.StateResetTimeReached, Enabled: true},
			wantAt:    fixedNow,
			wantSched: true,
			wantWhy:   ReasonInFlight,
		},
		{
			name:    "permanently failed reports the spent budget",
			session: storage.Session{State: storage.StateDiscovered, Enabled: true},
			wantWhy: ReasonRediscovered,
		},
		{
			name:    "unrecoverable reports that it cannot be recovered",
			session: storage.Session{State: storage.StateUnrecoverable, Enabled: true},
			wantWhy: ReasonUnrecoverable,
		},
		{
			// A resumed session is between cycles, not finished: the next thing
			// ccresu does is read the continuation transcript again, so the
			// instant is now rather than a time nothing computed.
			name:      "resumed reports that the next cycle reads the continuation",
			session:   storage.Session{State: storage.StateResumed, Enabled: true},
			wantAt:    fixedNow,
			wantSched: true,
			wantWhy:   ReasonResumed,
		},
		{
			// The rediscovery stage defers a resumed session whose continuation is
			// still producing output. That instant is the truthful answer: resuming
			// earlier would duplicate work that is already running.
			name: "a deferred resumed session reports the instant it is looked at again",
			session: storage.Session{
				State:   storage.StateResumed,
				Enabled: true,
				RetryAt: timePtr(fixedNow.Add(30 * time.Minute)),
			},
			wantAt:    fixedNow.Add(30 * time.Minute),
			wantSched: true,
			wantWhy:   ReasonStillWorking,
		},
		{
			name:    "completed reports that the session finished",
			session: storage.Session{State: storage.StateCompleted, Enabled: true},
			wantWhy: ReasonCompleted,
		},
		{
			name:    "user terminated reports that the operator ended it",
			session: storage.Session{State: storage.StateUserTerminated, Enabled: true},
			wantWhy: ReasonTerminated,
		},
		{
			name:    "a disabled state reports that it is disabled",
			session: storage.Session{State: storage.StateDisabled},
			wantWhy: ReasonDisabled,
		},
		{
			// A discovered session has no instant of its own: the next discovery
			// pass is what finds one, so the reason promises that rather than a
			// time nothing has computed.
			name:    "a discovered session reports rediscovery",
			session: storage.Session{State: storage.StateDiscovered, Enabled: true},
			wantWhy: ReasonRediscovered,
		},
		{
			name:    "usage limit detected but unscheduled reports none",
			session: storage.Session{State: storage.StateUsageLimitDetected, Enabled: true},
			wantWhy: ReasonNoLimit,
		},
		{
			// Disabled wins over every state: a disabled session is never acted
			// on, so reporting a pending wait for it would promise work the daemon
			// will not do — even when the stored instant is still there.
			name:      "disabled beats a stored retry instant",
			session:   storage.Session{State: storage.StateRetryWait, Enabled: false, RetryAt: at(retryAt)},
			wantSched: false,
			wantWhy:   ReasonDisabled,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NextAttemptFor(tc.session, tc.schedule, fixedNow)
			if got.Scheduled != tc.wantSched {
				t.Fatalf("scheduled = %v, want %v (reason %q)", got.Scheduled, tc.wantSched, got.Reason)
			}
			if got.Reason != tc.wantWhy {
				t.Fatalf("reason = %q, want %q", got.Reason, tc.wantWhy)
			}
			if tc.wantSched && !got.At.Equal(tc.wantAt) {
				t.Fatalf("at = %s, want %s", got.At, tc.wantAt)
			}
			if !tc.wantSched && !got.At.IsZero() {
				t.Fatalf("at = %s, want the zero time when nothing is scheduled", got.At)
			}
		})
	}
}

// TestNextAttemptReportsUTC pins the zone: an operator reading a UTC instant and
// one reading the JSON field must see the same moment, whatever zone the stored
// time carried.
func TestNextAttemptReportsUTC(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skipf("no tzdata for Europe/Berlin: %v", err)
	}
	stored := time.Date(2026, 11, 4, 20, 29, 0, 0, berlin)

	got := NextAttemptFor(
		storage.Session{State: storage.StateWaitingForReset, Enabled: true},
		&storage.Schedule{SessionID: "s", ResetAt: stored},
		fixedNow,
	)
	if got.At.Location() != time.UTC {
		t.Fatalf("zone = %s, want UTC", got.At.Location())
	}
	if !got.At.Equal(stored) {
		t.Fatalf("at = %s, want the same instant as %s", got.At, stored)
	}
}

func TestHumanizeUntil(t *testing.T) {
	cases := []struct {
		name string
		at   time.Time
		want string
	}{
		{"an unscheduled instant is a dash", time.Time{}, "-"},
		{"a past instant is due now", fixedNow.Add(-time.Minute), "due now"},
		{"the current instant is due now", fixedNow, "due now"},
		{"seconds only under a minute", fixedNow.Add(45 * time.Second), "in 45s"},
		{"sub-second waits round down to whole seconds", fixedNow.Add(1500 * time.Millisecond), "in 2s"},
		{"minutes with a seconds tail", fixedNow.Add(2*time.Minute + 30*time.Second), "in 2m30s"},
		{"a whole number of minutes has no tail", fixedNow.Add(5 * time.Minute), "in 5m"},
		{"hours with a minutes tail", fixedNow.Add(2*time.Hour + 14*time.Minute), "in 2h14m"},
		{"a whole number of hours has no tail", fixedNow.Add(3 * time.Hour), "in 3h"},
		{"a rounded-up minute is not reported as 59s", fixedNow.Add(59*time.Second + 700*time.Millisecond), "in 1m"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HumanizeUntil(tc.at, fixedNow); got != tc.want {
				t.Fatalf("HumanizeUntil(%s) = %q, want %q", tc.at, got, tc.want)
			}
		})
	}
}

// timePtr returns a pointer to t, for the retry instants these cases pin.
func timePtr(t time.Time) *time.Time { return &t }
