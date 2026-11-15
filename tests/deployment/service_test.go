// Package deployment_test reads the shipped deployment artifacts the way an
// operator will: as files, with no build steps in between.
//
// PRD 3 / R12 lives here because it is not a behaviour of the Go binary at all.
// It is a claim about the systemd unit: the stop timeout used to be sized around
// a `claude -p --resume` child that ccresu owned, and a comment explained that
// ownership. Neither is true once the lifecycle is delegated to the supervisor,
// and a unit that keeps the old number keeps an old model in the operator's head.
package deployment_test

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
)

// repoFile reads a file relative to this package's directory, so the test does
// not depend on the working directory the runner picks.
func repoFile(t *testing.T, rel string) string {
	t.Helper()
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(here), "..", "..", rel)
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

// TestTheUnitNoLongerSizesItsStopTimeoutAroundAnOwnedChild is R12.
//
// The unit may still wait, but only for the bounded commands ccresu actually
// runs: every supervisor command is capped by claude.QueryTimeout (R11), so a
// stop that waits longer than twice that budget is waiting for a child the set
// no longer owns.
func TestTheUnitNoLongerSizesItsStopTimeoutAroundAnOwnedChild(t *testing.T) {
	unit := repoFile(t, filepath.Join("deployments", "systemd", "ccresu.service"))

	stop := systemdDuration(t, unitValue(t, unit, "TimeoutStopSec"))
	budget := claude.DefaultQueryTimeout
	if stop > 2*budget {
		t.Errorf("TimeoutStopSec = %s, want at most %s: the longest child left is a query "+
			"bounded by DefaultQueryTimeout = %s, not a `claude -p --resume`", stop, 2*budget, budget)
	}

	// The comment that used to justify the 60s timeout described an ownership
	// model this PRD removes, so it may not survive in the file.
	if strings.Contains(unit, "`claude -p --resume` child") {
		t.Error("the unit still describes a `claude -p --resume` child it owns")
	}
	if !strings.Contains(unit, "supervisor") {
		t.Error("the unit no longer explains that the session's work belongs to the supervisor")
	}
}
