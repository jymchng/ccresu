package recovery

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"strings"
	"testing"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/storage"
)

func TestClassValidAndState(t *testing.T) {
	cases := []struct {
		class Class
		valid bool
		retry bool
		state State
	}{
		{ClassRetryable, true, true, StateRetryWait},
		{ClassPermanent, true, false, StateDiscovered},
		{ClassLimitInForce, true, false, StateWaitingForReset},
		{ClassUnrecoverable, true, false, StateUnrecoverable},
		{ClassCanceled, true, false, StateRetryWait},
		{Class("who"), false, false, StateDiscovered},
	}
	for _, tc := range cases {
		if got := tc.class.Valid(); got != tc.valid {
			t.Errorf("%q.Valid() = %v, want %v", tc.class, got, tc.valid)
		}
		if got := tc.class.Retryable(); got != tc.retry {
			t.Errorf("%q.Retryable() = %v, want %v", tc.class, got, tc.retry)
		}
		if got := tc.class.State(); got != tc.state {
			t.Errorf("%q.State() = %v, want %v", tc.class, got, tc.state)
		}
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want Class
	}{
		{"nil", nil, ClassRetryable},
		{"canceled", context.Canceled, ClassCanceled},
		{"wrapped canceled", fmt.Errorf("attempt: %w", context.Canceled), ClassCanceled},
		{"unrecoverable", claude.ErrUnrecoverable, ClassUnrecoverable},
		{"recovery unrecoverable", ErrUnrecoverable, ClassUnrecoverable},
		{"cli unavailable", claude.ErrCLIUnavailable, ClassPermanent},
		{"unsupported version", claude.ErrUnsupportedVersion, ClassPermanent},
		{"unrecognized version", claude.ErrUnrecognizedVersion, ClassPermanent},
		{"invalid session id", claude.ErrInvalidSessionID, ClassPermanent},
		{"empty message", claude.ErrEmptyMessage, ClassPermanent},
		{"message too long", claude.ErrMessageTooLong, ClassPermanent},
		{"session not preserved", claude.ErrSessionNotPreserved, ClassPermanent},
		{"session not found", claude.ErrSessionNotFound, ClassPermanent},
		{"store not found", storage.ErrNotFound, ClassPermanent},
		{"wrapped sentinel", fmt.Errorf("resume: %w", claude.ErrCLIUnavailable), ClassPermanent},
		// A limit refusal is its own class, decided by the sentinel and not by
		// the words in the banner.
		{"limit refusal", fmt.Errorf("claude -p --resume failed (exit 1): %w", claude.ErrUsageLimitInForce), ClassLimitInForce},
		{"bare limit refusal", claude.ErrUsageLimitInForce, ClassLimitInForce},
		// The banner is prose, and the permanent markers below are guesses made
		// from prose. The typed sentinel wins: a refusal that mentions the
		// account block, an auth failure or a reset transport is still a wait.
		{"refusal beats the account-limit marker",
			fmt.Errorf("usage limit reached for this account: %w", claude.ErrUsageLimitInForce), ClassLimitInForce},
		{"refusal beats the auth markers",
			fmt.Errorf("unauthorized: invalid api key: %w", claude.ErrUsageLimitInForce), ClassLimitInForce},
		{"refusal beats the billing marker",
			fmt.Errorf("credit balance is too low: %w", claude.ErrUsageLimitInForce), ClassLimitInForce},
		{"refusal beats the retryable markers",
			fmt.Errorf("connection reset: %w", claude.ErrUsageLimitInForce), ClassLimitInForce},
		// ErrCLIUnavailable keeps its meaning: it is not a refusal, so a real
		// exec failure is still permanent.
		{"cli unavailable beside a limit noun",
			fmt.Errorf("%w: usage limit reached", claude.ErrCLIUnavailable), ClassPermanent},
		// An untyped banner is prose only: it stays a guess, and the permanent
		// account-block marker keeps its current meaning.
		{"untyped account limit stays permanent", errors.New("usage limit reached for this account"), ClassPermanent},
		{"untyped window banner is retryable", errors.New("Claude usage limit reached. Your limit will reset at 10am"), ClassRetryable},
		{"invalid api key", errors.New("request failed: Invalid API key"), ClassPermanent},
		{"auth error", errors.New("{\"type\":\"authentication_error\"}"), ClassPermanent},
		{"unauthorized", errors.New("HTTP 401 Unauthorized"), ClassPermanent},
		{"forbidden", errors.New("403 Forbidden"), ClassPermanent},
		{"billing", errors.New("Your credit balance is too low"), ClassPermanent},
		{"account limit", errors.New("usage limit reached for this account"), ClassPermanent},
		{"permanent wins over retryable", errors.New("timeout: authentication_error"), ClassPermanent},
		{"connection reset", errors.New("read: Connection reset by peer"), ClassRetryable},
		{"connection refused", errors.New("dial tcp: connection refused"), ClassRetryable},
		{"eof", errors.New("unexpected EOF"), ClassRetryable},
		{"timeout text", errors.New("operation timed out"), ClassRetryable},
		{"429", errors.New("status 429"), ClassRetryable},
		{"503", errors.New("status 503"), ClassRetryable},
		{"overloaded", errors.New("Overloaded"), ClassRetryable},
		{"deadline", context.DeadlineExceeded, ClassRetryable},
		{"net error", &net.DNSError{Err: "failure", Name: "example.com"}, ClassRetryable},
		{"path error", &fs.PathError{Op: "run", Path: "/usr/bin/claude", Err: errors.New("busy")}, ClassRetryable},
		// The three cases above are the transient shapes that used to have
		// their own branches in Classify. The retryable fallthrough now
		// classifies them, so they must keep landing there.
		{"generic", errors.New("something odd happened"), ClassRetryable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.err); got != tc.want {
				t.Fatalf("Classify(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestRetryable(t *testing.T) {
	if Retryable(nil) {
		t.Error("a nil error is not retryable")
	}
	if !Retryable(errors.New("connection reset by peer")) {
		t.Error("a transport failure must be retryable")
	}
	if Retryable(claude.ErrCLIUnavailable) {
		t.Error("a missing CLI must not be retryable")
	}
	if Retryable(claude.ErrUsageLimitInForce) {
		t.Error("a limit refusal must not be retryable: the reset decides when to try again")
	}
}

// TestALimitRefusalNeverRoutesToDiscovered is the routing half of the contract.
//
// The class decides where a failed attempt leaves the session, and for a refusal
// that must be the wait: not DISCOVERED, which re-reads the transcript and
// schedules the next reset as if nothing had been learnt, and not RETRY_WAIT,
// which spends the budget asking the same question on a backoff.
func TestALimitRefusalNeverRoutesToDiscovered(t *testing.T) {
	refusal := fmt.Errorf("resume session abc: %w", claude.ErrUsageLimitInForce)
	class := Classify(refusal)

	if class == ClassPermanent {
		t.Fatal("a limit refusal classified as permanent: the conversation is fine, only the window is closed")
	}
	if got := class.State(); got != StateWaitingForReset {
		t.Errorf("class.State() = %s, want %s", got, StateWaitingForReset)
	}
	// The attempt budget must not change that: a refusal at the last attempt of
	// a cycle is still a wait, because the next useful moment is the reset, not
	// the next cycle.
	for _, attempts := range []int{0, 1, 9, 10, 99} {
		if got := targetFor(class, attempts, 10); got != StateWaitingForReset {
			t.Errorf("targetFor(%s, %d attempts, 10 max) = %s, want %s",
				class, attempts, got, StateWaitingForReset)
		}
	}
	// And the edge it needs must exist, or the manager cannot record it.
	if !CanTransition(StateResuming, StateWaitingForReset) {
		t.Error("RESUMING has no edge to WAITING_FOR_RESET: the refusal cannot be recorded")
	}
}

func TestExplain(t *testing.T) {
	if got := Explain(nil); got != "ok" {
		t.Errorf("Explain(nil) = %q, want %q", got, "ok")
	}
	got := Explain(claude.ErrCLIUnavailable)
	if !strings.HasPrefix(got, "permanent: ") {
		t.Errorf("Explain = %q, want a permanent prefix", got)
	}
	if !strings.Contains(got, claude.ErrCLIUnavailable.Error()) {
		t.Errorf("Explain = %q, want it to keep the cause", got)
	}
	if got := Explain(errors.New("connection reset")); !strings.HasPrefix(got, "retryable: ") {
		t.Errorf("Explain = %q, want a retryable prefix", got)
	}
}
