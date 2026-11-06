package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jymchng/ccresu/internal/recovery"
	"github.com/jymchng/ccresu/internal/storage"
)

// The sessions browser is a hand-written ANSI loop rather than a TUI framework.
//
// A framework would be a new module in a program whose only direct dependencies
// are a YAML parser and a pure-Go SQLite driver, and the browser needs exactly
// three things from the terminal: clear the screen, hide the cursor and read one
// keystroke at a time. Those are escape sequences and one ioctl apiece, and the
// ioctl lives behind build tags (tui_term_*.go), so nothing platform-specific
// reaches this file.
//
// The browser is deliberately a *view* over the same store every other command
// uses: it lists ccresu's own rows, and its actions are the exact store and
// manager calls `ccresu enable`, `disable`, `retry`, `recover` and
// `sessions delete` make. It never creates a session and never touches anything
// under ~/.claude, so "delete" here means what it means in the CLI: forget the
// row, keep the conversation.
const (
	// tuiPageSize is how many session rows one screen shows. A terminal size
	// query would be more precise, but a fixed page keeps the drawing code (and
	// therefore the tests) independent of the terminal it runs in, and fifteen
	// rows is more than a 24-line terminal can show anyway once the header,
	// status and key map are drawn.
	tuiPageSize = 15

	tuiAltScreenOn  = "\x1b[?1049h"
	tuiAltScreenOff = "\x1b[?1049l"
	tuiHideCursor   = "\x1b[?25l"
	tuiShowCursor   = "\x1b[?25h"
	tuiClearScreen  = "\x1b[H\x1b[2J"
)

// The browser's screens. They are a small state machine: keys mean different
// things per screen, and only the list screen performs an action.
const (
	tuiModeList    = "list"
	tuiModeFilter  = "filter"
	tuiModeDetail  = "detail"
	tuiModeHelp    = "help"
	tuiModeConfirm = "confirm"
)

// keyEvent is one keystroke, decoded from the terminal's byte stream.
type keyEvent struct {
	// name is a symbolic key (up, down, enter, esc, backspace, interrupt, eof)
	// or empty when the keystroke is the rune in r.
	name string
	r    rune
}

// The symbolic key names. Anything not listed is carried as a rune.
const (
	keyUp        = "up"
	keyDown      = "down"
	keyHome      = "home"
	keyEnd       = "end"
	keyEnter     = "enter"
	keyEsc       = "esc"
	keyBackspace = "backspace"
	keyInterrupt = "interrupt"
	keyEOF       = "eof"
)

// String renders the keystroke for a status message or a test failure.
func (k keyEvent) String() string {
	if k.name != "" {
		return k.name
	}
	return string(k.r)
}

// decodeKey reads one keystroke.
//
// A terminal in raw mode delivers arrow keys as the three-byte sequences
// ESC [ A/B/C/D, so the decoder has to look ahead: a lone ESC is the escape key,
// while ESC [ ... is a control sequence whose final letter names the key. Any
// sequence it does not know decodes to an empty keyEvent, which every screen
// ignores.
func decodeKey(in *bufio.Reader) (keyEvent, error) {
	b, err := in.ReadByte()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return keyEvent{name: keyEOF}, nil
		}
		return keyEvent{}, err
	}
	switch b {
	case 0x03: // Ctrl-C: raw mode carries it as a byte, not as a signal.
		return keyEvent{name: keyInterrupt}, nil
	case '\r', '\n':
		return keyEvent{name: keyEnter}, nil
	case 0x7f, 0x08:
		return keyEvent{name: keyBackspace}, nil
	case 0x1b:
		return decodeEscape(in)
	}
	if b < 0x20 {
		return keyEvent{}, nil
	}
	return keyEvent{r: rune(b)}, nil
}

// decodeEscape reads what follows an ESC: nothing (the escape key) or a control
// sequence such as ESC [ B for the down arrow.
func decodeEscape(in *bufio.Reader) (keyEvent, error) {
	next, err := in.ReadByte()
	if err != nil {
		return keyEvent{name: keyEsc}, nil
	}
	if next != '[' && next != 'O' {
		// ESC followed by an ordinary byte is the escape key plus that byte; the
		// simplest honest reading is a bare escape.
		_ = in.UnreadByte()
		return keyEvent{name: keyEsc}, nil
	}
	for {
		c, err := in.ReadByte()
		if err != nil {
			return keyEvent{name: keyEsc}, nil
		}
		switch c {
		case 'A':
			return keyEvent{name: keyUp}, nil
		case 'B':
			return keyEvent{name: keyDown}, nil
		case 'H':
			return keyEvent{name: keyHome}, nil
		case 'F':
			return keyEvent{name: keyEnd}, nil
		case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', ';':
			// A parameterised sequence (ESC [ 1 ; 5 B and friends): keep reading
			// until the final letter.
			continue
		case 'C', 'D':
			// Left and right mean the same as up and down in a one-column list.
			if c == 'C' {
				return keyEvent{name: keyDown}, nil
			}
			return keyEvent{name: keyUp}, nil
		default:
			if c >= '@' && c <= '~' {
				return keyEvent{}, nil
			}
		}
	}
}

// browserOptions is what the flag set asked for: the same --all, --state and
// --limit the table honours, so the browser never shows a different set.
type browserOptions struct {
	All    bool
	States []storage.State
	Limit  int
}

// browser is one interactive session listing.
type browser struct {
	app *App
	rt  *Runtime
	out io.Writer
	in  *bufio.Reader

	opts browserOptions
	// all is the live --all toggle ('a'); it starts at opts.All.
	all bool
	// search is the applied free-text filter, and filterInput is the text being
	// typed on the filter screen. Keeping them apart is what makes Esc a real
	// cancel.
	search      string
	filterInput string

	mode   string
	cursor int
	offset int
	status string

	// sessions is the filtered view, newest first, and schedules holds the
	// stored wait for each session so NEXT ATTEMPT can be rendered.
	sessions  []storage.Session
	schedules map[string]storage.Schedule
	now       time.Time

	// detail holds the rendered `sessions status` page of the inspected session
	// and detailOffset its scroll position.
	detail       []string
	detailOffset int

	// pendingDelete is the session id the confirm screen is asking about.
	pendingDelete string
}

// isTerminalWriter reports whether w is an interactive terminal.
//
// It asks the file itself rather than consulting the environment, so redirecting
// stdout to a file or a pipe automatically selects the table: a script that runs
// `ccresu sessions` gets the same bytes it always did.
func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// resolveSessionsUI decides whether `sessions` should open the browser.
//
// Precedence, from strongest to weakest: an explicit --tui or --no-tui, then
// --json (a machine-readable document can never be a full-screen browser), then
// the terminal itself.
func (a *App) resolveSessionsUI(force, never bool) (bool, error) {
	if force && never {
		return false, usagef("--tui and --no-tui are mutually exclusive")
	}
	if force && a.json {
		return false, usagef("--tui cannot be combined with --json; --json output is for scripts")
	}
	if force {
		return true, nil
	}
	if never || a.json {
		return false, nil
	}
	return a.isTerminal(a.stdout), nil
}

// runSessionBrowser runs the interactive listing until the operator quits.
func (a *App) runSessionBrowser(ctx context.Context, rt *Runtime, opts browserOptions) error {
	b := &browser{
		app:  a,
		rt:   rt,
		out:  a.stdout,
		in:   bufio.NewReader(a.tuiInput),
		opts: opts,
		all:  opts.All,
		mode: tuiModeList,
	}
	// Leaving the alternate screen and re-showing the cursor on every exit path,
	// including a failure, is what keeps a crashed browser from destroying the
	// operator's terminal.
	fmt.Fprint(b.out, tuiAltScreenOn+tuiHideCursor)
	defer fmt.Fprint(b.out, tuiShowCursor+tuiAltScreenOff)
	if f, ok := a.tuiInput.(*os.File); ok {
		if restore, err := enterRawMode(f); err == nil {
			defer restore()
		}
		// A terminal that refuses raw mode still works: the operator types a key
		// and presses Enter, which is exactly what the decoder can read too.
	}

	if err := b.reload(ctx); err != nil {
		return err
	}
	b.draw()
	for {
		key, err := decodeKey(b.in)
		if err != nil {
			return fmt.Errorf("read keystroke: %w", err)
		}
		if key.name == keyEOF {
			return nil
		}
		quit, err := b.handle(ctx, key)
		if err != nil {
			// An action that failed is reported on the status line and the
			// browser keeps running: quitting on a refused recover would throw
			// away the operator's place in the list.
			b.status = err.Error()
		}
		if quit {
			return nil
		}
		b.draw()
	}
}

// reload re-reads the store and re-applies the filters.
//
// Every action ends here rather than patching the in-memory row, so the browser
// can never show a state the database does not have.
func (b *browser) reload(ctx context.Context) error {
	filter := storage.SessionFilter{NewestFirst: true, States: b.opts.States}
	if !b.all {
		enabled := true
		filter.Enabled = &enabled
	}
	if b.opts.Limit > 0 {
		filter.Limit = b.opts.Limit
	}
	sessions, err := b.rt.Store.ListSessions(ctx, filter)
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}
	if b.search != "" {
		needle := strings.ToLower(b.search)
		kept := make([]storage.Session, 0, len(sessions))
		for _, session := range sessions {
			if matchesSearch(session, needle) {
				kept = append(kept, session)
			}
		}
		sessions = kept
	}
	schedules, err := b.rt.Store.LoadSchedules(ctx)
	if err != nil {
		return fmt.Errorf("load schedules: %w", err)
	}
	b.sessions = sessions
	b.schedules = make(map[string]storage.Schedule, len(schedules))
	for _, schedule := range schedules {
		b.schedules[schedule.SessionID] = schedule
	}
	b.now = b.rt.Clock.Now()
	if b.cursor >= len(b.sessions) {
		b.cursor = len(b.sessions) - 1
	}
	if b.cursor < 0 {
		b.cursor = 0
	}
	b.clampOffset()
	return nil
}

// matchesSearch reports whether a session mentions needle anywhere the list
// shows it, so `/flaky` finds a row by name, project, state or note.
func matchesSearch(session storage.Session, needle string) bool {
	fields := []string{
		session.ID, session.ContinuationSessionID, session.Name,
		session.ProjectPath, string(session.State), session.Reason,
		session.LastError, session.JobID,
	}
	for _, field := range fields {
		if field != "" && strings.Contains(strings.ToLower(field), needle) {
			return true
		}
	}
	return false
}

// clampOffset keeps the cursor on screen.
func (b *browser) clampOffset() {
	if b.cursor < b.offset {
		b.offset = b.cursor
	}
	if b.cursor >= b.offset+tuiPageSize {
		b.offset = b.cursor - tuiPageSize + 1
	}
	if b.offset < 0 {
		b.offset = 0
	}
}

// selected returns the highlighted session.
func (b *browser) selected() (storage.Session, bool) {
	if b.cursor < 0 || b.cursor >= len(b.sessions) {
		return storage.Session{}, false
	}
	return b.sessions[b.cursor], true
}

// handle processes one keystroke on the current screen and reports whether the
// browser should quit.
//
// The switch on mode first and key second is the key map the documentation
// reproduces: a key that acts on the list is written once, per screen.
func (b *browser) handle(ctx context.Context, key keyEvent) (bool, error) {
	switch b.mode {
	case tuiModeFilter:
		return false, b.handleFilter(key)
	case tuiModeDetail:
		return false, b.handleDetail(key)
	case tuiModeHelp:
		return false, b.handleHelp(key)
	case tuiModeConfirm:
		return b.handleConfirm(ctx, key)
	default:
		return b.handleList(ctx, key)
	}
}

// handleList is the primary key map.
func (b *browser) handleList(ctx context.Context, key keyEvent) (bool, error) {
	switch {
	case key.name == keyInterrupt:
		return true, nil
	case key.name == keyUp:
		b.moveCursor(-1)
		return false, nil
	case key.name == keyDown:
		b.moveCursor(1)
		return false, nil
	case key.name == keyHome:
		b.cursor = 0
		b.clampOffset()
		return false, nil
	case key.name == keyEnd:
		b.cursor = len(b.sessions) - 1
		b.clampOffset()
		return false, nil
	case key.name == keyEnter:
		return false, b.openDetail(ctx)
	}
	switch key.r {
	case 'q':
		return true, nil
	case 'j':
		b.moveCursor(1)
	case 'k':
		b.moveCursor(-1)
	case 'g':
		b.cursor = 0
		b.clampOffset()
	case 'G':
		b.cursor = len(b.sessions) - 1
		b.clampOffset()
	case 'a':
		b.all = !b.all
		b.status = "showing all sessions (disabled and settled included)"
		if !b.all {
			b.status = "showing enabled sessions only"
		}
		return false, b.reload(ctx)
	case '/':
		b.mode = tuiModeFilter
		b.filterInput = b.search
	case 'c':
		b.search = ""
		b.filterInput = ""
		b.status = "filter cleared"
		return false, b.reload(ctx)
	case 'i':
		return false, b.openDetail(ctx)
	case 'e':
		return false, b.act(ctx, "enable")
	case 'd':
		return false, b.act(ctx, "disable")
	case 'r':
		return false, b.act(ctx, "retry")
	case 'R':
		return false, b.act(ctx, "recover")
	case 'x':
		session, ok := b.selected()
		if !ok {
			b.status = "no session selected"
			return false, nil
		}
		b.pendingDelete = session.ID
		b.mode = tuiModeConfirm
	case '?':
		b.mode = tuiModeHelp
	}
	return false, nil
}

// moveCursor walks the selection, stopping at both ends.
func (b *browser) moveCursor(delta int) {
	b.cursor += delta
	if b.cursor < 0 {
		b.cursor = 0
	}
	if b.cursor >= len(b.sessions) {
		b.cursor = len(b.sessions) - 1
	}
	if b.cursor < 0 {
		b.cursor = 0
	}
	b.clampOffset()
}

// handleFilter is the text-entry screen behind '/'.
func (b *browser) handleFilter(key keyEvent) error {
	switch {
	case key.name == keyInterrupt:
		b.mode = tuiModeList
	case key.name == keyEnter:
		b.search = b.filterInput
		b.mode = tuiModeList
		b.cursor, b.offset = 0, 0
		if b.search == "" {
			b.status = "filter cleared"
		} else {
			b.status = fmt.Sprintf("filter %q", b.search)
		}
		return b.reload(context.Background())
	case key.name == keyEsc:
		b.mode = tuiModeList
		b.filterInput = b.search
		b.status = "filter unchanged"
	case key.name == keyBackspace:
		if runes := []rune(b.filterInput); len(runes) > 0 {
			b.filterInput = string(runes[:len(runes)-1])
		}
	case key.r != 0:
		b.filterInput += string(key.r)
	}
	return nil
}

// handleDetail scrolls the `sessions status` page of the inspected session.
func (b *browser) handleDetail(key keyEvent) error {
	switch {
	case key.name == keyInterrupt, key.name == keyEsc, key.name == keyEnter:
		b.mode = tuiModeList
		b.detail, b.detailOffset = nil, 0
	case key.name == keyUp:
		if b.detailOffset > 0 {
			b.detailOffset--
		}
	case key.name == keyDown:
		if b.detailOffset < len(b.detail)-tuiPageSize {
			b.detailOffset++
		}
	case key.name == keyHome:
		b.detailOffset = 0
	case key.name == keyEnd:
		if len(b.detail) > tuiPageSize {
			b.detailOffset = len(b.detail) - tuiPageSize
		}
	}
	switch key.r {
	case 'q':
		b.mode = tuiModeList
		b.detail, b.detailOffset = nil, 0
	case 'j':
		if b.detailOffset < len(b.detail)-tuiPageSize {
			b.detailOffset++
		}
	case 'k':
		if b.detailOffset > 0 {
			b.detailOffset--
		}
	}
	return nil
}

// handleHelp closes the key map.
func (b *browser) handleHelp(key keyEvent) error {
	b.mode = tuiModeList
	return nil
}

// handleConfirm is the delete confirmation. Only 'y' deletes: the destructive
// action never shares a keystroke with "close this screen".
func (b *browser) handleConfirm(ctx context.Context, key keyEvent) (bool, error) {
	id := b.pendingDelete
	b.pendingDelete = ""
	b.mode = tuiModeList
	switch {
	case key.name == keyInterrupt:
		return true, nil
	case key.r == 'y' || key.r == 'Y':
		return false, b.delete(ctx, id)
	}
	b.status = "delete cancelled"
	return false, nil
}

// openDetail renders the same page `ccresu sessions status <id>` prints, so the
// browser cannot drift from the CLI.
func (b *browser) openDetail(ctx context.Context) error {
	session, ok := b.selected()
	if !ok {
		b.status = "no session selected"
		return nil
	}
	var buf bytes.Buffer
	if err := b.app.writeSessionDetail(ctx, b.rt, session.ID, &buf); err != nil {
		return err
	}
	b.detail = strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	b.detailOffset = 0
	b.mode = tuiModeDetail
	b.status = "press enter, q or esc to return to the list"
	return nil
}

// act performs one of the commands the CLI has, against the selected session.
func (b *browser) act(ctx context.Context, action string) error {
	session, ok := b.selected()
	if !ok {
		b.status = "no session selected"
		return nil
	}
	var err error
	message := ""
	switch action {
	case "enable":
		err = b.rt.Store.SetSessionEnabled(ctx, session.ID, true)
	case "disable":
		err = b.rt.Store.SetSessionEnabled(ctx, session.ID, false)
	case "retry":
		var manager *recovery.Manager
		if manager, err = b.app.requireRecovery(b.rt); err == nil {
			var outcome recovery.Outcome
			if outcome, err = manager.Retry(ctx, session.ID); err == nil {
				message = outcomeMessage(action, session.ID, outcome)
			}
		}
	case "recover":
		var manager *recovery.Manager
		if manager, err = b.app.requireRecovery(b.rt); err == nil {
			var outcome recovery.Outcome
			if outcome, err = manager.RecoverRequest(ctx, recovery.Request{SessionID: session.ID}); err == nil {
				message = outcomeMessage(action, session.ID, outcome)
			}
		}
	}
	if err != nil {
		b.status = fmt.Sprintf("%s %s: %v", action, shortID(session.ID), err)
	} else if message != "" {
		b.status = message
	} else {
		b.status = fmt.Sprintf("%s %s: done", action, shortID(session.ID))
	}
	return b.reload(ctx)
}

// delete forgets the session, exactly as `sessions delete` does: the row goes,
// the conversation under ~/.claude stays.
func (b *browser) delete(ctx context.Context, id string) error {
	if id == "" {
		b.status = "delete cancelled"
		return nil
	}
	if _, _, err := b.rt.Store.ForgetSession(ctx, id); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			// Already gone is the outcome the operator asked for.
			b.status = fmt.Sprintf("delete %s: already forgotten", shortID(id))
			return b.reload(ctx)
		}
		b.status = fmt.Sprintf("delete %s: %v", shortID(id), err)
		return nil
	}
	b.status = fmt.Sprintf("delete %s: forgotten (the conversation was kept; 'ccresu enable %s' restores the row)", shortID(id), id)
	return b.reload(ctx)
}

// outcomeMessage renders a recovery outcome for the status line.
func outcomeMessage(action, id string, outcome recovery.Outcome) string {
	resumed := "no"
	if outcome.Resumed {
		resumed = "yes"
	}
	return fmt.Sprintf("%s %s: state %s, resumed %s", action, shortID(id), outcome.State, resumed)
}

// draw repaints the screen for the current mode.
func (b *browser) draw() {
	var out strings.Builder
	out.WriteString(tuiClearScreen)
	switch b.mode {
	case tuiModeDetail:
		b.drawDetail(&out)
	case tuiModeHelp:
		b.drawHelp(&out)
	default:
		b.drawList(&out)
	}
	fmt.Fprint(b.out, out.String())
}

// drawList paints the header, the page of rows, the status line and the key map.
func (b *browser) drawList(out *strings.Builder) {
	fmt.Fprintf(out, "ccresu sessions \u2014 %d shown", len(b.sessions))
	if !b.all {
		out.WriteString(" (enabled only; 'a' shows all)")
	} else {
		out.WriteString(" (all)")
	}
	if b.search != "" {
		fmt.Fprintf(out, "  filter %q ('c' clears)", b.search)
	}
	out.WriteString("\n")

	headers, rows := b.tableContent()
	if len(rows) == 0 {
		out.WriteString("\n  no sessions match\n")
	} else {
		out.WriteString("  " + strings.Join(headers, "  ") + "\n")
		last := b.offset + tuiPageSize
		if last > len(rows) {
			last = len(rows)
		}
		for i := b.offset; i < last; i++ {
			marker := "  "
			if i == b.cursor {
				marker = "> "
			}
			out.WriteString(marker + strings.Join(rows[i], "  ") + "\n")
		}
		if len(rows) > tuiPageSize {
			fmt.Fprintf(out, "  rows %d-%d of %d\n", b.offset+1, last, len(rows))
		}
	}

	if b.mode == tuiModeFilter {
		fmt.Fprintf(out, "filter: %s_\n", b.filterInput)
	} else if b.mode == tuiModeConfirm {
		fmt.Fprintf(out, "delete %s? this forgets ccresu's row, not the conversation. y/N ", b.pendingDelete)
		out.WriteString("\n")
	}
	if b.status != "" {
		fmt.Fprintf(out, "%s\n", truncateText(b.status, 160))
	}
	out.WriteString("\n? help  / filter  i inspect  e enable  d disable  r retry  R recover  x delete  a all  q quit\n")
}

// tableContent renders the header and the rows the list screen shows.
//
// The columns are the table's, minus CONTINUATION: it is a 36-character UUID and
// the browser is the one place with a fixed width, so it is shown on the detail
// screen instead of squeezing the rest of the row.
func (b *browser) tableContent() ([]string, [][]string) {
	loc := b.app.displayLocation(b.rt)
	headers := []string{"SESSION", "NAME", "STATE", "NEXT ATTEMPT", "ENABLED", "PROJECT", "LAST SEEN", "NOTE"}
	rows := make([][]string, 0, len(b.sessions))
	for _, session := range b.sessions {
		note := session.Reason
		if note == "" {
			note = session.LastError
		}
		next := nextAttemptCell(recovery.NextAttemptFor(session, schedulePtr(b.schedules, session.ID), b.now), b.now)
		rows = append(rows, []string{
			shortID(session.ID),
			emptyDash(truncateText(session.Name, 20)),
			string(session.State),
			next,
			yesNo(session.Enabled),
			emptyDash(truncateText(session.ProjectPath, 28)),
			formatTime(session.LastSeenAt, loc),
			emptyDash(truncateText(note, 28)),
		})
	}
	widths := make([]int, len(headers))
	for i, header := range headers {
		widths[i] = len(header)
	}
	for _, row := range rows {
		for i, cell := range row {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	pad := func(row []string) []string {
		padded := make([]string, len(row))
		for i, cell := range row {
			padded[i] = fmt.Sprintf("%-*s", widths[i], cell)
		}
		return padded
	}
	paddedHeader := pad(headers)
	paddedRows := make([][]string, 0, len(rows))
	for _, row := range rows {
		paddedRows = append(paddedRows, pad(row))
	}
	return paddedHeader, paddedRows
}

// drawDetail paints one session's `sessions status` page.
func (b *browser) drawDetail(out *strings.Builder) {
	out.WriteString("ccresu sessions \u2014 inspect\n\n")
	last := b.detailOffset + tuiPageSize
	if last > len(b.detail) {
		last = len(b.detail)
	}
	for i := b.detailOffset; i < last; i++ {
		out.WriteString(b.detail[i] + "\n")
	}
	if len(b.detail) > tuiPageSize {
		fmt.Fprintf(out, "\nlines %d-%d of %d\n", b.detailOffset+1, last, len(b.detail))
	}
	if b.status != "" {
		fmt.Fprintf(out, "\n%s\n", truncateText(b.status, 160))
	}
}

// drawHelp paints the key map, from the same table the documentation quotes.
func (b *browser) drawHelp(out *strings.Builder) {
	out.WriteString("ccresu sessions \u2014 keys\n\n")
	for _, binding := range tuiKeyMap() {
		fmt.Fprintf(out, "  %-12s %s\n", binding.keys, binding.meaning)
	}
	out.WriteString("\npress any key to return\n")
}

// keyBinding is one documented key map row.
type keyBinding struct {
	keys    string
	meaning string
}

// tuiKeyMap is the authoritative key map: the help screen renders it and the
// documentation reproduces it, so the two cannot disagree.
func tuiKeyMap() []keyBinding {
	return []keyBinding{
		{"j / down", "next session"},
		{"k / up", "previous session"},
		{"g / home", "first session"},
		{"G / end", "last session"},
		{"/", "filter by text (enter applies, esc cancels)"},
		{"c", "clear the filter"},
		{"a", "toggle --all (disabled and settled sessions)"},
		{"i / enter", "inspect the selected session"},
		{"e", "enable the selected session"},
		{"d", "disable the selected session"},
		{"r", "retry the selected session"},
		{"R", "recover the selected session now"},
		{"x", "delete the selected session (asks y/N)"},
		{"?", "this key map"},
		{"q / ctrl-c", "quit"},
	}
}
