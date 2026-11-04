package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/storage"
)

func TestToScheduleDTO(t *testing.T) {
	resetAt := baseTime.Add(time.Hour)
	schedule := storage.Schedule{
		SessionID:     sessionID,
		UsageLimitID:  41,
		ResetAt:       resetAt,
		ResetTimezone: "UTC",
		ResetRaw:      "resets at 10am",
		Unknown:       true,
		Attempts:      2,
		NextPollAt:    resetAt,
	}
	dto := toScheduleDTO(schedule)
	if dto.SessionID != sessionID || dto.UsageLimitID != 41 {
		t.Fatalf("dto = %+v", dto)
	}
	if !dto.ResetAt.Equal(resetAt) || dto.ResetTimezone != "UTC" || dto.ResetRaw != "resets at 10am" {
		t.Fatalf("dto = %+v", dto)
	}
	if !dto.Unknown || dto.Attempts != 2 {
		t.Fatalf("dto = %+v", dto)
	}
	if !dto.NextPollAt.Equal(resetAt) {
		t.Fatalf("next poll = %v, want %v", dto.NextPollAt, resetAt)
	}
	// DueAt is derived from the stored schedule rather than copied, so it has to
	// be present on the wire form.
	if !dto.DueAt.Equal(schedule.DueAt()) {
		t.Fatalf("due at = %v, want %v", dto.DueAt, schedule.DueAt())
	}
}

func TestToNotificationDTO(t *testing.T) {
	sentAt := baseTime.Add(time.Minute)
	notification := storage.Notification{
		ID:        9,
		SessionID: sessionID,
		Event:     storage.EventRecoverySuccess,
		Status:    storage.NotificationSent,
		Attempts:  1,
		LastError: "flaky",
		SentAt:    sentAt,
	}
	dto := toNotificationDTO(notification)
	if dto.ID != 9 || dto.SessionID != sessionID {
		t.Fatalf("dto = %+v", dto)
	}
	if dto.Event != string(storage.EventRecoverySuccess) || dto.Status != string(storage.NotificationSent) {
		t.Fatalf("dto = %+v", dto)
	}
	if dto.Attempts != 1 || dto.LastError != "flaky" {
		t.Fatalf("dto = %+v", dto)
	}
	if !dto.SentAt.Equal(sentAt) {
		t.Fatalf("sent at = %v, want %v", dto.SentAt, sentAt)
	}
}

func TestAPIErrorIsAnError(t *testing.T) {
	err := &apiError{Status: http.StatusNotFound, Code: codeNotFound, Message: "session x does not exist"}
	if err.Error() != "session x does not exist" {
		t.Fatalf("Error() = %q", err.Error())
	}
}

func TestDescribeState(t *testing.T) {
	if got := describeState(""); got != "unknown" {
		t.Fatalf("describeState(\"\") = %q", got)
	}
	if got := describeState(storage.StateRunning); got != string(storage.StateRunning) {
		t.Fatalf("describeState(running) = %q", got)
	}
}

func TestResponseRecorderRecordsStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	recorder := &responseRecorder{ResponseWriter: rec}

	recorder.WriteHeader(http.StatusAccepted)
	recorder.WriteHeader(http.StatusTeapot) // ignored: the first status wins
	if recorder.status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", recorder.status)
	}
	recorder.Flush()
	if !rec.Flushed {
		t.Fatal("Flush did not reach the underlying writer")
	}
	if recorder.Unwrap() != http.ResponseWriter(rec) {
		t.Fatal("Unwrap did not return the original writer")
	}

	// A write without an explicit status implies 200.
	implicit := &responseRecorder{ResponseWriter: httptest.NewRecorder()}
	if _, err := implicit.Write([]byte("ok")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if implicit.status != http.StatusOK {
		t.Fatalf("status = %d, want 200", implicit.status)
	}

	// Flush on a writer that cannot flush must not panic.
	crippled := &responseRecorder{ResponseWriter: nonFlusher{httptest.NewRecorder()}}
	crippled.Flush()
}

func TestRateLimiterSweepDropsIdleBuckets(t *testing.T) {
	limiter := newRateLimiter(1, 1)
	start := baseTime
	if ok, _ := limiter.allow("10.0.0.1", start); !ok {
		t.Fatal("first request was refused")
	}
	if _, ok := limiter.buckets["10.0.0.1"]; !ok {
		t.Fatal("bucket was not created")
	}

	// Well past the sweep interval and past a full refill, the idle bucket goes.
	later := start.Add(rlSweepInterval).Add(2 * time.Second)
	if ok, _ := limiter.allow("10.0.0.2", later); !ok {
		t.Fatal("request after the sweep was refused")
	}
	if _, ok := limiter.buckets["10.0.0.1"]; ok {
		t.Fatal("idle bucket survived the sweep")
	}
	if _, ok := limiter.buckets["10.0.0.2"]; !ok {
		t.Fatal("the active bucket was dropped")
	}
}

func TestRateLimiterSweepBoundsTheTable(t *testing.T) {
	limiter := newRateLimiter(1, 1)
	start := baseTime
	// More distinct clients than the table may hold.
	for i := 0; i < rlMaxBuckets+10; i++ {
		limiter.allow(string(rune('a'+i%26))+"-"+strconv.Itoa(i), start)
	}
	// Every entry shares one timestamp, so the idle sweep cannot help; the hard cap
	// has to bound the table anyway.
	if len(limiter.buckets) > rlMaxBuckets {
		t.Fatalf("bucket table = %d entries, want at most %d", len(limiter.buckets), rlMaxBuckets)
	}
	// The limiter stays usable after eviction.
	if ok, _ := limiter.allow("late", start); !ok {
		t.Fatal("the limiter refused a fresh client after a sweep")
	}
}
