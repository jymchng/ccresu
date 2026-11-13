package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/storage"
)

// runsLast returns the last full screen the browser drew.
//
// The browser repaints by writing the clear-screen escape and then the whole
// frame, so the text after the final escape is exactly what the operator would be
// looking at when the sequence of keys ran out.
func lastScreen(out string) string {
	if i := strings.LastIndex(out, tuiClearScreen); i >= 0 {
		return out[i+len(tuiClearScreen):]
	}
	return out
}

// markedID returns the short session id on the highlighted row, or "" when no row
// is marked.
func markedID(screen string) string {
	for _, line := range strings.Split(screen, "\n") {
		if strings.HasPrefix(line, "> ") {
			fields := strings.Fields(line[2:])
			if len(fields) > 0 {
				return fields[0]
			}
		}
	}
	return ""
}

// listedIDs returns the short session ids of the data rows, in the order drawn.
//
// Every row of the list screen starts with the two-character marker column, which
// is what separates the rows from the header, the status line and the key map.
func listedIDs(screen string) []string {
	ids := make([]string, 0, 4)
	for _, line := range strings.Split(screen, "\n") {
		if !strings.HasPrefix(line, "> ") && !strings.HasPrefix(line, "  ") {
			continue
		}
		fields := strings.Fields(line[2:])
		if len(fields) == 0 || fields[0] == "SESSION" {
			continue
		}
		if len(fields[0]) != len(shortID(canonicalSessionID)) {
			// A footer ("rows 1-3 of 3") or an empty-state sentence.
			continue
		}
		ids = append(ids, fields[0])
	}
	return ids
}

func TestDecodeKeyReadsTerminalSequences(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  keyEvent
	}{
		{"plain rune", "j", keyEvent{r: 'j'}},
		{"arrow up", "\x1b[A", keyEvent{name: keyUp}},
		{"arrow down", "\x1b[B", keyEvent{name: keyDown}},
		{"home", "\x1b[H", keyEvent{name: keyHome}},
		{"end", "\x1b[F", keyEvent{name: keyEnd}},
		{"parameterised arrow", "\x1b[1;5B", keyEvent{name: keyDown}},
		{"enter as carriage return", "\r", keyEvent{name: keyEnter}},
		{"enter as newline", "\n", keyEvent{name: keyEnter}},
		{"backspace", "\x7f", keyEvent{name: keyBackspace}},
		{"ctrl-c", "\x03", keyEvent{name: keyInterrupt}},
		{"bare escape", "\x1b", keyEvent{name: keyEsc}},
		{"escape then EOF", "\x1b[", keyEvent{name: keyEsc}},
		{"end of input", "", keyEvent{name: keyEOF}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeKey(bufio.NewReader(strings.NewReader(tc.input)))
			if err != nil {
				t.Fatalf("decodeKey(%q): %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("decodeKey(%q) = %+v, want %+v", tc.input, got, tc.want)
			}
		})
	}
}

func TestDecodeKeyIgnoresUnknownControlSequence(t *testing.T) {
	// ESC [ Z is shift-tab: a real sequence the browser has no binding for. It
	// must decode to nothing rather than to a rune the list would act on.
	got, err := decodeKey(bufio.NewReader(strings.NewReader("\x1b[Z")))
	if err != nil {
		t.Fatalf("decodeKey: %v", err)
	}
	if got.name != "" || got.r != 0 {
		t.Fatalf("decodeKey = %+v, want the zero key", got)
	}
}

func TestSessionsFallsBackToTheTableWithoutATerminal(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)

	code, stdout, stderr := h.exec(t, "sessions")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, stderr)
	}
	if strings.Contains(stdout, "\x1b") {
		t.Errorf("non-terminal output contains ANSI escapes:\n%q", stdout)
	}
	for _, want := range []string{"SESSION", "STATE", "NEXT ATTEMPT", "session(s) shown"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("table output missing %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stdout, shortID(canonicalSessionID)) {
		t.Errorf("table output missing the session id:\n%s", stdout)
	}

	// "Unchanged" means byte-for-byte: --no-tui on a terminal and no terminal at
	// all must print exactly the same bytes, so the TTY gate can never become a
	// second renderer that drifts from the table.
	h.terminal = true
	code, noTUI, stderr := h.exec(t, "sessions", "--no-tui")
	if code != ExitOK {
		t.Fatalf("--no-tui exit = %d (stderr %q)", code, stderr)
	}
	if noTUI != stdout {
		t.Errorf("the piped table and --no-tui differ:\npiped:\n%q\n--no-tui:\n%q", stdout, noTUI)
	}
}

func TestSessionsKeepsJSONWhenStdoutIsATerminal(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)
	h.terminal = true

	// --json wins over the terminal: a script must never get a full screen even
	// if it is run from an interactive shell.
	code, stdout, stderr := h.exec(t, "--json", "sessions")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, stderr)
	}
	if strings.Contains(stdout, "\x1b") {
		t.Errorf("JSON output contains ANSI escapes:\n%q", stdout)
	}
	var report []sessionReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, stdout)
	}
	if len(report) != 1 || report[0].ID != canonicalSessionID {
		t.Fatalf("report = %+v, want one row for %s", report, canonicalSessionID)
	}
}

func TestSessionsOpensTheBrowserOnATerminal(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)

	code := h.runWithKeys("q", "sessions")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	out := h.stdout.String()
	if !strings.Contains(out, tuiAltScreenOn) {
		t.Errorf("browser did not take the alternate screen:\n%q", out)
	}
	screen := lastScreen(out)
	for _, want := range []string{"ccresu sessions", shortID(canonicalSessionID), "? help", "q quit"} {
		if !strings.Contains(screen, want) {
			t.Errorf("browser screen missing %q:\n%s", want, screen)
		}
	}
	if strings.Contains(out, "session(s) shown") {
		t.Errorf("browser run also printed the table:\n%s", out)
	}
}

func TestSessionsNoTUIOnATerminalPrintsTheTable(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)

	code := h.runWithKeys("q", "sessions", "--no-tui")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	out := h.stdout.String()
	if strings.Contains(out, tuiAltScreenOn) || strings.Contains(out, "\x1b") {
		t.Errorf("--no-tui emitted ANSI escapes:\n%q", out)
	}
	if !strings.Contains(out, "session(s) shown") {
		t.Errorf("--no-tui did not print the table:\n%s", out)
	}
}

func TestSessionsTUIForcesTheBrowserWithoutATerminal(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)
	h.keys = "q"

	// The explicit flag is an override: it works on a pipe too, which is what
	// makes the browser testable and what an operator gets from a wrapper script.
	code, stdout, stderr := h.exec(t, "sessions", "--tui")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, stderr)
	}
	if !strings.Contains(stdout, tuiAltScreenOn) {
		t.Errorf("--tui did not draw the browser:\n%q", stdout)
	}
}

func TestSessionsTUIRejectsContradictoryFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"both", []string{"sessions", "--tui", "--no-tui"}, "mutually exclusive"},
		{"tui with json", []string{"sessions", "--tui", "--json"}, "cannot be combined with --json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			code, _, stderr := h.exec(t, tc.args...)
			if code != ExitUsage {
				t.Fatalf("exit = %d, want %d (stderr %q)", code, ExitUsage, stderr)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr = %q, want %q", stderr, tc.want)
			}
		})
	}
}

func TestSessionsBrowserMovesTheSelection(t *testing.T) {
	h := newHarness(t)
	first := h.seedSession(t, "aaaaaaaa-1111-4111-8111-111111111111", nil)
	second := h.seedSession(t, "bbbbbbbb-2222-4222-8222-222222222222", nil)
	third := h.seedSession(t, "cccccccc-3333-4333-8333-333333333333", nil)

	// Down twice, up once, then the last row: the marker has to end on the third
	// row after G whatever order the store returned.
	code := h.runWithKeys("jjkGq", "sessions", "--limit", "0", "--all")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	screen := lastScreen(h.stdout.String())
	ids := listedIDs(screen)
	if len(ids) != 3 {
		t.Fatalf("listed %d rows, want 3:\n%s", len(ids), screen)
	}
	if got := markedID(screen); got != ids[len(ids)-1] {
		t.Errorf("marked row = %q, want the last row %q:\n%s", got, ids[len(ids)-1], screen)
	}

	// g returns to the top.
	code = h.runWithKeys("jGgq", "sessions", "--limit", "0", "--all")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	screen = lastScreen(h.stdout.String())
	if got, want := markedID(screen), listedIDs(screen)[0]; got != want {
		t.Errorf("after g marked row = %q, want the first row %q:\n%s", got, want, screen)
	}

	// Arrow keys mean the same as j/k.
	code = h.runWithKeys("\x1b[Bq", "sessions", "--limit", "0", "--all")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	screen = lastScreen(h.stdout.String())
	if got, want := markedID(screen), listedIDs(screen)[1]; got != want {
		t.Errorf("after down-arrow marked row = %q, want the second row %q:\n%s", got, want, screen)
	}

	_ = first
	_ = second
	_ = third
}

func TestSessionsBrowserFiltersByText(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, "aaaaaaaa-1111-4111-8111-111111111111", func(s *storage.Session) { s.ProjectPath = "/work/flaky-build" })
	h.seedSession(t, "bbbbbbbb-2222-4222-8222-222222222222", func(s *storage.Session) { s.ProjectPath = "/work/quiet-run" })

	code := h.runWithKeys("/flaky\rq", "sessions", "--limit", "0", "--all")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	screen := lastScreen(h.stdout.String())
	if !strings.Contains(screen, "flaky-build") {
		t.Errorf("filtered screen dropped the matching row:\n%s", screen)
	}
	if strings.Contains(screen, "quiet-run") {
		t.Errorf("filtered screen still shows the non-matching row:\n%s", screen)
	}
	if !strings.Contains(screen, `filter "flaky"`) {
		t.Errorf("header does not state the filter:\n%s", screen)
	}

	// Escape cancels the edit: the applied filter is unchanged and both rows stay.
	code = h.runWithKeys("/quiet\x1bq", "sessions", "--limit", "0", "--all")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	screen = lastScreen(h.stdout.String())
	if !strings.Contains(screen, "flaky-build") || !strings.Contains(screen, "quiet-run") {
		t.Errorf("escape did not cancel the filter:\n%s", screen)
	}

	// 'c' clears an applied filter.
	code = h.runWithKeys("/quiet\rcq", "sessions", "--limit", "0", "--all")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	screen = lastScreen(h.stdout.String())
	if !strings.Contains(screen, "flaky-build") || strings.Contains(screen, `filter "quiet"`) {
		t.Errorf("'c' did not clear the filter:\n%s", screen)
	}
}

func TestSessionsBrowserTogglesAll(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.Enabled = false
		s.State = storage.StateDisabled
		s.ProjectPath = "/work/hidden-one"
	})

	// A bare listing is enabled-only, so the disabled row is not there until 'a'.
	code := h.runWithKeys("q", "sessions")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	if screen := lastScreen(h.stdout.String()); strings.Contains(screen, "hidden-one") {
		t.Errorf("a bare browser listing showed the disabled row:\n%s", screen)
	}

	code = h.runWithKeys("aq", "sessions")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	screen := lastScreen(h.stdout.String())
	if !strings.Contains(screen, "hidden-one") {
		t.Errorf("'a' did not reveal the disabled row:\n%s", screen)
	}
	if !strings.Contains(screen, "(all)") {
		t.Errorf("header does not say the listing is unfiltered:\n%s", screen)
	}
}

func TestSessionsBrowserEnablesAndDisables(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.Enabled = false
		s.State = storage.StateDisabled
	})
	ctx := context.Background()

	if code := h.runWithKeys("aeq", "sessions"); code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	session, err := h.store.GetSession(ctx, canonicalSessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if !session.Enabled || session.State != storage.StateDiscovered {
		t.Errorf("after 'e' enabled = %v state = %s, want enabled and DISCOVERED", session.Enabled, session.State)
	}

	// 'd' is the same call the CLI makes, so the row is disabled again.
	if code := h.runWithKeys("dq", "sessions"); code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	session, err = h.store.GetSession(ctx, canonicalSessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if session.Enabled || session.State != storage.StateDisabled {
		t.Errorf("after 'd' enabled = %v state = %s, want disabled and DISABLED", session.Enabled, session.State)
	}
}

func TestSessionsBrowserInspectsTheSelectedSession(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)

	code := h.runWithKeys("iq", "sessions")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	// The detail screen is writeSessionDetail's output, i.e. exactly what
	// `ccresu sessions status <id>` prints. The final frame is the list again (the
	// detail closes on the next keystroke), so the frame is found by content.
	screen := lastScreenBetween(h.stdout.String(), "Next attempt")
	if screen == "" {
		t.Fatalf("no detail screen was drawn:\n%s", h.stdout.String())
	}
	for _, want := range []string{"Next attempt", canonicalSessionID, "state:", "recoverable:"} {
		if !strings.Contains(screen, want) {
			t.Errorf("inspect screen missing %q:\n%s", want, screen)
		}
	}
	if !strings.Contains(screen, "press enter, q or esc to return") {
		t.Errorf("inspect screen does not say how to leave it:\n%s", screen)
	}

	// Enter opens it too, and enter closes it.
	if code := h.runWithKeys("\r\rq", "sessions"); code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	if !strings.Contains(h.stdout.String(), "Next attempt") {
		t.Errorf("enter did not open the detail screen:\n%s", h.stdout.String())
	}
}

func TestSessionsBrowserShowsTheKeyMap(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)

	code := h.runWithKeys("?xq", "sessions")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	out := h.stdout.String()
	keys := lastScreenBetween(out, "keys")
	if keys == "" {
		keys = out
	}
	for _, binding := range tuiKeyMap() {
		if !strings.Contains(keys, binding.keys) {
			t.Errorf("key map screen missing %q:\n%s", binding.keys, keys)
		}
	}
	if !strings.Contains(keys, "this key map") {
		t.Errorf("key map screen missing its own description:\n%s", keys)
	}
}

// lastScreenBetween returns the part of out that contains marker, which for the
// help screen is the whole frame it drew.
func lastScreenBetween(out, marker string) string {
	frames := strings.Split(out, tuiClearScreen)
	for i := len(frames) - 1; i >= 0; i-- {
		if strings.Contains(frames[i], marker) {
			return frames[i]
		}
	}
	return ""
}

func TestSessionsBrowserRecoversAndRetries(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
	}{
		{"recover", "R"},
		{"retry", "r"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
				s.State = storage.StateResetTimeReached
				s.TranscriptSize = 10
				s.TranscriptLines = 1
			})
			resumed := claude.Session{ID: canonicalSessionID, ProjectPath: "/work/project", Recoverable: true}
			h.provider.SetSession(resumed)
			h.provider.Statuses[canonicalSessionID] = claude.SessionStatus{
				Session:    resumed,
				Transcript: claude.TranscriptInfo{Path: "/tmp/t.jsonl", Size: 400, Lines: 9},
			}
			h.provider.OnResume = func(context.Context, string, string) (claude.ResumeResult, error) {
				h.provider.Statuses[canonicalSessionID] = claude.SessionStatus{
					Session:    resumed,
					Transcript: claude.TranscriptInfo{Path: "/tmp/t.jsonl", Size: 4096, Lines: 42},
				}
				return claude.ResumeResult{
					RequestedSessionID: canonicalSessionID,
					SessionID:          canonicalSessionID,
					Verified:           true,
				}, nil
			}

			code := h.runWithKeys(tc.key+"q", "sessions")
			if code != ExitOK {
				t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
			}
			ctx := context.Background()
			attempts, err := h.store.ListAttempts(ctx, canonicalSessionID, 0)
			if err != nil {
				t.Fatalf("list attempts: %v", err)
			}
			if len(attempts) != 1 {
				t.Fatalf("attempts = %d, want 1 (screen:\n%s)", len(attempts), lastScreen(h.stdout.String()))
			}
			session, err := h.store.GetSession(ctx, canonicalSessionID)
			if err != nil {
				t.Fatalf("get session: %v", err)
			}
			if session.State != storage.StateResumed {
				t.Errorf("state = %s, want RESUMED", session.State)
			}
		})
	}
}

func TestSessionsBrowserDeletesOnlyOnConfirmation(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)
	ctx := context.Background()

	// 'x' then 'n' must leave the row alone.
	if code := h.runWithKeys("xnq", "sessions"); code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	if _, err := h.store.GetSession(ctx, canonicalSessionID); err != nil {
		t.Fatalf("session was deleted without confirmation: %v", err)
	}
	if !strings.Contains(lastScreen(h.stdout.String()), "delete cancelled") {
		t.Errorf("screen does not report the cancelled delete:\n%s", lastScreen(h.stdout.String()))
	}

	// 'x' then 'y' forgets the row and leaves a tombstone, exactly like
	// `sessions delete`.
	if code := h.runWithKeys("xyq", "sessions"); code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	if _, err := h.store.GetSession(ctx, canonicalSessionID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("session still present after confirmation: %v", err)
	}
	if _, err := h.store.GetDeletedSession(ctx, canonicalSessionID); err != nil {
		t.Errorf("no tombstone after the delete: %v", err)
	}
	if !strings.Contains(h.stdout.String(), "the conversation was kept") {
		t.Errorf("delete did not say the conversation was kept:\n%s", lastScreen(h.stdout.String()))
	}
}

func TestSessionsBrowserRestoresTheTerminalOnEveryExit(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)

	if code := h.runWithKeys("q", "sessions"); code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	out := h.stdout.String()
	if !strings.HasSuffix(out, tuiShowCursor+tuiAltScreenOff) {
		t.Errorf("browser did not restore the cursor and the main screen:\n%q", out)
	}

	// Ctrl-C quits the same way instead of killing the process mid-frame: in raw
	// mode there is no SIGINT, so the browser owns that keystroke.
	if code := h.runWithKeys("\x03", "sessions"); code != ExitOK {
		t.Fatalf("ctrl-c exit = %d (stderr %q)", code, h.stderr.String())
	}
	if !strings.HasSuffix(h.stdout.String(), tuiShowCursor+tuiAltScreenOff) {
		t.Errorf("ctrl-c did not restore the terminal:\n%q", h.stdout.String())
	}
}

func TestSessionsBrowserQuitsAtEndOfInput(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)

	// A keystroke stream that ends without 'q' (a pty closed under the browser)
	// must not hang or error.
	if code := h.runWithKeys("j", "sessions"); code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	// Ending input is an exit path like any other, so the cursor and the main
	// screen come back: a closed pty must not leave the terminal in raw mode.
	if out := h.stdout.String(); !strings.HasSuffix(out, tuiShowCursor+tuiAltScreenOff) {
		t.Errorf("the browser did not restore the terminal at end of input:\n%q", out)
	}
}

func TestIsTerminalWriterAsksTheFile(t *testing.T) {
	// A buffer is not a terminal, which is why every existing test in this
	// package still exercises the table.
	if isTerminalWriter(&strings.Builder{}) {
		t.Error("a strings.Builder was reported as a terminal")
	}

	// A pipe is not a terminal either: `ccresu sessions | tee` must print the
	// table, not a full screen.
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer read.Close()
	defer write.Close()
	if isTerminalWriter(write) {
		t.Error("a pipe was reported as a terminal")
	}

	// /dev/null is a character device, so it exercises the positive branch
	// without needing a pty. The real terminal is the same answer by the same
	// test, which is what keeps this honest.
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("no %s on this platform: %v", os.DevNull, err)
	}
	defer devNull.Close()
	if !isTerminalWriter(devNull) {
		t.Errorf("%s was not reported as a character device", os.DevNull)
	}
}

func TestResolveSessionsUIPrecedence(t *testing.T) {
	cases := []struct {
		name      string
		force     bool
		never     bool
		json      bool
		terminal  bool
		want      bool
		wantUsage bool
	}{
		{name: "table by default on a pipe", want: false},
		{name: "browser by default on a terminal", terminal: true, want: true},
		{name: "json wins on a terminal", json: true, terminal: true, want: false},
		{name: "no-tui wins on a terminal", never: true, terminal: true, want: false},
		{name: "tui forces it on a pipe", force: true, want: true},
		{name: "tui with json is a usage error", force: true, json: true, terminal: true, wantUsage: true},
		{name: "tui with no-tui is a usage error", force: true, never: true, terminal: true, wantUsage: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newApp(Deps{IsTerminal: func(io.Writer) bool { return tc.terminal }}, BuildInfo{}, io.Discard, io.Discard)
			a.json = tc.json
			got, err := a.resolveSessionsUI(tc.force, tc.never)
			if tc.wantUsage {
				if err == nil {
					t.Fatalf("resolveSessionsUI(%v, %v) = %v, want a usage error", tc.force, tc.never, got)
				}
				if code := exitCode(err); code != ExitUsage {
					t.Errorf("exit code = %d, want %d", code, ExitUsage)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveSessionsUI: %v", err)
			}
			if got != tc.want {
				t.Errorf("resolveSessionsUI(%v, %v) = %v, want %v", tc.force, tc.never, got, tc.want)
			}
		})
	}
}

func TestSessionsBrowserCreatesByDiscovery(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// A conversation exists on disk but ccresu has no row for it: this is exactly
	// the state 'n' exists to fix.
	discoveredID := "cccccccc-3333-4333-8333-333333333333"
	h.provider.SetSession(claude.Session{
		ID:          discoveredID,
		ProjectPath: "/work/discovered",
		Recoverable: true,
	})
	if _, err := h.store.GetSession(ctx, discoveredID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("precondition: session already stored: %v", err)
	}

	if code := h.runWithKeys("nq", "sessions", "--all"); code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	session, err := h.store.GetSession(ctx, discoveredID)
	if err != nil {
		t.Fatalf("'n' did not create the discovered session: %v", err)
	}
	if session.ProjectPath != "/work/discovered" {
		t.Errorf("created row has project %q, want %q", session.ProjectPath, "/work/discovered")
	}
	screen := lastScreen(h.stdout.String())
	if !strings.Contains(screen, "create: 1 new session(s) of 1 discovered") {
		t.Errorf("status line does not report what was created:\n%s", screen)
	}
	if !strings.Contains(screen, shortID(discoveredID)) {
		t.Errorf("the new row is not in the list:\n%s", screen)
	}

	// A second press adds nothing: discovery is idempotent, so 'n' cannot
	// duplicate a row.
	if code := h.runWithKeys("nq", "sessions", "--all"); code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	if screen := lastScreen(h.stdout.String()); !strings.Contains(screen, "create: 0 new session(s) of 1 discovered") {
		t.Errorf("a repeat discovery reported new rows:\n%s", screen)
	}
}

func TestSessionsBrowserCreateRespectsDeletedSessions(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// The operator deleted this session, but its transcript is still on disk and
	// the provider still reports it. 'n' must not resurrect the row.
	h.seedSession(t, canonicalSessionID, nil)
	if _, _, err := h.store.ForgetSession(ctx, canonicalSessionID); err != nil {
		t.Fatalf("forget session: %v", err)
	}
	h.provider.SetSession(claude.Session{
		ID:          canonicalSessionID,
		ProjectPath: "/work/project",
		Recoverable: true,
	})

	if code := h.runWithKeys("nq", "sessions", "--all"); code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	if _, err := h.store.GetSession(ctx, canonicalSessionID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("'n' resurrected a deleted session: %v", err)
	}
	screen := lastScreen(h.stdout.String())
	if !strings.Contains(screen, "1 left forgotten") {
		t.Errorf("status line does not say the tombstone was honoured:\n%s", screen)
	}
}

func TestSessionsBrowserEnterRendersTheSessionsStatusPage(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)

	if code := h.runWithKeys("\rq", "sessions"); code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	browserOut := h.stdout.String()

	// The same session through the command, for a byte-level comparison of the
	// page itself.
	if code := h.run("sessions", "status", canonicalSessionID); code != ExitOK {
		t.Fatalf("sessions status exit = %d (stderr %q)", code, h.stderr.String())
	}
	want := h.stdout.String()

	frame := lastScreenBetween(browserOut, "Next attempt")
	if frame == "" {
		t.Fatalf("enter drew no detail screen:\n%s", browserOut)
	}
	for _, line := range strings.Split(strings.TrimRight(want, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.Contains(frame, line) {
			t.Errorf("detail screen is missing the `sessions status` line %q:\n%s", line, frame)
		}
	}
}

// TestSessionsBrowserActionsTargetTheCursorRow proves that a per-session action
// applies to the highlighted row and to no other row.
//
// The discrimination is the whole point: a browser that read sessions[0] instead
// of sessions[cursor] would pass every other test in this file, because with a
// single seeded session the two are the same row. Each case below therefore seeds
// two sessions, moves the cursor off the first, and asserts both that the cursor
// row changed and that the row above it did not.
func TestSessionsBrowserActionsTargetTheCursorRow(t *testing.T) {
	const (
		firstID  = "aaaaaaaa-1111-4111-8111-111111111111"
		secondID = "bbbbbbbb-2222-4222-8222-222222222222"
	)
	ctx := context.Background()

	// Both seeds share the harness clock, so the listing's
	// "last_seen_at DESC, id ASC" order is 'aaaa...' then 'bbbb...'. The cases
	// still read the drawn order off the screen rather than assuming it, so a
	// change to the ordering cannot quietly make this test vacuous.
	seed := func(t *testing.T) *harness {
		t.Helper()
		h := newHarness(t)
		h.seedSession(t, firstID, nil)
		h.seedSession(t, secondID, nil)
		return h
	}
	readSession := func(t *testing.T, h *harness, id string) storage.Session {
		t.Helper()
		session, err := h.store.GetSession(ctx, id)
		if err != nil {
			t.Fatalf("get session %s: %v", shortID(id), err)
		}
		return session
	}
	// secondRowKeys moves the cursor one row down and returns the short id it
	// landed on, after checking that it is the second of the two drawn rows.
	secondRowKeys := func(t *testing.T, keys string) string {
		t.Helper()
		h := seed(t)
		if code := h.runWithKeys(keys, "sessions", "--limit", "0", "--all"); code != ExitOK {
			t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
		}
		screen := lastScreen(h.stdout.String())
		order := listedIDs(screen)
		if len(order) != 2 {
			t.Fatalf("listed %d rows, want 2:\n%s", len(order), screen)
		}
		if order[0] == order[1] {
			t.Fatalf("both drawn rows are %q:\n%s", order[0], screen)
		}
		if marked := markedID(screen); marked != order[1] {
			t.Fatalf("the cursor is on %q, want the second row %q:\n%s", marked, order[1], screen)
		}
		return order[1]
	}

	cursorID := secondRowKeys(t, "jq")
	if cursorID != shortID(secondID) {
		t.Fatalf("the cursor row is %q, want %q", cursorID, shortID(secondID))
	}

	t.Run("disable", func(t *testing.T) {
		h := seed(t)
		if code := h.runWithKeys("jdq", "sessions", "--limit", "0", "--all"); code != ExitOK {
			t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
		}
		if got := readSession(t, h, secondID); got.Enabled || got.State != storage.StateDisabled {
			t.Errorf("the cursor row was not disabled: enabled = %v, state = %s", got.Enabled, got.State)
		}
		if got := readSession(t, h, firstID); !got.Enabled || got.State != storage.StateRunning {
			t.Errorf("'d' also touched the row above the cursor: enabled = %v, state = %s", got.Enabled, got.State)
		}
		if screen := lastScreen(h.stdout.String()); !strings.Contains(screen, "disable "+shortID(secondID)) {
			t.Errorf("the status line does not name the cursor row:\n%s", screen)
		}
	})

	t.Run("enable", func(t *testing.T) {
		h := newHarness(t)
		h.seedSession(t, firstID, nil)
		h.seedSession(t, secondID, func(s *storage.Session) {
			s.Enabled = false
			s.State = storage.StateDisabled
		})
		// --all is what makes both rows visible, so the cursor can sit on the
		// disabled one.
		if code := h.runWithKeys("jeq", "sessions", "--limit", "0", "--all"); code != ExitOK {
			t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
		}
		if got := readSession(t, h, secondID); !got.Enabled || got.State != storage.StateDiscovered {
			t.Errorf("the cursor row was not enabled: enabled = %v, state = %s", got.Enabled, got.State)
		}
		if got := readSession(t, h, firstID); !got.Enabled || got.State != storage.StateRunning {
			t.Errorf("'e' also touched the row above the cursor: enabled = %v, state = %s", got.Enabled, got.State)
		}
	})

	t.Run("delete", func(t *testing.T) {
		h := seed(t)
		if code := h.runWithKeys("jxyq", "sessions", "--limit", "0", "--all"); code != ExitOK {
			t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
		}
		if _, err := h.store.GetSession(ctx, secondID); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("the cursor row survived the confirmed delete: %v", err)
		}
		if _, err := h.store.GetSession(ctx, firstID); err != nil {
			t.Fatalf("'x' forgot the row above the cursor: %v", err)
		}
		if _, err := h.store.GetDeletedSession(ctx, secondID); err != nil {
			t.Errorf("the cursor row has no tombstone: %v", err)
		}
		if _, err := h.store.GetDeletedSession(ctx, firstID); err == nil {
			t.Errorf("the row above the cursor was tombstoned as well")
		}
	})

	for _, tc := range []struct{ name, key string }{
		{"enter", "\r"},
		{"i", "i"},
	} {
		t.Run("detail/"+tc.name, func(t *testing.T) {
			h := seed(t)
			if code := h.runWithKeys("j"+tc.key+"q", "sessions", "--limit", "0", "--all"); code != ExitOK {
				t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
			}
			frame := lastScreenBetween(h.stdout.String(), "Next attempt")
			if frame == "" {
				t.Fatalf("no detail screen was drawn:\n%s", h.stdout.String())
			}
			if !strings.Contains(frame, secondID) {
				t.Errorf("the detail screen is not the cursor row's:\n%s", frame)
			}
			if strings.Contains(frame, firstID) {
				t.Errorf("the detail screen shows the row above the cursor:\n%s", frame)
			}
		})
	}
}

// TestSessionsBrowserActionsDoNothingWithoutASelection proves that with an empty
// listing every action says so and reaches neither the store nor the recovery
// manager.
//
// The second subtest is the one that has teeth: the store holds a row, but the
// default enabled-only filter hides it, so the operator is looking at an empty
// list. If an action fell back to "the first session in the store" instead of the
// cursor, there would be a real session for it to act on, and every assertion
// below would catch it.
func TestSessionsBrowserActionsDoNothingWithoutASelection(t *testing.T) {
	ctx := context.Background()
	actions := []struct{ name, key string }{
		{"enable", "e"},
		{"disable", "d"},
		{"retry", "r"},
		{"recover", "R"},
		{"delete", "x"},
		{"detail", "\r"},
		{"detail-i", "i"},
	}

	// noProviderAction fails the test if the browser reached the discovery or
	// resume paths, which are the only calls 'r', 'R' and 'n' can make.
	noProviderAction := func(t *testing.T, h *harness) {
		t.Helper()
		for _, call := range h.provider.Calls() {
			if call.Name == "ResumeSession" || call.Name == "Discover" {
				t.Errorf("the browser called the provider's %s with no session selected", call.Name)
			}
		}
	}
	// sawNoSelection checks the shared half of both subtests: the message, and the
	// absence of a confirm screen or a detail page.
	sawNoSelection := func(t *testing.T, h *harness) {
		t.Helper()
		screen := lastScreen(h.stdout.String())
		if !strings.Contains(screen, "no session selected") {
			t.Errorf("no 'no session selected' message:\n%s", screen)
		}
		if strings.Contains(screen, "y/N") {
			t.Errorf("an empty list still opened the delete confirmation:\n%s", screen)
		}
		if strings.Contains(h.stdout.String(), "Next attempt") {
			t.Errorf("an empty list still opened a detail page:\n%s", h.stdout.String())
		}
	}

	t.Run("empty store", func(t *testing.T) {
		for _, tc := range actions {
			t.Run(tc.name, func(t *testing.T) {
				h := newHarness(t)
				if code := h.runWithKeys(tc.key+"q", "sessions"); code != ExitOK {
					t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
				}
				sawNoSelection(t, h)
				noProviderAction(t, h)
				sessions, err := h.store.ListSessions(ctx, storage.SessionFilter{})
				if err != nil {
					t.Fatalf("list sessions: %v", err)
				}
				if len(sessions) != 0 {
					t.Errorf("the browser created %d session(s) out of nothing", len(sessions))
				}
			})
		}
	})

	t.Run("hidden by the enabled filter", func(t *testing.T) {
		for _, tc := range actions {
			t.Run(tc.name, func(t *testing.T) {
				h := newHarness(t)
				h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
					s.Enabled = false
					s.State = storage.StateDisabled
				})
				if code := h.runWithKeys(tc.key+"q", "sessions"); code != ExitOK {
					t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
				}
				if screen := lastScreen(h.stdout.String()); !strings.Contains(screen, "no sessions match") {
					t.Fatalf("the listing was not empty:\n%s", screen)
				}
				sawNoSelection(t, h)
				noProviderAction(t, h)

				session, err := h.store.GetSession(ctx, canonicalSessionID)
				if err != nil {
					t.Fatalf("get session: %v", err)
				}
				if session.Enabled || session.State != storage.StateDisabled {
					t.Errorf("the hidden row was touched: enabled = %v, state = %s", session.Enabled, session.State)
				}
				attempts, err := h.store.ListAttempts(ctx, canonicalSessionID, 0)
				if err != nil {
					t.Fatalf("list attempts: %v", err)
				}
				if len(attempts) != 0 {
					t.Errorf("the hidden row gained %d attempt(s)", len(attempts))
				}
				if _, err := h.store.GetDeletedSession(ctx, canonicalSessionID); err == nil {
					t.Errorf("the hidden row was forgotten")
				}
			})
		}
	})
}

// TestSessionsBrowserClampsTheCursorAtBothEnds proves the selection stops at the
// first and last row instead of running off either end, that the page follows the
// cursor, and that a reload which shortens the list pulls the cursor back inside
// it.
//
// Running off an end is the failure that turns into a panic: selected() indexes
// the slice, so a cursor of -1 or len(sessions) would either crash the browser or
// silently act on the wrong row.
func TestSessionsBrowserClampsTheCursorAtBothEnds(t *testing.T) {
	// seedRows seeds n sessions whose ids sort in seed order, so the drawn order is
	// "00000000", "00000001", … — the listing is last_seen_at DESC, id ASC and all
	// seeds share the harness clock.
	seedRows := func(t *testing.T, n int) *harness {
		t.Helper()
		h := newHarness(t)
		for i := 0; i < n; i++ {
			h.seedSession(t, fmt.Sprintf("%08d-0000-4000-8000-000000000000", i), nil)
		}
		return h
	}
	run := func(t *testing.T, h *harness, keys string, args ...string) string {
		t.Helper()
		if code := h.runWithKeys(keys, args...); code != ExitOK {
			t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
		}
		return lastScreen(h.stdout.String())
	}

	t.Run("up at the top stays on the first row", func(t *testing.T) {
		for _, keys := range []string{"kq", "kkkkq", "\x1b[Aq"} {
			h := seedRows(t, 3)
			screen := run(t, h, keys, "sessions", "--limit", "0", "--all")
			if got, want := markedID(screen), listedIDs(screen)[0]; got != want {
				t.Errorf("after %q the cursor is on %q, want the first row %q:\n%s", keys, got, want, screen)
			}
		}
	})

	t.Run("down past the end stays on the last row", func(t *testing.T) {
		for _, keys := range []string{"jjjjjjjjq", "Gjq", "\x1b[B\x1b[B\x1b[B\x1b[Bq"} {
			h := seedRows(t, 3)
			screen := run(t, h, keys, "sessions", "--limit", "0", "--all")
			ids := listedIDs(screen)
			if len(ids) != 3 {
				t.Fatalf("listed %d rows, want 3:\n%s", len(ids), screen)
			}
			if got, want := markedID(screen), ids[len(ids)-1]; got != want {
				t.Errorf("after %q the cursor is on %q, want the last row %q:\n%s", keys, got, want, screen)
			}
		}
	})

	t.Run("the page follows the cursor", func(t *testing.T) {
		// Twenty rows is more than tuiPageSize, so the footer reports the window and
		// the offset has to move with the cursor instead of staying at the top.
		h := seedRows(t, 20)
		screen := run(t, h, "Gq", "sessions", "--limit", "0", "--all")
		if got, want := markedID(screen), "00000019"; got != want {
			t.Errorf("after 'G' the cursor is on %q, want the last row %q:\n%s", got, want, screen)
		}
		if !strings.Contains(screen, "rows 6-20 of 20") {
			t.Errorf("the last page does not end on the last row:\n%s", screen)
		}

		h = seedRows(t, 20)
		screen = run(t, h, "gq", "sessions", "--limit", "0", "--all")
		if got, want := markedID(screen), "00000000"; got != want {
			t.Errorf("after 'g' the cursor is on %q, want the first row %q:\n%s", got, want, screen)
		}
		if !strings.Contains(screen, "rows 1-15 of 20") {
			t.Errorf("the first page does not start at the first row:\n%s", screen)
		}
	})

	t.Run("a reload that shortens the list pulls the cursor in", func(t *testing.T) {
		// Two enabled rows and the cursor on the last one. Disabling it removes the
		// row from the default enabled-only listing, so the cursor would point past
		// the end unless reload clamps it.
		h := seedRows(t, 2)
		screen := run(t, h, "Gdq", "sessions", "--limit", "0")
		ids := listedIDs(screen)
		if len(ids) != 1 {
			t.Fatalf("listed %d rows after the disable, want 1:\n%s", len(ids), screen)
		}
		if got, want := markedID(screen), ids[0]; got != want {
			t.Errorf("the cursor did not follow the shortened list: on %q, want %q:\n%s", got, want, screen)
		}
	})

	t.Run("the cursor is still usable after the clamp", func(t *testing.T) {
		// Clamping must not wedge the selection: after running into an end, a move in
		// the other direction has to work again. Four 'j' presses from row 0 of three
		// stop on the last row; one 'k' then steps back to the middle one.
		h := seedRows(t, 3)
		screen := run(t, h, "jjjjkq", "sessions", "--limit", "0", "--all")
		ids := listedIDs(screen)
		if got, want := markedID(screen), ids[len(ids)-2]; got != want {
			t.Errorf("after running into the end and back the cursor is on %q, want %q:\n%s", got, want, screen)
		}
	})
}
