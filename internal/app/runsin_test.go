package app

import (
	"strings"
	"testing"

	"github.com/jymchng/ccresu/internal/storage"
)

// TestSessionsShowsTheRunDirectoryDistinctly covers PRD 6 R12: where a session
// started and where it actually runs are two facts, and an operator must be able
// to see the difference rather than infer it. A background session's worktree and
// its launch checkout are exactly the pair that used to be one column.
func TestSessionsShowsTheRunDirectoryDistinctly(t *testing.T) {
	const checkout = "/srv/checkout"
	// Short enough that the table's 40-column truncation leaves it intact, so the
	// assertion is about the column existing, not about the elision.
	const worktree = "/srv/checkout/wt/session-1"

	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.ProjectPath = checkout
		s.RunsInPath = worktree
	})

	code, stdout, stderr := h.exec(t, "sessions")
	if code != ExitOK {
		t.Fatalf("sessions exit = %d (stderr %q)", code, stderr)
	}
	// Two distinct labels, and both paths.
	for _, want := range []string{"PROJECT", "RUNS IN", checkout, worktree} {
		if !strings.Contains(stdout, want) {
			t.Errorf("sessions output missing %q:\n%s", want, stdout)
		}
	}

	code, stdout, stderr = h.exec(t, "inspect", canonicalSessionID)
	if code != ExitOK {
		t.Fatalf("inspect exit = %d (stderr %q)", code, stderr)
	}
	for _, want := range []string{"project", "runs in", checkout, worktree} {
		if !strings.Contains(stdout, want) {
			t.Errorf("inspect output missing %q:\n%s", want, stdout)
		}
	}
}

// TestSessionsMarksTheRunDirectoryWhenItMatchesTheProject covers the other half
// of R12: a second column repeating the first path is noise, so a session that
// never moved is marked rather than duplicated.
func TestSessionsMarksTheRunDirectoryWhenItMatchesTheProject(t *testing.T) {
	const checkout = "/srv/checkout"

	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.ProjectPath = checkout
		s.RunsInPath = checkout
	})

	code, stdout, stderr := h.exec(t, "sessions")
	if code != ExitOK {
		t.Fatalf("sessions exit = %d (stderr %q)", code, stderr)
	}
	if strings.Count(stdout, checkout) != 1 {
		t.Errorf("sessions output repeats the path instead of marking it:\n%s", stdout)
	}

	code, stdout, _ = h.exec(t, "inspect", canonicalSessionID)
	if code != ExitOK {
		t.Fatalf("inspect exit = %d", code)
	}
	if !strings.Contains(stdout, "(same as the project)") {
		t.Errorf("inspect output does not say the run directory is the project's:\n%s", stdout)
	}

	// A session no observation has recorded a run directory for is reported as
	// unknown, never as "it runs where it started": the two are different claims.
	h2 := newHarness(t)
	h2.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.ProjectPath = checkout
		s.RunsInPath = ""
	})
	code, stdout, _ = h2.exec(t, "inspect", canonicalSessionID)
	if code != ExitOK {
		t.Fatalf("inspect exit = %d", code)
	}
	if !strings.Contains(stdout, "unknown (no observation has recorded one") {
		t.Errorf("inspect output claims a run directory nobody observed:\n%s", stdout)
	}
}
