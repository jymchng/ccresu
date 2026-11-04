//go:build !linux

package process

import (
	"context"
	"fmt"
)

// NewManager returns a Manager that cannot resolve process identity on this
// platform.
//
// ccresu's strongest recovery guarantee is that the *same* process instance is
// still running, which requires procfs start times. On non-Linux systems callers
// receive ErrUnsupported and must fall back to transcript-based verification
// (see internal/recovery). This is a documented limitation, not a silent
// failure: the error is returned, never swallowed.
func NewManager(_ string) Manager {
	return unsupportedManager{}
}

type unsupportedManager struct{}

func (unsupportedManager) Identity(ctx context.Context, pid int) (Identity, error) {
	if err := ctx.Err(); err != nil {
		return Identity{}, err
	}
	return Identity{}, fmt.Errorf("%w: cannot inspect pid %d", ErrUnsupported, pid)
}

func (unsupportedManager) Alive(ctx context.Context, _ int) bool { return false }

func (unsupportedManager) Verify(ctx context.Context, want Identity) error {
	return fmt.Errorf("%w: cannot verify pid %d", ErrUnsupported, want.PID)
}
