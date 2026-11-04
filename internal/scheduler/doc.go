// Package scheduler turns a usage-limit reset phrase into the moment ccresu may
// resume a session, and waits for it.
//
// # Why this is its own package
//
// The detector deliberately does not resolve "12pm" into an instant. It reports
// a phrase — "12pm", "tomorrow at 9am", "in 4 hours", "2026-06-15 09:00" — and
// this package decides what that means for a particular reader, calendar and
// timezone. Keeping the two apart means the same phrase can be resolved again
// later (after a restart, or when the user's configured zone changes) without
// re-reading a transcript.
//
// # The three kinds of answer
//
// A phrase resolves in one of three ways:
//
//   - Epoch: the message carried a Unix timestamp. Already absolute.
//   - Relative: a duration such as "in 4 hours". Measured from the detection
//     time with plain duration arithmetic, because a duration has no wall clock
//     and therefore no daylight-saving problem.
//   - Absolute: a civil date and time ("12pm", "Monday 9am", "June 15"),
//     resolved against a timezone.
//
// Only the last kind involves a calendar, and only it can land on a
// daylight-saving boundary.
//
// # Timezones
//
// A phrase that names a zone is resolved in that zone. Everything stored or
// compared is UTC; the original zone name is preserved alongside so a user
// still sees the time their message was written in.
//
// Resolution never depends on the host's TZ setting: the IANA database is
// embedded in the binary, a phrase without a zone uses the configured default
// (app.timezone, otherwise UTC) rather than the ambient zone, and the tests
// prove identical results under several host zones.
//
// # Abbreviations are offsets, not rules
//
// "EST" means UTC-5 even in July, when New York actually observes EDT. Mapping
// the abbreviation onto America/New_York would apply a summer-time shift the
// message never mentioned, so each known abbreviation becomes a fixed offset
// zone that keeps its spelling.
//
// # Daylight saving
//
// A reset phrase names a wall-clock time, and twice a year a wall clock is
// either repeated or missing. The safe direction is "late":
//
//   - Repeated (clocks went back): the later of the two instants is chosen, so
//     a resume cannot start during the hour the limit may still be active.
//   - Missing (clocks jumped forward): the instant after the gap is chosen, for
//     the same reason. Waiting is cheap; a wasted resume attempt costs a retry
//     and a notification.
//
// Both cases are reported in Resolution.DSTCase and explained in Notes. Setting
// Options.PreferEarlierOnAmbiguous inverts the first rule for callers that would
// rather be early.
//
// # Times that have already passed
//
// A clock time in the past means the *next* one: a banner read at 14:00 that
// says "resets 12pm" means tomorrow at noon. The roll happens on the civil date
// and the wall clock is resolved again, never by adding 24 hours to an instant,
// because on a daylight-saving day those differ by an hour.
//
// A calendar date behaves differently on purpose: "June 15" seen on 15 June is
// due now, not next year. Rolling a year forward would strand a session for
// twelve months, which is far worse than checking a little early.
//
// # When the reset time is unknown
//
// Not every banner states a reset time, and a phrase may name a zone this
// package cannot parse. That is not a reason to give up: the schedule is marked
// Unknown and polled with exponential backoff — five minutes initially, doubling
// to a configurable maximum (thirty minutes by default). The attempt count is
// persisted, so a restart continues at the same interval instead of hammering
// the CLI again from five minutes.
//
// # Persistence
//
// Schedules are stored, not merely held in memory: a daemon restart or a reboot
// must resume the wait rather than lose the session. Store is the interface;
// MemoryStore serves tests and the SQLite repository is adapted by
// NewStorageStore. The stored row keeps the original phrase and the resolved
// instant, so the reason for a wait can still be explained later.
//
// # Injection
//
// The clock is injectable (internal/clock), so waiting and backoff are tested
// deterministically by advancing a fake clock rather than sleeping.
//
// # Example
//
//	event := detector.Analyze("You've hit your 5-hour limit \u00b7 resets 12pm (Europe/Berlin)")
//	sched, _ := scheduler.New(scheduler.Config{Clock: clock.System()})
//	planned, resolution, err := sched.PlanEvent(sessionID, event, sched.Now())
//	if err != nil {
//		return err
//	}
//	// planned.ResetAt is 10:00Z, from 12:00 Berlin in summer.
//	if _, err := sched.Wait(ctx, planned); err != nil {
//		return err
//	}
//	// ... hand over to the recovery state machine.
package scheduler
