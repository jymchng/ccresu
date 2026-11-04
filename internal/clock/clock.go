// Package clock provides an injectable time source so that scheduling and
// retry logic can be tested deterministically.
package clock

import (
	"sync"
	"time"
)

// Clock is the minimal time source used across ccresu.
type Clock interface {
	// Now reports the current time.
	Now() time.Time
	// Since reports the elapsed time since t.
	Since(t time.Time) time.Duration
	// Sleep blocks for d or until the clock is advanced past it.
	Sleep(d time.Duration)
	// After returns a channel that receives once d has elapsed.
	After(d time.Duration) <-chan time.Time
	// NewTimer returns a stopwatch-style timer.
	NewTimer(d time.Duration) Timer
}

// Timer mirrors the subset of *time.Timer used by ccresu.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
	Reset(d time.Duration) bool
}

// System returns a Clock backed by the real time package.
func System() Clock { return systemClock{} }

type systemClock struct{}

func (systemClock) Now() time.Time                         { return time.Now() }
func (systemClock) Since(t time.Time) time.Duration        { return time.Since(t) }
func (systemClock) Sleep(d time.Duration)                  { time.Sleep(d) }
func (systemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
func (systemClock) NewTimer(d time.Duration) Timer         { return &systemTimer{timer: time.NewTimer(d)} }

type systemTimer struct{ timer *time.Timer }

func (t *systemTimer) C() <-chan time.Time        { return t.timer.C }
func (t *systemTimer) Stop() bool                 { return t.timer.Stop() }
func (t *systemTimer) Reset(d time.Duration) bool { return t.timer.Reset(d) }

// Fake is a manually advanced Clock for tests. It is safe for concurrent use.
type Fake struct {
	mu     sync.Mutex
	now    time.Time
	waiter []*fakeTimer
}

// NewFake returns a Fake clock frozen at start.
func NewFake(start time.Time) *Fake {
	return &Fake{now: start}
}

// Now reports the fake clock's current time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Since reports the fake elapsed time since t.
func (f *Fake) Since(t time.Time) time.Duration { return f.Now().Sub(t) }

// Sleep blocks until the fake clock advances by d, or forever if it never does.
func (f *Fake) Sleep(d time.Duration) {
	<-f.After(d)
}

// After returns a channel that fires when the fake clock reaches now+d.
func (f *Fake) After(d time.Duration) <-chan time.Time {
	timer := f.NewTimer(d)
	return timer.C()
}

// NewTimer registers a timer that fires once the clock has advanced by d.
func (f *Fake) NewTimer(d time.Duration) Timer {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := &fakeTimer{clock: f, ch: make(chan time.Time, 1), deadline: f.now.Add(d)}
	if d <= 0 {
		t.fireLocked(f.now)
		return t
	}
	f.waiter = append(f.waiter, t)
	return t
}

// Advance moves the clock forward by d, firing every timer that becomes due.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
	now := f.now
	remaining := f.waiter[:0]
	for _, t := range f.waiter {
		if t.stopped {
			continue
		}
		if !t.deadline.After(now) {
			t.fireLocked(now)
			continue
		}
		remaining = append(remaining, t)
	}
	f.waiter = remaining
}

// Set moves the clock to a specific time without firing timers that are already
// due; use Advance for normal test progression.
func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = t
}

// HasWaiters reports whether any timer is currently registered.
//
// Tests that start a goroutine which sleeps on the clock need to know it has
// registered its timer before advancing; otherwise the result depends on
// scheduling luck.
func (f *Fake) HasWaiters() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.waiter) > 0
}

type fakeTimer struct {
	clock    *Fake
	ch       chan time.Time
	deadline time.Time
	stopped  bool
	tracked  bool
}

func (t *fakeTimer) C() <-chan time.Time { return t.ch }

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	if t.stopped {
		return false
	}
	t.stopped = true
	return true
}

func (t *fakeTimer) Reset(d time.Duration) bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	active := !t.stopped
	t.stopped = false
	t.deadline = t.clock.now.Add(d)
	t.clock.waiter = append(t.clock.waiter, t)
	return active
}

// fireLocked delivers the timer signal. The caller must hold clock.mu.
func (t *fakeTimer) fireLocked(now time.Time) {
	t.stopped = true
	select {
	case t.ch <- now:
	default:
	}
}
