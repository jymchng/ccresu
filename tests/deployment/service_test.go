// Package deployment_test reads the shipped deployment artifacts the way an
// operator will: as files, with no build steps in between.
//
// R12 lives here because it is not a behaviour of the Go binary at all. It is a
// claim about the systemd unit: the stop timeout is sized for the daemon's own
// bookkeeping, not for the resume turn. ccresu runs exactly one long child —
// `claude --resume <session-id> "<message>"`, the whole turn, bounded by
// recovery.turn_timeout — and a stop deliberately does not wait for it, because
// an interrupted turn is retried on the next pass. A unit that waited for the
// turn would hang every stop for up to half an hour.
package deployment_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
)

// repoRoot finds the module root by walking up from the working directory,
// which `go test` sets to the package's own directory.
//
// It deliberately does not use runtime.Caller: CI builds with -trimpath, so a
// caller's file name is the module-relative
// "github.com/jymchng/ccresu/tests/deployment/service_test.go". Joining "..",
// ".." onto that names a directory that does not exist, and the read fails with
// the module path where a file path belongs — green locally, red on the runner.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod in %s or any parent", dir)
		}
		dir = parent
	}
}

// repoFile reads a file relative to the module root, so the test does not
// depend on the working directory the runner picks.
func repoFile(t *testing.T, rel string) string {
	t.Helper()
	path := filepath.Join(repoRoot(t), rel)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// unitValue returns the single setting of key in a systemd unit file.
func unitValue(t *testing.T, unit, key string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `=(.*)$`)
	m := re.FindStringSubmatch(unit)
	if m == nil {
		t.Fatalf("unit has no %s= setting", key)
	}
	return strings.TrimSpace(m[1])
}

// systemdDuration parses the duration syntax systemd accepts in a unit (a bare
// number means seconds, and the usual unit suffixes are allowed).
func systemdDuration(t *testing.T, value string) time.Duration {
	t.Helper()
	if d, err := time.ParseDuration(value); err == nil {
		return d
	}
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil {
		t.Fatalf("TimeoutStopSec=%q is not a systemd duration: %v", value, err)
	}
	return time.Duration(seconds * float64(time.Second))
}

// TestTheUnitSizesItsStopTimeoutForARetryableTurn is R12.
//
// The unit may wait only for the daemon's own bookkeeping. The one long child is
// the resume turn — `claude --resume <session-id> "<message>"` — and a stop
// deliberately does not wait for it: the transcript keeps what was written and
// the next pass resumes the session again, so an interrupted turn is retried
// rather than lost. The timeout must therefore stay far below the turn budget,
// and the unit may not still describe the retired supervisor or its background
// handoff.
func TestTheUnitSizesItsStopTimeoutForARetryableTurn(t *testing.T) {
	unit := repoFile(t, filepath.Join("deployments", "systemd", "ccresu.service"))

	stop := systemdDuration(t, unitValue(t, unit, "TimeoutStopSec"))
	budget := claude.DefaultQueryTimeout
	if stop > 2*budget {
		t.Errorf("TimeoutStopSec = %s, want at most %s: the only children worth waiting for are "+
			"bounded by DefaultQueryTimeout = %s, and the resume turn (%s) is retried rather "+
			"than waited for", stop, 2*budget, budget, claude.DefaultTurnTimeout)
	}
	if stop >= claude.DefaultTurnTimeout {
		t.Errorf("TimeoutStopSec = %s would wait for the turn itself, bounded by "+
			"DefaultTurnTimeout = %s", stop, claude.DefaultTurnTimeout)
	}

	// The supervisor and the background handoff are gone from the model, so they
	// may not survive in the operator-facing prose that explains this timeout.
	for _, retired := range []string{"supervisor", "--bg", "daemon status", "claude -p --resume", "handoff"} {
		if strings.Contains(unit, retired) {
			t.Errorf("the unit still describes the retired behaviour %q", retired)
		}
	}

	// And the reason it may stop early has to be stated: what the child is, and
	// that stopping mid-turn is safe.
	if !strings.Contains(unit, "`claude --resume <session-id>") {
		t.Error("the unit no longer says which child it runs and does not wait for")
	}
	if !strings.Contains(unit, "retried rather than") {
		t.Error("the unit no longer explains that an interrupted turn is retried, which is why the timeout is short")
	}
}
