package app

// What the browser does while a command runs.
//
// The reported defect was that `ccresu sessions` stopped answering the keyboard
// while it waited for `claude`: Enter painted nothing until the CLI replied, the
// keys pressed meanwhile were buffered by the terminal and then applied to the
// screen the reply produced, and `ctrl-c` — which raw mode delivers as a byte —
// could not cancel anything because the only reader of bytes was the goroutine
// doing the waiting. These tests hold a command open and assert what the operator
// sees in the meantime, which is the one thing the old suite could not do: it
// handed the browser a finished keystroke string and an instant fake, so a wait
// could not exist.

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/storage"
)

// blockingProvider is the harness provider plus the supervisor surface, with the
// calls the browser makes held open on channels.
//
// It embeds *claude.FakeProvider rather than replacing it, so a test only has to
// say which call should block; and it is a test double rather than a method on
// FakeProvider because "a provider with no supervisor surface" is a real case the
// browser must keep handling (see TestInspectSaysWhenThereIsNoSupervisor).
type blockingProvider struct {
	*claude.FakeProvider

	mu sync.Mutex
	// discoverBlock holds Discover open until it is closed, and discoverStarted
	// is closed when Discover is entered.
	discoverBlock   chan struct{}
	discoverStarted chan struct{}
	discoverOnce    sync.Once
	discoverCalls   int
	discoverDone    bool
	// ignoreContext makes Discover wait only for its channel, which is how a test
	// can tell "the browser cancelled the action" from "the action happened to
	// finish quickly".
	ignoreContext bool
	// statusBlock does the same for DaemonStatus.
	statusBlock   chan struct{}
	statusStarted chan struct{}
	statusOnce    sync.Once
	cancelOnce    sync.Once
	statusCalls   int
	statusCancels chan struct{}
	status        claude.SupervisorStatus
	statusErr     error
	daemonLog     claude.LogsResult
	logs          claude.LogsResult
}

// Discover implements claude.SessionProvider, optionally holding the call open.
func (p *blockingProvider) Discover(ctx context.Context, filter claude.DiscoverFilter) ([]claude.Session, error) {
	p.mu.Lock()
	p.discoverCalls++
	started, block := p.discoverStarted, p.discoverBlock
	p.mu.Unlock()
	if started != nil {
		p.discoverOnce.Do(func() { close(started) })
	}
	if block != nil {
		if p.ignoreContext {
			<-block
		} else {
			select {
			case <-block:
			case <-ctx.Done():
				p.mu.Lock()
				p.discoverDone = true
				p.mu.Unlock()
				return nil, ctx.Err()
			}
		}
	}
	sessions, err := p.FakeProvider.Discover(ctx, filter)
	p.mu.Lock()
	p.discoverDone = true
	p.mu.Unlock()
	return sessions, err
}

// DaemonStatus implements SupervisorReader, optionally holding the call open.
func (p *blockingProvider) DaemonStatus(ctx context.Context) (claude.SupervisorStatus, error) {
	p.mu.Lock()
	p.statusCalls++
	started, block := p.statusStarted, p.statusBlock
	status, err := p.status, p.statusErr
	p.mu.Unlock()
	if started != nil {
		p.statusOnce.Do(func() { close(started) })
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			if p.statusCancels != nil {
				p.cancelOnce.Do(func() { close(p.statusCancels) })
			}
			return claude.SupervisorStatus{}, ctx.Err()
		}
	}
	if err != nil {
		return status, err
	}
	return status, nil
}

// SessionLogs implements SupervisorReader.
func (p *blockingProvider) SessionLogs(context.Context, string) (claude.LogsResult, error) {
	return p.logs, nil
}

// DaemonLogs implements SupervisorReader.
func (p *blockingProvider) DaemonLogs(context.Context) (claude.LogsResult, error) {
	return p.daemonLog, nil
}

// counts reports how often each held-open call was made, and whether the
// discovery call has finished.
func (p *blockingProvider) counts() (discover, status int, discoverDone bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.discoverCalls, p.statusCalls, p.discoverDone
}

// TestTheBrowserPaintsTheFactsWhileTheSupervisorIsRead is the reported defect
// itself: Enter must paint the page from the store at once, and say that the
// supervisor is being read, rather than painting nothing until the CLI answers.
func TestTheBrowserPaintsTheFactsWhileTheSupervisorIsRead(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)
	provider := &blockingProvider{
		FakeProvider:  h.provider,
		statusBlock:   make(chan struct{}),
		statusStarted: make(chan struct{}),
		status:        claude.SupervisorStatus{Running: true, State: "running"},
	}
	h.rt.Provider = provider

	paced := h.startBrowser(t, "sessions")
	paced.waitForScreen(t, "enter detail")
	paced.send(t, "\r")

	// The page is on screen, from the store, while the read is still held open.
	paced.waitForScreen(t, "reading the supervisor")
	screen := paced.screen()
	for _, want := range []string{canonicalSessionID, "Next attempt", "j/k scroll"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("the detail page is missing %q before the supervisor answered:\n%s", want, screen)
		}
	}
	select {
	case <-provider.statusStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("the supervisor read was never started")
	}

	// Releasing the read fills the block in. The block is below the first screen
	// of a page this long, so the test scrolls to it exactly as the operator does.
	close(provider.statusBlock)
	paced.waitForScreenWithout(t, "reading the supervisor")
	paced.send(t, "G")
	paced.waitForScreen(t, "Supervisor")
	if screen := paced.screen(); !strings.Contains(screen, "running") {
		t.Fatalf("the supervisor block was not filled in:\n%s", screen)
	}
	if code := paced.quit(t); code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
}

// TestTheDetailPageMatchesSessionsStatus pins the promise the browser makes: the
// page it shows is the page `sessions status` prints, supervisor block included.
func TestTheDetailPageMatchesSessionsStatus(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.State = storage.StateWaitingForReset
		s.Reason = "usage limit"
	})
	status := claude.SupervisorStatus{
		Running:      true,
		State:        "running",
		Workers:      2,
		WorkersKnown: true,
		SocketDir:    "/tmp/cc-daemon-0/abc",
	}
	logs := claude.LogsResult{Text: "daemon line one\ndaemon line two"}
	// The command is a batch command and may wait, so its provider answers at once.
	h.rt.Provider = &blockingProvider{FakeProvider: h.provider, status: status, daemonLog: logs}
	code, stdout, stderr := h.exec(t, "sessions", "status", canonicalSessionID)
	if code != ExitOK {
		t.Fatalf("sessions status exit = %d (stderr %q)", code, stderr)
	}

	// The browser's read is held open so that "the page was painted before the
	// supervisor answered" is a fact the test can observe rather than a race it
	// hopes for.
	provider := &blockingProvider{
		FakeProvider:  h.provider,
		statusBlock:   make(chan struct{}),
		statusStarted: make(chan struct{}),
		status:        status,
		daemonLog:     logs,
	}
	h.rt.Provider = provider

	paced := h.startBrowser(t, "sessions")
	paced.waitForScreen(t, "enter detail")
	paced.send(t, "\r")
	paced.waitForScreen(t, "reading the supervisor")
	close(provider.statusBlock)
	paced.waitForScreenWithout(t, "reading the supervisor")
	paced.send(t, "G")
	paced.waitForScreen(t, "daemon line two")
	// The page is 21 lines and the screen shows 15, so the comparison is made
	// against every frame the detail screen painted, not only the last one.
	screens := h.stdout.String()
	for _, line := range strings.Split(strings.TrimRight(stdout, "\n"), "\n") {
		line = strings.TrimRight(line, " ")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.Contains(screens, line) {
			t.Errorf("the detail screen is missing the CLI's line %q:\n%s", line, paced.screen())
		}
	}
	if code := paced.quit(t); code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
}

// TestTheSupervisorFillGivesUpOnItsBudget: a supervisor that never answers must
// cost the operator one line, not the page.
func TestTheSupervisorFillGivesUpOnItsBudget(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)
	provider := &blockingProvider{
		FakeProvider:  h.provider,
		statusBlock:   make(chan struct{}),
		statusStarted: make(chan struct{}),
		statusCancels: make(chan struct{}),
	}
	h.rt.Provider = provider
	t.Cleanup(func() { close(provider.statusBlock) })

	paced := h.startBrowser(t, "sessions")
	paced.waitForScreen(t, "enter detail")
	paced.send(t, "\r")
	paced.waitForScreen(t, "reading the supervisor")
	// The browser uses the shipped budget, so the assertion is that the block ends
	// by itself rather than how quickly: the wait is deliberately longer than
	// tuiSupervisorBudget.
	paced.waitForScreenWithoutWithin(t, "reading the supervisor", tuiSupervisorBudget+5*time.Second)
	paced.send(t, "G")
	paced.waitForScreen(t, "not read")
	screen := paced.screen()
	if !strings.Contains(screen, "did not answer within") {
		t.Fatalf("the block does not say why it was not read:\n%s", screen)
	}
	// The facts are still there: the budget bounds the read, not the page.
	if !strings.Contains(screen, canonicalSessionID) {
		t.Fatalf("the page lost the session's facts:\n%s", screen)
	}
	if code := paced.quit(t); code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
}

// TestGatedKeysAreRefusedWhileAnActionRuns: a key that starts work is refused
// while work is running, instead of being buffered by the terminal and applied to
// whatever screen the work produces.
func TestGatedKeysAreRefusedWhileAnActionRuns(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)
	provider := &blockingProvider{
		FakeProvider:    h.provider,
		discoverBlock:   make(chan struct{}),
		discoverStarted: make(chan struct{}),
	}
	h.rt.Provider = provider

	paced := h.startBrowser(t, "sessions")
	paced.waitForScreen(t, "enter detail")
	paced.send(t, "n")
	paced.waitForScreen(t, "busy: discovering")

	// Enter, recover and create are all refused, and none of them starts work or
	// changes the screen.
	paced.send(t, "\r")
	paced.waitForScreen(t, "busy: discovering")
	if strings.Contains(paced.screen(), "inspect") {
		t.Fatalf("enter opened the detail screen while an action ran:\n%s", paced.screen())
	}
	paced.send(t, "R")
	paced.send(t, "n")
	paced.waitForScreen(t, "busy: discovering")

	discover, _, _ := provider.counts()
	if discover != 1 {
		t.Fatalf("Discover was called %d times, want 1: a refused key started work", discover)
	}

	close(provider.discoverBlock)
	paced.waitForScreen(t, "create: 0 new session(s)")
	if code := paced.quit(t); code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
}

// TestCtrlCCancelsTheActionAndKeepsTheBrowser: ctrl-c is the operator's cancel
// key, and it must work while the command is still running.
func TestCtrlCCancelsTheActionAndKeepsTheBrowser(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)
	provider := &blockingProvider{
		FakeProvider:    h.provider,
		discoverBlock:   make(chan struct{}),
		discoverStarted: make(chan struct{}),
	}
	h.rt.Provider = provider

	paced := h.startBrowser(t, "sessions")
	paced.waitForScreen(t, "enter detail")
	paced.send(t, "n")
	paced.waitForScreen(t, "busy: discovering")
	paced.send(t, "\x03")
	paced.waitForScreen(t, "cancelled")

	// The browser is still there: the list is drawn and a key still works.
	if screen := paced.screen(); !strings.Contains(screen, "enter detail") {
		t.Fatalf("the browser did not return to the list after ctrl-c:\n%s", screen)
	}
	if _, _, done := provider.counts(); !done {
		t.Fatal("ctrl-c did not cancel the action")
	}
	if code := paced.quit(t); code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
}

// TestQQuitsWithoutWaitingForTheAction: quitting must not wait for the CLI.
func TestQQuitsWithoutWaitingForTheAction(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)
	provider := &blockingProvider{
		FakeProvider:    h.provider,
		discoverBlock:   make(chan struct{}),
		discoverStarted: make(chan struct{}),
		// The fake never notices the cancellation, so "the browser exited" cannot
		// be explained by the action finishing on its own.
		ignoreContext: true,
	}
	h.rt.Provider = provider
	t.Cleanup(func() { close(provider.discoverBlock) })

	paced := h.startBrowser(t, "sessions")
	paced.waitForScreen(t, "enter detail")
	paced.send(t, "n")
	paced.waitForScreen(t, "busy: discovering")
	paced.send(t, "q")
	if code := paced.wait(t); code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	if _, _, done := provider.counts(); done {
		t.Fatal("the browser waited for the action instead of cancelling it")
	}
	if out := h.stdout.String(); !strings.HasSuffix(out, tuiShowCursor+tuiAltScreenOff) {
		t.Errorf("the terminal was not restored on the way out:\n%q", out)
	}
}

// TestTheSecondEnterClosesADetailThatWasPainted: the double press that used to
// discard the page now opens it, shows it, and closes it — in that order.
func TestTheSecondEnterClosesADetailThatWasPainted(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)

	paced := h.startBrowser(t, "sessions")
	paced.waitForScreen(t, "enter detail")
	paced.send(t, "\r")
	paced.waitForScreen(t, "Next attempt")
	paced.send(t, "\r")
	paced.waitForScreen(t, "ccresu sessions — 1 shown")

	// The frames are in order: the page was painted before the key that closed it
	// was decoded, which is the property the queued keystrokes used to break.
	out := h.stdout.String()
	detail := strings.Index(out, "ccresu sessions — inspect")
	back := strings.LastIndex(out, "ccresu sessions — 1 shown")
	if detail < 0 || back < 0 || detail > back {
		t.Fatalf("the detail page was not painted before the list returned:\n%s", out)
	}
	if code := paced.quit(t); code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
}

// TestTheStatusLineDoesNotSurviveAScreenChange: a message belongs to the screen
// that wrote it. This is the stale "press enter, q or esc" line the operator saw
// on the list after the detail screen had gone.
func TestTheStatusLineDoesNotSurviveAScreenChange(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)

	paced := h.startBrowser(t, "sessions")
	paced.waitForScreen(t, "enter detail")
	paced.send(t, "\r")
	paced.waitForScreen(t, "Next attempt")
	paced.send(t, "\r")
	paced.waitForScreen(t, "ccresu sessions — 1 shown")

	screen := paced.screen()
	if strings.Contains(screen, "enter, q or esc") {
		t.Fatalf("the detail screen's footer is painted on the list:\n%s", screen)
	}
	if code := paced.quit(t); code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
}

// TestALoneEscapeIsDecodedWithoutASecondKey: the detail screen's footer advertises
// `esc`, so `esc` has to work on its own.
func TestALoneEscapeIsDecodedWithoutASecondKey(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)

	paced := h.startBrowser(t, "sessions")
	paced.waitForScreen(t, "enter detail")
	paced.send(t, "\r")
	paced.waitForScreen(t, "Next attempt")
	// Nothing follows the escape: the decoder must decide it by itself.
	paced.send(t, "\x1b")
	paced.waitForScreen(t, "ccresu sessions — 1 shown")
	if code := paced.quit(t); code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
}

// TestTheBrowserRestoresTheScreenWhenSignalled: SIGINT and SIGTERM cancel the
// context the browser runs under, and the loop has to return so the deferred
// restore can run. SIGKILL cannot be caught, which is why the documentation names
// `reset` as the remedy for it.
func TestTheBrowserRestoresTheScreenWhenSignalled(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)

	paced := h.startBrowser(t, "sessions")
	paced.waitForScreen(t, "enter detail")
	h.signals <- os.Interrupt
	if code := paced.wait(t); code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
	if out := h.stdout.String(); !strings.HasSuffix(out, tuiShowCursor+tuiAltScreenOff) {
		t.Errorf("the terminal was not restored after a signal:\n%q", out)
	}
}

// TestTheBrowserNeverCallsTheCLIOnTheKeystrokePath is the structural half of the
// fix: the keys may dispatch work, but the functions that handle them must not run
// a command themselves.
//
// It is a source guard in the spirit of TestCcresuNeverRespawns: the property is
// about which goroutine a call runs on, and no runtime assertion can see that as
// directly as the call graph can.
func TestTheBrowserNeverCallsTheCLIOnTheKeystrokePath(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "tui.go", nil, 0)
	if err != nil {
		t.Fatalf("parse tui.go: %v", err)
	}
	forbidden := map[string]string{
		"Discover":           "starts a discovery pass",
		"Retry":              "runs a recovery",
		"RecoverRequest":     "runs a recovery",
		"Recover":            "runs a recovery",
		"DaemonStatus":       "reads the supervisor",
		"DaemonLogs":         "reads the supervisor",
		"SessionLogs":        "reads the supervisor",
		"readSupervisor":     "reads the supervisor",
		"supervisorReport":   "reads the supervisor",
		"writeSessionDetail": "reads the supervisor",
	}
	// The screens the keyboard reaches, plus the two functions that prepare work
	// for a worker: everything they may do is dispatch, read the store, or draw.
	keystrokePath := map[string]bool{
		"loop": true, "handle": true, "handleBusy": true, "handleList": true,
		"handleDetail": true, "handleFilter": true, "handleHelp": true,
		"handleConfirm": true, "openDetail": true, "act": true, "create": true,
		"delete": true,
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 || fn.Body == nil {
			continue
		}
		if receiverName(fn.Recv.List[0].Type) != "browser" || !keystrokePath[fn.Name.Name] {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			// A function literal is the closure handed to startAction: that is the
			// worker, which is exactly where the calls belong.
			if _, isFuncLit := node.(*ast.FuncLit); isFuncLit {
				return false
			}
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if reason, banned := forbidden[selector.Sel.Name]; banned {
				t.Errorf("browser.%s calls %s, which %s on the goroutine that reads the keyboard",
					fn.Name.Name, selector.Sel.Name, reason)
			}
			return true
		})
	}
}

// receiverName returns the type name of a method receiver.
func receiverName(expr ast.Expr) string {
	switch typed := expr.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.StarExpr:
		return receiverName(typed.X)
	}
	return ""
}

// TestTheFillIsDiscardedWhenTheScreenChanges: an answer that arrives after the
// operator has left the detail screen must not be painted over the list.
func TestTheFillIsDiscardedWhenTheScreenChanges(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)
	provider := &blockingProvider{
		FakeProvider:  h.provider,
		statusBlock:   make(chan struct{}),
		statusStarted: make(chan struct{}),
		status:        claude.SupervisorStatus{Running: true, State: "running"},
	}
	h.rt.Provider = provider

	paced := h.startBrowser(t, "sessions")
	paced.waitForScreen(t, "enter detail")
	paced.send(t, "\r")
	paced.waitForScreen(t, "reading the supervisor")
	// Leave the screen while the read is still held open, then let it answer.
	paced.send(t, "q")
	paced.waitForScreen(t, "ccresu sessions — 1 shown")
	close(provider.statusBlock)
	// Give the answer a moment to arrive and be discarded.
	time.Sleep(50 * time.Millisecond)
	if screen := paced.screen(); strings.Contains(screen, "Supervisor") {
		t.Fatalf("a supervisor block was painted on the list:\n%s", screen)
	}
	if code := paced.quit(t); code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
}

// TestTheDetailFillIsCancelledByLeaving guards the resource the fill holds: leaving
// the detail screen cancels the read rather than leaving it running.
func TestTheDetailFillIsCancelledByLeaving(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)
	provider := &blockingProvider{
		FakeProvider:  h.provider,
		statusBlock:   make(chan struct{}),
		statusStarted: make(chan struct{}),
		statusCancels: make(chan struct{}),
	}
	h.rt.Provider = provider
	t.Cleanup(func() {
		select {
		case <-provider.statusBlock:
		default:
			close(provider.statusBlock)
		}
	})

	paced := h.startBrowser(t, "sessions")
	paced.waitForScreen(t, "enter detail")
	paced.send(t, "\r")
	paced.waitForScreen(t, "reading the supervisor")
	paced.send(t, "\r") // back to the list: the fill is abandoned
	paced.waitForScreen(t, "ccresu sessions — 1 shown")

	select {
	case <-provider.statusCancels:
	case <-time.After(2 * time.Second):
		t.Fatal("leaving the detail screen did not cancel the supervisor read")
	}
	if code := paced.quit(t); code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
}

// TestTheBrowserStillWorksWithoutASupervisorSurface keeps the "unavailable"
// provider honest: no block, no fill, no goroutine, and the page is still the
// page `sessions status` prints for that provider.
func TestTheBrowserStillWorksWithoutASupervisorSurface(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)

	code, stdout, stderr := h.exec(t, "sessions", "status", canonicalSessionID)
	if code != ExitOK {
		t.Fatalf("sessions status exit = %d (stderr %q)", code, stderr)
	}
	if strings.Contains(stdout, "Supervisor") {
		t.Fatalf("the CLI printed a supervisor block for a provider with no surface:\n%s", stdout)
	}

	paced := h.startBrowser(t, "sessions")
	paced.waitForScreen(t, "enter detail")
	paced.send(t, "\r")
	paced.waitForScreen(t, "Next attempt")
	if screen := paced.screen(); strings.Contains(screen, "Supervisor") {
		t.Fatalf("the browser invented a supervisor block:\n%s", screen)
	}
	if code := paced.quit(t); code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
}

// TestARefusedActionIsReportedOnTheStatusLine: an action that cannot run must say
// why, on the screen the operator is looking at.
func TestARefusedActionIsReportedOnTheStatusLine(t *testing.T) {
	h := newHarness(t)
	// No session at all: the browser has nothing to act on.
	paced := h.startBrowser(t, "sessions")
	paced.waitForScreen(t, "enter detail")
	paced.send(t, "R")
	paced.waitForScreen(t, "no session selected")
	if code := paced.quit(t); code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
}

// TestAnActionFailureKeepsTheBrowserRunning: the failure goes to the status line
// and the operator keeps their place.
func TestAnActionFailureKeepsTheBrowserRunning(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.State = storage.StateResetTimeReached
		s.TranscriptSize = 10
		s.TranscriptLines = 1
	})
	h.provider.SetSession(claude.Session{ID: canonicalSessionID, ProjectPath: "/work/project", Recoverable: true})
	// The handoff fails, which is what the status line has to report.
	h.provider.ResumeErr = errors.New("claude: the session is already running")

	paced := h.startBrowser(t, "sessions")
	paced.waitForScreen(t, "enter detail")
	paced.send(t, "R")
	paced.waitForScreen(t, "recover "+shortID(canonicalSessionID)+":")
	if screen := paced.screen(); !strings.Contains(screen, "enter detail") {
		t.Fatalf("the browser left the list after a failed action:\n%s", screen)
	}
	if code := paced.quit(t); code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, h.stderr.String())
	}
}
