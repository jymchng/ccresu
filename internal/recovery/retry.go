package recovery

import (
	"fmt"
	"math"
	"math/rand/v2"
	"time"
)

// The retry policy defaults, matching configs/config.example.yaml.
const (
	// DefaultInitialDelay is the wait before the second attempt.
	DefaultInitialDelay = 30 * time.Second
	// DefaultMaxDelay caps the wait between attempts.
	DefaultMaxDelay = 15 * time.Minute
	// DefaultMultiplier is the per-attempt growth factor.
	DefaultMultiplier = 2.0
	// DefaultJitter is the fraction of the delay that is randomised, ±15%.
	DefaultJitter = 0.15
	// DefaultMaxAttempts is the attempt budget per session.
	DefaultMaxAttempts = 10
)

// Backoff computes the wait between recovery attempts.
//
// The delay grows geometrically — 30s, 1m, 2m, 4m, 8m, then 15m for ever — and
// is then spread by ±Jitter so a fleet of sessions that all hit the same limit
// does not resume in lockstep.
//
// Rand is injected so tests are deterministic; nil means the package-level
// generator.
type Backoff struct {
	// Initial is the wait before the second attempt.
	Initial time.Duration
	// Max caps the wait, before jitter is applied.
	Max time.Duration
	// Multiplier grows the wait per attempt. It must be at least 1; 1 means no
	// growth. Zero means "unset, use DefaultMultiplier".
	Multiplier float64
	// Jitter is the fraction of the delay that is randomised, in [0,1). Zero
	// disables jitter and is a legitimate explicit choice, not an unset field.
	Jitter float64
	// Rand returns a fraction in [0,1). Nil means the package generator.
	Rand func() float64
}

// isZero reports whether the policy was left entirely unset, which is how New
// distinguishes "use the documented defaults" from "no jitter, explicitly".
func (b Backoff) isZero() bool {
	return b.Initial == 0 && b.Max == 0 && b.Multiplier == 0 && b.Jitter == 0 && b.Rand == nil
}

// DefaultBackoff returns the policy from the specification.
func DefaultBackoff() Backoff {
	return Backoff{
		Initial:    DefaultInitialDelay,
		Max:        DefaultMaxDelay,
		Multiplier: DefaultMultiplier,
		Jitter:     DefaultJitter,
	}
}

// withDefaults returns a usable copy of the policy: a field left unset takes the
// documented default, and a value that cannot mean anything useful is floored.
//
// This is a *use-time* fallback, not validation. It exists so Base and Delay are
// total functions for a zero value and for a caller that never went through New.
// A policy handed to New is checked by Validate first, on the values exactly as
// supplied, so a misconfiguration is reported rather than quietly repaired here.
func (b Backoff) withDefaults() Backoff {
	if b.Initial <= 0 {
		b.Initial = DefaultInitialDelay
	}
	if b.Max <= 0 {
		b.Max = DefaultMaxDelay
	}
	if b.Max < b.Initial {
		b.Max = b.Initial
	}
	if b.Multiplier <= 0 {
		b.Multiplier = DefaultMultiplier
	}
	if b.Multiplier < 1 {
		b.Multiplier = 1
	}
	// Jitter is pinned into [0,1] so the factor in Delay can never be negative
	// and therefore never produce a wait below zero, whatever Rand returns.
	if b.Jitter < 0 {
		b.Jitter = 0
	}
	if b.Jitter > 1 {
		b.Jitter = 1
	}
	return b
}

// Base returns the un-jittered wait before the given attempt.
//
// attempt is one-based and names the attempt that just failed: after attempt 1
// the wait is Initial. Attempts below 1 are treated as 1, so a caller that lost
// count backs off from the floor rather than from zero.
func (b Backoff) Base(attempt int) time.Duration {
	b = b.withDefaults()
	if attempt < 1 {
		attempt = 1
	}
	delay := float64(b.Initial) * math.Pow(b.Multiplier, float64(attempt-1))
	// After withDefaults, Initial is positive and Multiplier is at least 1, so
	// delay is positive; the only ways past the cap are a very large attempt
	// (including a +Inf overflow) or a NaN from an extreme Multiplier, and the
	// negated comparison catches all three.
	if !(delay < float64(b.Max)) {
		return b.Max
	}
	return time.Duration(delay)
}

// Delay returns the jittered wait before the next attempt.
//
// The result is never negative and never exceeds Max*(1+Jitter), so a caller can
// hand it straight to a timer.
func (b Backoff) Delay(attempt int) time.Duration {
	b = b.withDefaults()
	base := b.Base(attempt)
	if b.Jitter == 0 {
		return base
	}
	r := b.Rand
	if r == nil {
		r = rand.Float64
	}
	// withDefaults pins Jitter into [0,1] and Rand is a fraction in [0,1], so
	// the factor lies in [0, 1+Jitter]: the wait is never negative and never
	// exceeds Max*(1+Jitter). Nothing needs clamping, so nothing is clamped and
	// the caller gets exactly the arithmetic the doc comment promises. The
	// result is rounded rather than truncated, so 30s at -10% is 27s and not
	// 26.999999999s because of a binary-fraction error.
	spread := (2*r() - 1) * b.Jitter
	return time.Duration(math.Round(float64(base) * (1 + spread)))
}

// Sequence returns the waits before attempts 2..n+1, jitter included. It exists
// so `ccresu doctor` and the tests can show the whole schedule at once.
func (b Backoff) Sequence(n int) []time.Duration {
	if n <= 0 {
		return nil
	}
	out := make([]time.Duration, 0, n)
	for attempt := 1; attempt <= n; attempt++ {
		out = append(out, b.Delay(attempt))
	}
	return out
}

// Validate checks the policy is usable.
//
// The range checks run on the policy exactly as supplied, before any defaulting:
// defaulting first would rewrite precisely the misconfigurations this function
// exists to report, and ccresu does not silently repair invalid configuration.
// A zero field means "unset" and takes the documented default; an explicit
// Jitter of zero is a legitimate choice (no jitter) and is accepted.
func (b Backoff) Validate() error {
	if b.Initial < 0 {
		return fmt.Errorf("%w: initial delay must not be negative", ErrInvalidConfig)
	}
	if b.Max < 0 {
		return fmt.Errorf("%w: max delay must not be negative", ErrInvalidConfig)
	}
	if b.Multiplier < 0 || (b.Multiplier > 0 && b.Multiplier < 1) {
		return fmt.Errorf("%w: multiplier %g must be at least 1", ErrInvalidConfig, b.Multiplier)
	}
	if b.Jitter < 0 || b.Jitter >= 1 {
		return fmt.Errorf("%w: jitter %g must be within [0,1)", ErrInvalidConfig, b.Jitter)
	}
	// The relational rule compares the values that will actually be used, so an
	// omitted Max is replaced by the documented cap while an omitted Initial
	// takes the documented floor: Initial=20m with Max unset is a policy that
	// would never grow, and it is reported rather than quietly pinned to 20m.
	initial, maxDelay := b.Initial, b.Max
	if initial <= 0 {
		initial = DefaultInitialDelay
	}
	if maxDelay <= 0 {
		maxDelay = DefaultMaxDelay
	}
	if maxDelay < initial {
		return fmt.Errorf("%w: max delay %s is shorter than the initial delay %s",
			ErrInvalidConfig, maxDelay, initial)
	}
	return nil
}

// FromConfig builds a policy from the recovery configuration values.
func FromConfigValues(initial, max time.Duration, multiplier, jitter float64) Backoff {
	return Backoff{
		Initial:    initial,
		Max:        max,
		Multiplier: multiplier,
		Jitter:     jitter,
	}
}
