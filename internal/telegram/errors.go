package telegram

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Errors reported by this package. Every one of them is safe to log: the bot
// token is never part of an error value, only of the request URL that produced
// it.
var (
	// ErrDisabled reports that the event's notification switch is off.
	ErrDisabled = errors.New("telegram: notifications are disabled")
	// ErrInvalidConfig reports unusable client or notifier settings.
	ErrInvalidConfig = errors.New("telegram: invalid configuration")
	// ErrEmptyMessage reports an empty message body.
	ErrEmptyMessage = errors.New("telegram: message text is required")
	// ErrTooLong reports a body above the Bot API's 4096-character limit.
	ErrTooLong = errors.New("telegram: message is too long")
	// ErrMalformedResponse reports a reply that is not the documented envelope.
	ErrMalformedResponse = errors.New("telegram: malformed API response")
	// ErrNotDelivered wraps every delivery failure, so a caller can decide with
	// a single errors.Is whether a notification still needs attention.
	ErrNotDelivered = errors.New("telegram: message was not delivered")
	// ErrInvalidEvent reports an event this package cannot render.
	ErrInvalidEvent = errors.New("telegram: unknown notification event")
)

// APIError is a rejection reported by the Bot API.
//
// It keeps the HTTP status, the API error code and the human-readable
// description, because those three together are what tells an operator whether
// the token is wrong (401), the chat id is wrong (400 "chat not found") or the
// bot is being rate limited (429).
type APIError struct {
	// Method is the Bot API method that was called.
	Method string
	// StatusCode is the HTTP status of the reply.
	StatusCode int
	// ErrorCode is the Bot API's own error code, when present.
	ErrorCode int
	// Description is the API's explanation, already redacted.
	Description string
	// RetryAfter is how long the API asked the caller to wait, when it said so.
	RetryAfter time.Duration
}

// Error implements error.
func (e *APIError) Error() string {
	description := strings.TrimSpace(e.Description)
	if description == "" {
		description = "no description"
	}
	message := fmt.Sprintf("telegram: %s rejected with status %d: %s", e.Method, e.StatusCode, description)
	if e.RetryAfter > 0 {
		message += fmt.Sprintf(" (retry after %s)", e.RetryAfter)
	}
	return message
}

// Unwrap exposes ErrNotDelivered, so errors.Is(err, ErrNotDelivered) holds for
// every rejection.
func (e *APIError) Unwrap() error { return ErrNotDelivered }

// Retryable reports whether repeating the call could succeed: rate limits and
// server faults can clear, a rejected token or chat id cannot.
func (e *APIError) Retryable() bool {
	return e.StatusCode == 429 || e.StatusCode >= 500 || e.ErrorCode == 429
}

// transportError is a failure that never reached the Bot API: DNS, connection,
// TLS or a malformed reply. Its Cause is already redacted.
type transportError struct {
	Method string
	Cause  string
}

// Error implements error.
func (e *transportError) Error() string {
	return fmt.Sprintf("telegram: %s failed: %s", e.Method, e.Cause)
}

// Unwrap exposes ErrNotDelivered.
func (e *transportError) Unwrap() error { return ErrNotDelivered }

// Retryable reports that a transport failure is worth another attempt.
func (e *transportError) Retryable() bool { return true }

// retryable reports whether err is worth retrying.
//
// An unmapped error is treated as retryable but bounded by the attempt budget:
// a transient library fault is more likely than a permanent one, and the
// budget stops it from looping.
func retryable(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Retryable()
	}
	var transportErr *transportError
	if errors.As(err, &transportErr) {
		return true
	}
	if errors.Is(err, ErrMalformedResponse) {
		return true
	}
	return true
}
