package recovery

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/storage"
)

// recoveryWorktree creates a real git repository with a real linked worktree and
// returns both paths.
//
// The fixture has to be an actual `git worktree add`: PRD 6 is about a session
// that lives in `.claude/worktrees/…` rather than in the checkout it was
// dispatched from, and a plain temporary directory would satisfy every assertion
// while proving nothing about that case. Production code never runs
// `git worktree` (R6); this is the test standing in for the operator.
func recoveryWorktree(t *testing.T) (repo, worktree string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed, so a linked worktree cannot be created")
	}
	repo = t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=ccresu", "GIT_AUTHOR_EMAIL=ccresu@example.com",
			"GIT_COMMITTER_NAME=ccresu", "GIT_COMMITTER_EMAIL=ccresu@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	run(repo, "init", "-q", "-b", "main")
	run(repo, "add", "README.md")
	run(repo, "commit", "-q", "-m", "fixture")
	worktree = filepath.Join(t.TempDir(), "session-worktree")
	run(repo, "worktree", "add", "-q", "-b", "session", worktree)
	return repo, worktree
}

// worktreeList returns `git worktree list --porcelain` for repo, which is how a
// test proves R10: N recovery attempts leave exactly the worktrees that existed
// before the first one.
func worktreeList(t *testing.T, repo string) string {
	t.Helper()
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git worktree list: %v", err)
	}
	return string(out)
}

// resumerStub is a providerStub that can be told a directory.
//
// providerStub deliberately does not implement claude.SessionResumerIn, so
// without this type every recovery test would exercise only the engine's
// compatibility fallback and never the resolution PRD 6 adds.
type resumerStub struct {
	*providerStub
	dirMu sync.Mutex
	dirs  []string
	err   error
}

// newResumerStub returns a resumer whose status checks report a grown transcript
// and whose resume runs its turn to the end.
func newResumerStub() *resumerStub {
	return &resumerStub{providerStub: growthProvider(10, 12)}
}

// ResumeSessionIn implements claude.SessionResumerIn.
func (r *resumerStub) ResumeSessionIn(_ context.Context, sessionID, _, dir string) (claude.ResumeResult, error) {
	r.dirMu.Lock()
	r.dirs = append(r.dirs, dir)
	err := r.err
	r.dirMu.Unlock()
	if err != nil {
		return claude.ResumeResult{}, err
	}
	return resumeResult(sessionID), nil
}

// resumeDirs returns the directories the resumes were told to run in.
func (r *resumerStub) resumeDirs() []string {
	r.dirMu.Lock()
	defer r.dirMu.Unlock()
	return append([]string(nil), r.dirs...)
}

var _ claude.SessionResumerIn = (*resumerStub)(nil)

// worktreeSession is a recoverable session that started in projectPath and now
// runs in runsInPath.
func worktreeSession(id, projectPath, runsInPath string) storage.Session {
	session := recoverableSession(id)
	session.ProjectPath = projectPath
	session.RunsInPath = runsInPath
	session.TranscriptPath = ""
	return session
}

// captureManagerLogger returns a manager option recording everything the pass
// says, so R8's "logs once" can be asserted.
func captureManagerLogger(logs *bytes.Buffer) func(*Config) {
	return func(cfg *Config) {
		cfg.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
}

func TestRecoveryRunsInTheRecordedWorktree(t *testing.T) {
	// PRD 6's falsifiable requirement: the directory handed to the child is the
	// worktree the session occupies, not the project checkout and not the
	// daemon's cwd.
	repo, worktree := recoveryWorktree(t)
	store := newTestStore(t)
	seedSession(t, store, worktreeSession(sessionA, repo, worktree))
	provider := newResumerStub()

	outcome, err := testManager(t, store, provider, nil).Recover(context.Background(), sessionA)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if outcome.State != StateResumed {
		t.Errorf("state = %s, want RESUMED", outcome.State)
	}
	dirs := provider.resumeDirs()
	if len(dirs) != 1 {
		t.Fatalf("resumes = %d, want exactly 1", len(dirs))
	}
	if dirs[0] != worktree {
		t.Errorf("resume ran in %q, want the recorded worktree %q (project is %q)", dirs[0], worktree, repo)
	}
}

func TestRecoveryFallsBackToProjectPath(t *testing.T) {
	// §6.5 "no run directory recorded yet": every v0.6.0 row, and any session
	// the CLI has not reported a cwd for. The project directory answers — never
	// the test process's cwd, which is the daemon's cwd in production.
	repo := t.TempDir()
	store := newTestStore(t)
	seedSession(t, store, worktreeSession(sessionA, repo, ""))
	provider := newResumerStub()

	if _, err := testManager(t, store, provider, nil).Recover(context.Background(), sessionA); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	dirs := provider.resumeDirs()
	if len(dirs) != 1 {
		t.Fatalf("resumes = %d, want exactly 1", len(dirs))
	}
	if dirs[0] != repo {
		t.Errorf("resume ran in %q, want the project directory %q", dirs[0], repo)
	}
	// The failure this guards against: an empty Dir, which inherits the process
	// working directory.
	if cwd, err := os.Getwd(); err == nil && dirs[0] == cwd {
		t.Errorf("resume ran in the process cwd %q, want the project directory", cwd)
	}
}

func TestEmptyWorkingDirectoryIsANamedError(t *testing.T) {
	// R2 / §6.5 "ProjectPath empty as well": the attempt fails with a named
	// error and no child process is spawned. An inherited cwd would report
	// success for a recovery that ran in $HOME.
	store := newTestStore(t)
	seedSession(t, store, worktreeSession(sessionA, "", ""))
	provider := newResumerStub()

	outcome, err := testManager(t, store, provider, nil).Recover(context.Background(), sessionA)
	if !errors.Is(err, claude.ErrWorkspaceUnresolved) {
		t.Fatalf("Recover = %v, want ErrWorkspaceUnresolved", err)
	}
	if errors.Is(err, claude.ErrUnrecoverable) {
		t.Error("error wraps ErrUnrecoverable, want a failed attempt: a directory can come back")
	}
	if dirs := provider.resumeDirs(); len(dirs) != 0 {
		t.Errorf("resumes = %d, want 0: nothing may run without a directory", len(dirs))
	}
	if outcome.State == StateResumed {
		t.Error("state = RESUMED, want the attempt recorded as failed")
	}

	// The attempt is still persisted, so `ccresu history` shows why it failed.
	history, histErr := testManager(t, store, provider, nil).History(context.Background(), sessionA)
	if histErr != nil {
		t.Fatalf("History: %v", histErr)
	}
	if len(history.Attempts) != 1 {
		t.Fatalf("attempt history = %d rows, want 1", len(history.Attempts))
	}
	if history.Attempts[0].Error == "" {
		t.Error("attempt error = empty, want the named error recorded")
	}
}

func TestMissingRunDirectoryFallsBackAndProceeds(t *testing.T) {
	// R8: a worktree that has been cleared is a fact to observe. The attempt
	// proceeds in the project directory and the fallback is announced once.
	repo := t.TempDir()
	gone := filepath.Join(t.TempDir(), "cleared-worktree")
	store := newTestStore(t)
	seedSession(t, store, worktreeSession(sessionA, repo, gone))
	provider := newResumerStub()
	var logs bytes.Buffer

	outcome, err := testManager(t, store, provider, captureManagerLogger(&logs)).Recover(context.Background(), sessionA)
	if err != nil {
		t.Fatalf("Recover: %v, want the attempt to proceed", err)
	}
	if outcome.State != StateResumed {
		t.Errorf("state = %s, want RESUMED", outcome.State)
	}
	dirs := provider.resumeDirs()
	if len(dirs) != 1 || dirs[0] != repo {
		t.Fatalf("resumes = %v, want exactly one in the project directory %q", dirs, repo)
	}
	if !strings.Contains(logs.String(), "recorded run directory is gone") {
		t.Errorf("logs = %q, want the fallback announced", logs.String())
	}
	if got := strings.Count(logs.String(), "recorded run directory is gone"); got != 1 {
		t.Errorf("fallback logged %d times, want once", got)
	}
	if !strings.Contains(logs.String(), gone) {
		t.Errorf("logs = %q, want the skipped path named", logs.String())
	}
}

func TestObservationBeatsThePersistedRow(t *testing.T) {
	// R9 at the engine: the freshest observation is the only source that knows
	// a session moved since the last pass, so it wins over the persisted row.
	repo := t.TempDir()
	persisted := t.TempDir()
	observed := t.TempDir()
	store := newTestStore(t)
	seedSession(t, store, worktreeSession(sessionA, repo, persisted))

	provider := newResumerStub()
	// Report growth (so the pass reaches the resume) *and* a run directory that
	// disagrees with the stored one.
	calls := 0
	provider.statusFn = func(_ int, sessionID string) (claude.SessionStatus, error) {
		calls++
		lines := int64(10)
		if calls > 1 {
			lines = 12
		}
		return claude.SessionStatus{
			Session:    claude.Session{ID: sessionID, Recoverable: true, ProjectPath: repo, RunsInPath: observed},
			Transcript: claude.TranscriptInfo{Path: "/t/" + sessionID + ".jsonl", Lines: lines, Size: lines * 20},
		}, nil
	}

	if _, err := testManager(t, store, provider, nil).Recover(context.Background(), sessionA); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	dirs := provider.resumeDirs()
	if len(dirs) != 1 {
		t.Fatalf("resumes = %d, want exactly 1", len(dirs))
	}
	if dirs[0] != observed {
		t.Errorf("resume ran in %q, want the newer observation %q (persisted row was %q)", dirs[0], observed, persisted)
	}
}

func TestRecoveryIsWorktreeNeutral(t *testing.T) {
	// R10 / §6.4: N attempts for one session leave exactly the worktrees that
	// existed before the first attempt. Zero created, zero removed — the copy
	// hazard (RC6) would otherwise produce one worktree per attempt.
	repo, worktree := recoveryWorktree(t)
	before := worktreeList(t, repo)
	if got := strings.Count(before, "worktree "); got != 2 {
		t.Fatalf("fixture has %d worktrees, want 2 (checkout + linked):\n%s", got, before)
	}

	for attempt := 1; attempt <= 3; attempt++ {
		// A fresh store and manager per attempt so all three really are
		// attempts; the session id and the repository are the same throughout.
		store := newTestStore(t)
		seedSession(t, store, worktreeSession(sessionA, repo, worktree))
		provider := newResumerStub()
		if _, err := testManager(t, store, provider, nil).Recover(context.Background(), sessionA); err != nil {
			t.Fatalf("attempt %d: Recover: %v", attempt, err)
		}
		dirs := provider.resumeDirs()
		if len(dirs) != 1 || dirs[0] != worktree {
			t.Fatalf("attempt %d: resumes = %v, want one in %q", attempt, dirs, worktree)
		}
	}

	after := worktreeList(t, repo)
	if after != before {
		t.Errorf("worktrees changed across three attempts:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if got := strings.Count(after, "worktree "); got != 2 {
		t.Errorf("worktrees after three attempts = %d, want 2: a recovery must not create one", got)
	}
}
