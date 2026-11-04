package recovery

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/storage"
)

func TestRetryErrorCarriesTheSchedule(t *testing.T) {
	cause := errors.New("connection reset by peer")
	err := &RetryError{SessionID: "abc", Attempt: 2, Max: 10, Delay: 90 * time.Second, Cause: cause}

	message := err.Error()
	for _, want := range []string{"session abc", "attempt 2/10", "retryable", "1m30s", "connection reset"} {
		if !strings.Contains(message, want) {
			t.Errorf("message %q is missing %q", message, want)
		}
	}
	if !errors.Is(err, cause) {
		t.Error("RetryError must expose its cause to errors.Is")
	}
	var recovered *RetryError
	if !errors.As(errors.Join(errors.New("outer"), err), &recovered) {
		t.Error("RetryError must be recoverable through errors.As")
	}
}

func TestTerminalErrorNamesTheState(t *testing.T) {
	cause := errors.New("boom")
	err := &terminalError{SessionID: "abc", State: StatePermanentlyFailed, Cause: cause}
	message := err.Error()
	if !strings.Contains(message, "session abc") || !strings.Contains(message, string(StatePermanentlyFailed)) {
		t.Errorf("message %q does not name the session and state", message)
	}
	if !errors.Is(err, cause) {
		t.Error("terminalError must expose its cause")
	}
}

func TestSessionErrorWrapsSessionID(t *testing.T) {
	err := sessionError(ErrDisabled, "abc")
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("error = %v, want ErrDisabled", err)
	}
	if !strings.Contains(err.Error(), "abc") {
		t.Errorf("error %q does not name the session", err)
	}
}

func TestStateErrorIsNotEligible(t *testing.T) {
	err := stateError("abc", storage.StateCompleted, storage.StateCheckingSession)
	if !errors.Is(err, ErrNotEligible) {
		t.Fatalf("error = %v, want ErrNotEligible", err)
	}
	for _, want := range []string{"abc", string(storage.StateCompleted), string(storage.StateCheckingSession)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err, want)
		}
	}
}
