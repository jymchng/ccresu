package process

import (
	"context"
	"errors"
	"testing"
)

func TestFakeManagerLifecycle(t *testing.T) {
	t.Parallel()

	fake := NewFake()
	fake.Add(Identity{PID: 10, PPID: 1, StartTime: 100, Name: "claude", WorkingDir: "/tmp/project"})

	identity, err := fake.Identity(t.Context(), 10)
	if err != nil {
		t.Fatalf("Identity: %v", err)
	}
	if identity.Name != "claude" || identity.WorkingDir != "/tmp/project" {
		t.Errorf("unexpected identity: %+v", identity)
	}

	if !fake.Alive(t.Context(), 10) {
		t.Error("pid 10 should be alive")
	}
	if err := fake.Verify(t.Context(), Identity{PID: 10, StartTime: 100}); err != nil {
		t.Errorf("Verify: %v", err)
	}

	fake.Remove(10)
	if fake.Alive(t.Context(), 10) {
		t.Error("pid 10 should be gone")
	}
	if _, err := fake.Identity(t.Context(), 10); !errors.Is(err, ErrNotRunning) {
		t.Errorf("Identity after Remove = %v, want ErrNotRunning", err)
	}
}

func TestFakeManagerDetectsPIDReuse(t *testing.T) {
	t.Parallel()

	fake := NewFake()
	fake.Add(Identity{PID: 10, StartTime: 100})

	// Simulate the kernel reusing the PID for a new process.
	fake.Add(Identity{PID: 10, StartTime: 200})

	if err := fake.Verify(t.Context(), Identity{PID: 10, StartTime: 100}); !errors.Is(err, ErrProcessReused) {
		t.Errorf("Verify = %v, want ErrProcessReused", err)
	}
	if err := fake.Verify(t.Context(), Identity{PID: 10, StartTime: 200}); err != nil {
		t.Errorf("Verify for the new process should pass: %v", err)
	}
}

func TestFakeManagerErrors(t *testing.T) {
	t.Parallel()

	fake := NewFake()
	sentinel := errors.New("procfs unavailable")
	fake.SetError(sentinel)

	if _, err := fake.Identity(t.Context(), 1); !errors.Is(err, sentinel) {
		t.Errorf("Identity = %v, want the injected error", err)
	}
	if err := fake.Verify(t.Context(), Identity{PID: 1}); !errors.Is(err, sentinel) {
		t.Errorf("Verify = %v, want the injected error", err)
	}

	fake.SetError(nil)
	fake.Add(Identity{PID: 1, StartTime: 1})
	if _, err := fake.Identity(t.Context(), 1); err != nil {
		t.Errorf("Identity after clearing the error: %v", err)
	}
}

func TestFakeManagerContextCancellation(t *testing.T) {
	t.Parallel()

	fake := NewFake()
	fake.Add(Identity{PID: 1, StartTime: 1})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := fake.Identity(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Errorf("Identity = %v, want context.Canceled", err)
	}
	if fake.Alive(ctx, 1) {
		t.Error("Alive should be false with a cancelled context")
	}
}

func TestFakeManagerPIDsAreSorted(t *testing.T) {
	t.Parallel()

	fake := NewFake()
	fake.Add(Identity{PID: 30})
	fake.Add(Identity{PID: 10})
	fake.Add(Identity{PID: 20})

	got := fake.PIDs()
	want := []int{10, 20, 30}
	if len(got) != len(want) {
		t.Fatalf("PIDs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("PIDs() = %v, want %v", got, want)
		}
	}
}

func TestFakeManagerIsConcurrencySafe(t *testing.T) {
	t.Parallel()

	fake := NewFake()
	done := make(chan struct{})

	go func() {
		defer close(done)
		for i := 1; i <= 100; i++ {
			fake.Add(Identity{PID: i, StartTime: uint64(i)})
			_ = fake.Alive(context.Background(), i)
		}
	}()

	for i := 1; i <= 100; i++ {
		_, _ = fake.Identity(context.Background(), i)
		_ = fake.PIDs()
	}
	<-done
}
