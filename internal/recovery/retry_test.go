package recovery

import (
	"testing"
	"time"
)

func TestDefaultBackoffMatchesTheSpecification(t *testing.T) {
	b := DefaultBackoff()
	if b.Initial != 30*time.Second {
		t.Errorf("Initial = %s, want 30s", b.Initial)
	}
	if b.Max != 15*time.Minute {
		t.Errorf("Max = %s, want 15m", b.Max)
	}
	if b.Multiplier != 2 {
		t.Errorf("Multiplier = %g, want 2", b.Multiplier)
	}
	if b.Jitter != 0.15 {
		t.Errorf("Jitter = %g, want 0.15", b.Jitter)
	}
	if DefaultMaxAttempts != 10 {
		t.Errorf("DefaultMaxAttempts = %d, want 10", DefaultMaxAttempts)
	}
	if err := b.Validate(); err != nil {
		t.Errorf("Validate = %v, want nil", err)
	}
}

func TestBackoffBaseDoublesAndCaps(t *testing.T) {
	b := Backoff{Initial: 30 * time.Second, Max: 15 * time.Minute, Multiplier: 2, Jitter: 0}
	want := []time.Duration{
		30 * time.Second,
		1 * time.Minute,
		2 * time.Minute,
		4 * time.Minute,
		8 * time.Minute,
		15 * time.Minute,
		15 * time.Minute,
	}
	for i, expected := range want {
		attempt := i + 1
		if got := b.Base(attempt); got != expected {
			t.Errorf("Base(%d) = %s, want %s", attempt, got, expected)
		}
		if got := b.Delay(attempt); got != expected {
			t.Errorf("Delay(%d) with no jitter = %s, want %s", attempt, got, expected)
		}
	}
	if b.Base(0) != b.Base(1) || b.Base(-7) != b.Base(1) {
		t.Error("attempts below one must back off from the floor")
	}
}

func TestBackoffZeroValueUsesDefaults(t *testing.T) {
	var b Backoff
	if got := b.Base(1); got != DefaultInitialDelay {
		t.Errorf("Base(1) = %s, want %s", got, DefaultInitialDelay)
	}
	if got := b.withDefaults(); got.Initial != DefaultInitialDelay || got.Max != DefaultMaxDelay ||
		got.Multiplier != DefaultMultiplier || got.Jitter != 0 {
		t.Errorf("withDefaults() = %+v, want the documented defaults", got)
	}
}

func TestBackoffWithDefaultsRepairsWildValues(t *testing.T) {
	b := Backoff{Initial: 10 * time.Minute, Max: time.Minute, Multiplier: 0.2, Jitter: -3}
	fixed := b.withDefaults()
	if fixed.Max != fixed.Initial {
		t.Errorf("Max = %s, want it raised to Initial %s", fixed.Max, fixed.Initial)
	}
	if fixed.Multiplier != 1 {
		t.Errorf("Multiplier = %g, want 1", fixed.Multiplier)
	}
	if fixed.Jitter != 0 {
		t.Errorf("Jitter = %g, want 0", fixed.Jitter)
	}

	high := Backoff{Jitter: 4}.withDefaults()
	if high.Jitter != 1 {
		t.Errorf("Jitter = %g, want it clamped to 1", high.Jitter)
	}
}

func TestBackoffDelayJitterStaysWithinBounds(t *testing.T) {
	low := Backoff{Initial: 30 * time.Second, Max: 15 * time.Minute, Multiplier: 2, Jitter: 0.15,
		Rand: func() float64 { return 0 }}
	if got, want := low.Delay(1), time.Duration(float64(30*time.Second)*0.85); got != want {
		t.Errorf("Delay(1) at the bottom of the jitter = %s, want %s", got, want)
	}

	high := Backoff{Initial: 30 * time.Second, Max: 15 * time.Minute, Multiplier: 2, Jitter: 0.15,
		Rand: func() float64 { return 0.9999 }}
	if got := high.Delay(1); got <= 30*time.Second || got >= time.Duration(float64(30*time.Second)*1.15) {
		t.Errorf("Delay(1) at the top of the jitter = %s, want it above the base and below the jittered cap", got)
	}

	// A huge jitter at the floor is clamped to half the initial delay.
	crooked := Backoff{Initial: 30 * time.Second, Max: 15 * time.Minute, Multiplier: 2, Jitter: 1,
		Rand: func() float64 { return 0 }}
	if got := crooked.Delay(1); got != 15*time.Second {
		t.Errorf("Delay(1) at the clamp = %s, want 15s", got)
	}

	// The cap applies after jitter.
	top := Backoff{Initial: 30 * time.Second, Max: 15 * time.Minute, Multiplier: 2, Jitter: 1,
		Rand: func() float64 { return 1 }}
	if got, limit := top.Delay(9), 30*time.Minute; got != limit {
		t.Errorf("Delay(9) = %s, want the jittered cap %s", got, limit)
	}
}

func TestBackoffDelayWithRandomJitterIsBounded(t *testing.T) {
	b := DefaultBackoff()
	cap := DefaultMaxDelay
	for attempt := 1; attempt <= 12; attempt++ {
		delay := b.Delay(attempt)
		if delay <= 0 {
			t.Fatalf("Delay(%d) = %s, want a positive wait", attempt, delay)
		}
		if delay < DefaultInitialDelay/2 {
			t.Fatalf("Delay(%d) = %s, want at least half the initial delay", attempt, delay)
		}
		if limit := time.Duration(float64(cap) * 1.15); delay > limit {
			t.Fatalf("Delay(%d) = %s, want at most %s", attempt, delay, limit)
		}
	}
}

func TestBackoffBaseOverflowFallsBackToMax(t *testing.T) {
	b := Backoff{Initial: time.Second, Max: time.Minute, Multiplier: 10}
	if got := b.Base(400); got != time.Minute {
		t.Errorf("Base(400) = %s, want the cap %s", got, time.Minute)
	}
	neg := Backoff{Initial: -time.Second, Max: time.Minute, Multiplier: 2}
	if got := neg.Base(1); got != DefaultInitialDelay {
		t.Errorf("Base(1) with a negative initial = %s, want the default floor %s", got, DefaultInitialDelay)
	}
}

func TestBackoffSequence(t *testing.T) {
	b := Backoff{Initial: time.Second, Max: time.Minute, Multiplier: 2, Jitter: 0}
	if got := b.Sequence(0); got != nil {
		t.Errorf("Sequence(0) = %v, want nil", got)
	}
	if got := b.Sequence(-3); got != nil {
		t.Errorf("Sequence(-3) = %v, want nil", got)
	}
	sequence := b.Sequence(4)
	if len(sequence) != 4 {
		t.Fatalf("len(Sequence(4)) = %d, want 4", len(sequence))
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	for i := range want {
		if sequence[i] != want[i] {
			t.Errorf("Sequence(4)[%d] = %s, want %s", i, sequence[i], want[i])
		}
	}
}

func TestBackoffValidateAcceptsUsablePolicies(t *testing.T) {
	policies := []Backoff{
		{},
		DefaultBackoff(),
		{Initial: time.Second, Max: time.Second, Multiplier: 1, Jitter: 0},
	}
	for _, policy := range policies {
		if err := policy.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v, want nil", policy, err)
		}
	}
	// withDefaults repairs every field, so Validate can only accept.
	if err := (Backoff{Initial: -1, Jitter: 9}).Validate(); err != nil {
		t.Errorf("Validate must repair wild values, got %v", err)
	}
}

func TestFromConfigValues(t *testing.T) {
	b := FromConfigValues(5*time.Second, 2*time.Minute, 3, 0.25)
	if b.Initial != 5*time.Second || b.Max != 2*time.Minute || b.Multiplier != 3 || b.Jitter != 0.25 {
		t.Errorf("FromConfigValues = %+v, want the supplied values", b)
	}
}
