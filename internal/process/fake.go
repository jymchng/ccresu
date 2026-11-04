package process

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Fake is an in-memory Manager for tests and for platforms without procfs.
// It is safe for concurrent use.
type Fake struct {
	mu    sync.RWMutex
	procs map[int]Identity
	err   error
}

// NewFake returns an empty Fake manager.
func NewFake() *Fake { return &Fake{procs: map[int]Identity{}} }

// Add registers (or replaces) a process identity.
func (f *Fake) Add(identity Identity) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.procs == nil {
		f.procs = map[int]Identity{}
	}
	f.procs[identity.PID] = identity
}

// Remove deletes a process, simulating it exiting.
func (f *Fake) Remove(pid int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.procs, pid)
}

// SetError makes every Identity call fail with err. Pass nil to clear it.
func (f *Fake) SetError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// Identity implements Manager.
func (f *Fake) Identity(ctx context.Context, pid int) (Identity, error) {
	if err := ctx.Err(); err != nil {
		return Identity{}, err
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.err != nil {
		return Identity{}, f.err
	}
	identity, ok := f.procs[pid]
	if !ok {
		return Identity{}, fmt.Errorf("%w: pid %d", ErrNotRunning, pid)
	}
	return identity, nil
}

// Alive implements Manager.
func (f *Fake) Alive(ctx context.Context, pid int) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	_, ok := f.procs[pid]
	return ok
}

// Verify implements Manager.
func (f *Fake) Verify(ctx context.Context, want Identity) error {
	current, err := f.Identity(ctx, want.PID)
	if err != nil {
		return err
	}
	if !want.SameProcess(current) {
		return fmt.Errorf("%w: pid %d", ErrProcessReused, want.PID)
	}
	return nil
}

// PIDs returns the sorted list of registered PIDs, for deterministic tests.
func (f *Fake) PIDs() []int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	pids := make([]int, 0, len(f.procs))
	for pid := range f.procs {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	return pids
}

var _ Manager = (*Fake)(nil)
