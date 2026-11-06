package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/recovery"
	"github.com/jymchng/ccresu/internal/storage"
)

// maxContinuationMessage bounds the only free-form text the API accepts.
const maxContinuationMessage = 4096

// maxSettingValue bounds one settings value.
const maxSettingValue = 4096

// marshalJSON is the single encoder, so every response is shaped the same way.
func marshalJSON(payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

// decodeJSON reads an optional JSON body into dst.
//
// Unknown fields are an error: a client that sends {"command": "..."} to a
// recover endpoint must be told the field is meaningless rather than have it
// silently ignored, which is also how the "no command execution surface" promise
// stays auditable.
func (s *Server) decodeJSON(r *http.Request, dst any, allowEmpty bool) *apiError {
	if r.Body == nil {
		if allowEmpty {
			return nil
		}
		return errInvalid("a JSON request body is required")
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return &apiError{
				Status:  http.StatusRequestEntityTooLarge,
				Code:    codeTooLarge,
				Message: fmt.Sprintf("request body must be at most %d bytes", s.cfg.MaxBodyBytes),
			}
		}
		if errors.Is(err, io.EOF) {
			if allowEmpty {
				return nil
			}
			return errInvalid("a JSON request body is required")
		}
		return errInvalid("the request body is not valid JSON for this endpoint", err.Error())
	}
	// A second value in the stream means the client sent something this endpoint
	// does not understand; refuse instead of processing half of it.
	if err := decoder.Decode(&struct{}{}); err != nil && !errors.Is(err, io.EOF) {
		return errInvalid("the request body must contain exactly one JSON object")
	}
	return nil
}

// sessionDTO is the wire form of a session.
type sessionDTO struct {
	ID             string `json:"id"`
	ProjectPath    string `json:"project_path,omitempty"`
	TranscriptPath string `json:"transcript_path,omitempty"`
	// Name is the session name `claude agents --all --json` reports, empty when
	// the CLI reported none.
	Name string `json:"name,omitempty"`
	// ContinuationSessionID is the session id the resumed conversation continued
	// under, when a background job reported one. It is normally a different id
	// from ID: the dispatch does not keep the id it was given.
	ContinuationSessionID string `json:"continuation_session_id,omitempty"`
	// ContinuationTranscriptPath is the transcript that continuation keeps
	// growing, which is the file detection reads for the next reset time.
	ContinuationTranscriptPath string `json:"continuation_transcript_path,omitempty"`
	// JobID is the background job the most recent resume created, and the handle
	// a WORKING session is polled by.
	JobID           string    `json:"job_id,omitempty"`
	Provider        string    `json:"provider,omitempty"`
	Type            string    `json:"type"`
	PID             int       `json:"pid,omitempty"`
	PPID            int       `json:"ppid,omitempty"`
	State           string    `json:"state"`
	Enabled         bool      `json:"enabled"`
	Recoverable     bool      `json:"recoverable"`
	Reason          string    `json:"reason,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
	TranscriptSize  int64     `json:"transcript_size"`
	TranscriptLines int64     `json:"transcript_lines"`
	DiscoveredAt    time.Time `json:"discovered_at"`
	LastSeenAt      time.Time `json:"last_seen_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// toSessionDTO converts a stored session.
func toSessionDTO(session storage.Session) sessionDTO {
	return sessionDTO{
		ID:                         session.ID,
		ProjectPath:                session.ProjectPath,
		TranscriptPath:             session.TranscriptPath,
		Name:                       session.Name,
		ContinuationSessionID:      session.ContinuationSessionID,
		ContinuationTranscriptPath: session.ContinuationTranscriptPath,
		JobID:                      session.JobID,
		Provider:                   session.Provider,
		Type:                       string(session.Type),
		PID:                        session.PID,
		PPID:                       session.PPID,
		State:                      string(session.State),
		Enabled:                    session.Enabled,
		Recoverable:                session.Recoverable,
		Reason:                     session.Reason,
		LastError:                  session.LastError,
		TranscriptSize:             session.TranscriptSize,
		TranscriptLines:            session.TranscriptLines,
		DiscoveredAt:               session.DiscoveredAt,
		LastSeenAt:                 session.LastSeenAt,
		UpdatedAt:                  session.UpdatedAt,
	}
}

// toSessionDTOs converts a list.
func toSessionDTOs(sessions []storage.Session) []sessionDTO {
	out := make([]sessionDTO, 0, len(sessions))
	for _, session := range sessions {
		out = append(out, toSessionDTO(session))
	}
	return out
}

// usageLimitDTO is the wire form of a detected limit.
type usageLimitDTO struct {
	ID            int64     `json:"id"`
	SessionID     string    `json:"session_id"`
	LimitType     string    `json:"limit_type"`
	ResetAt       time.Time `json:"reset_at,omitempty"`
	ResetTimezone string    `json:"reset_timezone,omitempty"`
	ResetRaw      string    `json:"reset_raw,omitempty"`
	Confidence    float64   `json:"confidence"`
	PausedGoal    bool      `json:"paused_goal"`
	RawMessage    string    `json:"raw_message,omitempty"`
	DetectedAt    time.Time `json:"detected_at"`
}

// toUsageLimitDTO converts a stored limit.
func toUsageLimitDTO(limit storage.UsageLimit) usageLimitDTO {
	return usageLimitDTO{
		ID:            limit.ID,
		SessionID:     limit.SessionID,
		LimitType:     string(limit.LimitType),
		ResetAt:       limit.ResetAt,
		ResetTimezone: limit.ResetTimezone,
		ResetRaw:      limit.ResetRaw,
		Confidence:    limit.Confidence,
		PausedGoal:    limit.PausedGoal,
		RawMessage:    limit.RawMessage,
		DetectedAt:    limit.DetectedAt,
	}
}

// scheduleDTO is the wire form of a persisted wait.
type scheduleDTO struct {
	SessionID     string    `json:"session_id"`
	UsageLimitID  int64     `json:"usage_limit_id,omitempty"`
	ResetAt       time.Time `json:"reset_at,omitempty"`
	ResetTimezone string    `json:"reset_timezone,omitempty"`
	ResetRaw      string    `json:"reset_raw,omitempty"`
	Unknown       bool      `json:"unknown"`
	Attempts      int       `json:"attempts"`
	NextPollAt    time.Time `json:"next_poll_at,omitempty"`
	DueAt         time.Time `json:"due_at,omitempty"`
}

// toScheduleDTO converts a stored schedule.
func toScheduleDTO(schedule storage.Schedule) scheduleDTO {
	return scheduleDTO{
		SessionID:     schedule.SessionID,
		UsageLimitID:  schedule.UsageLimitID,
		ResetAt:       schedule.ResetAt,
		ResetTimezone: schedule.ResetTimezone,
		ResetRaw:      schedule.ResetRaw,
		Unknown:       schedule.Unknown,
		Attempts:      schedule.Attempts,
		NextPollAt:    schedule.NextPollAt,
		DueAt:         schedule.DueAt(),
	}
}

// attemptDTO is the wire form of one recovery attempt.
type attemptDTO struct {
	ID            int64     `json:"id"`
	SessionID     string    `json:"session_id"`
	AttemptNumber int       `json:"attempt_number"`
	State         string    `json:"state"`
	Command       string    `json:"command,omitempty"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at,omitempty"`
	Success       *bool     `json:"success,omitempty"`
	// ContinuationConfirmed reports whether the dispatch reported a continuation
	// session at all. The column behind it is still called session_id_verified,
	// which is historical: a background dispatch does not preserve the id it was
	// given, so the verdict cannot be "the id was verified".
	ContinuationConfirmed bool  `json:"continuation_confirmed,omitempty"`
	TranscriptGrew        *bool `json:"transcript_grew,omitempty"`
	// ContinuationSessionID is the session id the job reported, when it did.
	ContinuationSessionID string `json:"continuation_session_id,omitempty"`
	Error                 string `json:"error,omitempty"`
}

// toAttemptDTO converts a stored attempt.
func toAttemptDTO(attempt storage.RecoveryAttempt) attemptDTO {
	return attemptDTO{
		ID:                    attempt.ID,
		SessionID:             attempt.SessionID,
		AttemptNumber:         attempt.AttemptNumber,
		State:                 string(attempt.State),
		Command:               attempt.Command,
		StartedAt:             attempt.StartedAt,
		FinishedAt:            attempt.FinishedAt,
		Success:               attempt.Success,
		ContinuationConfirmed: attempt.SessionIDVerified != nil && *attempt.SessionIDVerified,
		TranscriptGrew:        attempt.TranscriptGrew,
		ContinuationSessionID: attempt.ContinuationSessionID,
		Error:                 attempt.Error,
	}
}

// transitionDTO is the wire form of one state change.
type transitionDTO struct {
	ID         int64     `json:"id"`
	SessionID  string    `json:"session_id"`
	AttemptID  int64     `json:"attempt_id,omitempty"`
	FromState  string    `json:"from_state,omitempty"`
	ToState    string    `json:"to_state"`
	Reason     string    `json:"reason,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
}

// toTransitionDTO converts a stored transition.
func toTransitionDTO(transition storage.Transition) transitionDTO {
	return transitionDTO{
		ID:         transition.ID,
		SessionID:  transition.SessionID,
		AttemptID:  transition.AttemptID,
		FromState:  string(transition.FromState),
		ToState:    string(transition.ToState),
		Reason:     transition.Reason,
		OccurredAt: transition.OccurredAt,
	}
}

// notificationDTO is the wire form of one queued message.
type notificationDTO struct {
	ID        int64     `json:"id"`
	SessionID string    `json:"session_id,omitempty"`
	Event     string    `json:"event"`
	Status    string    `json:"status"`
	Attempts  int       `json:"attempts"`
	LastError string    `json:"last_error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	SentAt    time.Time `json:"sent_at,omitempty"`
}

// toNotificationDTO converts a stored notification.
func toNotificationDTO(notification storage.Notification) notificationDTO {
	return notificationDTO{
		ID:        notification.ID,
		SessionID: notification.SessionID,
		Event:     string(notification.Event),
		Status:    string(notification.Status),
		Attempts:  notification.Attempts,
		LastError: notification.LastError,
		CreatedAt: notification.CreatedAt,
		SentAt:    notification.SentAt,
	}
}

// verificationDTO is the wire form of the resume proof.
//
// The field used to be called session_id_preserved, and it was a lie twice over
// once resumes became background dispatches: the id is not preserved (a
// background agent continues the conversation under its own id) and what the
// verifier actually proves is that the job reported a continuation whose
// transcript grew. The key is renamed rather than deprecated because nothing
// reads it but ccresu's own tests.
type verificationDTO struct {
	// ContinuationSessionID is the session id the job reported, and
	// ContinuationTranscriptPath is the file its growth was measured on. They
	// differ from the requested id: a background agent continues the
	// conversation under its own.
	ContinuationSessionID      string   `json:"continuation_session_id,omitempty"`
	ContinuationTranscriptPath string   `json:"continuation_transcript_path,omitempty"`
	ContinuationConfirmed      bool     `json:"continuation_confirmed"`
	TranscriptGrew             bool     `json:"transcript_grew"`
	Proven                     bool     `json:"proven"`
	LinesBefore                int64    `json:"lines_before"`
	LinesAfter                 int64    `json:"lines_after"`
	SizeBefore                 int64    `json:"size_before"`
	SizeAfter                  int64    `json:"size_after"`
	Running                    bool     `json:"running"`
	Notes                      []string `json:"notes,omitempty"`
}

// toVerificationDTO converts a verification result.
func toVerificationDTO(verification recovery.Verification) verificationDTO {
	return verificationDTO{
		ContinuationSessionID:      verification.ContinuationSessionID,
		ContinuationTranscriptPath: verification.ContinuationTranscriptPath,
		ContinuationConfirmed:      verification.ContinuationConfirmed,
		TranscriptGrew:             verification.TranscriptGrew,
		Proven:                     verification.Proven,
		LinesBefore:                verification.LinesBefore,
		LinesAfter:                 verification.LinesAfter,
		SizeBefore:                 verification.SizeBefore,
		SizeAfter:                  verification.SizeAfter,
		Running:                    verification.Running,
		Notes:                      verification.Notes,
	}
}

// outcomeDTO is the wire form of one recovery attempt.
type outcomeDTO struct {
	SessionID    string          `json:"session_id"`
	State        string          `json:"state"`
	Resumed      bool            `json:"resumed"`
	RetryInMS    int64           `json:"retry_in_ms,omitempty"`
	Attempt      attemptDTO      `json:"attempt"`
	Verification verificationDTO `json:"verification"`
}

// toOutcomeDTO converts a recovery outcome.
func toOutcomeDTO(outcome recovery.Outcome) outcomeDTO {
	return outcomeDTO{
		SessionID:    outcome.SessionID,
		State:        string(outcome.State),
		Resumed:      outcome.Resumed,
		RetryInMS:    outcome.Delay.Milliseconds(),
		Attempt:      toAttemptDTO(outcome.Attempt),
		Verification: toVerificationDTO(outcome.Verification),
	}
}

// validateSessionID checks the shape of a session id from the path.
//
// Claude Code ids are UUIDs, but the check is deliberately about characters, not
// format, so a future id shape still works while anything that could confuse a
// path, a shell or a log line is rejected outright.
func validateSessionID(id string) *apiError {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return errInvalid("a session id is required")
	}
	if len(trimmed) > 128 {
		return errInvalid("a session id must be at most 128 characters")
	}
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.', r == ':':
		default:
			return errInvalid("the session id contains a character that is not allowed",
				fmt.Sprintf("%q is not allowed", string(r)))
		}
	}
	return nil
}

// validateLimitType checks a state filter.
func parseState(raw string) (storage.State, *apiError) {
	state := storage.State(strings.ToUpper(strings.TrimSpace(raw)))
	if !state.Valid() {
		return "", errInvalid("unknown session state", raw)
	}
	return state, nil
}

// claudeVersion is the provider half of GET /version.
type claudeVersion struct {
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
	Error     string `json:"error,omitempty"`
}

// probeClaudeVersion reports the installed Claude Code version, degrading to an
// explanatory message rather than failing the whole endpoint.
func (s *Server) probeClaudeVersion(r *http.Request) *claudeVersion {
	if s.cfg.VersionProbe == nil {
		return nil
	}
	report, err := s.cfg.VersionProbe.Version(r.Context())
	if err != nil {
		return &claudeVersion{Available: false, Error: err.Error()}
	}
	return &claudeVersion{Available: true, Version: report.Version.String()}
}

// versionResponse is the body of GET /version.
type versionResponse struct {
	Version   string         `json:"version"`
	Commit    string         `json:"commit"`
	BuildDate string         `json:"build_date"`
	Go        string         `json:"go"`
	Platform  string         `json:"platform"`
	API       string         `json:"api"`
	Claude    *claudeVersion `json:"claude,omitempty"`
}

// compile-time proof that the shapes above stay in step with the domain types.
var _ = claude.VersionReport{}
