package recovery

import (
	"time"

	"github.com/jymchng/ccresu/internal/storage"
)

// NextAttempt answers "when will ccresu next touch this session, and why".
//
// It is derived from persisted rows only — the session's own state and its
// stored retry instant, plus the session's Schedule when one exists — so what an
// operator is told matches what the daemon will actually do. Nothing here is
// recomputed from the retry policy: the backoff was computed once, when the
// attempt failed, and written down. Recomputing it would mean guessing at the
// jitter and reporting a time the daemon never agreed to.
type NextAttempt struct {
	// At is the instant ccresu will next act, in UTC. It is zero when nothing
	// is scheduled.
	At time.Time
	// Reason explains what will happen then, or why nothing is scheduled.
	Reason string
	// Scheduled reports whether At carries a real instant.
	Scheduled bool
}

// nextAttemptReasons names the non-scheduled outcomes, so `ccresu inspect` and
// the `ccresu sessions` table describe the same situation in the same words.
const (
	// ReasonDisabled is reported for a session the operator turned off, and for one
	// that stopped at a spend cap: both are DISABLED, and both need the same act —
	// `ccresu enable` — before anything else happens.
	ReasonDisabled = "session is disabled; run `ccresu enable` to resume it"
	// ReasonNoLimit is reported for a session with no usage limit detected yet.
	ReasonNoLimit = "no usage limit detected yet"
	// ReasonRediscovered is reported for a session returned to discovery, which
	// is where a failed attempt and a spent attempt budget both leave it. The
	// daemon re-reads the transcript and schedules the next reset on its own, so
	// the reason promises work rather than asking the operator for a command.
	ReasonRediscovered = "returned to discovery; ccresu will re-read the transcript and schedule the next reset"
	// ReasonUnrecoverable is reported when the session cannot be resumed at all.
	ReasonUnrecoverable = "session cannot be recovered"
	// ReasonResumed is reported for a session ccresu has resumed and then
	// settled: it is alive again, and nothing further is scheduled until its next
	// usage limit is seen.
	ReasonResumed = "resumed; nothing scheduled until the next usage limit"
	// ReasonCompleted is reported for a session that finished normally.
	ReasonCompleted = "session completed"
	// ReasonInFlight is reported while a recovery is actually running.
	ReasonInFlight = "recovery in progress"
	// ReasonNoSchedule is reported when a session is parked but neither a schedule
	// nor a retry instant is stored, which means the store lost it.
	ReasonNoSchedule = "parked but neither a reset time nor a retry instant is stored"
	// ReasonRetrying is reported for a session parked after a failed attempt: the
	// instant is the backoff ccresu computed, not a usage-limit reset.
	ReasonRetrying = "retrying after a failed attempt"
)

// NextAttemptFor resolves the next attempt for a session.
//
// sched is the session's Schedule row, or nil when it has none. now is only used
// to describe a recovery that is happening as this is read.
func NextAttemptFor(session storage.Session, sched *storage.Schedule, now time.Time) NextAttempt {
	// A disabled session is never acted on, whatever state it is in: reporting a
	// pending wait for it would promise work the daemon will not do.
	if !session.Enabled {
		return NextAttempt{Reason: ReasonDisabled}
	}

	switch session.State {
	case storage.StateWaitingForReset:
		// A park is woken either by the schedule that holds the reset instant or,
		// when the session was parked by a failure rather than a banner, by the
		// retry instant the backoff computed. Preferring the schedule keeps a real
		// reset authoritative when both exist.
		if sched != nil {
			if at := sched.DueAt(); !at.IsZero() {
				return NextAttempt{At: at.UTC(), Reason: waitingReason(sched), Scheduled: true}
			}
		}
		if session.RetryAt != nil {
			return NextAttempt{At: session.RetryAt.UTC(), Reason: ReasonRetrying, Scheduled: true}
		}
		return NextAttempt{Reason: ReasonNoSchedule}

	case storage.StateResuming:
		// The instant is now; the wait is over and the work has started.
		return NextAttempt{At: now.UTC(), Reason: ReasonInFlight, Scheduled: true}

	case storage.StateDiscovered:
		if session.RetryAt != nil {
			return NextAttempt{At: session.RetryAt.UTC(), Reason: ReasonRetrying, Scheduled: true}
		}
		return NextAttempt{Reason: ReasonRediscovered}
	case storage.StateResumed:
		return NextAttempt{Reason: ReasonResumed}
	case storage.StateCompleted:
		return NextAttempt{Reason: ReasonCompleted}
	case storage.StateDisabled:
		return NextAttempt{Reason: ReasonDisabled}
	case storage.StateUnrecoverable:
		return NextAttempt{Reason: ReasonUnrecoverable}

	default:
		// A state this build does not know: nothing is scheduled, and saying so is
		// better than inventing an instant.
		return NextAttempt{Reason: ReasonNoLimit}
	}
}

// waitingReason distinguishes a wait with a known reset time from one that has to
// poll, because the second is why an operator sees a time they did not read in
// the original banner.
func waitingReason(sched *storage.Schedule) string {
	if sched.Unknown {
		return "polling until the usage limit clears"
	}
	return "waiting for the usage limit to reset"
}

// HumanizeUntil renders how far away at is, in the compact form the sessions
// table uses: "in 2h14m", "in 45s", "due now". A past instant reads "due now"
// rather than a negative countdown, because that is what it means: the daemon
// will act the moment it next looks.
func HumanizeUntil(at, now time.Time) string {
	if at.IsZero() {
		return "-"
	}
	remaining := at.Sub(now)
	if remaining <= 0 {
		return "due now"
	}
	// Round to the nearest second first so a 2h14m59.6s wait does not print as
	// 2h14m59s when a reader would call it 2h15m.
	remaining = remaining.Round(time.Second)
	if remaining < time.Minute {
		return "in " + trimDuration(remaining)
	}
	// Below an hour, minutes and seconds; above, hours and minutes. Anything
	// coarser hides the difference between "soon" and "in a while", and anything
	// finer is noise at this distance.
	if remaining < time.Hour {
		return "in " + trimDuration(remaining.Truncate(time.Second))
	}
	hours := int64(remaining / time.Hour)
	minutes := int64((remaining % time.Hour) / time.Minute)
	if minutes == 0 {
		return "in " + itoa(hours) + "h"
	}
	return "in " + itoa(hours) + "h" + itoa(minutes) + "m"
}

// trimDuration drops the "0s"/"0m" tail so 45s and 5m0s both read naturally.
func trimDuration(d time.Duration) string {
	if d < time.Minute {
		return itoa(int64(d/time.Second)) + "s"
	}
	minutes := int64(d / time.Minute)
	seconds := int64((d % time.Minute) / time.Second)
	if seconds == 0 {
		return itoa(minutes) + "m"
	}
	return itoa(minutes) + "m" + itoa(seconds) + "s"
}

// itoa is a tiny non-negative integer formatter, kept local so this file needs no
// dependency and cannot format a negative wait.
func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
