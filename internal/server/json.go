package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/jymchng/ccresu/internal/recovery"
	"github.com/jymchng/ccresu/internal/storage"
)

// apiError is the single error shape the API returns.
//
// Status is carried alongside the JSON document so the handler layer and the
// middleware layer agree on the contract without a second lookup table.
type apiError struct {
	Status  int      `json:"-"`
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Details []string `json:"details,omitempty"`
}

// Error implements error so a handler can return a *apiError like any other.
func (e *apiError) Error() string { return e.Message }

// errorEnvelope wraps the error so a client always reads {"error": {...}} and
// never has to distinguish an object from a bare string.
type errorEnvelope struct {
	Error *apiError `json:"error"`
}

// Error codes. They are stable strings a script may branch on; the human text is
// documentation, not API.
const (
	codeUnauthorized    = "unauthorized"
	codeOriginForbidden = "origin_not_allowed"
	codeNotFound        = "not_found"
	codeMethod          = "method_not_allowed"
	codeInvalid         = "invalid_argument"
	codeNotEligible     = "not_eligible"
	codeLimitNotCleared = "limit_not_cleared"
	codeTooLarge        = "payload_too_large"
	codeUnsupportedType = "unsupported_media_type"
	codeRateLimited     = "too_many_requests"
	codeUnavailable     = "unavailable"
	codeInternal        = "internal"
)

// errUnauthorized reports a missing or wrong bearer token.
func errUnauthorized(message string) *apiError {
	return &apiError{Status: http.StatusUnauthorized, Code: codeUnauthorized, Message: message}
}

// errNotFound reports a missing route, session or resource.
func errNotFound(message string) *apiError {
	return &apiError{Status: http.StatusNotFound, Code: codeNotFound, Message: message}
}

// errInvalid reports a rejected argument, naming the field when there is one.
func errInvalid(message string, details ...string) *apiError {
	return &apiError{Status: http.StatusBadRequest, Code: codeInvalid, Message: message, Details: details}
}

// errNotEligible reports a session that exists but whose state forbids the
// operation, which is the same distinction the CLI draws with exit code 4.
func errNotEligible(message string) *apiError {
	return &apiError{Status: http.StatusConflict, Code: codeNotEligible, Message: message}
}

// errLimitNotCleared reports a resume that was refused because the usage limit
// has not reset yet.
//
// It is deliberately its own code rather than not_eligible. The session is
// eligible and the attempt was made, so "settled, disabled or unrecoverable"
// would be the wrong reading; and unlike a state refusal this condition has a
// known end — the reset instant — so a script can branch on the code and ask
// again after it instead of treating the session as finished. It is a 409
// because the request conflicts with the state the account is in, not with the
// request itself.
func errLimitNotCleared(message string) *apiError {
	return &apiError{Status: http.StatusConflict, Code: codeLimitNotCleared, Message: message}
}

// errInternal reports an unexpected server-side failure. The underlying error is
// logged, never returned, so a database path or driver detail cannot leak into a
// response.
func errInternal() *apiError {
	return &apiError{Status: http.StatusInternalServerError, Code: codeInternal,
		Message: "the server could not complete the request; see the ccresu log"}
}

// mapError converts a domain error into the API contract.
//
// The mapping mirrors the CLI exit codes: not-found is 404, a state-machine
// refusal is 409, an interrupted request is 503. Anything unrecognised becomes a
// logged 500 rather than a guess.
func mapError(err error) *apiError {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, storage.ErrNotFound):
		return errNotFound("the requested resource does not exist")
	case errors.Is(err, recovery.ErrNoSession):
		return errNotFound("the session id is required")
	case errors.Is(err, recovery.ErrNotEligible),
		errors.Is(err, recovery.ErrAlreadyRunning),
		errors.Is(err, recovery.ErrConcurrencyLimit),
		errors.Is(err, recovery.ErrMaxAttempts),
		errors.Is(err, recovery.ErrDisabled),
		errors.Is(err, recovery.ErrUnrecoverable):
		return errNotEligible(err.Error())
	case errors.Is(err, recovery.ErrLimitNotCleared):
		// Both a refusal (*LimitInForceError, which reports the sentinel through
		// Is) and the pre-resume limit check arrive here. Without this case they
		// fell through to the default branch and answered 500 for the most
		// routine outcome in the product: the window has not reopened yet.
		return errLimitNotCleared(err.Error())
	case errors.Is(err, storage.ErrInvalid):
		return errInvalid(err.Error())
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return &apiError{Status: http.StatusServiceUnavailable, Code: codeUnavailable,
			Message: "the request was cancelled before it finished"}
	case errors.Is(err, storage.ErrClosed):
		return &apiError{Status: http.StatusServiceUnavailable, Code: codeUnavailable,
			Message: "the database is closed"}
	default:
		return errInternal()
	}
}

// writeJSON encodes payload with the status code.
//
// Encoding happens into a buffer first so a failure halfway through cannot emit a
// truncated body after a 200 has already been written.
func (s *Server) writeJSON(w http.ResponseWriter, status int, payload any) {
	body, err := marshalJSON(payload)
	if err != nil {
		s.cfg.Log.Error("encode api response", "error", err)
		http.Error(w, `{"error":{"code":"internal","message":"the response could not be encoded"}}`,
			http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		s.cfg.Log.Debug("write api response", "error", err)
	}
}

// writeError renders one error envelope.
func (s *Server) writeError(w http.ResponseWriter, e *apiError) {
	if e == nil {
		return
	}
	if e.Status >= http.StatusInternalServerError {
		s.cfg.Log.Error("api request failed", "status", e.Status, "code", e.Code, "message", e.Message)
	}
	if e.Status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="ccresu"`)
	}
	s.writeJSON(w, e.Status, errorEnvelope{Error: e})
}

// statusReport is the body of GET /status.
type statusReport struct {
	Status               string         `json:"status"`
	Version              string         `json:"version"`
	Now                  time.Time      `json:"now"`
	UptimeSeconds        float64        `json:"uptime_seconds"`
	ConfigPath           string         `json:"config_path,omitempty"`
	DatabasePath         string         `json:"database_path,omitempty"`
	Sessions             int            `json:"sessions"`
	EnabledSessions      int            `json:"enabled_sessions"`
	States               map[string]int `json:"states"`
	PendingRecoveries    int            `json:"pending_recoveries"`
	Schedules            int            `json:"schedules"`
	NextScheduleAt       *time.Time     `json:"next_schedule_at,omitempty"`
	PendingNotifications int            `json:"pending_notifications"`
	TelegramEnabled      bool           `json:"telegram_enabled"`
	RecoveryEnabled      bool           `json:"recovery_enabled"`
	MaxAttempts          int            `json:"max_attempts"`
	Monitoring           bool           `json:"monitoring"`
}

// stateOrder is the stable order states are listed in, so a report does not
// reshuffle between calls.
var stateOrder = []storage.State{
	storage.StateDiscovered, storage.StateRunning, storage.StateUsageLimitDetected,
	storage.StateWaitingForReset, storage.StateResetTimeReached, storage.StateCheckingSession,
	storage.StateCheckingLimit, storage.StateResuming, storage.StateResumed,
	storage.StateRetryWait, storage.StateCompleted,
	storage.StateUserTerminated, storage.StateDisabled, storage.StateUnrecoverable,
}

// sortedStates renders the state histogram in the documented order.
func sortedStates(counts map[string]int) map[string]int {
	out := make(map[string]int, len(counts))
	for _, state := range stateOrder {
		if count, ok := counts[string(state)]; ok {
			out[string(state)] = count
		}
	}
	// Any state the histogram has that the list somehow missed still appears, so
	// a report never silently hides a row.
	extra := make([]string, 0)
	for state := range counts {
		if _, ok := out[state]; !ok {
			extra = append(extra, state)
		}
	}
	sort.Strings(extra)
	for _, state := range extra {
		out[state] = counts[state]
	}
	return out
}

// unsafeValue renders a value that must never leak: the Telegram bot token.
func unsafeValue() string { return "[redacted]" }

// describeState renders a state for an error message.
func describeState(state storage.State) string {
	if state == "" {
		return "unknown"
	}
	return string(state)
}

// queryInt parses an optional integer query parameter within [min, max].
func queryInt(values map[string][]string, key string, fallback, min, max int) (int, *apiError) {
	raw, ok := values[key]
	if !ok || len(raw) == 0 || raw[0] == "" {
		return fallback, nil
	}
	parsed, err := parsePositiveInt(raw[0])
	if err != nil {
		return 0, errInvalid(fmt.Sprintf("query parameter %q must be a whole number", key), raw[0])
	}
	if parsed < min || parsed > max {
		return 0, errInvalid(fmt.Sprintf("query parameter %q must be between %d and %d", key, min, max),
			fmt.Sprintf("%d", parsed))
	}
	return parsed, nil
}

// parsePositiveInt parses a base-10 non-negative integer without accepting signs
// or spaces, so " 3" and "+3" are rejected instead of quietly accepted.
func parsePositiveInt(raw string) (int, error) {
	if raw == "" {
		return 0, fmt.Errorf("empty")
	}
	value := 0
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not a digit")
		}
		value = value*10 + int(r-'0')
		if value > 1_000_000 {
			return 0, fmt.Errorf("too large")
		}
	}
	return value, nil
}

// queryBool parses an optional boolean query parameter.
func queryBool(values map[string][]string, key string) (*bool, *apiError) {
	raw, ok := values[key]
	if !ok || len(raw) == 0 || raw[0] == "" {
		return nil, nil
	}
	switch raw[0] {
	case "true", "1", "yes":
		value := true
		return &value, nil
	case "false", "0", "no":
		value := false
		return &value, nil
	default:
		return nil, errInvalid(fmt.Sprintf("query parameter %q must be true or false", key), raw[0])
	}
}
