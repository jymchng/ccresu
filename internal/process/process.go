// Package process resolves and verifies Unix process identity.
package process

import "context"

// Manager resolves process identity for monitored sessions.
type Manager interface {
	// Identity returns the current identity of pid.
	Identity(ctx context.Context, pid int) (Identity, error)
	// Alive reports whether pid currently exists.
	Alive(ctx context.Context, pid int) bool
	// Verify returns nil only when want still describes the live process.
	Verify(ctx context.Context, want Identity) error
}

// ProcRoot is the default procfs mount point.
const ProcRoot = "/proc"
