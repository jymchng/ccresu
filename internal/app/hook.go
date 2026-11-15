package app

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/jymchng/ccresu/internal/logging"
	"github.com/jymchng/ccresu/internal/recovery"
	"github.com/jymchng/ccresu/internal/storage"
)

// The hook's whole contract in constants, so the bounds are stated once.
const (
	// hookMaxPayloadBytes bounds one hook invocation's stdin. A Notification
	// payload is a few hundred bytes; the cap exists so a caller that pipes a
	// file into `ccresu hook` cannot make it allocate without limit.
	hookMaxPayloadBytes = 64 << 10
	// hookReadTimeout bounds the stdin read. A hook is on somebody else's
	// critical path: if the writer never closes the pipe, ccresu gives up and
	// lets the session continue.
	hookReadTimeout = 2 * time.Second
	// hookTimeout bounds the whole invocation — the database, the one listing
	// read and the queue insert. It is deliberately far below the 600-second
	// default a command hook would otherwise be given, and it is the reason the
	// documented installation sets an explicit `timeout`.
	hookTimeout = 10 * time.Second

	// hookNeedsInputType is the notification type Claude Code fires when a
	// background session starts waiting for a human. It is the only type whose
	// meaning ccresu models.
	hookNeedsInputType = "agent_needs_input"
	// hookCompletedType fires when a background session "finishes or fails".
	// ccresu acts on it, but only through the same observation: a finished turn
	// is not a settlement and a failure is the engine's verdict, so the only
	// thing worth reporting is a session that is *still* blocked (PRD 4 §6.1).
	hookCompletedType = "agent_completed"

	// hookBlockedState is the published state that makes a needs_input message
	// true. It is compared case-insensitively because the parser lower-cases,
	// but a future payload that does not must not silently stop working.
	hookBlockedState = "blocked"
)

// hookPayload is the part of a Notification hook's stdin document ccresu reads.
//
// The struct is deliberately not exhaustive and unmarshalling does not reject
// unknown fields: the CLI reserves the right to add or drop keys between
// releases, so an unknown key is ignored and a missing one is absent rather than
// an error (the same tolerance `internal/claude/agents.go` applies to the
// published listing).
type hookPayload struct {
	// SessionID is the conversation the notification is about.
	SessionID string `json:"session_id"`
	// NotificationType says which notification fired.
	NotificationType string `json:"notification_type"`
	// HookEventName is the hook event (`Notification`), kept for the record.
	HookEventName string `json:"hook_event_name"`
	// Cwd is the session's working directory, as the CLI reported it. It is
	// read but never used to resolve anything: the hook observes, it does not
	// discover (PRD 4 R11).
	Cwd string `json:"cwd"`
	// Message is the notification text, which ccresu does not forward: the
	// message it sends is rendered from the observation instead.
	Message string `json:"message"`
}

// cmdHook runs one Notification hook.
//
// It returns nil unconditionally, which is the whole point: `Execute` prints
// whatever a command returns and exits with a mapped code, and a hook that did
// either would be a notice in somebody's transcript. Exit 0 with no output is the
// contract, so every failure below is a silent return (PRD 4 R1).
func (a *App) cmdHook(ctx context.Context, _ []string) error {
	a.runHook(ctx)
	return nil
}

// runHook performs the hook's work, discarding every error.
//
// The stages are in dependency order and each one may end the invocation: a
// payload that cannot be read is the same outcome as a hook that was never
// installed, which is the outcome the poll already handles (PRD 4 R9).
func (a *App) runHook(ctx context.Context) {
	payload, ok := readHookPayload(a.stdin, hookReadTimeout)
	if !ok {
		return
	}
	// Only the two documented types are acted on. Every other type describes an
	// interactive terminal session, which ccresu does not model; mapping them
	// would make ccresu a second notifier for events it does not own (R3).
	switch payload.NotificationType {
	case hookNeedsInputType, hookCompletedType:
	default:
		return
	}
	sessionID := strings.TrimSpace(payload.SessionID)
	if sessionID == "" {
		return
	}

	// Everything after the read is bounded by one deadline, so a slow database
	// or a hanging CLI leaves the session to carry on without waiting for us.
	ctx, cancel := context.WithTimeout(ctx, hookTimeout)
	defer cancel()

	runtime, err := a.hookRuntime(ctx)
	if err != nil {
		return
	}
	defer func() {
		// Closing must not print either: `Runtime.Close` returns its error, so
		// discarding it here is the difference between a silent hook and a line
		// of stderr in somebody's transcript.
		_ = runtime.Close()
	}()

	// R11: the hook never creates, discovers, enables or recovers a session. A
	// session ccresu does not already know is discovery's business, so an
	// unknown id ends the invocation rather than starting one.
	session, err := runtime.Store.GetSession(ctx, sessionID)
	if err != nil {
		return
	}
	if runtime.Notifier == nil || !runtime.Notifier.Enabled(storage.EventNeedsInput) {
		return
	}
	if runtime.Provider == nil {
		return
	}

	// R5: one bounded reading of the published listing, filtered to the session
	// the payload named. Not a transcript read, and never agent view itself,
	// which requires an interactive terminal.
	listing, err := runtime.Provider.PublishedListing(ctx)
	if err != nil {
		return
	}
	entry, listed := listing.Entry(sessionID)
	if !listed || !strings.EqualFold(strings.TrimSpace(entry.State), hookBlockedState) {
		// The human answered between the notification and this hook, or the
		// session finished: the hook reports a past moment, the observation is
		// the present, and the present is the only one that may be announced.
		return
	}

	// R4/R14: the hook does not decide the event. It raises the one milestone a
	// hook may raise — the same one PRD 2's observer raises — and hands it to the
	// producer that already exists, so a hook observation and a poll observation
	// of the same block produce the same event, the same body and the same
	// dedupe key.
	observation := recovery.StateObservation{
		SessionID:  sessionID,
		State:      entry.State,
		Status:     entry.Status,
		WaitingFor: entry.WaitingFor,
		ObservedAt: listing.ReadAt,
		InListing:  true,
	}
	observer := notificationObserver(runtime.Notifier, runtime.Store, runtime.Log)
	if observer == nil {
		return
	}
	observer(ctx, recovery.Milestone{
		Kind: recovery.MilestoneNeedsInput,
		Outcome: recovery.Outcome{
			SessionID: sessionID,
			State:     storage.StateWaitingForInput,
			// No attempt: a blocked session was deliberately not handed off, so
			// there is no row here and the message must not imply one.
		},
		ProjectPath: session.ProjectPath,
		RunsInPath:  session.RunsInPath,
		Observation: observation,
	})
}

// hookRuntime builds the runtime the hook uses, without ever writing to stdout or
// stderr.
//
// It is separate from openRuntime for one reason: openRuntime reports failures
// the way a human-facing command should, and a hook may not speak. The logger is
// therefore discarded rather than pointed at the process's stderr, and a broken
// configuration, a locked database or a missing binary all read as "no runtime"
// (PRD 4 R9).
func (a *App) hookRuntime(ctx context.Context) (*Runtime, error) {
	cfg, err := a.loadConfig()
	if err != nil {
		return nil, err
	}
	logger, err := logging.New(logging.Options{
		Level:    "error",
		Format:   cfg.App.LogFormat,
		Output:   io.Discard,
		Redactor: logging.NewRedactor(),
		TimeSource: func() time.Time {
			return a.clock.Now().UTC()
		},
	})
	if err != nil {
		return nil, err
	}
	runtime, err := a.deps.BuildRuntime(ctx, cfg, a.clock, logger)
	if err != nil {
		return nil, err
	}
	return runtime, nil
}

// readHookPayload reads one bounded JSON document from stdin.
//
// The read is bounded twice: by a size cap, so a caller cannot make ccresu read
// a whole file, and by a deadline, so a writer that never closes the pipe cannot
// hold the session. The deadline is enforced by reading on its own goroutine; the
// goroutine is abandoned rather than cancelled because a blocking read on an
// inherited pipe cannot be interrupted, and the process is about to exit anyway.
//
// A payload that is empty, truncated, not JSON, of the wrong shape, or larger
// than the cap is ignored — `ok=false` — which the caller treats exactly like a
// hook that never ran (R2).
func readHookPayload(r io.Reader, timeout time.Duration) (hookPayload, bool) {
	if r == nil {
		return hookPayload{}, false
	}
	if timeout <= 0 {
		timeout = hookReadTimeout
	}
	type read struct {
		data []byte
		err  error
	}
	done := make(chan read, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(r, hookMaxPayloadBytes+1))
		done <- read{data: data, err: err}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var result read
	select {
	case result = <-done:
	case <-timer.C:
		return hookPayload{}, false
	}
	if result.err != nil {
		return hookPayload{}, false
	}
	if int64(len(result.data)) > hookMaxPayloadBytes {
		return hookPayload{}, false
	}
	var payload hookPayload
	if err := json.Unmarshal(result.data, &payload); err != nil {
		return hookPayload{}, false
	}
	return payload, true
}
