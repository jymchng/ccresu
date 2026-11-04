package scheduler

import "time"

// The polling defaults for a reset time the message did not state.
const (
	// DefaultInitialInterval is the first poll delay. Five minutes matches the
	// cadence of the upstream five-hour window: polling more often than the
	// window can move gains nothing and costs a resume attempt.
	DefaultInitialInterval = 5 * time.Minute
	// DefaultMaxInterval caps the growth of the poll delay, so a long limit is
	// still noticed within half an hour.
	DefaultMaxInterval = 30 * time.Minute
)

// Backoff returns the poll delay after the given number of polls have already
// been spent.
//
// The delay doubles from initial up to max: 5m, 10m, 20m, 30m, 30m… attempt is
// one-based, so attempt 1 (no polls spent yet) yields the initial interval.
func Backoff(attempt int, initial, max time.Duration) time.Duration {
	if initial <= 0 {
		initial = DefaultInitialInterval
	}
	if max <= 0 {
		max = DefaultMaxInterval
	}
	if max < initial {
		max = initial
	}
	if attempt < 1 {
		return initial
	}
	delay := initial
	// Doubling in a loop rather than shifting avoids integer overflow for a
	// large attempt count, which a corrupted persisted counter could supply.
	for i := 1; i < attempt; i++ {
		if delay >= max/2 {
			return max
		}
		delay *= 2
	}
	if delay > max {
		return max
	}
	return delay
}
