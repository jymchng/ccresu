//go:build linux

package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// NewManager returns a Manager backed by procfs rooted at root.
// Pass an empty root to use /proc. Tests point root at a temporary directory.
func NewManager(root string) Manager {
	if strings.TrimSpace(root) == "" {
		root = ProcRoot
	}
	return &procManager{root: root}
}

type procManager struct {
	root string
}

// Identity reads /proc/<pid>/stat, cmdline, exe and cwd.
//
// Unreadable cmdline/exe/cwd are not fatal: a process owned by another user may
// hide them, and identity comparison only needs the start time.
func (m *procManager) Identity(ctx context.Context, pid int) (Identity, error) {
	if err := ctx.Err(); err != nil {
		return Identity{}, err
	}
	if pid <= 0 {
		return Identity{}, fmt.Errorf("%w: invalid pid %d", ErrNotRunning, pid)
	}

	dir := filepath.Join(m.root, fmt.Sprint(pid))
	statData, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Identity{}, fmt.Errorf("%w: pid %d", ErrNotRunning, pid)
		}
		return Identity{}, fmt.Errorf("read process stat for pid %d: %w", pid, err)
	}

	stat, err := ParseStat(string(statData))
	if err != nil {
		return Identity{}, fmt.Errorf("pid %d: %w", pid, err)
	}

	identity := Identity{
		PID:       stat.PID,
		PPID:      stat.PPID,
		StartTime: stat.StartTime,
		Name:      stat.Name,
	}

	if cmdline, readErr := os.ReadFile(filepath.Join(dir, "cmdline")); readErr == nil {
		identity.Args = ParseCmdline(string(cmdline))
	}
	if exe, linkErr := os.Readlink(filepath.Join(dir, "exe")); linkErr == nil {
		identity.Executable = exe
	}
	if cwd, linkErr := os.Readlink(filepath.Join(dir, "cwd")); linkErr == nil {
		identity.WorkingDir = cwd
	}

	return identity, nil
}

// Alive reports whether pid exists, treating permission errors as "alive".
func (m *procManager) Alive(ctx context.Context, pid int) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(m.root, fmt.Sprint(pid)))
	return err == nil
}

// Verify checks that want still describes the live process behind want.PID.
func (m *procManager) Verify(ctx context.Context, want Identity) error {
	current, err := m.Identity(ctx, want.PID)
	if err != nil {
		return err
	}
	if !want.SameProcess(current) {
		return fmt.Errorf("%w: pid %d start time changed", ErrProcessReused, want.PID)
	}
	return nil
}

var _ Manager = (*procManager)(nil)
