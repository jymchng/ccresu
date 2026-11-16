package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
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
// `sessions delete` make. Its one additive action, 'n', runs the daemon's
// discovery stage — Provider.Discover into UpsertSession — so it can only ever
// record conversations that already exist on disk. It never starts a Claude
// conversation and never touches anything under ~/.claude, so "delete" here means
// what it means in the CLI: forget the row, keep the conversation.
//
// # One loop, three goroutines
//
// The browser answers the keyboard, not `claude`. Four of its keys run a command
// that invokes the CLI — enter and 'i' render the `sessions status` page, which
// reads the supervisor on demand; 'n' runs a discovery pass; 'r' and 'R' run a
// whole recovery — and any of those can take seconds. So the work never runs on
// the goroutine that paints:
//
//	terminal ──► readKeys (bytes, then keys) ──► loop ──► draw
//	                                              │
//	                     startAction ─────────────┴──► worker ──► results
//	                     startFill  ────────────────────────────► fills
//
// The loop selects over the keyboard, finished actions, finished supervisor
// reads, the context and a hangup channel. It is the only thing that paints and
// the only thing that touches the browser's state, so the screens cannot race
// each other. `ctrl-c` reaches it as a byte (ISIG is cleared) and cancels the
// action in flight; a signal reaches it as a hangup, which is the one exit path
// the deferred screen restore cannot otherwise be given.
const (
	// tuiPageSize is how many session rows one screen shows. A terminal size
	// query would be more precise, but a fixed page keeps the drawing code (and
	// therefore the tests) independent of the terminal it runs in, and fifteen
	// rows is more than a 24-line terminal can show anyway once the header,
	// status and key map are drawn.
	tuiPageSize = 15

	// tuiEscapeTimeout is how long a lone escape waits for the byte that would
	// make it a control sequence. A terminal sends ESC on its own and then waits
	// for the operator, so without a bound the key is only decoded when the next
	// keystroke arrives — which is what made `esc` look broken on the detail
	// screen.
	tuiEscapeTimeout = 50 * time.Millisecond

	// tuiSupervisorBudget bounds the background supervisor read behind the detail
	// screen. It is a fill, not a wait: the page is painted from the store first,
	// so the budget only decides how long the block says "reading…" before it
	// says it could not read. A healthy `claude daemon status` costs about a
	// second; a wedged supervisor is bounded by the CLI's own query timeout.
	tuiSupervisorBudget = 5 * time.Second

	tuiAltScreenOn  = "\x1b[?1049h"
	tuiAltScreenOff = "\x1b[?1049l"
	tuiHideCursor   = "\x1b[?25l"
	tuiShowCursor   = "\x1b[?25h"
	tuiClearScreen  = "\x1b[H\x1b[2J"
)

// The browser's screens. They are a small state machine: keys mean different
// things per screen, and only the list screen starts an action.
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

// keyDecoder turns a terminal byte stream into keystrokes.
//
// A decoder that looks ahead for the byte after ESC — which is what reading an
// escape sequence means — cannot tell "the operator pressed escape" from "the
// first byte of an arrow key" until the next keystroke arrives, and a terminal
// sends a lone ESC immediately and then waits. So this decoder keeps an escape
// *pending* instead of blocking: the caller decides it with a timer
// (tuiEscapeTimeout), which is portable to every platform and needs no read
// deadline — os.Stdin on a Linux terminal cannot carry one ("file type does not
// support deadline").
type keyDecoder struct {
	// escape is set while an escape sequence is incomplete.
	escape bool
	// introduced is set once the byte after ESC was '[' or 'O', which is what
	// separates a control sequence from an escape followed by a key.
	introduced bool
}

// feed consumes one byte and reports the keystrokes it completed.
//
// complete reports whether the keystroke is finished: an unknown control
// sequence finishes with no keystroke at all, which is how shift-tab and the
// rest stay inert instead of turning into the rune that ends them.
func (d *keyDecoder) feed(b byte) (events []keyEvent, complete bool) {
	if d.escape {
		if !d.introduced {
			if b == '[' || b == 'O' {
				d.introduced = true
				return nil, false
			}
			// ESC followed by an ordinary byte is the escape key plus that byte.
			d.reset()
			events := []keyEvent{{name: keyEsc}}
			if b == 0x1b {
				// ESC ESC: the first is the escape key, the second starts a new
				// sequence.
				d.escape = true
				return events, true
			}
			return append(events, d.ordinary(b)...), true
		}
		switch b {
		case 'A':
			d.reset()
			return []keyEvent{{name: keyUp}}, true
		case 'B':
			d.reset()
			return []keyEvent{{name: keyDown}}, true
		case 'H':
			d.reset()
			return []keyEvent{{name: keyHome}}, true
		case 'F':
			d.reset()
			return []keyEvent{{name: keyEnd}}, true
		case 'C', 'D':
			// Left and right mean the same as up and down in a one-column list.
			d.reset()
			if b == 'C' {
				return []keyEvent{{name: keyDown}}, true
			}
			return []keyEvent{{name: keyUp}}, true
		case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', ';':
			// A parameterised sequence (ESC [ 1 ; 5 B and friends): keep reading
			// until the final letter.
			return nil, false
		}
		if b >= '@' && b <= '~' {
			// A final byte this browser has no binding for.
			d.reset()
			return nil, true
		}
		return nil, false
	}
	if b == 0x1b {
		d.escape, d.introduced = true, false
		return nil, false
	}
	// Every other byte finishes a keystroke, even when it produces none: a
	// control byte this browser has no binding for is inert, not pending.
	return d.ordinary(b), true
}

// ordinary decodes one byte that is not part of an escape sequence.
func (d *keyDecoder) ordinary(b byte) []keyEvent {
	switch b {
	case 0x03: // Ctrl-C: raw mode carries it as a byte, not as a signal.
		return []keyEvent{{name: keyInterrupt}}
	case '\r', '\n':
		return []keyEvent{{name: keyEnter}}
	case 0x7f, 0x08:
		return []keyEvent{{name: keyBackspace}}
	}
	if b < 0x20 {
		return nil
	}
	return []keyEvent{{r: rune(b)}}
}

// waiting reports whether an escape is pending a decision.
func (d *keyDecoder) waiting() bool { return d.escape }

// flush decides a pending escape, which is what a lone ESC turns out to be.
func (d *keyDecoder) flush() (keyEvent, bool) {
	if !d.escape {
		return keyEvent{}, false
	}
	d.reset()
	return keyEvent{name: keyEsc}, true
}

// reset forgets any escape in progress.
func (d *keyDecoder) reset() { d.escape, d.introduced = false, false }

// readKeys decodes keystrokes from in and sends them on keys until the input ends
// or the browser is done.
//
// The terminal read is its own goroutine because a blocking read cannot be
// interrupted — not by the loop, not by a timer — and the browser must keep
// painting while it waits. That goroutine is deliberately left behind on exit: a
// read blocked on a terminal that the operator has just quit cannot be woken, and
// the process is on its way out. Nothing here touches the browser's state.
func readKeys(in *bufio.Reader, keys chan<- keyEvent, errs chan<- error, done <-chan struct{}, escapeTimeout time.Duration) {
	bytesIn := make(chan byte)
	readErrs := make(chan error, 1)
	go func() {
		defer close(bytesIn)
		for {
			b, err := in.ReadByte()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					select {
					case readErrs <- err:
					default:
					}
				}
				return
			}
			select {
			case bytesIn <- b:
			case <-done:
				return
			}
		}
	}()

	send := func(key keyEvent) bool {
		select {
		case keys <- key:
			return true
		case <-done:
			return false
		}
	}

	var decoder keyDecoder
	timer := time.NewTimer(escapeTimeout)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	for {
		select {
		case b, ok := <-bytesIn:
			if !ok {
				// End of input: a pending escape is still an escape, and then the
				// stream is over.
				if key, flushed := decoder.flush(); flushed && !send(key) {
					return
				}
				select {
				case err := <-readErrs:
					select {
					case errs <- err:
					case <-done:
					}
				default:
					send(keyEvent{name: keyEOF})
				}
				return
			}
			events, _ := decoder.feed(b)
			for _, key := range events {
				if !send(key) {
					return
				}
			}
			if decoder.waiting() {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(escapeTimeout)
			} else {
				timer.Stop()
			}
		case <-timer.C:
			if key, flushed := decoder.flush(); flushed && !send(key) {
				return
			}
		case <-done:
			return
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

// busyAction is the command in flight, and the handle that cancels it.
type busyAction struct {
	// seq identifies this action, so a result that arrives after the operator
	// cancelled it — or after it was replaced — is discarded rather than painted.
	seq    int
	label  string
	cancel context.CancelFunc
}

// actionResult is one finished action, reported to the loop.
type actionResult struct {
	seq    int
	status string
}

// detailFill is the supervisor read behind the detail screen. Its sequence number
// is what makes a late answer for a screen the operator has left discardable.
type detailFill struct {
	seq    int
	cancel context.CancelFunc
}

// supervisorFill is a finished supervisor read, reported to the loop.
type supervisorFill struct {
	seq    int
	report supervisorReport
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

	// detail is the rendered `sessions status` page of the inspected session and
	// detailOffset its scroll position. detailHead and detailTail are the store's
	// half of that page, kept so the supervisor block can be filled in later
	// without re-reading the session.
	detail       []string
	detailOffset int
	detailHead   []string
	detailTail   []string

	// pendingDelete is the session id the confirm screen is asking about.
	pendingDelete string

	// busy is the action in flight, or nil; results carries its outcome back to
	// the loop and seq makes a superseded one discardable.
	busy      *busyAction
	actionSeq int
	results   chan actionResult

	// fill is the supervisor read in flight, or nil, and fills carries its
	// outcome back to the loop.
	fill    *detailFill
	fillSeq int
	fills   chan supervisorFill

	// supervisorBudget bounds the supervisor fill and escapeTimeout a lone
	// escape. They are fields rather than constants so a test can shrink them.
	supervisorBudget time.Duration
	escapeTimeout    time.Duration

	// done is closed when the loop returns, and hangup when a signal asks it to.
	done   chan struct{}
	hangup <-chan struct{}
}

// newBrowser assembles a browser over the injected streams.
func newBrowser(a *App, rt *Runtime, in io.Reader, opts browserOptions) *browser {
	return &browser{
		app:              a,
		rt:               rt,
		out:              a.stdout,
		in:               bufio.NewReader(in),
		opts:             opts,
		all:              opts.All,
		mode:             tuiModeList,
		results:          make(chan actionResult, 1),
		fills:            make(chan supervisorFill, 1),
		supervisorBudget: tuiSupervisorBudget,
		escapeTimeout:    tuiEscapeTimeout,
		done:             make(chan struct{}),
		hangup:           make(chan struct{}),
	}
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
	b := newBrowser(a, rt, a.tuiInput, opts)
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
	// A signal is the one exit path the deferred restore cannot reach by itself:
	// the defers run when the loop returns, so a hangup is how SIGTERM and SIGHUP
	// get it there. Ctrl-C is a byte rather than a signal (ISIG is cleared), so it
	// is the loop's own cancel key and not this channel's.
	hangup, stopSignals := browserHangup()
	defer stopSignals()
	b.hangup = hangup
	return b.loop(ctx)
}

// browserHangup reports a channel closed when the process is asked to stop by a
// signal, and the function that releases the handler.
func browserHangup() (<-chan struct{}, func()) {
	hangup := make(chan struct{})
	signals := browserSignals()
	if len(signals) == 0 {
		return hangup, func() {}
	}
	received := make(chan os.Signal, 1)
	signal.Notify(received, signals...)
	stop := make(chan struct{})
	go func() {
		defer close(hangup)
		select {
		case <-received:
		case <-stop:
		}
	}()
	return hangup, func() {
		signal.Stop(received)
		close(stop)
	}
}

// loop runs the browser until the operator quits or the context ends.
//
// It is the only goroutine that paints and the only one that touches the
// browser's state, so every screen is drawn from a settled value and the keys
// cannot race the work they start.
func (b *browser) loop(ctx context.Context) error {
	defer close(b.done)
	defer func() {
		// Whatever is in flight dies with the browser: a cancelled recovery is a
		// cancelled context, not a goroutine left holding the store.
		if b.busy != nil {
			b.busy.cancel()
			b.busy = nil
		}
		b.cancelFill()
	}()

	keys := make(chan keyEvent)
	readErrs := make(chan error, 1)
	go readKeys(b.in, keys, readErrs, b.done, b.escapeTimeout)

	if err := b.reload(ctx); err != nil {
		return err
	}
	b.draw()
	for {
		select {
		case <-ctx.Done():
			// SIGINT or SIGTERM: the screen is restored by runSessionBrowser's
			// defers, and quitting is the whole of the handler.
			return nil
		case <-b.hangup:
			return nil
		case err := <-readErrs:
			return fmt.Errorf("read keystroke: %w", err)
		case key := <-keys:
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
		case result := <-b.results:
			b.finishAction(ctx, result)
		case fill := <-b.fills:
			b.applyFill(fill)
		}
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
// shows it, so `/flaky` finds a row by project, state or note.
func matchesSearch(session storage.Session, needle string) bool {
	fields := []string{
		session.ID, session.ProjectPath, session.RunsInPath, session.TranscriptPath,
		string(session.State), session.Reason, session.LastError,
		string(session.Type),
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

// setMode changes screens and clears the status line.
//
// A status line belongs to the screen that wrote it: the detail screen's "enter,
// q or esc returns" painted on the list is how the operator came to believe a key
// had done nothing.
func (b *browser) setMode(mode string) {
	if b.mode == mode {
		return
	}
	b.mode = mode
	b.status = ""
}

// handle processes one keystroke on the current screen and reports whether the
// browser should quit.
//
// The switch on mode first and key second is the key map the documentation
// reproduces: a key that acts on the list is written once, per screen.
func (b *browser) handle(ctx context.Context, key keyEvent) (bool, error) {
	if b.busy != nil {
		return b.handleBusy(key)
	}
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

// handleBusy is the key map while a command runs.
//
// The keys that start work are refused rather than queued: the terminal buffers
// keystrokes for a process that is not reading, and a queued Enter used to be
// applied to the screen the work produced. Movement stays available because it
// reads the store, not the CLI.
func (b *browser) handleBusy(key keyEvent) (bool, error) {
	switch {
	case key.name == keyInterrupt:
		b.cancelAction("cancelled")
		return false, nil
	case key.r == 'q':
		b.cancelAction("cancelled")
		return true, nil
	case key.name == keyEnter, key.r == 'i', key.r == 'n', key.r == 'e',
		key.r == 'd', key.r == 'r', key.r == 'R', key.r == 'x':
		b.refuse()
		return false, nil
	}
	if b.mode != tuiModeList {
		return false, nil
	}
	switch key.name {
	case keyUp:
		b.moveCursor(-1)
	case keyDown:
		b.moveCursor(1)
	case keyHome:
		b.cursor = 0
		b.clampOffset()
	case keyEnd:
		b.cursor = len(b.sessions) - 1
		b.clampOffset()
	}
	switch key.r {
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
	case '?':
		b.setMode(tuiModeHelp)
	}
	return false, nil
}

// refuse says why a key did nothing, because silence is what made the reported
// symptom ambiguous.
func (b *browser) refuse() {
	if b.busy == nil {
		return
	}
	b.status = fmt.Sprintf("busy: %s (ctrl-c cancels)", b.busy.label)
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
		b.setMode(tuiModeFilter)
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
		b.setMode(tuiModeConfirm)
	case 'n':
		// Creating is discovery, not invention: ccresu records what is already
		// on disk, exactly as the daemon's first stage does.
		return false, b.create(ctx)
	case '?':
		b.setMode(tuiModeHelp)
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
		b.setMode(tuiModeList)
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
		b.setMode(tuiModeList)
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
		b.closeDetail()
		return nil
	case key.name == keyUp:
		b.scrollDetail(-1)
		return nil
	case key.name == keyDown:
		b.scrollDetail(1)
		return nil
	case key.name == keyHome:
		b.detailOffset = 0
		return nil
	case key.name == keyEnd:
		b.detailOffset = b.detailLimit()
		return nil
	}
	switch key.r {
	case 'q':
		b.closeDetail()
	case 'j':
		b.scrollDetail(1)
	case 'k':
		b.scrollDetail(-1)
	case 'g':
		b.detailOffset = 0
	case 'G':
		b.detailOffset = b.detailLimit()
	}
	return nil
}

// scrollDetail moves the page by delta lines, stopping at both ends.
func (b *browser) scrollDetail(delta int) {
	b.detailOffset += delta
	b.clampDetailOffset()
}

// detailLimit is the largest scroll offset that still fills the page.
func (b *browser) detailLimit() int {
	if len(b.detail) <= tuiPageSize {
		return 0
	}
	return len(b.detail) - tuiPageSize
}

// clampDetailOffset keeps the scroll position inside the page, which matters when
// the supervisor block replaces the placeholder and the page grows.
func (b *browser) clampDetailOffset() {
	if limit := b.detailLimit(); b.detailOffset > limit {
		b.detailOffset = limit
	}
	if b.detailOffset < 0 {
		b.detailOffset = 0
	}
}

// closeDetail returns to the list and abandons the supervisor read behind it.
func (b *browser) closeDetail() {
	b.cancelFill()
	b.detail, b.detailOffset = nil, 0
	b.detailHead, b.detailTail = nil, nil
	b.setMode(tuiModeList)
}

// handleHelp closes the key map.
func (b *browser) handleHelp(key keyEvent) error {
	b.setMode(tuiModeList)
	return nil
}

// handleConfirm is the delete confirmation. Only 'y' deletes: the destructive
// action never shares a keystroke with "close this screen".
func (b *browser) handleConfirm(ctx context.Context, key keyEvent) (bool, error) {
	id := b.pendingDelete
	b.pendingDelete = ""
	b.setMode(tuiModeList)
	switch {
	case key.name == keyInterrupt:
		return true, nil
	case key.r == 'y' || key.r == 'Y':
		return false, b.delete(ctx, id)
	}
	b.status = "delete cancelled"
	return false, nil
}

// startAction hands one command to its own goroutine.
//
// The worker gets a context of its own so the operator's ctrl-c can cancel it
// without cancelling the browser, and its outcome comes back through results
// rather than being applied here.
func (b *browser) startAction(ctx context.Context, label string, run func(context.Context) string) {
	actionCtx, cancel := context.WithCancel(ctx)
	b.actionSeq++
	seq := b.actionSeq
	b.busy = &busyAction{seq: seq, label: label, cancel: cancel}
	go func() {
		status := run(actionCtx)
		cancel()
		select {
		case b.results <- actionResult{seq: seq, status: status}:
		case <-b.done:
		}
	}()
}

// finishAction applies one finished action to the screen.
func (b *browser) finishAction(ctx context.Context, result actionResult) {
	if b.busy == nil || b.busy.seq != result.seq {
		// The operator cancelled it, or started another one: this outcome is for a
		// command nobody is waiting for any more.
		return
	}
	b.busy.cancel()
	b.busy = nil
	b.status = result.status
	if err := b.reload(ctx); err != nil {
		b.status = err.Error()
	}
	b.draw()
}

// cancelAction stops the action in flight and says so.
func (b *browser) cancelAction(status string) {
	if b.busy == nil {
		return
	}
	b.busy.cancel()
	b.busy = nil
	b.status = status
}

// actionLabel is what the busy line calls an action.
func actionLabel(action string) string {
	switch action {
	case "enable":
		return "enabling"
	case "disable":
		return "disabling"
	case "retry":
		return "retrying"
	case "recover":
		return "recovering"
	case "create":
		return "discovering"
	case "delete":
		return "deleting"
	}
	return action
}

// act performs one of the commands the CLI has, against the selected session.
//
// `enable` and `disable` are one store write and stay on this goroutine, like the
// movement keys' reload; `retry` and `recover` run a command that invokes the CLI
// and are handed to a worker.
func (b *browser) act(ctx context.Context, action string) error {
	session, ok := b.selected()
	if !ok {
		b.status = "no session selected"
		return nil
	}
	switch action {
	case "enable", "disable":
		if b.busy != nil {
			b.refuse()
			return nil
		}
		err := b.rt.Store.SetSessionEnabled(ctx, session.ID, action == "enable")
		if err != nil {
			b.status = fmt.Sprintf("%s %s: %v", action, shortID(session.ID), err)
		} else {
			b.status = fmt.Sprintf("%s %s: done", action, shortID(session.ID))
		}
		return b.reload(ctx)
	case "retry", "recover":
		if b.busy != nil {
			b.refuse()
			return nil
		}
		b.startAction(ctx, actionLabel(action)+" "+shortID(session.ID)+"\u2026", func(ctx context.Context) string {
			var manager *recovery.Manager
			var err error
			message := ""
			if manager, err = b.app.requireRecovery(b.rt); err == nil {
				var outcome recovery.Outcome
				if action == "retry" {
					outcome, err = manager.Retry(ctx, session.ID)
				} else {
					outcome, err = manager.RecoverRequest(ctx, recovery.Request{SessionID: session.ID, Manual: true})
				}
				if err == nil {
					message = outcomeMessage(action, session.ID, outcome)
				}
			}
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return "cancelled"
				}
				return fmt.Sprintf("%s %s: %v", action, shortID(session.ID), err)
			}
			if message != "" {
				return message
			}
			return fmt.Sprintf("%s %s: done", action, shortID(session.ID))
		})
	}
	return nil
}

// delete forgets the session, exactly as `sessions delete` does: the row goes,
// the conversation under ~/.claude stays.
func (b *browser) delete(ctx context.Context, id string) error {
	if b.busy != nil {
		b.refuse()
		return nil
	}
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

// create adds the sessions the provider can see but ccresu does not know yet.
//
// It runs the same two calls the daemon's discovery stage runs —
// Provider.Discover then Store.UpsertSession — and honours the delete
// tombstones, so pressing 'n' can never resurrect a session the operator forgot.
// Nothing is started and nothing under ~/.claude is touched: this is ccresu
// catching up with the conversations already on disk, which is the only sense in
// which a session can be "created" here.
//
// Discovery asks the CLI twice (`--version`, then `agents --json`), so it is a
// worker action like a recovery.
func (b *browser) create(ctx context.Context) error {
	if b.busy != nil {
		b.refuse()
		return nil
	}
	if b.rt.Provider == nil || b.rt.Store == nil {
		b.status = "create: discovery is unavailable in this runtime"
		return nil
	}
	b.startAction(ctx, actionLabel("create")+"\u2026", func(ctx context.Context) string {
		discovered, err := b.rt.Provider.Discover(ctx, claude.DiscoverFilter{})
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return "cancelled"
			}
			return fmt.Sprintf("create: discover sessions: %v", err)
		}
		deleted, err := b.rt.Store.DeletedSessionIDs(ctx)
		if err != nil {
			return fmt.Sprintf("create: list deleted sessions: %v", err)
		}
		added, forgotten := 0, 0
		for _, session := range discovered {
			if _, gone := deleted[session.ID]; gone {
				forgotten++
				continue
			}
			created, err := b.rt.Store.UpsertSession(ctx, storageSession(session))
			if err != nil {
				return fmt.Sprintf("create: persist session %s: %v", shortID(session.ID), err)
			}
			if created {
				added++
			}
		}
		status := fmt.Sprintf("create: %d new session(s) of %d discovered", added, len(discovered))
		if forgotten > 0 {
			status += fmt.Sprintf(", %d left forgotten", forgotten)
		}
		return status
	})
	return nil
}

// openDetail renders the same page `ccresu sessions status <id>` prints, so the
// browser cannot drift from the CLI.
//
// The store's half of the page is painted from the keystroke; the supervisor's
// half is read behind it (startFill) because it costs an invocation of the CLI
// and the operator must not be made to wait for a page they can already read.
func (b *browser) openDetail(ctx context.Context) error {
	session, ok := b.selected()
	if !ok {
		b.status = "no session selected"
		return nil
	}
	detail, err := b.app.loadSessionDetail(ctx, b.rt, session.ID)
	if err != nil {
		return err
	}
	var head, tail bytes.Buffer
	b.app.writeDetailFacts(&head, detail)
	b.app.writeDetailTail(&tail, detail)
	b.detailHead = splitLines(head.String())
	b.detailTail = splitLines(tail.String())
	b.detailOffset = 0
	b.setMode(tuiModeDetail)
	if reader, ok := b.supervisorReader(); ok {
		b.detail = b.composeDetail(b.supervisorBlock(supervisorReport{
			Status:  "reading\u2026",
			Pending: true,
		}))
		b.startFill(ctx, reader, detail)
	} else {
		// A provider with no supervisor surface leaves the block out of the text
		// rendering entirely, exactly as `sessions status` does.
		b.detail = b.composeDetail(nil)
	}
	return nil
}

// supervisorReader reports the provider's supervisor surface, when it has one.
func (b *browser) supervisorReader() (SupervisorReader, bool) {
	if b.rt == nil || b.rt.Provider == nil {
		return nil, false
	}
	reader, ok := b.rt.Provider.(SupervisorReader)
	if !ok || reader == nil {
		return nil, false
	}
	return reader, true
}

// startFill reads the supervisor in the background and reports it to the loop.
//
// Only one fill runs at a time: a new detail request cancels the previous one, and
// the answer carries the sequence number of the request that started it, so a late
// answer for a session the operator has left is discarded rather than painted.
func (b *browser) startFill(ctx context.Context, reader SupervisorReader, detail sessionDetail) {
	b.cancelFill()
	b.fillSeq++
	fillCtx, cancel := context.WithTimeout(ctx, b.supervisorBudget)
	fill := &detailFill{seq: b.fillSeq, cancel: cancel}
	b.fill = fill
	budget := b.supervisorBudget
	attempt, attemptErr := detail.Attempt, detail.AttemptErr
	go func() {
		report := b.app.readSupervisor(fillCtx, reader, attempt, attemptErr)
		if errors.Is(fillCtx.Err(), context.DeadlineExceeded) {
			// A budget that expires is a fact about the read, not about the
			// session: say which call did not answer rather than reporting an
			// empty supervisor.
			report = supervisorReport{
				Status: "not read",
				Error: fmt.Sprintf("`claude daemon status` did not answer within %s; press 'i' to try again",
					budget),
			}
		}
		cancel()
		select {
		case b.fills <- supervisorFill{seq: fill.seq, report: report}:
		case <-b.done:
		}
	}()
}

// cancelFill abandons the supervisor read in flight, if any.
func (b *browser) cancelFill() {
	if b.fill == nil {
		return
	}
	b.fill.cancel()
	b.fill = nil
}

// applyFill paints the supervisor block once it has been read.
func (b *browser) applyFill(fill supervisorFill) {
	if b.mode != tuiModeDetail || b.fill == nil || b.fill.seq != fill.seq {
		return
	}
	b.fill.cancel()
	b.fill = nil
	b.detail = b.composeDetail(b.supervisorBlock(fill.report))
	b.clampDetailOffset()
	b.draw()
}

// supervisorBlock renders one supervisor report as the page's block.
func (b *browser) supervisorBlock(report supervisorReport) []string {
	var buf bytes.Buffer
	b.app.writeSupervisorDetail(&buf, report)
	return splitLines(buf.String())
}

// composeDetail joins the store's half of the page, the supervisor block and the
// rest of the page into the lines the detail screen draws.
func (b *browser) composeDetail(block []string) []string {
	lines := make([]string, 0, len(b.detailHead)+len(block)+len(b.detailTail))
	lines = append(lines, b.detailHead...)
	lines = append(lines, block...)
	lines = append(lines, b.detailTail...)
	return lines
}

// splitLines splits rendered text into the lines a screen draws.
func splitLines(text string) []string {
	trimmed := strings.TrimRight(text, "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
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
	if b.busy != nil {
		fmt.Fprintf(out, "busy: %s (ctrl-c cancels)\n", truncateText(b.busy.label, 120))
	}
	if b.status != "" {
		fmt.Fprintf(out, "%s\n", truncateText(b.status, 160))
	}
	out.WriteString("\nenter detail  n new  x delete  R resume  e enable  d disable  r retry  / filter  a all  ? help  q quit\n")
}

// tableContent renders the header and the rows the list screen shows.
//
// The columns are the table's, minus the full session id: it is a 36-character
// UUID and the browser is the one place with a fixed width, so the row shows the
// short id and the detail screen shows the whole one.
func (b *browser) tableContent() ([]string, [][]string) {
	loc := b.app.displayLocation(b.rt)
	headers := []string{"SESSION", "STATE", "NEXT ATTEMPT", "ENABLED", "PROJECT", "RUNS IN", "LAST SEEN", "NOTE"}
	rows := make([][]string, 0, len(b.sessions))
	for _, session := range b.sessions {
		note := session.Reason
		if note == "" {
			note = session.LastError
		}
		next := nextAttemptCell(recovery.NextAttemptFor(session, schedulePtr(b.schedules, session.ID), b.now), b.now)
		rows = append(rows, []string{
			shortID(session.ID),
			string(session.State),
			next,
			yesNo(session.Enabled),
			emptyDash(truncateText(session.ProjectPath, 28)),
			// PRD 6 R12: the same second directory the CLI table shows, with
			// the same dash-when-identical rule, so the two surfaces cannot
			// disagree about where a session runs.
			runsInCell(session),
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
	if b.fill != nil {
		// The block itself sits further down the page than the first screen shows,
		// so the fact that it is being read is stated where it can be seen.
		out.WriteString("\nreading the supervisor\u2026 (the page fills in)\n")
	}
	out.WriteString("\n" + tuiDetailFooter() + "\n")
}

// drawHelp paints the key map, from the same table the documentation quotes.
func (b *browser) drawHelp(out *strings.Builder) {
	out.WriteString("ccresu sessions \u2014 keys\n\n")
	for _, binding := range tuiKeyMap() {
		fmt.Fprintf(out, "  %-12s %s\n", binding.keys, binding.meaning)
	}
	out.WriteString("\ndetail screen\n")
	for _, binding := range tuiDetailKeyMap() {
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
		{"enter / i", "session detail (the `sessions status` page)"},
		{"n", "create: add the sessions discovered on disk since the last pass"},
		{"e", "enable the selected session"},
		{"d", "disable the selected session"},
		{"r", "retry the selected session"},
		{"R", "resume the selected session now (the call `ccresu recover` makes)"},
		{"x", "delete the selected session (asks y/N)"},
		{"?", "this key map"},
		{"q / ctrl-c", "quit"},
	}
}

// tuiDetailKeyMap is the detail screen's own key map, which its footer quotes and
// the help screen lists under the list's.
func tuiDetailKeyMap() []keyBinding {
	return []keyBinding{
		{"j / down", "scroll down one line"},
		{"k / up", "scroll up one line"},
		{"g / home", "first line"},
		{"G / end", "last line"},
		{"enter / q / esc", "back to the list"},
		{"ctrl-c", "back to the list"},
	}
}

// tuiDetailFooter is the detail screen's key map as one line.
func tuiDetailFooter() string {
	return "j/k scroll  g/G first/last  enter, q or esc back to the list"
}
