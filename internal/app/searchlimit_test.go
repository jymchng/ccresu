package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/storage"
)

// seedSearchableSession seeds one session the search may start from and gives it
// the transcript entries the command will read.
//
// RESUMED is the state `search-limit` exists for: it is Settled, so no daemon
// pass would ever read that transcript again. The caller names the state so the
// "nothing is searched that is not idle" side can be pinned too.
func seedSearchableSession(t *testing.T, h *harness, id string, state storage.State, entries ...claude.Entry) storage.Session {
	t.Helper()
	session := h.seedSession(t, id, func(s *storage.Session) {
		s.State = state
		s.TranscriptLines = int64(len(entries))
	})
	h.seedTranscript(session.TranscriptPath, entries...)
	return session
}

// TestSearchLimitCommandParsesAndDispatches covers the top-level command surface:
// `ccresu search-limit <id> [--tail N]` parses, and a dispatch reaches the
// recovery engine rather than merely returning 0.
//
// The `--tail` cases are the reason this test exists. A flag that parses but is
// dropped on the way to the reader looks identical to one that works whenever the
// banner happens to be in the default window, so the banner is seeded as the
// oldest entry: a tail too short to reach it must find nothing, and a tail long
// enough must find it. Only a flag that actually reaches the reader can tell
// those two apart.
func TestSearchLimitCommandParsesAndDispatches(t *testing.T) {
	t.Run("the default tail finds the banner and parks the session", func(t *testing.T) {
		h := newHarness(t)
		ctx := context.Background()
		seedSearchableSession(t, h, canonicalSessionID, storage.StateResumed,
			claude.Entry{Text: "hello"}, claude.Entry{Text: limitBanner})

		code, stdout, stderr := h.exec(t, "search-limit", canonicalSessionID)
		if code != ExitOK {
			t.Fatalf("exit = %d, want %d (stderr %q)", code, ExitOK, stderr)
		}
		if !strings.Contains(stdout, "usage limit found: yes") {
			t.Errorf("stdout = %q, want the search to report the banner it found", stdout)
		}
		if !strings.Contains(stdout, string(storage.StateWaitingForReset)) {
			t.Errorf("stdout = %q, want the state the session was parked in (%s)", stdout, storage.StateWaitingForReset)
		}

		stored, err := h.store.GetSession(ctx, canonicalSessionID)
		if err != nil {
			t.Fatalf("read session: %v", err)
		}
		if stored.State != storage.StateWaitingForReset {
			t.Errorf("state = %s, want %s: a found banner parks the session", stored.State, storage.StateWaitingForReset)
		}
		limit, err := h.store.LatestUsageLimit(ctx, canonicalSessionID)
		if err != nil {
			t.Fatalf("latest usage limit: %v (the command must record what it found)", err)
		}
		if !strings.Contains(limit.RawMessage, "5-hour limit") {
			t.Errorf("recorded banner = %q, want the CLI's own words", limit.RawMessage)
		}
		schedules, err := h.rt.Scheduler.Restore(ctx)
		if err != nil {
			t.Fatalf("restore schedules: %v", err)
		}
		if len(schedules) != 1 || schedules[0].SessionID != canonicalSessionID {
			t.Errorf("schedules = %+v, want exactly one wait for %s", schedules, canonicalSessionID)
		}
	})

	// Three entries, with the banner oldest, so the tail decides the answer.
	short := []claude.Entry{
		{Text: limitBanner},
		{Text: "later, unrelated output"},
		{Text: "the newest line"},
	}

	t.Run("a tail too short to reach the banner finds nothing", func(t *testing.T) {
		h := newHarness(t)
		ctx := context.Background()
		seedSearchableSession(t, h, canonicalSessionID, storage.StateResumed, short...)

		code, stdout, stderr := h.exec(t, "search-limit", canonicalSessionID, "--tail", "2")
		if code != ExitOK {
			t.Fatalf("exit = %d, want %d: a search that finds nothing is an answer, not a failure (stderr %q)",
				code, ExitOK, stderr)
		}
		if !strings.Contains(stdout, "usage limit found: no") {
			t.Errorf("stdout = %q, want the search to report that it found nothing", stdout)
		}
		if _, err := h.store.LatestUsageLimit(ctx, canonicalSessionID); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("usage limit = %v, want none: the banner was outside the requested tail", err)
		}
		stored, err := h.store.GetSession(ctx, canonicalSessionID)
		if err != nil {
			t.Fatalf("read session: %v", err)
		}
		if stored.State != storage.StateResumed {
			t.Errorf("state = %s, want it unchanged at %s: nothing was found, so nothing moves",
				stored.State, storage.StateResumed)
		}
	})

	t.Run("a tail long enough to reach the banner finds it", func(t *testing.T) {
		h := newHarness(t)
		ctx := context.Background()
		seedSearchableSession(t, h, canonicalSessionID, storage.StateResumed, short...)

		code, stdout, stderr := h.exec(t, "search-limit", canonicalSessionID, "--tail", "3")
		if code != ExitOK {
			t.Fatalf("exit = %d, want %d (stderr %q)", code, ExitOK, stderr)
		}
		if !strings.Contains(stdout, "usage limit found: yes") {
			t.Errorf("stdout = %q, want the banner the longer tail reached", stdout)
		}
		stored, err := h.store.GetSession(ctx, canonicalSessionID)
		if err != nil {
			t.Fatalf("read session: %v", err)
		}
		if stored.State != storage.StateWaitingForReset {
			t.Errorf("state = %s, want %s", stored.State, storage.StateWaitingForReset)
		}
	})

	t.Run("the equals form of the tail is parsed too", func(t *testing.T) {
		h := newHarness(t)
		seedSearchableSession(t, h, canonicalSessionID, storage.StateResumed, short...)

		code, stdout, stderr := h.exec(t, "search-limit", canonicalSessionID, "--tail=3")
		if code != ExitOK {
			t.Fatalf("exit = %d, want %d (stderr %q)", code, ExitOK, stderr)
		}
		if !strings.Contains(stdout, "usage limit found: yes") {
			t.Errorf("stdout = %q, want --tail=3 to mean the same as --tail 3", stdout)
		}
	})

	t.Run("the JSON report says what the search found", func(t *testing.T) {
		h := newHarness(t)
		seedSearchableSession(t, h, canonicalSessionID, storage.StateResumed,
			claude.Entry{Text: limitBanner})

		code, stdout, stderr := h.exec(t, "--json", "search-limit", canonicalSessionID)
		if code != ExitOK {
			t.Fatalf("exit = %d, want %d (stderr %q)", code, ExitOK, stderr)
		}
		// The report is indented JSON, so the fields are separated by ": ".
		if !strings.Contains(stdout, `"found": true`) {
			t.Errorf("stdout = %q, want the JSON report to carry found: true", stdout)
		}
		if !strings.Contains(stdout, `"state": "WAITING_FOR_RESET"`) {
			t.Errorf("stdout = %q, want the JSON report to carry the parked state", stdout)
		}
	})

	t.Run("a missing session id is a usage error", func(t *testing.T) {
		h := newHarness(t)

		code, _, stderr := h.exec(t, "search-limit")
		if code != ExitUsage {
			t.Fatalf("exit = %d, want %d", code, ExitUsage)
		}
		if !strings.Contains(stderr, "search-limit needs a session id") {
			t.Errorf("stderr = %q, want the command to name the missing argument", stderr)
		}
	})

	t.Run("a non-positive tail is a usage error", func(t *testing.T) {
		h := newHarness(t)
		seedSearchableSession(t, h, canonicalSessionID, storage.StateResumed,
			claude.Entry{Text: limitBanner})

		for _, tail := range []string{"0", "-1"} {
			code, _, stderr := h.exec(t, "search-limit", canonicalSessionID, "--tail", tail)
			if code != ExitUsage {
				t.Errorf("--tail %s: exit = %d, want %d", tail, code, ExitUsage)
			}
			if !strings.Contains(stderr, "--tail must be positive") {
				t.Errorf("--tail %s: stderr = %q, want the tail to be refused", tail, stderr)
			}
		}
	})

	t.Run("an unknown session is not found", func(t *testing.T) {
		h := newHarness(t)

		const unknown = "99999999-9999-4999-8999-999999999999"
		code, _, stderr := h.exec(t, "search-limit", unknown)
		if code != ExitNotFound {
			t.Fatalf("exit = %d, want %d (stderr %q)", code, ExitNotFound, stderr)
		}
		if !strings.Contains(stderr, unknown) {
			t.Errorf("stderr = %q, want the unknown id named", stderr)
		}
	})
}

// TestSearchLimitParsesAResetTimeForAnyState is the regression this command's
// contract was rewritten for.
//
// A session parked in WAITING_FOR_RESET was refused ("only an idle session can be
// searched"), which is the one state where an operator most needs the search: it
// is the state whose stored reset can be wrong. The read must happen whatever the
// state; what varies is whether the wait is recorded.
func TestSearchLimitParsesAResetTimeForAnyState(t *testing.T) {
	t.Run("a parked session is re-parsed and its wait corrected", func(t *testing.T) {
		h := newHarness(t)
		ctx := context.Background()
		seedSearchableSession(t, h, canonicalSessionID, storage.StateWaitingForReset,
			claude.Entry{Text: "hello"}, claude.Entry{Text: limitBanner})

		// A stale wait the row already carries, so the search has something to
		// correct rather than something to create.
		stale := h.clock.Now().UTC().Add(19*time.Hour + 27*time.Minute)
		if _, err := h.store.SaveSchedule(ctx, storage.Schedule{
			SessionID: canonicalSessionID, ResetAt: stale, ResetRaw: "the stale phrase",
		}); err != nil {
			t.Fatalf("save schedule: %v", err)
		}

		code, stdout, stderr := h.exec(t, "search-limit", canonicalSessionID)
		if code != ExitOK {
			t.Fatalf("exit = %d, want %d: a parked session must be searchable (stderr %q)", code, ExitOK, stderr)
		}
		if !strings.Contains(stdout, "usage limit found: yes") {
			t.Errorf("stdout = %q, want the banner reported", stdout)
		}
		if !strings.Contains(stdout, "wait recorded: yes") {
			t.Errorf("stdout = %q, want the corrected wait reported as recorded", stdout)
		}
		if !strings.Contains(stdout, "next attempt") {
			t.Errorf("stdout = %q, want the re-parsed reset time printed", stdout)
		}
		if !strings.Contains(stdout, string(storage.StateWaitingForReset)) {
			t.Errorf("stdout = %q, want the state to stay %s", stdout, storage.StateWaitingForReset)
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
		// The seeded wait is replaced by the one this search planned. The test
		// banner names no instant ("resets soon"), so the plan is a poll of an
		// unknown reset: what matters is that the row is no longer the stale one,
		// and that it points at the evidence this search recorded.
		if sched.ResetRaw == "the stale phrase" {
			t.Errorf("schedule = %+v, want the wait replaced by the one the banner planned", sched)
		}
		if sched.UsageLimitID == 0 {
			t.Error("schedule is not linked to the usage limit this search recorded")
		}
		if sched.Unknown && sched.NextPollAt.IsZero() {
			t.Error("unknown reset with no poll instant: the session would never be woken")
		}
		limit, err := h.store.LatestUsageLimit(ctx, canonicalSessionID)
		if err != nil {
			t.Fatalf("latest usage limit: %v", err)
		}
		if !strings.Contains(limit.RawMessage, "5-hour limit") {
			t.Errorf("recorded banner = %q, want the banner the re-parse used", limit.RawMessage)
		}
	})

	t.Run("a completed session is read and left alone", func(t *testing.T) {
		h := newHarness(t)
		ctx := context.Background()
		seedSearchableSession(t, h, canonicalSessionID, storage.StateCompleted,
			claude.Entry{Text: limitBanner})

		code, stdout, stderr := h.exec(t, "search-limit", canonicalSessionID)
		if code != ExitOK {
			t.Fatalf("exit = %d, want %d: reading a finished session is still an answer (stderr %q)",
				code, ExitOK, stderr)
		}
		if !strings.Contains(stdout, "usage limit found: yes") {
			t.Errorf("stdout = %q, want the banner reported", stdout)
		}
		if !strings.Contains(stdout, "wait recorded: no") {
			t.Errorf("stdout = %q, want it to say nothing was written", stdout)
		}
		if strings.Contains(stdout, "next attempt") {
			t.Errorf("stdout = %q, want no wait printed for a session that takes no park", stdout)
		}
		stored, err := h.store.GetSession(ctx, canonicalSessionID)
		if err != nil {
			t.Fatalf("read session: %v", err)
		}
		if stored.State != storage.StateCompleted {
			t.Errorf("state = %s, want it unchanged at %s", stored.State, storage.StateCompleted)
		}
		if _, err := h.store.LatestUsageLimit(ctx, canonicalSessionID); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("usage limit = %v, want none: a verdict is not walked back by a read", err)
		}
	})

	t.Run("the JSON report carries applied", func(t *testing.T) {
		h := newHarness(t)
		seedSearchableSession(t, h, canonicalSessionID, storage.StateWaitingForReset,
			claude.Entry{Text: limitBanner})

		code, stdout, stderr := h.exec(t, "--json", "search-limit", canonicalSessionID)
		if code != ExitOK {
			t.Fatalf("exit = %d, want %d (stderr %q)", code, ExitOK, stderr)
		}
		if !strings.Contains(stdout, `"applied": true`) {
			t.Errorf("stdout = %q, want the JSON report to carry applied: true", stdout)
		}
	})
}

// TestSessionsSearchLimitAliasIsRecognisedAndRouted covers the namespaced
// spelling: `ccresu sessions search-limit <id>`.
//
// Recognition alone is not the claim. A word the scanner accepts but cmdSessions
// never dispatches would fall through to the listing and be rejected as a stray
// argument, so the routing cases run the real command and require the session to
// be parked. The flags written before the subcommand are checked as well: they
// are forwarded to the handler, so `sessions --tail 1 search-limit <id>` must
// behave like the top-level command with the same flag.
func TestSessionsSearchLimitAliasIsRecognisedAndRouted(t *testing.T) {
	t.Run("isSessionsSubcommand accepts the alias and nothing near it", func(t *testing.T) {
		for _, word := range []string{subcommandStatus, subcommandDelete, subcommandSearchLimit} {
			if !isSessionsSubcommand(word) {
				t.Errorf("isSessionsSubcommand(%q) = false, want true", word)
			}
		}
		for _, word := range []string{"", "search_limits", "Search-Limit", "search", "limits", "search-limit "} {
			if isSessionsSubcommand(word) {
				t.Errorf("isSessionsSubcommand(%q) = true, want false: only the documented spelling is a subcommand", word)
			}
		}
	})

	t.Run("sessionsSubcommand scans the alias in every accepted shape", func(t *testing.T) {
		const id = "id"
		cases := []struct {
			name string
			args []string
			rest []string
			ok   bool
		}{
			{name: "plain", args: []string{"search-limit", id}, rest: []string{id}, ok: true},
			// Flags before the subcommand are forwarded, not dropped: that is what
			// makes `sessions --json search-limit <id>` produce JSON.
			{name: "json first", args: []string{"--json", "search-limit", id}, rest: []string{"--json", id}, ok: true},
			// --tail takes a value, so the value must be skipped rather than read
			// as the subcommand.
			{name: "tail first", args: []string{"--tail", "5", "search-limit", id}, rest: []string{"--tail", "5", id}, ok: true},
			{name: "tail equals first", args: []string{"--tail=5", "search-limit", id}, rest: []string{"--tail=5", id}, ok: true},
			{name: "after separator", args: []string{"--", "search-limit", id}, rest: []string{"--", id}, ok: true},
			{name: "no session id", args: []string{"search-limit"}, rest: nil, ok: true},
			// A flag's value is never a subcommand.
			{name: "value only", args: []string{"--tail", "5"}, rest: nil, ok: false},
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
				if name != subcommandSearchLimit {
					t.Errorf("subcommand = %q, want %q", name, subcommandSearchLimit)
				}
				if got, want := strings.Join(rest, ","), strings.Join(tc.rest, ","); got != want {
					t.Errorf("rest = %q, want %q", rest, tc.rest)
				}
			})
		}
	})

	t.Run("cmdSessions routes the alias to the search", func(t *testing.T) {
		h := newHarness(t)
		ctx := context.Background()
		seedSearchableSession(t, h, canonicalSessionID, storage.StateResumed,
			claude.Entry{Text: "hello"}, claude.Entry{Text: limitBanner})

		code, stdout, stderr := h.exec(t, "sessions", "search-limit", canonicalSessionID)
		if code != ExitOK {
			t.Fatalf("exit = %d, want %d (stderr %q): the alias must dispatch, not fall through to the listing",
				code, ExitOK, stderr)
		}
		if !strings.Contains(stdout, "usage limit found: yes") {
			t.Errorf("stdout = %q, want the search report", stdout)
		}
		stored, err := h.store.GetSession(ctx, canonicalSessionID)
		if err != nil {
			t.Fatalf("read session: %v", err)
		}
		if stored.State != storage.StateWaitingForReset {
			t.Errorf("state = %s, want %s", stored.State, storage.StateWaitingForReset)
		}
	})

	t.Run("a flag written before the alias reaches the handler", func(t *testing.T) {
		h := newHarness(t)
		ctx := context.Background()
		// The banner is the oldest entry, so --tail 1 cannot reach it.
		seedSearchableSession(t, h, canonicalSessionID, storage.StateResumed,
			claude.Entry{Text: limitBanner}, claude.Entry{Text: "the newest line"})

		code, stdout, stderr := h.exec(t, "sessions", "--tail", "1", "search-limit", canonicalSessionID)
		if code != ExitOK {
			t.Fatalf("exit = %d, want %d (stderr %q)", code, ExitOK, stderr)
		}
		if !strings.Contains(stdout, "usage limit found: no") {
			t.Errorf("stdout = %q, want the forwarded --tail 1 to shorten the window", stdout)
		}
		if _, err := h.store.LatestUsageLimit(ctx, canonicalSessionID); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("usage limit = %v, want none: the forwarded tail must be the one that is read", err)
		}
	})

	t.Run("the alias and the top-level command are one handler", func(t *testing.T) {
		// Two sessions in one harness, not two harnesses: the in-memory database
		// is named from the clock, and a harness's clock is fixed, so a second
		// harness in the same test would share the first one's database.
		const otherID = "66666666-6666-4666-8666-666666666666"
		entries := []claude.Entry{{Text: "hello"}, {Text: limitBanner}}
		h := newHarness(t)
		seedSearchableSession(t, h, canonicalSessionID, storage.StateResumed, entries...)
		seedSearchableSession(t, h, otherID, storage.StateResumed, entries...)

		_, wantOut, wantErr := h.exec(t, "search-limit", canonicalSessionID)
		_, gotOut, gotErr := h.exec(t, "sessions", "search-limit", otherID)

		// The id is the only thing that may differ: replace it so the reports are
		// comparable byte for byte.
		gotOut = strings.ReplaceAll(gotOut, otherID, canonicalSessionID)
		gotErr = strings.ReplaceAll(gotErr, otherID, canonicalSessionID)
		if gotOut != wantOut || gotErr != wantErr {
			t.Errorf("the two spellings disagree:\ntop-level stdout %q stderr %q\nalias     stdout %q stderr %q",
				wantOut, wantErr, gotOut, gotErr)
		}
	})
}
