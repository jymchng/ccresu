package scheduler_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/clock"
	"github.com/jymchng/ccresu/internal/detector"
	"github.com/jymchng/ccresu/internal/scheduler"
)

// The canonical banner from the README and the end-to-end scenario.
const canonicalBanner = "You've hit your 5-hour limit \u00b7 resets 12pm (Europe/Berlin)"

// TestAcceptanceCanonicalResetResolvesInBerlin is the goal's stated acceptance
// criterion: `resets 12pm (Europe/Berlin)` must mean 12:00 Berlin.
// The instant is 10:00Z in June, when Berlin is on CEST (UTC+2).
func TestAcceptanceCanonicalResetResolvesInBerlin(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	event := detector.Analyze(canonicalBanner)
	if !event.Detected {
		t.Fatalf("the canonical banner was not detected: %v", event.Notes)
	}
	if event.Reset.Expression != "12pm" || event.Reset.Timezone != "Europe/Berlin" {
		t.Fatalf("the detector decomposed the phrase as %+v", event.Reset)
	}

	res := scheduler.Resolve(event.Reset, now, scheduler.DefaultOptions())
	if !res.Known {
		t.Fatalf("the reset phrase did not resolve: %v", res.Notes)
	}
	if want := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC); !res.At.Equal(want) {
		t.Fatalf("resolved to %s, want %s", res.At.Format(time.RFC3339), want.Format(time.RFC3339))
	}
	if res.LocationName != "Europe/Berlin" {
		t.Fatalf("the original timezone was not preserved: %q", res.LocationName)
	}
	if res.Local != "2026-06-15 12:00" {
		t.Fatalf("local wall clock = %q, want 2026-06-15 12:00", res.Local)
	}
	if res.At.Location() != time.UTC {
		t.Fatalf("the instant is not stored in UTC: %v", res.At.Location())
	}
}

// TestAcceptanceResolutionIgnoresHostTimezone runs the same resolution in child
// processes with different TZ settings. Go caches time.Local, so changing it
// inside the test process would not prove anything.
func TestAcceptanceResolutionIgnoresHostTimezone(t *testing.T) {
	t.Parallel()

	hostZones := []string{"UTC", "America/New_York", "Asia/Kolkata", "Pacific/Auckland", "Europe/London"}

	var want string
	for _, zone := range hostZones {
		cmd := exec.Command(os.Args[0], "-test.run=TestAcceptanceHostTimezoneHelper", "-test.v=false")
		cmd.Env = append(os.Environ(), "TZ="+zone, "CCRESU_TZ_HELPER=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("child process with TZ=%s failed: %v\n%s", zone, err, out)
		}
		got := strings.TrimSpace(string(out))
		// The child also prints PASS, so the result line is extracted.
		const prefix = "RESULT:"
		var resolved string
		for _, line := range strings.Split(got, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), prefix) {
				resolved = strings.TrimPrefix(strings.TrimSpace(line), prefix)
			}
		}
		// Both cases must hold in every host zone:
		//   explicit: 12pm (Europe/Berlin) at 09:00Z is 10:00Z (CEST, UTC+2)
		//   implicit: 12pm with no zone is 12:00Z, because the default is UTC
		const wantResult = "explicit=2026-06-15T10:00:00Z implicit=2026-06-15T12:00:00Z"
		if resolved != wantResult {
			t.Fatalf("with TZ=%s the child resolved to %q, want %q\n%s", zone, resolved, wantResult, got)
		}
		if want == "" {
			want = resolved
			continue
		}
		if resolved != want {
			t.Fatalf("resolution differs between host zones: %q vs %q", want, resolved)
		}
	}
}

// TestAcceptanceHostTimezoneHelper is not a real test: it is the body the parent
// test runs in a child process with a specific TZ.
func TestAcceptanceHostTimezoneHelper(t *testing.T) {
	if os.Getenv("CCRESU_TZ_HELPER") != "1" {
		t.Skip("helper for TestAcceptanceResolutionIgnoresHostTimezone")
	}
	now := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)

	// A phrase that names its own zone.
	explicit := detector.Analyze(canonicalBanner)
	explicitAt := scheduler.Resolve(explicit.Reset, now, scheduler.DefaultOptions()).At

	// A phrase that names none, so the configured default applies.
	implicit := detector.Analyze("You've hit your 5-hour limit \u00b7 resets 12pm")
	implicitAt := scheduler.Resolve(implicit.Reset, now, scheduler.DefaultOptions()).At

	print("RESULT:explicit=" + explicitAt.UTC().Format(time.RFC3339) +
		" implicit=" + implicitAt.UTC().Format(time.RFC3339) + "\n")
}

// TestAcceptanceNoResetTimePollsInsteadOfGivingUp covers the paused-goal banner,
// which carries no reset time at all.
func TestAcceptanceNoResetTimePollsInsteadOfGivingUp(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	event := detector.Analyze("Goal paused \u00b7 usage limit reached \u00b7 send a message after it resets")
	if !event.Detected {
		t.Fatalf("the paused-goal banner was not detected: %v", event.Notes)
	}
	if event.Reset.Present {
		t.Fatalf("the banner unexpectedly carried a reset phrase: %+v", event.Reset)
	}

	sched, err := scheduler.New(scheduler.Config{Clock: clock.NewFake(start)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	planned, res, err := sched.PlanEvent("sess-1", event, start)
	if err != nil {
		t.Fatalf("PlanEvent: %v", err)
	}
	if res.Known {
		t.Fatal("a banner with no reset time must not resolve to an instant")
	}
	if !planned.Unknown {
		t.Fatal("the schedule must fall back to polling")
	}
	if want := start.Add(5 * time.Minute); !planned.NextPollAt.Equal(want) {
		t.Fatalf("first poll = %s, want %s (five minutes)", planned.NextPollAt.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// TestAcceptanceDSTTransitions proves a reset written across a daylight-saving
// change lands on a real instant in the zone the message named.
func TestAcceptanceDSTTransitions(t *testing.T) {
	t.Parallel()

	berlin, err := scheduler.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	opts := scheduler.Options{Location: berlin, LocationName: "Europe/Berlin"}
	tests := []struct {
		name     string
		expr     string
		now      time.Time
		wantUTC  time.Time
		wantCase scheduler.DSTCase
	}{
		{
			name:     "spring forward gap moves past the missing hour",
			expr:     "2:30am",
			now:      time.Date(2026, 3, 28, 12, 0, 0, 0, berlin),
			wantUTC:  time.Date(2026, 3, 29, 1, 30, 0, 0, time.UTC),
			wantCase: scheduler.DSTGap,
		},
		{
			name:     "autumn fall back picks the later instant",
			expr:     "2:30am",
			now:      time.Date(2026, 10, 24, 12, 0, 0, 0, berlin),
			wantUTC:  time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC),
			wantCase: scheduler.DSTAmbiguous,
		},
		{
			name:     "summer time",
			expr:     "12pm",
			now:      time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC),
			wantUTC:  time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC),
			wantCase: scheduler.DSTNone,
		},
		{
			name:     "winter time",
			expr:     "12pm",
			now:      time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC),
			wantUTC:  time.Date(2026, 12, 1, 11, 0, 0, 0, time.UTC),
			wantCase: scheduler.DSTNone,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res := scheduler.Resolve(detector.ResetPhrase{Present: true, Expression: tc.expr, Raw: tc.expr}, tc.now, opts)
			if !res.Known {
				t.Fatalf("did not resolve: %v", res.Notes)
			}
			if !res.At.Equal(tc.wantUTC) {
				t.Fatalf("resolved to %s, want %s", res.At.Format(time.RFC3339), tc.wantUTC.Format(time.RFC3339))
			}
			if res.DSTCase != tc.wantCase {
				t.Fatalf("DST case = %q, want %q", res.DSTCase, tc.wantCase)
			}
		})
	}
}

// TestAcceptanceSchedulerWakesOnTime drives the full wait through a fake clock,
// proving the wake-up happens at the resolved instant and not before.
func TestAcceptanceSchedulerWakesOnTime(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	fake := clock.NewFake(start)
	sched, err := scheduler.New(scheduler.Config{Clock: fake})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	event := detector.Analyze(canonicalBanner)
	planned, _, err := sched.PlanEvent("sess-1", event, start)
	if err != nil {
		t.Fatalf("PlanEvent: %v", err)
	}

	woke := make(chan time.Time, 1)
	errs := make(chan error, 1)
	go func() {
		at, err := sched.Wait(t.Context(), planned)
		errs <- err
		woke <- at
	}()

	// Wait until the waiter has registered its timer, then step the fake clock.
	deadline := time.Now().Add(time.Second)
	for !fake.HasWaiters() && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if !fake.HasWaiters() {
		t.Fatal("the waiter never registered a timer")
	}

	fake.Advance(59 * time.Minute) // 09:59Z, still one minute early
	select {
	case <-woke:
		t.Fatal("recovery woke before the reset time")
	case <-time.After(20 * time.Millisecond):
	}

	fake.Advance(2 * time.Minute) // 10:01Z, past the reset
	select {
	case err := <-errs:
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
		if at := <-woke; at.Before(planned.ResetAt) {
			t.Fatalf("woke at %s, before the reset at %s", at.Format(time.RFC3339), planned.ResetAt.Format(time.RFC3339))
		}
	case <-time.After(time.Second):
		t.Fatal("recovery did not wake after the reset time")
	}
}

// TestAcceptanceEventShape pins the fields a caller may rely on.
func TestAcceptanceEventShape(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	sched, err := scheduler.New(scheduler.Config{Clock: clock.NewFake(start)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	event := detector.Analyze(canonicalBanner)
	planned, res, err := sched.PlanEvent("sess-1", event, start)
	if err != nil {
		t.Fatalf("PlanEvent: %v", err)
	}

	if planned.SessionID != "sess-1" {
		t.Fatalf("SessionID = %q", planned.SessionID)
	}
	if !planned.ResetAt.After(start) {
		t.Fatalf("ResetAt = %s, want a future instant", planned.ResetAt.Format(time.RFC3339))
	}
	if planned.ResetRaw == "" {
		t.Fatal("the original phrase was not preserved on the schedule")
	}
	if res.Phrase == "" {
		t.Fatal("the original phrase was not preserved on the resolution")
	}
	if len(res.Notes) != 0 {
		t.Fatalf("an unambiguous phrase should not need notes: %v", res.Notes)
	}
	// Everything persisted or compared is UTC.
	if planned.ResetAt.Location() != time.UTC || res.At.Location() != time.UTC {
		t.Fatal("timestamps must be UTC internally")
	}
}
