// Package process resolves and verifies Unix process identity.
//
// ccresu never signals or kills processes; it only needs to answer "is this
// still the same process?" before resuming a session. PID alone is not an
// answer, because PIDs are reused. Identity therefore includes the process
// start time (field 22 of /proc/<pid>/stat), which is stable for the lifetime of
// the process and different for a recycled PID.
package process

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Sentinel errors returned by process lookups.
var (
	// ErrNotRunning means no process with that PID exists.
	ErrNotRunning = errors.New("process is not running")
	// ErrProcessReused means the PID now belongs to a different process.
	ErrProcessReused = errors.New("process identity was reused by another process")
	// ErrUnsupported means the platform has no procfs identity source.
	ErrUnsupported = errors.New("process identity is not supported on this platform")
)

// Identity describes a process well enough to recognise it later.
type Identity struct {
	PID        int
	PPID       int
	StartTime  uint64
	Name       string
	Executable string
	Args       []string
	WorkingDir string
}

// Valid reports whether the identity carries the fields needed for comparison.
func (i Identity) Valid() bool { return i.PID > 0 }

// SameProcess reports whether other identifies the same process instance.
//
// When either side lacks a start time the comparison falls back to the PID, but
// callers should treat that as weaker evidence and re-check the session instead.
func (i Identity) SameProcess(other Identity) bool {
	if i.PID != other.PID || i.PID <= 0 {
		return false
	}
	if i.StartTime == 0 || other.StartTime == 0 {
		return true
	}
	return i.StartTime == other.StartTime
}

// String renders a short, log-friendly description.
func (i Identity) String() string {
	name := i.Name
	if name == "" {
		name = "unknown"
	}
	return fmt.Sprintf("pid=%d ppid=%d start=%d name=%s", i.PID, i.PPID, i.StartTime, name)
}

// Stat holds the fields of /proc/<pid>/stat that ccresu cares about.
type Stat struct {
	PID       int
	Name      string
	State     string
	PPID      int
	StartTime uint64
}

// ParseStat parses /proc/<pid>/stat content.
//
// The second field is the executable name wrapped in parentheses and may itself
// contain spaces or parentheses, so parsing starts after the final ')'.
func ParseStat(data string) (Stat, error) {
	trimmed := strings.TrimSpace(data)
	if trimmed == "" {
		return Stat{}, fmt.Errorf("empty stat content")
	}

	open := strings.IndexByte(trimmed, '(')
	close := strings.LastIndexByte(trimmed, ')')
	if open < 0 || close < open {
		return Stat{}, fmt.Errorf("malformed stat content: missing executable name")
	}

	pid, err := strconv.Atoi(strings.TrimSpace(trimmed[:open]))
	if err != nil {
		return Stat{}, fmt.Errorf("malformed stat content: bad pid: %w", err)
	}

	rest := strings.Fields(trimmed[close+1:])
	// rest[0] is field 3 (state); fields 4.. are ppid, pgrp, ... starttime is
	// field 22, i.e. index 19 in rest.
	const startTimeIndex = 19
	if len(rest) <= startTimeIndex {
		return Stat{}, fmt.Errorf("malformed stat content: expected at least %d fields after the name, got %d", startTimeIndex+1, len(rest))
	}

	ppid, err := strconv.Atoi(rest[1])
	if err != nil {
		return Stat{}, fmt.Errorf("malformed stat content: bad ppid: %w", err)
	}

	start, err := strconv.ParseUint(rest[startTimeIndex], 10, 64)
	if err != nil {
		return Stat{}, fmt.Errorf("malformed stat content: bad starttime: %w", err)
	}

	return Stat{
		PID:       pid,
		Name:      trimmed[open+1 : close],
		State:     rest[0],
		PPID:      ppid,
		StartTime: start,
	}, nil
}

// ParseCmdline splits /proc/<pid>/cmdline (NUL-separated) into arguments.
func ParseCmdline(data string) []string {
	if data == "" {
		return nil
	}
	parts := strings.Split(data, "\x00")
	args := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		args = append(args, part)
	}
	if len(args) == 0 {
		return nil
	}
	return args
}
