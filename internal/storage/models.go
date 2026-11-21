package storage

import "time"

// State is the persisted recovery state of a session.
//
// The values mirror the state machine documented in the specification. They are
// stored as text so that a database can be inspected with any SQLite client and
// so that new states can be added without a migration.
type State string

// The persisted session states.
//
// The machine is deliberately small. It answers one question — has this
// session's usage limit reset, and is it time to resume it? — and the live path
// is four beats:
//
//	DISCOVERED -> WAITING_FOR_RESET -> RESUMING -> RESUMED -> DISCOVERED
//
// A session with nothing pending sits in DISCOVERED, where the scan reads its
// transcript. A usage limit found there parks it in WAITING_FOR_RESET with the
// reset instant on the schedule. When the instant arrives it is RESUMING, and a
// successful resume leaves it RESUMED until the next limit is noticed, which
// starts the loop again. Everything outside that loop is a verdict that stops
// it, and there are only three.
const (
	// StateDiscovered is a known session with nothing pending: the scan reads its
	// transcript, and a usage limit found there parks it on the reset.
	StateDiscovered State = "DISCOVERED"
	// StateWaitingForReset is a session parked until its reset time (or its next
	// poll, when the banner named no reset). The schedule holds the instant; this
	// state records that a wait is what the session is doing.
	StateWaitingForReset State = "WAITING_FOR_RESET"
	// StateResuming is a resume in flight: the limit has cleared and the session
	// is being handed the continuation.
	StateResuming State = "RESUMING"
	// StateResumed is a session that was resumed and is alive. It is a resting
	// state, not a terminal one, and it exists for one reason: a just-resumed
	// session must not be resumed again while the banner that parked it is still
	// inside the transcript tail the detector reads.
	StateResumed State = "RESUMED"
	// StateCompleted is a session that finished its last turn: there is nothing
	// to continue, and handing it a continuation would be a mistake.
	StateCompleted State = "COMPLETED"
	// StateDisabled is the loop's off switch: the operator turned the session
	// off, or a verdict parked it with no way forward.
	StateDisabled State = "DISABLED"
	// StateUnrecoverable is a session ccresu has positively established it has no
	// verified way to resume.
	StateUnrecoverable State = "UNRECOVERABLE"
)

// allStates is the closed set of states accepted by the database. Validation in
// the repository layer uses it so a typo cannot persist an unreachable state.
var allStates = map[State]struct{}{
	StateDiscovered:      {},
	StateWaitingForReset: {},
	StateResuming:        {},
	StateResumed:         {},
	StateCompleted:       {},
	StateDisabled:        {},
	StateUnrecoverable:   {},
}

// Valid reports whether s is a known state.
func (s State) Valid() bool { _, ok := allStates[s]; return ok }

// SessionType classifies how a Claude Code session runs.
type SessionType string

// The supported session types.
const (
	// SessionTypeInteractive is a session driven by a terminal/TUI.
	SessionTypeInteractive SessionType = "interactive"
	// SessionTypeHeadless is a `claude -p` style session.
	SessionTypeHeadless SessionType = "headless"
	// SessionTypeUnknown is a session whose type could not be determined.
	SessionTypeUnknown SessionType = "unknown"
)

// Valid reports whether t is a known session type.
func (t SessionType) Valid() bool {
	switch t {
	case SessionTypeInteractive, SessionTypeHeadless, SessionTypeUnknown:
		return true
	default:
		return false
	}
}

// LimitType classifies a detected usage limit.
type LimitType string

// The recognised usage-limit kinds.
const (
	LimitTypeFiveHour LimitType = "five_hour"
	LimitTypeWeekly   LimitType = "weekly"
	LimitTypeOther    LimitType = "other"
)

// Valid reports whether l is a known limit type.
func (l LimitType) Valid() bool {
	switch l {
	case LimitTypeFiveHour, LimitTypeWeekly, LimitTypeOther:
		return true
	default:
		return false
	}
}

// NotificationStatus is the delivery state of a notification.
type NotificationStatus string

// The notification lifecycle.
const (
	// NotificationPending is queued but not yet delivered.
	NotificationPending NotificationStatus = "pending"
	// NotificationSent was accepted by Telegram.
	NotificationSent NotificationStatus = "sent"
	// NotificationFailed exhausted its delivery attempts.
	NotificationFailed NotificationStatus = "failed"
	// NotificationDropped was deliberately not delivered (deduplicated or
	// disabled by configuration).
	NotificationDropped NotificationStatus = "dropped"
)

// Valid reports whether s is a known notification status.
func (s NotificationStatus) Valid() bool {
	switch s {
	case NotificationPending, NotificationSent, NotificationFailed, NotificationDropped:
		return true
	default:
		return false
	}
}

// NotificationEvent names every message class ccresu can deliver.
//
// The set is the six documented events plus session completion. Adding a value
// here is never a local change: it has to reach telegram.Events(), the templates
// and the API's event filter as well, and
// TestModelValidationHelpers holds the storage list and the notifier's
// enumeration equal in both directions so an omission fails a test.
type NotificationEvent string

// The notification events ccresu can deliver.
const (
	EventLimitDetected    NotificationEvent = "limit_detected"
	EventWaiting          NotificationEvent = "waiting_for_reset"
	EventRecoveryStarted  NotificationEvent = "recovery_started"
	EventRecoverySuccess  NotificationEvent = "recovery_success"
	EventRecoveryFailure  NotificationEvent = "recovery_failure"
	EventPermanentFailure NotificationEvent = "permanent_failure"
	EventSessionCompleted NotificationEvent = "session_completed"
)

// notificationEvents is the one list of events ccresu can deliver. Valid reads
// it, and NotificationEvents returns a copy, so the database's notion of a valid
// event is a list rather than a switch that a new constant can be forgotten
// from: an event once reached the switch and telegram.Events() but not the
// storage test's hand-typed slice, and nothing failed.
var notificationEvents = []NotificationEvent{
	EventLimitDetected,
	EventWaiting,
	EventRecoveryStarted,
	EventRecoverySuccess,
	EventRecoveryFailure,
	EventPermanentFailure,
	EventSessionCompleted,
}

// NotificationEvents returns every event ccresu can deliver, in the documented
// order. It is the storage-side counterpart of telegram.Events(), and
// TestModelValidationHelpers holds the two equal in both directions, so an event
// added to one and not the other fails a test instead of going missing.
//
// The result is a copy: callers may sort or filter it without disturbing Valid.
func NotificationEvents() []NotificationEvent {
	return append([]NotificationEvent(nil), notificationEvents...)
}

// Valid reports whether e is a known notification event.
func (e NotificationEvent) Valid() bool {
	for _, known := range notificationEvents {
		if e == known {
			return true
		}
	}
	return false
}

// Session is one tracked Claude Code session.
type Session struct {
	// ID is the Claude Code session UUID and the natural primary key.
	ID string
	// ProjectPath is where the session started: the launch directory, kept
	// stable across passes so the fallback and the display have a fixed meaning
	// (PRD 6 R1).
	ProjectPath string
	// RunsInPath is where the session actually runs, refreshed from the
	// published `cwd` on every observation (PRD 6 R1, R4). Empty means "not
	// observed yet", which is a different fact from "the same as the project":
	// the resolver falls back to ProjectPath, and the display says nothing
	// rather than repeating one path twice.
	RunsInPath string
	// TranscriptPath is the on-disk .jsonl transcript, when known.
	TranscriptPath string
	// Provider records which adapter discovered the session.
	Provider string
	// Type classifies the session.
	Type SessionType
	// PID is the process id the published listing reported. It is recorded for
	// evidence only: no lifecycle decision reads it (PRD 3 R8). Liveness comes
	// from the published state and, for the supervisor itself, from
	// `claude daemon status`.
	PID int
	// State is the persisted recovery state.
	State State
	// Enabled reports whether ccresu may recover this session.
	Enabled bool
	// Recoverable is false when the session type cannot be resumed (for example
	// an unsupported session kind); Reason explains why.
	Recoverable bool
	Reason      string
	// LastError is the most recent failure text, if any.
	LastError string
	// TranscriptSize and TranscriptLines snapshot the transcript when the
	// session was last seen, so a later resume can prove the conversation grew.
	TranscriptSize  int64
	TranscriptLines int64
	// RetryAt is the instant a failed attempt's backoff ends: the delay ccresu
	// computed when the resume did not take, written down rather than re-derived
	// so a restart waits the same time instead of retrying at once. It is a
	// schedule, not a state — no state owns it and every transition clears it, so
	// a stale wait is never reported for a session that moved on. A parked
	// session's instant lives on its Schedule row instead. Nil means exactly "no
	// retry pending": it is a pointer so that "no retry" cannot be mistaken for a
	// real instant.
	RetryAt      *time.Time `json:"retry_at,omitempty"`
	DiscoveredAt time.Time
	LastSeenAt   time.Time
	UpdatedAt    time.Time
}

// UsageLimit is one detected usage-limit event.
type UsageLimit struct {
	ID        int64
	SessionID string
	LimitType LimitType
	// RawMessage is the transcript text that triggered detection.
	RawMessage string
	// ResetAt is the resolved reset instant in UTC; zero when the message
	// carried no usable reset time.
	ResetAt time.Time
	// ResetTimezone preserves the timezone the reset time was written in.
	ResetTimezone string
	// ResetRaw preserves the original reset phrase, e.g. "12pm (Europe/Berlin)".
	ResetRaw string
	// Confidence is the detector's confidence in [0,1].
	Confidence float64
	// PausedGoal reports whether the message also said the active goal is paused.
	PausedGoal bool
	DetectedAt time.Time
}

// RecoveryAttempt is one attempt to resume a session.
type RecoveryAttempt struct {
	ID            int64
	SessionID     string
	UsageLimitID  int64
	AttemptNumber int
	State         State
	// Command is the resume command that was run, with secrets redacted.
	Command   string
	StartedAt time.Time
	// FinishedAt is zero while the attempt is still running.
	FinishedAt time.Time
	// Success is nil while unresolved.
	Success *bool
	Output  string
	Error   string
	// SessionIDVerified reports whether the resumed session kept the original
	// session ID, i.e. the conversation was continued rather than restarted.
	SessionIDVerified *bool
	// TranscriptGrew reports whether the transcript gained content across the
	// resume.
	//
	// It is a diagnostic rather than the verdict. The resume's exit status says
	// whether the conversation continued; this measurement is what an operator
	// reads afterwards, and a transcript that did not change shape does not turn a
	// finished turn into a failure.
	TranscriptGrew *bool
	// CostUSD is what the CLI reported this attempt cost, in US dollars.
	//
	// Nil means *unknown*, not free: a CLI that reports no figure has not said the
	// attempt was free, and PRD 5 R10 requires that an unknown attempt neither
	// counts toward a cap nor blocks a resume. The pointer is what keeps those two
	// apart, so a later reader cannot mistake "we were not told" for "it cost
	// nothing".
	CostUSD *float64
}

// Transition is one entry in the persisted state history.
type Transition struct {
	ID         int64
	SessionID  string
	AttemptID  int64
	FromState  State
	ToState    State
	Reason     string
	OccurredAt time.Time
}

// Notification is one queued Telegram message.
type Notification struct {
	ID        int64
	SessionID string
	AttemptID int64
	Event     NotificationEvent
	// DedupeKey uniquely identifies the logical notification, so a restart
	// cannot deliver the same message twice.
	DedupeKey string
	// Body is the rendered message text.
	Body   string
	Status NotificationStatus
	// Attempts counts delivery attempts.
	Attempts  int
	LastError string
	CreatedAt time.Time
	UpdatedAt time.Time
	// SentAt is zero until delivery succeeds.
	SentAt time.Time
}

// Setting is one key/value row of the settings table.
type Setting struct {
	Key       string
	Value     string
	UpdatedAt time.Time
}

// Schedule is one persisted wait for a usage limit to reset.
//
// It is stored rather than held in memory so that a daemon restart — or a
// reboot — resumes waiting instead of losing the session. The row survives
// across restarts and carries the original phrase, so the reason for the wait
// can still be explained later without the transcript.
type Schedule struct {
	// SessionID is the session being waited for; it is also the primary key.
	SessionID    string
	UsageLimitID int64
	// ResetAt is the resolved reset instant in UTC. It is zero when the message
	// carried no usable reset time, in which case Unknown is true.
	ResetAt time.Time
	// ResetTimezone preserves the zone the reset time was written in.
	ResetTimezone string
	// ResetRaw preserves the original phrase, e.g. "12pm (Europe/Berlin)".
	ResetRaw string
	// Unknown reports that the reset time is not known and the session must be
	// polled instead of woken at a fixed instant.
	Unknown bool
	// Attempts counts the polls already spent on an unknown reset. It persists
	// so the backoff survives a restart.
	Attempts int
	// NextPollAt is when an unknown reset should next be polled.
	NextPollAt time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// DueAt is the instant the schedule is interested in: the reset time when it is
// known, otherwise the next poll time. It returns the zero time when the row is
// not schedulable at all.
func (s Schedule) DueAt() time.Time {
	if s.Unknown {
		return s.NextPollAt
	}
	return s.ResetAt
}
