package storage

import "time"

// State is the persisted recovery state of a session.
//
// The values mirror the state machine documented in the specification. They are
// stored as text so that a database can be inspected with any SQLite client and
// so that new states can be added without a migration.
type State string

// The persisted session states.
const (
	StateDiscovered         State = "DISCOVERED"
	StateRunning            State = "RUNNING"
	StateUsageLimitDetected State = "USAGE_LIMIT_DETECTED"
	StateWaitingForReset    State = "WAITING_FOR_RESET"
	StateResetTimeReached   State = "RESET_TIME_REACHED"
	StateCheckingSession    State = "CHECKING_SESSION"
	StateCheckingLimit      State = "CHECKING_LIMIT"
	StateResuming           State = "RESUMING"
	StateResumed            State = "RESUMED"
	StateRetryWait          State = "RETRY_WAIT"
	StateCompleted          State = "COMPLETED"
	StateUserTerminated     State = "USER_TERMINATED"
	StateDisabled           State = "DISABLED"
	StateUnrecoverable      State = "UNRECOVERABLE"
)

// allStates is the closed set of states accepted by the database. Validation in
// the repository layer uses it so a typo cannot persist an unreachable state.
var allStates = map[State]struct{}{
	StateDiscovered:         {},
	StateRunning:            {},
	StateUsageLimitDetected: {},
	StateWaitingForReset:    {},
	StateResetTimeReached:   {},
	StateCheckingSession:    {},
	StateCheckingLimit:      {},
	StateResuming:           {},
	StateResumed:            {},
	StateRetryWait:          {},
	StateCompleted:          {},
	StateUserTerminated:     {},
	StateDisabled:           {},
	StateUnrecoverable:      {},
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

// NotificationEvent names the six documented notification events plus session
// completion.
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

// Valid reports whether e is a known notification event.
func (e NotificationEvent) Valid() bool {
	switch e {
	case EventLimitDetected, EventWaiting, EventRecoveryStarted,
		EventRecoverySuccess, EventRecoveryFailure, EventPermanentFailure,
		EventSessionCompleted:
		return true
	default:
		return false
	}
}

// Session is one tracked Claude Code session.
type Session struct {
	// ID is the Claude Code session UUID and the natural primary key.
	ID string
	// ProjectPath is the working directory the session belongs to.
	ProjectPath string
	// TranscriptPath is the on-disk .jsonl transcript, when known.
	TranscriptPath string
	// Provider records which adapter discovered the session.
	Provider string
	// Type classifies the session.
	Type SessionType
	// PID, PPID and ProcessStartTime capture process identity so a recycled PID
	// is never mistaken for the original process.
	PID              int
	PPID             int
	ProcessStartTime uint64
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
	// RetryAt is the instant the wait ends for a session in RETRY_WAIT: the
	// backoff ccresu computed when the attempt failed, written down rather than
	// re-derived. Leaving RETRY_WAIT clears it, so a stale wait is never reported
	// for a session that moved on. A WAITING_FOR_RESET session's instant lives on
	// its Schedule row instead, and nil here means exactly "no retry pending" —
	// it is a pointer so that "no retry" cannot be mistaken for a real instant.
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
	// TranscriptGrew reports whether the transcript gained lines, which is the
	// cross-platform proof that the conversation actually advanced.
	TranscriptGrew *bool
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
