package recovery

import (
	"errors"
	"strings"
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

func TestBackoffWithDefaultsIsAUseTimeFloor(t *testing.T) {
	// withDefaults still produces something usable from nonsense, so Base and
	// Delay stay total for a caller that never went through New. That is a
	// fallback, not validation: Validate rejects these policies first.
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
		t.Errorf("Jitter = %g, want it pinned to 1", high.Jitter)
	}

	// The floor never invents a value for a field the caller set.
	explicit := Backoff{Initial: 5 * time.Second, Max: 2 * time.Minute, Multiplier: 3, Jitter: 0.4}.withDefaults()
	if explicit.Initial != 5*time.Second || explicit.Max != 2*time.Minute ||
		explicit.Multiplier != 3 || explicit.Jitter != 0.4 {
		t.Errorf("withDefaults(%+v) = %+v, want the supplied values untouched", explicit, explicit)
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

	// The cap applies after jitter, and needs no clamp: base never exceeds Max,
	// so the jittered wait never exceeds Max*(1+Jitter).
	top := Backoff{Initial: 30 * time.Second, Max: 15 * time.Minute, Multiplier: 2, Jitter: 1,
		Rand: func() float64 { return 1 }}
	if got, limit := top.Delay(9), 30*time.Minute; got != limit {
		t.Errorf("Delay(9) = %s, want the jittered cap %s", got, limit)
	}

	// At the bottom of the jitter the wait is base*(1-Jitter) exactly; the old
	// half-the-initial-delay clamp is gone, so no hidden floor distorts it.
	bottom := Backoff{Initial: 30 * time.Second, Max: 15 * time.Minute, Multiplier: 2, Jitter: 0.9,
		Rand: func() float64 { return 0 }}
	if got, want := bottom.Delay(1), time.Duration(float64(30*time.Second)*0.1); got != want {
		t.Errorf("Delay(1) at the bottom of a wide jitter = %s, want %s", got, want)
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
		// Only the fields the caller set are checked; an unset Max takes the
		// documented 15m cap, which comfortably covers a 30s floor.
		{Initial: 30 * time.Second},
		{Jitter: 0.5},
	}
	for _, policy := range policies {
		if err := policy.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v, want nil", policy, err)
		}
	}
}

func TestBackoffValidateRejectsMisconfiguration(t *testing.T) {
	cases := []struct {
		name   string
		policy Backoff
		want   string
	}{
		{
			name:   "negative initial delay",
			policy: Backoff{Initial: -time.Second},
			want:   "initial delay must not be negative",
		},
		{
			name:   "negative max delay",
			policy: Backoff{Max: -time.Second},
			want:   "max delay must not be negative",
		},
		{
			name:   "max shorter than initial",
			policy: Backoff{Initial: 10 * time.Minute, Max: time.Minute},
			want:   "max delay 1m0s is shorter than the initial delay 10m0s",
		},
		{
			name:   "max unset but smaller than an explicit initial",
			policy: Backoff{Initial: DefaultMaxDelay + time.Minute},
			want:   "shorter than the initial delay",
		},
		{
			name:   "multiplier below one",
			policy: Backoff{Multiplier: 0.5},
			want:   "multiplier 0.5 must be at least 1",
		},
		{
			name:   "negative multiplier",
			policy: Backoff{Multiplier: -2},
			want:   "multiplier -2 must be at least 1",
		},
		{
			name:   "negative jitter",
			policy: Backoff{Jitter: -0.1},
			want:   "jitter -0.1 must be within [0,1)",
		},
		{
			name:   "jitter of one",
			policy: Backoff{Jitter: 1},
			want:   "jitter 1 must be within [0,1)",
		},
		{
			name:   "jitter above one",
			policy: Backoff{Jitter: 4},
			want:   "jitter 4 must be within [0,1)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.policy.Validate()
			if err == nil {
				t.Fatalf("Validate(%+v) = nil, want a rejection", tc.policy)
			}
			if !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("Validate error = %v, want it to wrap ErrInvalidConfig", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Validate error = %q, want it to mention %q", err, tc.want)
			}
		})
	}

	// A misconfigured policy is never repaired into acceptance: the same object
	// that withDefaults would fix is still rejected on the way in.
	wild := Backoff{Initial: 10 * time.Minute, Max: time.Minute, Multiplier: 0.2, Jitter: -3}
	if err := wild.Validate(); err == nil {
		t.Error("Validate must reject a policy that defaulting would otherwise repair")
	}
}

func TestBackoffDelayNeverNegativeForExtremeRand(t *testing.T) {
	// withDefaults pins Jitter into [0,1], which is what keeps the wait from
	// going negative; no branch in Delay has to paper over it.
	for _, jitter := range []float64{-5, 0, 0.15, 1, 9} {
		for _, r := range []float64{0, 0.5, 1} {
			b := Backoff{Initial: time.Second, Max: time.Minute, Multiplier: 2, Jitter: jitter,
				Rand: func() float64 { return r }}
			for attempt := 1; attempt <= 4; attempt++ {
				delay := b.Delay(attempt)
				if delay < 0 {
					t.Errorf("Delay(%d) with jitter %g, rand %g = %s, want a non-negative wait",
						attempt, jitter, r, delay)
				}
				if limit := time.Duration(float64(time.Minute) * (1 + min(max(jitter, 0), 1))); delay > limit {
					t.Errorf("Delay(%d) with jitter %g, rand %g = %s, want at most %s",
						attempt, jitter, r, delay, limit)
				}
			}
		}
	}
}

func TestFromConfigValues(t *testing.T) {
	b := FromConfigValues(5*time.Second, 2*time.Minute, 3, 0.25)
	if b.Initial != 5*time.Second || b.Max != 2*time.Minute || b.Multiplier != 3 || b.Jitter != 0.25 {
		t.Errorf("FromConfigValues = %+v, want the supplied values", b)
	}
}
