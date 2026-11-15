package app

import (
	"strings"
	"testing"

	"github.com/jymchng/ccresu/internal/storage"
	"github.com/jymchng/ccresu/internal/telegram"
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

// TestSessionsBrowserShowsTheRunDirectoryColumn covers PRD 6 R12 at the third
// place a session is displayed: the interactive browser.
//
// The CLI table and the browser render the same two directories with the same
// rule (a dash when they agree), so this test asserts the column exists, carries
// the worktree, and does not repeat the project path when the session never
// moved — the same shape the `sessions` table is held to.
func TestSessionsBrowserShowsTheRunDirectoryColumn(t *testing.T) {
	const checkout = "/w/checkout"
	const worktree = "/w/checkout/wt"

	moved := newHarness(t)
	moved.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.ProjectPath = checkout
		s.RunsInPath = worktree
	})
	if code := moved.runWithKeys("q", "sessions", "--limit", "0", "--all"); code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, moved.stderr.String())
	}
	screen := lastScreen(moved.stdout.String())
	for _, want := range []string{"RUNS IN", checkout, worktree} {
		if !strings.Contains(screen, want) {
			t.Errorf("browser screen missing %q:\n%s", want, screen)
		}
	}

	stayed := newHarness(t)
	stayed.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.ProjectPath = checkout
		s.RunsInPath = checkout
	})
	if code := stayed.runWithKeys("q", "sessions", "--limit", "0", "--all"); code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, stayed.stderr.String())
	}
	screen = lastScreen(stayed.stdout.String())
	if !strings.Contains(screen, "RUNS IN") {
		t.Errorf("browser screen has no run-directory column:\n%s", screen)
	}
	if got := strings.Count(screen, checkout); got != 1 {
		t.Errorf("the project path appears %d times, want once (a dash when the two agree):\n%s", got, screen)
	}
}

// TestHookCarriesTheRunDirectory covers PRD 6 R12 on the hook's payload: the
// notification the hook enqueues names where the session runs, so the operator
// who is asked to unblock it can see the worktree rather than the checkout it was
// dispatched from.
func TestHookCarriesTheRunDirectory(t *testing.T) {
	const (
		project  = "/work/hooked"
		worktree = "/work/hooked/.claude/worktrees/fix-auth"
	)

	h := newHarness(t)
	h.seedSession(t, hookSession, func(s *storage.Session) {
		s.ProjectPath = project
		s.RunsInPath = worktree
	})
	publishBlocked(t, h, hookSession, "permission to edit .env")
	h.stdin = hookJSON(hookNeedsInputType, hookSession)

	if code, stdout, stderr := h.exec(t, "hook"); code != ExitOK || stdout != "" || stderr != "" {
		t.Fatalf("hook = exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	requests := h.notifier.queued()
	if len(requests) != 1 {
		t.Fatalf("queued %d notification(s), want exactly one", len(requests))
	}
	data := requests[0].Data
	if data.RunsInPath != worktree {
		t.Errorf("payload run directory = %q, want %q", data.RunsInPath, worktree)
	}
	if data.ProjectPath != project {
		t.Errorf("payload project = %q, want %q: the two are separate facts", data.ProjectPath, project)
	}

	// The queued payload is what gets rendered, so the rendered message has to
	// show both directories and not just the one the payload happened to keep.
	body, err := telegram.Render(requests[0].Event, data)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(body, "Runs in:") || !strings.Contains(body, worktree) {
		t.Errorf("message does not name the run directory %q:\n%s", worktree, body)
	}
	if !strings.Contains(body, project) {
		t.Errorf("message does not name the project directory %q:\n%s", project, body)
	}
}
