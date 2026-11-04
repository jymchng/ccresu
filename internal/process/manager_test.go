//go:build linux

package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const sampleStat = "4242 (claude code) S 100 4242 4242 0 -1 4194560 1 0 0 0 10 5 0 0 20 0 4 0 987654 0 0"

// writeProcEntry creates a fake /proc/<pid> directory tree.
func writeProcEntry(t *testing.T, root string, pid int, stat string, withExtra bool) {
	t.Helper()

	dir := filepath.Join(root, itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create proc dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o600); err != nil {
		t.Fatalf("write stat: %v", err)
	}
	if !withExtra {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte("claude\x00-p\x00hello\x00"), 0o600); err != nil {
		t.Fatalf("write cmdline: %v", err)
	}
	if err := os.Symlink("/usr/local/bin/claude", filepath.Join(dir, "exe")); err != nil {
		t.Fatalf("symlink exe: %v", err)
	}
	if err := os.Symlink("/home/user/project", filepath.Join(dir, "cwd")); err != nil {
		t.Fatalf("symlink cwd: %v", err)
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	digits := ""
	for v > 0 {
		digits = string(rune('0'+v%10)) + digits
		v /= 10
	}
	return digits
}

func TestProcManagerIdentity(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeProcEntry(t, root, 4242, sampleStat, true)

	manager := NewManager(root)

	identity, err := manager.Identity(t.Context(), 4242)
	if err != nil {
		t.Fatalf("Identity: %v", err)
	}

	if identity.PID != 4242 {
		t.Errorf("PID = %d", identity.PID)
	}
	if identity.PPID != 100 {
		t.Errorf("PPID = %d, want 100", identity.PPID)
	}
	if identity.StartTime != 987654 {
		t.Errorf("StartTime = %d, want 987654", identity.StartTime)
	}
	if identity.Name != "claude code" {
		t.Errorf("Name = %q", identity.Name)
	}
	if identity.Executable != "/usr/local/bin/claude" {
		t.Errorf("Executable = %q", identity.Executable)
	}
	if identity.WorkingDir != "/home/user/project" {
		t.Errorf("WorkingDir = %q", identity.WorkingDir)
	}
	if len(identity.Args) != 3 || identity.Args[0] != "claude" {
		t.Errorf("Args = %v", identity.Args)
	}
}

func TestProcManagerIdentityWithoutOptionalFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeProcEntry(t, root, 7, "7 (claude) S 1 7 7 0 -1 0 0 0 0 0 0 0 0 0 20 0 4 0 111 0 0", false)

	identity, err := NewManager(root).Identity(t.Context(), 7)
	if err != nil {
		t.Fatalf("Identity: %v", err)
	}
	if identity.Args != nil {
		t.Errorf("Args should be nil when cmdline is absent, got %v", identity.Args)
	}
	if identity.Executable != "" || identity.WorkingDir != "" {
		t.Errorf("optional fields should stay empty: %+v", identity)
	}
}

func TestProcManagerIdentityErrors(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeProcEntry(t, root, 5, "not a valid stat line", false)

	manager := NewManager(root)

	if _, err := manager.Identity(t.Context(), 999); !errors.Is(err, ErrNotRunning) {
		t.Errorf("missing process should return ErrNotRunning, got %v", err)
	}
	if _, err := manager.Identity(t.Context(), 0); !errors.Is(err, ErrNotRunning) {
		t.Errorf("zero pid should return ErrNotRunning, got %v", err)
	}
	if _, err := manager.Identity(t.Context(), -3); !errors.Is(err, ErrNotRunning) {
		t.Errorf("negative pid should return ErrNotRunning, got %v", err)
	}
	if _, err := manager.Identity(t.Context(), 5); err == nil {
		t.Error("malformed stat should return an error")
	}
}

func TestProcManagerRespectsContext(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeProcEntry(t, root, 4242, sampleStat, true)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	manager := NewManager(root)
	if _, err := manager.Identity(ctx, 4242); !errors.Is(err, context.Canceled) {
		t.Errorf("Identity should return context.Canceled, got %v", err)
	}
	if manager.Alive(ctx, 4242) {
		t.Error("Alive should be false when the context is cancelled")
	}
	if err := manager.Verify(ctx, Identity{PID: 4242}); !errors.Is(err, context.Canceled) {
		t.Errorf("Verify should return context.Canceled, got %v", err)
	}
}

func TestProcManagerAlive(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeProcEntry(t, root, 4242, sampleStat, true)
	manager := NewManager(root)

	if !manager.Alive(t.Context(), 4242) {
		t.Error("pid 4242 should be alive")
	}
	if manager.Alive(t.Context(), 4243) {
		t.Error("pid 4243 should not be alive")
	}
}

func TestProcManagerVerify(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeProcEntry(t, root, 4242, sampleStat, true)
	manager := NewManager(root)

	if err := manager.Verify(t.Context(), Identity{PID: 4242, StartTime: 987654}); err != nil {
		t.Errorf("Verify with the correct start time should succeed: %v", err)
	}

	if err := manager.Verify(t.Context(), Identity{PID: 4242, StartTime: 42}); !errors.Is(err, ErrProcessReused) {
		t.Errorf("Verify with a different start time should return ErrProcessReused, got %v", err)
	}

	if err := manager.Verify(t.Context(), Identity{PID: 4243}); !errors.Is(err, ErrNotRunning) {
		t.Errorf("Verify for a missing process should return ErrNotRunning, got %v", err)
	}
}

func TestNewManagerDefaultsToProcRoot(t *testing.T) {
	t.Parallel()

	manager := NewManager("")
	proc, ok := manager.(*procManager)
	if !ok {
		t.Fatalf("NewManager returned %T", manager)
	}
	if proc.root != ProcRoot {
		t.Errorf("root = %q, want %q", proc.root, ProcRoot)
	}

	// The real procfs must at least resolve the current process.
	if _, err := NewManager(ProcRoot).Identity(t.Context(), os.Getpid()); err != nil {
		t.Errorf("identity of the current process should resolve: %v", err)
	}
}
