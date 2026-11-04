package clock

import (
	"testing"
	"time"
)

func TestSystemClock(t *testing.T) {
	t.Parallel()

	clock := System()

	before := time.Now()
	now := clock.Now()
	after := time.Now()
	if now.Before(before) || now.After(after) {
		t.Errorf("Now() = %v, expected between %v and %v", now, before, after)
	}

	if elapsed := clock.Since(before); elapsed < 0 {
		t.Errorf("Since() = %v, want non-negative", elapsed)
	}

	select {
	case <-clock.After(time.Millisecond):
	case <-time.After(time.Second):
		t.Error("After did not fire")
	}

	timer := clock.NewTimer(10 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C():
	case <-time.After(time.Second):
		t.Error("timer did not fire")
	}
}

func TestSystemSleepReturns(t *testing.T) {
	t.Parallel()

	start := time.Now()
	System().Sleep(2 * time.Millisecond)
	if time.Since(start) < time.Millisecond {
		t.Error("Sleep returned too early")
	}
}

func TestFakeClockAdvanceFiresTimer(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	fake := NewFake(start)

	if got := fake.Now(); !got.Equal(start) {
		t.Fatalf("Now() = %v, want %v", got, start)
	}

	fired := fake.After(30 * time.Second)
	select {
	case <-fired:
		t.Fatal("timer fired before the clock advanced")
	default:
	}

	fake.Advance(29 * time.Second)
	select {
	case <-fired:
		t.Fatal("timer fired one second early")
	default:
	}

	fake.Advance(time.Second)
	select {
	case at := <-fired:
		if want := start.Add(30 * time.Second); !at.Equal(want) {
			t.Errorf("timer delivered %v, want %v", at, want)
		}
	default:
		t.Fatal("timer did not fire after advancing past the deadline")
	}

	if got, want := fake.Since(start), 30*time.Second; got != want {
		t.Errorf("Since() = %v, want %v", got, want)
	}
}

func TestFakeClockImmediateTimer(t *testing.T) {
	t.Parallel()

	fake := NewFake(time.Now())
	select {
	case <-fake.After(0):
	case <-time.After(time.Second):
		t.Fatal("zero-duration timer should fire immediately")
	}
}

func TestFakeClockStopPreventsFiring(t *testing.T) {
	t.Parallel()

	fake := NewFake(time.Now())
	timer := fake.NewTimer(time.Minute)

	if !timer.Stop() {
		t.Fatal("Stop should report the timer was active")
	}
	if timer.Stop() {
		t.Fatal("second Stop should report the timer was already stopped")
	}

	fake.Advance(2 * time.Minute)
	select {
	case <-timer.C():
		t.Fatal("stopped timer must not fire")
	default:
	}
}

func TestFakeClockResetReschedules(t *testing.T) {
	t.Parallel()

	fake := NewFake(time.Now())
	timer := fake.NewTimer(time.Minute)

	if !timer.Reset(10 * time.Second) {
		t.Error("Reset should report that the timer was still active")
	}

	fake.Advance(9 * time.Second)
	select {
	case <-timer.C():
		t.Fatal("timer fired before the new deadline")
	default:
	}

	fake.Advance(time.Second)
	select {
	case <-timer.C():
	case <-time.After(time.Second):
		t.Fatal("timer did not fire after Reset")
	}
}

func TestFakeClockSleepReleasesOnAdvance(t *testing.T) {
	t.Parallel()

	fake := NewFake(time.Now())
	done := make(chan struct{})
	go func() {
		fake.Sleep(5 * time.Minute)
		close(done)
	}()

	// Give the goroutine a chance to block, then advance the clock.
	time.Sleep(10 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("Sleep returned before the clock advanced")
	default:
	}

	fake.Advance(5 * time.Minute)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Sleep did not return after advancing the clock")
	}
}

func TestFakeClockSet(t *testing.T) {
	t.Parallel()

	fake := NewFake(time.Now())
	target := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	fake.Set(target)
	if got := fake.Now(); !got.Equal(target) {
		t.Errorf("Now() = %v, want %v", got, target)
	}
}

func TestFakeClockIsConcurrencySafe(t *testing.T) {
	t.Parallel()

	fake := NewFake(time.Now())
	done := make(chan struct{})

	go func() {
		defer close(done)
		for range 200 {
			fake.Advance(time.Millisecond)
		}
	}()

	for range 200 {
		timer := fake.NewTimer(time.Millisecond)
		timer.Stop()
		_ = fake.Now()
	}
	<-done
}
