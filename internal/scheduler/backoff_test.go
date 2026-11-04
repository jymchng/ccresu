package scheduler

import (
	"testing"
	"time"
)

func TestBackoff(t *testing.T) {
	t.Parallel()

	initial := 5 * time.Minute
	max := 30 * time.Minute

	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 5 * time.Minute},
		{1, 5 * time.Minute},
		{2, 10 * time.Minute},
		{3, 20 * time.Minute},
		{4, 30 * time.Minute}, // 40m would exceed the cap
		{5, 30 * time.Minute},
		{100, 30 * time.Minute},
		{1 << 20, 30 * time.Minute}, // must not overflow
	}

	for _, tc := range tests {
		if got := Backoff(tc.attempt, initial, max); got != tc.want {
			t.Fatalf("Backoff(%d) = %s, want %s", tc.attempt, got, tc.want)
		}
	}
}

func TestBackoffDefaults(t *testing.T) {
	t.Parallel()

	if got := Backoff(1, 0, 0); got != DefaultInitialInterval {
		t.Fatalf("Backoff with zero bounds = %s, want %s", got, DefaultInitialInterval)
	}
	// A max below the initial interval is raised to it rather than producing a
	// delay that shrinks.
	if got := Backoff(3, 10*time.Minute, time.Minute); got != 10*time.Minute {
		t.Fatalf("Backoff with max < initial = %s, want 10m", got)
	}
	if got := Backoff(1, -time.Minute, -time.Minute); got != DefaultInitialInterval {
		t.Fatalf("Backoff with negative bounds = %s", got)
	}
}

// TestBackoffIsMonotonic proves the delay never shrinks and never exceeds the
// cap, which is what makes the polling strategy safe to persist.
func TestBackoffIsMonotonic(t *testing.T) {
	t.Parallel()

	initial, max := 5*time.Minute, 30*time.Minute
	previous := time.Duration(0)
	for attempt := 1; attempt <= 50; attempt++ {
		got := Backoff(attempt, initial, max)
		if got < previous {
			t.Fatalf("Backoff(%d) = %s shrank below %s", attempt, got, previous)
		}
		if got > max {
			t.Fatalf("Backoff(%d) = %s exceeds the cap %s", attempt, got, max)
		}
		previous = got
	}
}
