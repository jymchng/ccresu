package telegram

import (
	"fmt"
	"strings"
	"time"

	"github.com/jymchng/ccresu/internal/storage"
)

// Events lists every notification ccresu can deliver, in the documented order.
func Events() []storage.NotificationEvent {
	return []storage.NotificationEvent{
		storage.EventLimitDetected,
		storage.EventWaiting,
		storage.EventRecoveryStarted,
		storage.EventRecoverySuccess,
		storage.EventRecoveryFailure,
		storage.EventPermanentFailure,
		storage.EventSessionCompleted,
	}
}

// EventData is everything a template may use.
//
// It is a flat value rather than the storage models so the templates stay
// decoupled from the database schema, and so a caller that only knows a CLI
// error can still render a precisely worded message.
type EventData struct {
	// SessionID identifies the conversation the message is about.
	SessionID string
	// ProjectPath is the working directory the session belongs to.
	ProjectPath string
	// LimitID is the persisted usage-limit row, used for deduplication.
	LimitID int64
	// LimitType is five_hour, weekly or other.
	LimitType storage.LimitType
	// ResetAt, ResetTimezone and ResetRaw describe the reset instant: the
	// resolved time in UTC, the zone it was written in and the original phrase.
	ResetAt       time.Time
	ResetTimezone string
	ResetRaw      string
	// AttemptID is the persisted attempt row, used for deduplication.
	AttemptID int64
	// Attempt and MaxAttempts describe the retry budget position.
	Attempt     int
	MaxAttempts int
	// Delay is the wait before the next attempt, for a retry notification.
	Delay time.Duration
	// Reason is the failure text or explanation, already human-readable.
	Reason string
	// State is the session state the message refers to.
	State string
	// Duration is how long the resumed turn took, when known.
	Duration time.Duration
}

// Subject returns a short title for the event, for the API and the CLI.
func Subject(event storage.NotificationEvent) string {
	switch event {
	case storage.EventLimitDetected:
		return "Usage limit detected"
	case storage.EventWaiting:
		return "Waiting for the usage limit to reset"
	case storage.EventRecoveryStarted:
		return "Recovery started"
	case storage.EventRecoverySuccess:
		return "Recovery succeeded"
	case storage.EventRecoveryFailure:
		return "Recovery attempt failed"
	case storage.EventPermanentFailure:
		return "Automatic recovery stopped"
	case storage.EventSessionCompleted:
		return "Session completed"
	default:
		return ""
	}
}

// Render builds the HTML message body for event.
//
// Every value interpolated into the body is escaped, so a hostile project path
// or CLI error cannot break the markup. An unknown event is an error rather
// than an empty message: silently sending nothing would look like delivery.
func Render(event storage.NotificationEvent, data EventData) (string, error) {
	var body strings.Builder
	session := Code(data.SessionID)

	switch event {
	case storage.EventLimitDetected:
		fmt.Fprintf(&body, "%s\n", Bold("Usage limit detected"))
		fmt.Fprintf(&body, "Session: %s\n", session)
		if data.ProjectPath != "" {
			fmt.Fprintf(&body, "Project: %s\n", Code(data.ProjectPath))
		}
		fmt.Fprintf(&body, "Limit: %s\n", Escape(limitName(data.LimitType)))
		fmt.Fprintf(&body, "Resets: %s\n", describeReset(data))
		body.WriteString("ccresu is waiting for the reset and will resume this session automatically.")

	case storage.EventWaiting:
		fmt.Fprintf(&body, "%s\n", Bold("Waiting for the usage limit to reset"))
		fmt.Fprintf(&body, "Session: %s\n", session)
		fmt.Fprintf(&body, "Resets: %s\n", describeReset(data))
		if data.ResetRaw != "" && !data.ResetAt.IsZero() {
			fmt.Fprintf(&body, "Original message: %s\n", Escape(data.ResetRaw))
		}
		body.WriteString("No action is needed: resuming happens automatically once the window reopens.")

	case storage.EventRecoveryStarted:
		fmt.Fprintf(&body, "%s\n", Bold("Recovery started"))
		fmt.Fprintf(&body, "Session: %s\n", session)
		fmt.Fprintf(&body, "Attempt: %s\n", Escape(attemptText(data)))
		body.WriteString("Dispatching the original conversation in the background with ")
		fmt.Fprintf(&body, "%s.\n", Code("claude --resume "+data.SessionID+" --bg"))
		body.WriteString("The job continues the conversation under a session id of its own, which ccresu follows until it finishes.")

	case storage.EventRecoverySuccess:
		fmt.Fprintf(&body, "%s\n", Bold("Recovery succeeded"))
		fmt.Fprintf(&body, "Session: %s\n", session)
		fmt.Fprintf(&body, "Attempt: %s\n", Escape(attemptText(data)))
		body.WriteString("The original conversation resumed and its transcript grew.")
		if data.Duration > 0 {
			fmt.Fprintf(&body, "\nDuration: %s", Escape(data.Duration.Round(time.Second).String()))
		}
		body.WriteString("\n")

	case storage.EventRecoveryFailure:
		fmt.Fprintf(&body, "%s\n", Bold("Recovery attempt failed"))
		fmt.Fprintf(&body, "Session: %s\n", session)
		fmt.Fprintf(&body, "Attempt: %s\n", Escape(attemptText(data)))
		if data.Delay > 0 {
			fmt.Fprintf(&body, "Next attempt in %s\n", Escape(data.Delay.Round(time.Second).String()))
		}
		if data.Reason != "" {
			fmt.Fprintf(&body, "Reason: %s\n", Escape(firstLine(data.Reason)))
		}
		body.WriteString("ccresu keeps the session and tries again automatically.")

	case storage.EventPermanentFailure:
		fmt.Fprintf(&body, "%s\n", Bold("Automatic recovery stopped"))
		fmt.Fprintf(&body, "Session: %s\n", session)
		if data.Attempt > 0 || data.MaxAttempts > 0 {
			fmt.Fprintf(&body, "Attempts: %s\n", Escape(attemptText(data)))
		}
		if data.State != "" {
			fmt.Fprintf(&body, "State: %s\n", Escape(data.State))
		}
		if data.Reason != "" {
			fmt.Fprintf(&body, "Reason: %s\n", Escape(firstLine(data.Reason)))
		}
		fmt.Fprintf(&body, "Run %s to try once more.\n", Code("ccresu retry "+data.SessionID))

	case storage.EventSessionCompleted:
		fmt.Fprintf(&body, "%s\n", Bold("Session completed"))
		fmt.Fprintf(&body, "Session: %s\n", session)
		if data.ProjectPath != "" {
			fmt.Fprintf(&body, "Project: %s\n", Code(data.ProjectPath))
		}
		if data.State != "" {
			fmt.Fprintf(&body, "State: %s\n", Escape(data.State))
		}
		body.WriteString("ccresu will not resume this session again unless you ask it to.")

	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidEvent, event)
	}

	return strings.TrimRight(body.String(), "\n") + "\n", nil
}

// DedupeKey builds the stable identity of one logical notification.
//
// The same event can be observed twice — a restart re-reads the transcript, and
// a retry re-checks the limit — so the key includes the row that caused it: the
// usage-limit id for detection events and the attempt for recovery events. That
// makes "the same alert twice" impossible while still allowing a second
// recovery of the same session to be announced.
func DedupeKey(event storage.NotificationEvent, data EventData) string {
	var key strings.Builder
	key.WriteString(string(event))
	key.WriteByte(':')
	key.WriteString(data.SessionID)

	switch event {
	case storage.EventLimitDetected, storage.EventWaiting:
		if data.LimitID != 0 {
			fmt.Fprintf(&key, ":%d", data.LimitID)
		}
	case storage.EventRecoveryStarted, storage.EventRecoverySuccess,
		storage.EventRecoveryFailure, storage.EventPermanentFailure:
		switch {
		case data.AttemptID != 0:
			fmt.Fprintf(&key, ":%d", data.AttemptID)
		case data.Attempt != 0:
			fmt.Fprintf(&key, ":%d", data.Attempt)
		}
	}
	return key.String()
}

// limitName renders a limit kind for a human.
func limitName(limit storage.LimitType) string {
	switch limit {
	case storage.LimitTypeFiveHour:
		return "5-hour window"
	case storage.LimitTypeWeekly:
		return "weekly window"
	case storage.LimitTypeOther:
		return "usage window"
	default:
		return "usage window"
	}
}

// describeReset renders the reset instant in the zone it was written in, keeping
// the original phrase so the message can be checked against the transcript.
func describeReset(data EventData) string {
	if data.ResetAt.IsZero() {
		return "unknown — ccresu will poll until the limit clears"
	}
	at := data.ResetAt.UTC()
	if data.ResetTimezone != "" {
		if loc, err := time.LoadLocation(data.ResetTimezone); err == nil {
			at = data.ResetAt.In(loc)
		}
	}
	text := at.Format("Mon 2 Jan 2006 15:04 MST")
	if data.ResetRaw != "" {
		text += " (" + data.ResetRaw + ")"
	}
	return Escape(text)
}

// attemptText renders "n/max", or just the attempt when the budget is unknown.
func attemptText(data EventData) string {
	switch {
	case data.Attempt > 0 && data.MaxAttempts > 0:
		return fmt.Sprintf("%d/%d", data.Attempt, data.MaxAttempts)
	case data.Attempt > 0:
		return fmt.Sprintf("%d", data.Attempt)
	default:
		return "unknown"
	}
}

// firstLine keeps only the first non-empty line of reason, so a multi-line CLI
// dump does not dominate the message.
func firstLine(reason string) string {
	for _, line := range strings.Split(reason, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
