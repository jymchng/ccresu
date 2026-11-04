package process_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jymchng/ccresu/internal/process"
)

// statLine builds a /proc/<pid>/stat line: 20 fields after the executable name,
// with starttime last (field 22).
func statLine(pid int, name string, ppid int, start uint64) string {
	rest := []string{fmt.Sprintf("%d", ppid)}
	for i := 0; i < 17; i++ {
		rest = append(rest, "0")
	}
	rest = append(rest, fmt.Sprintf("%d", start))
	return fmt.Sprintf("%d (%s) S %s\n", pid, name, strings.Join(rest, " "))
}

func writeProc(t *testing.T, root string, pid int, content string) {
	t.Helper()
	dir := filepath.Join(root, fmt.Sprint(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestVerifyRealSelfIdentity exercises the real procfs manager against this
// process, which is the only trustworthy source of identity evidence.
func TestVerifyRealSelfIdentity(t *testing.T) {
	manager := process.NewManager("")
	ctx := context.Background()

	self, err := manager.Identity(ctx, os.Getpid())
	if err != nil {
		t.Fatalf("Identity(self): %v", err)
	}
	if self.PID != os.Getpid() || self.StartTime == 0 || self.Name == "" {
		t.Fatalf("incomplete self identity: %+v", self)
	}
	if self.PPID == 0 {
		t.Fatalf("expected a parent pid: %+v", self)
	}
	if !manager.Alive(ctx, os.Getpid()) {
		t.Fatal("Alive(self) = false")
	}
	if err := manager.Verify(ctx, self); err != nil {
		t.Fatalf("Verify(self) = %v", err)
	}

	// A different start time for the same PID must be reported as reused.
	reused := self
	reused.StartTime++
	if err := manager.Verify(ctx, reused); !errors.Is(err, process.ErrProcessReused) {
		t.Fatalf("expected ErrProcessReused, got %v", err)
	}

	// A definitely-free PID must be reported as not running.
	if _, err := manager.Identity(ctx, 999999); !errors.Is(err, process.ErrNotRunning) {
		t.Fatalf("expected ErrNotRunning for bogus pid, got %v", err)
	}
	if manager.Alive(ctx, 999999) {
		t.Fatal("Alive(bogus pid) = true")
	}
}

// TestVerifyRecycledPIDDetected drives the procfs layout directly so PID reuse
// is proven, not merely asserted in a comment.
func TestVerifyRecycledPIDDetected(t *testing.T) {
	root := t.TempDir()
	writeProc(t, root, 4242, statLine(4242, "claude", 1, 1000))
	if err := os.WriteFile(filepath.Join(root, "4242", "cmdline"), []byte("claude\x00-p\x00--resume\x00abc\x00"), 0o644); err != nil {
		t.Fatal(err)
	}

	manager := process.NewManager(root)
	ctx := context.Background()
	original, err := manager.Identity(ctx, 4242)
	if err != nil {
		t.Fatalf("Identity: %v", err)
	}
	if original.Name != "claude" || original.PPID != 1 || original.StartTime != 1000 {
		t.Fatalf("wrong identity: %+v", original)
	}
	if len(original.Args) != 4 || original.Args[3] != "abc" {
		t.Fatalf("wrong argv: %v", original.Args)
	}
	if err := manager.Verify(ctx, original); err != nil {
		t.Fatalf("Verify before recycle: %v", err)
	}

	// Same PID, different start time = a different process.
	writeProc(t, root, 4242, statLine(4242, "other", 7, 2000))
	if err := manager.Verify(ctx, original); !errors.Is(err, process.ErrProcessReused) {
		t.Fatalf("recycled PID not detected: %v", err)
	}

	// Process gone.
	if err := os.RemoveAll(filepath.Join(root, "4242")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Identity(ctx, 4242); !errors.Is(err, process.ErrNotRunning) {
		t.Fatalf("expected ErrNotRunning, got %v", err)
	}
}

// TestVerifyParseStatHandlesAwkwardNames covers names containing spaces and
// parentheses, which naive field splitting gets wrong.
func TestVerifyParseStatHandlesAwkwardNames(t *testing.T) {
	line := statLine(77, "weird (name) with spaces", 5, 31337)
	stat, err := process.ParseStat(line)
	if err != nil {
		t.Fatalf("ParseStat: %v", err)
	}
	if stat.PID != 77 || stat.PPID != 5 || stat.StartTime != 31337 {
		t.Fatalf("wrong parse: %+v", stat)
	}
	if stat.Name != "weird (name) with spaces" {
		t.Fatalf("wrong name: %q", stat.Name)
	}

	for _, bad := range []string{"", "   ", "notanumber (x) S 1", "5 (unclosed S 1", "5 (x) S 1 2"} {
		if _, err := process.ParseStat(bad); err == nil {
			t.Fatalf("ParseStat(%q) accepted malformed input", bad)
		}
	}
}

// TestVerifyContextCancellationIsHonoured proves cancellation surfaces rather
// than being ignored.
func TestVerifyContextCancellationIsHonoured(t *testing.T) {
	manager := process.NewManager("")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.Identity(ctx, os.Getpid()); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if manager.Alive(ctx, os.Getpid()) {
		t.Fatal("Alive must be false on a cancelled context")
	}
}

// TestVerifyFakeMirrorsRealContract proves the fake used by later packages obeys
// the same identity rules as procfs.
func TestVerifyFakeMirrorsRealContract(t *testing.T) {
	fake := process.NewFake()
	fake.Add(process.Identity{PID: 10, PPID: 1, StartTime: 500})
	if got := fake.PIDs(); len(got) != 1 || got[0] != 10 {
		t.Fatalf("PIDs() = %v", got)
	}
	if err := fake.Verify(context.Background(), process.Identity{PID: 10, StartTime: 500}); err != nil {
		t.Fatalf("Verify(same) = %v", err)
	}
	if err := fake.Verify(context.Background(), process.Identity{PID: 10, StartTime: 501}); !errors.Is(err, process.ErrProcessReused) {
		t.Fatalf("expected ErrProcessReused, got %v", err)
	}
	fake.Remove(10)
	if _, err := fake.Identity(context.Background(), 10); !errors.Is(err, process.ErrNotRunning) {
		t.Fatalf("expected ErrNotRunning, got %v", err)
	}
}
