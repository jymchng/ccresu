package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
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
	h.seedSession(t, "aaaaaaaa-1111-4111-8111-111111111111", func(s *storage.Session) { s.Name = "flaky build" })
	h.seedSession(t, "bbbbbbbb-2222-4222-8222-222222222222", func(s *storage.Session) { s.Name = "quiet run" })

	code := h.runWithKeys("/flaky\rq", "sessions", "--limit", "0", "--all")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	screen := lastScreen(h.stdout.String())
	if !strings.Contains(screen, "flaky build") {
		t.Errorf("filtered screen dropped the matching row:\n%s", screen)
	}
	if strings.Contains(screen, "quiet run") {
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
	if !strings.Contains(screen, "flaky build") || !strings.Contains(screen, "quiet run") {
		t.Errorf("escape did not cancel the filter:\n%s", screen)
	}

	// 'c' clears an applied filter.
	code = h.runWithKeys("/quiet\rcq", "sessions", "--limit", "0", "--all")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	screen = lastScreen(h.stdout.String())
	if !strings.Contains(screen, "flaky build") || strings.Contains(screen, `filter "quiet"`) {
		t.Errorf("'c' did not clear the filter:\n%s", screen)
	}
}

func TestSessionsBrowserTogglesAll(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.Enabled = false
		s.State = storage.StateDisabled
		s.Name = "hidden one"
	})

	// A bare listing is enabled-only, so the disabled row is not there until 'a'.
	code := h.runWithKeys("q", "sessions")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	if screen := lastScreen(h.stdout.String()); strings.Contains(screen, "hidden one") {
		t.Errorf("a bare browser listing showed the disabled row:\n%s", screen)
	}

	code = h.runWithKeys("aq", "sessions")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	screen := lastScreen(h.stdout.String())
	if !strings.Contains(screen, "hidden one") {
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
				jobID := claude.FakeJobID(canonicalSessionID)
				h.provider.SetJob(claude.AgentEntry{
					StateID: jobID, SessionID: canonicalSessionID, Kind: "background", State: "done",
				})
				h.provider.Statuses[canonicalSessionID] = claude.SessionStatus{
					Session:    resumed,
					Transcript: claude.TranscriptInfo{Path: "/tmp/t.jsonl", Size: 4096, Lines: 42},
				}
				return claude.ResumeResult{
					RequestedSessionID:    canonicalSessionID,
					JobID:                 jobID,
					ContinuationSessionID: canonicalSessionID,
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
