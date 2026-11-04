package claude

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Call is one recorded provider or execer invocation.
type Call struct {
	// Name is the command name for execer calls, and the method name for
	// provider calls.
	Name string
	// Args is the argument vector.
	Args []string
	// Options is the execer run configuration.
	Options RunOptions
	// SessionID and Message are recorded for provider calls.
	SessionID string
	Message   string
}

// FakeExecer is an Execer that records calls and returns scripted results.
//
// It is safe for concurrent use and lets tests assert the exact argv without
// spawning processes.
type FakeExecer struct {
	mu       sync.Mutex
	calls    []Call
	handler  func(Call) (Output, error)
	outputs  []Output
	errs     []error
	fallback Output
	fallErr  error
}

// NewFakeExecer returns an execer whose responses are supplied by handler.
func NewFakeExecer(handler func(Call) (Output, error)) *FakeExecer {
	return &FakeExecer{handler: handler}
}

// NewScriptedExecer returns an execer that replies with outputs and errors in
// order, then falls back to fallback/fallErr.
func NewScriptedExecer(outputs []Output, errs []error, fallback Output, fallErr error) *FakeExecer {
	return &FakeExecer{
		outputs:  append([]Output(nil), outputs...),
		errs:     append([]error(nil), errs...),
		fallback: fallback,
		fallErr:  fallErr,
	}
}

// Run implements Execer.
func (f *FakeExecer) Run(ctx context.Context, name string, args []string, opts RunOptions) (Output, error) {
	call := Call{Name: name, Args: append([]string(nil), args...), Options: opts}

	f.mu.Lock()
	f.calls = append(f.calls, call)
	handler := f.handler
	var output Output
	var err error
	if handler != nil {
		f.mu.Unlock()
		output, err = handler(call)
		f.mu.Lock()
	} else if len(f.outputs) > 0 {
		output = f.outputs[0]
		f.outputs = f.outputs[1:]
		if len(f.errs) > 0 {
			err = f.errs[0]
			f.errs = f.errs[1:]
		}
	} else {
		output, err = f.fallback, f.fallErr
	}
	f.mu.Unlock()

	if ctxErr := ctx.Err(); ctxErr != nil {
		return output, ctxErr
	}
	return output, err
}

// Calls returns a copy of the recorded calls.
func (f *FakeExecer) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// LastCall returns the most recent call, if any.
func (f *FakeExecer) LastCall() (Call, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return Call{}, false
	}
	return f.calls[len(f.calls)-1], true
}

// FakeProvider is an in-memory SessionProvider for tests and for callers that
// must not touch the real Claude Code installation.
//
// It records every call and lets each method's behaviour be overridden, so the
// recovery engine can be tested against deterministic sessions.
type FakeProvider struct {
	mu sync.Mutex

	// Sessions is the discovery result, keyed by session id.
	Sessions map[string]Session
	// Statuses overrides GetSessionStatus results.
	Statuses map[string]SessionStatus
	// ResumeBehaviour is returned by ResumeSession for the matching id.
	ResumeResults map[string]ResumeResult

	// Now supplies timestamps; nil means time.Now.
	Now func() time.Time

	DiscoverErr error
	GetErr      error
	StatusErr   error
	ResumeErr   error

	// OnResume, when set, runs instead of the default resume behaviour.
	OnResume func(ctx context.Context, sessionID, message string) (ResumeResult, error)

	calls []Call
}

// NewFakeProvider returns a fake with the supplied sessions.
func NewFakeProvider(sessions ...Session) *FakeProvider {
	f := &FakeProvider{
		Sessions:      make(map[string]Session, len(sessions)),
		Statuses:      make(map[string]SessionStatus),
		ResumeResults: make(map[string]ResumeResult),
	}
	for _, session := range sessions {
		f.Sessions[session.ID] = session
	}
	return f
}

// SetSession adds or replaces a session.
func (f *FakeProvider) SetSession(session Session) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Sessions[session.ID] = session
}

// Calls returns a copy of the recorded calls.
func (f *FakeProvider) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// Now returns the fake clock, defaulting to time.Now.
func (f *FakeProvider) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

// Discover implements SessionProvider.
func (f *FakeProvider) Discover(ctx context.Context, filter DiscoverFilter) ([]Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Call{Name: "Discover"})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.DiscoverErr != nil {
		return nil, f.DiscoverErr
	}
	out := make([]Session, 0, len(f.Sessions))
	for _, session := range f.Sessions {
		if !filter.matches(session) {
			continue
		}
		out = append(out, session)
	}
	sortSessions(out)
	return out, nil
}

// GetSession implements SessionProvider.
func (f *FakeProvider) GetSession(ctx context.Context, sessionID string) (Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Call{Name: "GetSession", SessionID: sessionID})
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	if f.GetErr != nil {
		return Session{}, f.GetErr
	}
	session, ok := f.Sessions[sessionID]
	if !ok {
		return Session{}, errors.Join(ErrSessionNotFound, errors.New(sessionID))
	}
	return session, nil
}

// GetSessionStatus implements SessionProvider.
func (f *FakeProvider) GetSessionStatus(ctx context.Context, sessionID string) (SessionStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Call{Name: "GetSessionStatus", SessionID: sessionID})
	if err := ctx.Err(); err != nil {
		return SessionStatus{}, err
	}
	if f.StatusErr != nil {
		return SessionStatus{}, f.StatusErr
	}
	if status, ok := f.Statuses[sessionID]; ok {
		if status.CheckedAt.IsZero() {
			status.CheckedAt = f.now().UTC()
		}
		return status, nil
	}
	session, ok := f.Sessions[sessionID]
	if !ok {
		return SessionStatus{}, errors.Join(ErrSessionNotFound, errors.New(sessionID))
	}
	return SessionStatus{
		Session:    session,
		Status:     session.Status,
		State:      session.State,
		WaitingFor: session.WaitingFor,
		Running:    sessionRunning(session),
		CheckedAt:  f.now().UTC(),
	}, nil
}

// ResumeSession implements SessionProvider.
func (f *FakeProvider) ResumeSession(ctx context.Context, sessionID, message string) (ResumeResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, Call{Name: "ResumeSession", SessionID: sessionID, Message: message})
	onResume := f.OnResume
	err := f.ResumeErr
	result, hasResult := f.ResumeResults[sessionID]
	session, sessionKnown := f.Sessions[sessionID]
	f.mu.Unlock()

	if ctxErr := ctx.Err(); ctxErr != nil {
		return ResumeResult{}, ctxErr
	}
	if err != nil {
		return ResumeResult{}, err
	}
	if onResume != nil {
		return onResume(ctx, sessionID, message)
	}
	if !sessionKnown {
		return ResumeResult{}, errors.Join(ErrSessionNotFound, errors.New(sessionID))
	}
	if session.Unrecoverable() {
		return ResumeResult{}, errors.Join(ErrUnrecoverable, errors.New(session.Reason))
	}
	if hasResult {
		result.RequestedSessionID = sessionID
		result.Verified = result.SessionID == sessionID
		return result, nil
	}
	return ResumeResult{
		RequestedSessionID: sessionID,
		SessionID:          sessionID,
		Verified:           true,
		Result:             "resumed",
		Subtype:            "success",
		NumTurns:           1,
		Command:            append([]string{DefaultExecutable}, ResumeArgs(sessionID, message)...),
	}, nil
}

var (
	_ SessionProvider = (*FakeProvider)(nil)
	_ Execer          = (*FakeExecer)(nil)
)
