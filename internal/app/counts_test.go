package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jymchng/ccresu/internal/storage"
)

// seedMixedSessions writes one session per interesting combination of enabled,
// recoverable and terminal state, so the counting rules are exercised on a real
// database rather than a stub.
//
// The shape mirrors what the user hit: many discovered rows, only a few of which
// a bare `ccresu sessions` lists.
func seedMixedSessions(t *testing.T, h *harness) {
	t.Helper()
	type seed struct {
		id          string
		state       storage.State
		enabled     bool
		recoverable bool
	}
	seeds := []seed{
		{"aaaaaaaa-0000-4000-8000-000000000001", storage.StateRunning, true, true},
		{"aaaaaaaa-0000-4000-8000-000000000002", storage.StateWaitingForReset, true, true},
		{"aaaaaaaa-0000-4000-8000-000000000003", storage.StateRetryWait, true, true},
		{"aaaaaaaa-0000-4000-8000-000000000004", storage.StateDisabled, false, true},
		{"aaaaaaaa-0000-4000-8000-000000000005", storage.StateUsageLimitDetected, true, false},
		{"aaaaaaaa-0000-4000-8000-000000000006", storage.StatePermanentlyFailed, true, false},
		{"aaaaaaaa-0000-4000-8000-000000000007", storage.StateResumed, true, false},
		{"aaaaaaaa-0000-4000-8000-000000000008", storage.StateCompleted, false, false},
		{"aaaaaaaa-0000-4000-8000-000000000009", storage.StateUnrecoverable, false, false},
		{"aaaaaaaa-0000-4000-8000-00000000000a", storage.StateUserTerminated, false, false},
	}
	for _, s := range seeds {
		session := s
		h.seedSession(t, session.id, func(stored *storage.Session) {
			stored.State = session.state
			stored.Enabled = session.enabled
			stored.Recoverable = session.recoverable
		})
	}
}

// TestStatusTotalsAgreeWithSessions is the regression test for the report that
// `status` said "sessions: 27 (1 enabled)" while `sessions` listed one row.
//
// The two commands now answer the same question in the same units: the total
// `status` counts is exactly what `sessions --all` lists, and the enabled subset
// it names is exactly what a bare `sessions` lists.
func TestStatusTotalsAgreeWithSessions(t *testing.T) {
	h := newHarness(t)
	seedMixedSessions(t, h)

	code, statusOut, stderr := h.exec(t, "--json", "status")
	if code != ExitOK {
		t.Fatalf("status --json = exit %d: %s", code, stderr)
	}
	var report statusReport
	if err := json.Unmarshal([]byte(statusOut), &report); err != nil {
		t.Fatalf("status --json is not JSON: %v (%q)", err, statusOut)
	}

	code, allOut, stderr := h.exec(t, "--json", "sessions", "--all")
	if code != ExitOK {
		t.Fatalf("sessions --all --json = exit %d: %s", code, stderr)
	}
	var all []sessionReport
	if err := json.Unmarshal([]byte(allOut), &all); err != nil {
		t.Fatalf("sessions --all --json is not JSON: %v (%q)", err, allOut)
	}

	code, defaultOut, stderr := h.exec(t, "--json", "sessions")
	if code != ExitOK {
		t.Fatalf("sessions --json = exit %d: %s", code, stderr)
	}
	var listed []sessionReport
	if err := json.Unmarshal([]byte(defaultOut), &listed); err != nil {
		t.Fatalf("sessions --json is not JSON: %v (%q)", err, defaultOut)
	}

	if report.Sessions != len(all) {
		t.Errorf("status counts %d sessions but `sessions --all` lists %d", report.Sessions, len(all))
	}
	if report.EnabledSessions != len(listed) {
		t.Errorf("status counts %d enabled sessions but `sessions` lists %d", report.EnabledSessions, len(listed))
	}
	if report.SessionsListedByDefault != len(listed) {
		t.Errorf("status says a bare listing shows %d sessions; `sessions` lists %d",
			report.SessionsListedByDefault, len(listed))
	}
	if len(all) != 10 || len(listed) != 6 {
		t.Errorf("seeds listed %d of %d, want 6 of 10", len(listed), len(all))
	}

	// Every state the histogram reports must add up to the total it prints.
	var histogram int
	for _, count := range report.States {
		histogram += count
	}
	if histogram != report.Sessions {
		t.Errorf("state histogram sums to %d, but status reports %d sessions: %+v",
			histogram, report.Sessions, report.States)
	}
}

// TestStatusNamesTheListingFilter pins the human-readable form: the total and the
// enabled count are both labelled, and the line names the command that shows the
// rest, so "27 total, 1 enabled" cannot be read as a contradiction with a
// one-row listing.
func TestStatusNamesTheListingFilter(t *testing.T) {
	h := newHarness(t)
	seedMixedSessions(t, h)

	code, stdout, stderr := h.exec(t, "status")
	if code != ExitOK {
		t.Fatalf("status = exit %d: %s", code, stderr)
	}
	for _, want := range []string{
		"10 total, 6 enabled",
		"ccresu sessions shows the 6 enabled",
		"ccresu sessions --all shows all 10",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status output missing %q:\n%s", want, stdout)
		}
	}
}

// TestSessionsFooterStatesTheFilter pins the other half: the table footer says the
// listing is enabled-only and how many rows it is hiding.
func TestSessionsFooterStatesTheFilter(t *testing.T) {
	h := newHarness(t)
	seedMixedSessions(t, h)

	code, stdout, stderr := h.exec(t, "sessions")
	if code != ExitOK {
		t.Fatalf("sessions = exit %d: %s", code, stderr)
	}
	if want := "6 session(s) shown: enabled only (of 10 matching; --all includes disabled and settled)"; !strings.Contains(stdout, want) {
		t.Errorf("default footer missing %q:\n%s", want, stdout)
	}

	code, stdout, stderr = h.exec(t, "sessions", "--all")
	if code != ExitOK {
		t.Fatalf("sessions --all = exit %d: %s", code, stderr)
	}
	if want := "10 session(s) shown (--all: disabled and settled sessions included)"; !strings.Contains(stdout, want) {
		t.Errorf("--all footer missing %q:\n%s", want, stdout)
	}

	// The state filter narrows both the listing and the "matching" count.
	code, stdout, stderr = h.exec(t, "sessions", "--state", "DISABLED")
	if code != ExitOK {
		t.Fatalf("sessions --state DISABLED = exit %d: %s", code, stderr)
	}
	if want := "no enabled sessions match; 1 match with --all"; !strings.Contains(stdout, want) {
		t.Errorf("disabled-only listing missing %q:\n%s", want, stdout)
	}
	if strings.Contains(stdout, "no sessions match") {
		t.Errorf("a disabled-only database must not claim there are no sessions:\n%s", stdout)
	}
}

// TestSessionsFooterNamesATruncatedLimit keeps a full page from looking like the
// whole table: the footer says the limit cut the result short.
func TestSessionsFooterNamesATruncatedLimit(t *testing.T) {
	h := newHarness(t)
	seedMixedSessions(t, h)

	code, stdout, stderr := h.exec(t, "sessions", "--all", "--limit", "3")
	if code != ExitOK {
		t.Fatalf("sessions --all --limit 3 = exit %d: %s", code, stderr)
	}
	if want := "limit 3 reached; use --limit 0 to list every match"; !strings.Contains(stdout, want) {
		t.Errorf("truncated footer missing %q:\n%s", want, stdout)
	}

	// A limit larger than the result set is not truncation and must not say so.
	code, stdout, stderr = h.exec(t, "sessions", "--all", "--limit", "50")
	if code != ExitOK {
		t.Fatalf("sessions --all --limit 50 = exit %d: %s", code, stderr)
	}
	if strings.Contains(stdout, "limit 50 reached") {
		t.Errorf("an unreached limit was reported as truncation:\n%s", stdout)
	}
	if want := fmt.Sprintf("%d session(s) shown", 10); !strings.Contains(stdout, want) {
		t.Errorf("footer missing %q:\n%s", want, stdout)
	}
}

// TestSessionsFootnote pins the helper directly, including the zero-rows case.
func TestSessionsFootnote(t *testing.T) {
	cases := []struct {
		name            string
		shown, matching int
		all             bool
		limit           int
		want, notWanted string
	}{
		{"default filtered", 6, 10, false, 0, "6 session(s) shown: enabled only (of 10 matching; --all includes disabled and settled)\n", "limit"},
		{"default unfiltered", 10, 10, false, 0, "10 session(s) shown: enabled only (--all includes disabled and settled)\n", "of 10"},
		{"all", 10, 10, true, 0, "10 session(s) shown (--all: disabled and settled sessions included)\n", "enabled only"},
		{"limit reached", 3, 10, true, 3, "limit 3 reached; use --limit 0 to list every match\n", ""},
		{"limit not reached", 2, 10, true, 5, "2 session(s) shown", "limit 5 reached"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sessionsFootnote(tc.shown, tc.matching, tc.all, tc.limit)
			if !strings.Contains(got, tc.want) {
				t.Errorf("sessionsFootnote(%d, %d, %v, %d) = %q, want it to contain %q",
					tc.shown, tc.matching, tc.all, tc.limit, got, tc.want)
			}
			if tc.notWanted != "" && strings.Contains(got, tc.notWanted) {
				t.Errorf("sessionsFootnote(%d, %d, %v, %d) = %q, must not contain %q",
					tc.shown, tc.matching, tc.all, tc.limit, got, tc.notWanted)
			}
		})
	}
}
