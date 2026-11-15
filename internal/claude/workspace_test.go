package claude

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
	"time"

	"github.com/jymchng/ccresu/internal/clock"
)

// linkedWorktree creates a real git repository and a real linked worktree of it,
// and returns both paths.
//
// PRD 6 is about a session that lives in `.claude/worktrees/…` rather than in
// the checkout it was dispatched from, so the fixture has to be an actual
// `git worktree add`: a plain temporary directory would pass every assertion
// while proving nothing about the case the PRD exists for. The production code
// never runs `git worktree` (R6) — this is the test doing what an operator does.
func linkedWorktree(t *testing.T) (repo, worktree string) {
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

// agentsJSONWithCWD renders one `claude agents --json` entry for a background
// session whose published `cwd` is cwd.
//
// The `cwd` is the whole point: it is the only supported source for where a
// session runs (PRD 6 §6.2), so every worktree test drives the provider through
// this listing rather than setting Session fields directly.
func agentsJSONWithCWD(id, cwd string) string {
	return `[{"pid":4242,"id":"agent-state-1","cwd":"` + cwd + `","kind":"background",` +
		`"startedAt":1750000000000,"sessionId":"` + id + `","name":"refactor things",` +
		`"state":"working","status":"busy"}]`
}

// observingProvider returns a Provider whose `agents --json` answer walks cwds,
// one entry per call, and whose handoff is acknowledged.
//
// The walk is what makes R4 and R9 testable: a session that moves into a
// worktree mid-life is only observable if two consecutive passes can disagree.
func observingProvider(t *testing.T, home string, cwds []string, logger *slog.Logger) (*Provider, *FakeExecer) {
	t.Helper()
	var mu sync.Mutex
	call := 0
	execer := NewFakeExecer(func(c Call) (Output, error) {
		argv := strings.Join(c.Args, " ")
		switch {
		case argv == "--version":
			return Output{Stdout: []byte(testVersionOutput)}, nil
		case strings.HasPrefix(argv, "agents --json"):
			mu.Lock()
			cwd := cwds[len(cwds)-1]
			if call < len(cwds) {
				cwd = cwds[call]
			}
			call++
			mu.Unlock()
			return Output{Stdout: []byte(agentsJSONWithCWD(testSessionID, cwd))}, nil
		case strings.HasPrefix(argv, "--bg --resume"):
			return Output{Stdout: []byte(testHandoffBlock)}, nil
		default:
			return Output{ExitCode: -1}, errors.New("unexpected claude invocation: " + argv)
		}
	})
	provider, err := New(Options{
		Executable: "claude",
		Home:       home,
		Execer:     execer,
		Clock:      clock.NewFake(time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)),
		Logger:     logger,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return provider, execer
}

// resumeCalls returns the recorded `--bg --resume` invocations.
func resumeCalls(execer *FakeExecer) []Call {
	var out []Call
	for _, call := range execer.Calls() {
		if len(call.Args) > 0 && call.Args[0] == "--bg" {
			out = append(out, call)
		}
	}
	return out
}

// captureLogger returns a logger writing at Debug to the returned buffer.
func captureLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

func TestResolveWorkspacePrefersTheRecordedRunDirectory(t *testing.T) {
	// R3: the order is a rule. The recorded run directory wins even though the
	// project directory also exists and is a perfectly good directory.
	repo, worktree := linkedWorktree(t)

	workspace, err := ResolveWorkspace(worktree, repo)
	if err != nil {
		t.Fatalf("ResolveWorkspace = %v, want nil", err)
	}
	if workspace.Dir != worktree {
		t.Errorf("Dir = %q, want the recorded run directory %q", workspace.Dir, worktree)
	}
	if workspace.FellBack {
		t.Error("FellBack = true, want false: the recorded directory exists")
	}
	if workspace.Missing != "" {
		t.Errorf("Missing = %q, want empty when nothing was skipped", workspace.Missing)
	}
}

func TestResolveWorkspaceUsesALinkedGitWorktree(t *testing.T) {
	// PRD 6 §6.1 row 2: the session is already inside a linked worktree, so the
	// recovery cwd is that worktree and never the main checkout.
	repo, worktree := linkedWorktree(t)
	if filepath.Dir(worktree) == repo {
		t.Fatalf("fixture worktree %q is inside the repo %q", worktree, repo)
	}
	// The worktree is a real checkout: it has the committed file and its own
	// .git file pointing at the main repository.
	if _, err := os.Stat(filepath.Join(worktree, "README.md")); err != nil {
		t.Fatalf("worktree is not a checkout: %v", err)
	}

	workspace, err := ResolveWorkspace(worktree, repo)
	if err != nil {
		t.Fatalf("ResolveWorkspace = %v, want nil", err)
	}
	if workspace.Dir != worktree {
		t.Errorf("Dir = %q, want the worktree %q and not the checkout %q", workspace.Dir, worktree, repo)
	}
}

func TestResolveWorkspaceFallsBackToProjectPathWhenTheRunDirectoryIsGone(t *testing.T) {
	// R8: a deleted worktree is a fact to observe, not a reason to abandon the
	// session. The fallback is reported so the caller can say which path it
	// skipped.
	repo := t.TempDir()
	gone := filepath.Join(t.TempDir(), "cleared-worktree")

	workspace, err := ResolveWorkspace(gone, repo)
	if err != nil {
		t.Fatalf("ResolveWorkspace = %v, want the documented fallback", err)
	}
	if workspace.Dir != repo {
		t.Errorf("Dir = %q, want the project directory %q", workspace.Dir, repo)
	}
	if !workspace.FellBack {
		t.Error("FellBack = false, want true: the recorded run directory no longer exists")
	}
	if workspace.Missing != gone {
		t.Errorf("Missing = %q, want the path that could not be used %q", workspace.Missing, gone)
	}
}

func TestResolveWorkspaceFailsWithANamedErrorWhenNeitherIsUsable(t *testing.T) {
	// R2: an empty Dir is the bug. The attempt fails with a name instead of
	// inheriting the daemon's cwd, which under the shipped systemd unit is $HOME.
	cases := []struct {
		name     string
		runsIn   string
		project  string
		wantRuns string
	}{
		{name: "both empty"},
		{name: "recorded directory gone and no project", runsIn: filepath.Join(t.TempDir(), "gone")},
		{name: "recorded directory gone and project gone", runsIn: filepath.Join(t.TempDir(), "a"), project: filepath.Join(t.TempDir(), "b")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			workspace, err := ResolveWorkspace(tc.runsIn, tc.project)
			if !errors.Is(err, ErrWorkspaceUnresolved) {
				t.Fatalf("ResolveWorkspace(%q, %q) = %v, want ErrWorkspaceUnresolved", tc.runsIn, tc.project, err)
			}
			if workspace.Dir != "" {
				t.Errorf("Dir = %q, want empty: nothing is usable, so nothing may be inherited", workspace.Dir)
			}
			// The error names both candidates, so an operator can see which
			// paths ccresu tried rather than only that it gave up.
			if !strings.Contains(err.Error(), "recorded run directory") {
				t.Errorf("error = %q, want it to name the candidates", err)
			}
		})
	}
}

func TestResolveWorkspaceIgnoresBlankAndNonDirectoryPaths(t *testing.T) {
	// A blank recorded path is "not observed yet" (§6.5): nothing was skipped,
	// so nothing is reported as a fallback. A path that exists but is a file is
	// a binding that cannot be used, so it falls back *and* is named.
	repo := t.TempDir()
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}

	for _, runsIn := range []string{"", "   "} {
		workspace, err := ResolveWorkspace(runsIn, repo)
		if err != nil {
			t.Fatalf("ResolveWorkspace(%q, repo) = %v, want the project directory", runsIn, err)
		}
		if workspace.Dir != repo {
			t.Errorf("ResolveWorkspace(%q) Dir = %q, want %q", runsIn, workspace.Dir, repo)
		}
		if workspace.FellBack {
			t.Errorf("ResolveWorkspace(%q) FellBack = true, want false: there was no binding to skip", runsIn)
		}
	}

	// A file is not a working directory, so it cannot win; it is still reported,
	// because the operator recorded it and needs to know it was skipped.
	workspace, err := ResolveWorkspace(file, repo)
	if err != nil {
		t.Fatalf("ResolveWorkspace(file, repo) = %v, want the project directory", err)
	}
	if workspace.Dir != repo {
		t.Errorf("Dir = %q, want %q: a file is not a working directory", workspace.Dir, repo)
	}
	if !workspace.FellBack || workspace.Missing != file {
		t.Errorf("workspace = %+v, want the unusable binding reported", workspace)
	}

	// A project path that is a file is not a project path either.
	if _, err := ResolveWorkspace("", file); !errors.Is(err, ErrWorkspaceUnresolved) {
		t.Errorf("ResolveWorkspace(\"\", file) = %v, want ErrWorkspaceUnresolved", err)
	}
}

func TestRunDirectoryIsRefreshedOnEveryObservation(t *testing.T) {
	// R4: a session that moves into a worktree mid-life has its run directory
	// updated on the next observation, and R1: the launch directory is not
	// overwritten by it, so both facts survive.
	repo, worktree := linkedWorktree(t)
	logger, _ := captureLogger()
	provider, _ := observingProvider(t, t.TempDir(), []string{repo, worktree}, logger)
	ctx := context.Background()

	first, err := provider.Discover(ctx, DiscoverFilter{})
	if err != nil {
		t.Fatalf("first Discover: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("first Discover returned %d sessions, want 1", len(first))
	}
	if first[0].RunsInPath != repo {
		t.Fatalf("first RunsInPath = %q, want %q", first[0].RunsInPath, repo)
	}
	if first[0].ProjectPath != repo {
		t.Fatalf("first ProjectPath = %q, want %q", first[0].ProjectPath, repo)
	}

	second, err := provider.Discover(ctx, DiscoverFilter{})
	if err != nil {
		t.Fatalf("second Discover: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("second Discover returned %d sessions, want 1", len(second))
	}
	if second[0].RunsInPath != worktree {
		t.Errorf("second RunsInPath = %q, want the worktree %q", second[0].RunsInPath, worktree)
	}
	if second[0].ProjectPath != repo {
		t.Errorf("second ProjectPath = %q, want the launch directory %q kept", second[0].ProjectPath, repo)
	}
}

func TestNewerObservationWinsOverAStaleBinding(t *testing.T) {
	// R9: a recorded run directory that disagrees with the newest observation
	// must not be used silently. The newer value wins and the disagreement is
	// logged, because resuming into a tree the session does not occupy is the
	// failure this PRD exists to prevent.
	repo, worktree := linkedWorktree(t)
	logger, logs := captureLogger()
	provider, execer := observingProvider(t, t.TempDir(), []string{repo, worktree}, logger)
	ctx := context.Background()

	if _, err := provider.Discover(ctx, DiscoverFilter{}); err != nil {
		t.Fatalf("first Discover: %v", err)
	}
	// ResumeSession re-observes, so the handoff must follow the newer cwd rather
	// than the binding recorded on the first pass.
	result, err := provider.ResumeSession(ctx, testSessionID, "continue")
	if err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	if result.CWD != worktree {
		t.Errorf("Result.CWD = %q, want the newer observation %q", result.CWD, worktree)
	}
	calls := resumeCalls(execer)
	if len(calls) != 1 {
		t.Fatalf("handoff calls = %d, want exactly 1", len(calls))
	}
	if calls[0].Options.Dir != worktree {
		t.Errorf("handoff ran in %q, want the newer observation %q", calls[0].Options.Dir, worktree)
	}
	if !strings.Contains(logs.String(), "changed its working directory") {
		t.Errorf("logs = %q, want the disagreement recorded", logs.String())
	}
	if !strings.Contains(logs.String(), repo) || !strings.Contains(logs.String(), worktree) {
		t.Errorf("logs = %q, want both the old and the new directory named", logs.String())
	}
}

func TestResumeSessionInRunsTheHandoffInTheGivenDirectory(t *testing.T) {
	// R2/R3: the directory the caller resolved is the one the child process
	// runs in. This is the falsifiable requirement — `RunOptions.Dir` on the
	// invocation the fake execer received, not a field on a result.
	repo, worktree := linkedWorktree(t)
	provider, execer := scriptedProvider(t, t.TempDir(), cliScript{agents: agentsJSONWithCWD(testSessionID, repo)})

	result, err := provider.ResumeSessionIn(context.Background(), testSessionID, "continue", worktree)
	if err != nil {
		t.Fatalf("ResumeSessionIn: %v", err)
	}
	if result.CWD != worktree {
		t.Errorf("Result.CWD = %q, want %q", result.CWD, worktree)
	}
	calls := resumeCalls(execer)
	if len(calls) != 1 {
		t.Fatalf("handoff calls = %d, want exactly 1", len(calls))
	}
	if calls[0].Options.Dir != worktree {
		t.Errorf("handoff Dir = %q, want the worktree %q and not the daemon's cwd", calls[0].Options.Dir, worktree)
	}

	// R2 also covers the query invocations: the version probe and the listing
	// must not inherit the session's directory either.
	for _, call := range execer.Calls() {
		if call.Args[0] == "--bg" {
			continue
		}
		if call.Options.Dir == worktree {
			t.Errorf("%v ran in the session's worktree %q, want ccresu's own directory", call.Args, worktree)
		}
		if call.Options.Dir == "" {
			t.Errorf("%v inherited an empty Dir, want ccresu's own directory passed explicitly", call.Args)
		}
	}
}

func TestResumeVectorHasNoWorktreeFlag(t *testing.T) {
	// R5/R6/R7: a recovery re-enters the tree the session occupies. It never
	// asks the CLI to create a worktree, and ccresu never runs git worktree or
	// `claude rm` itself.
	repo, worktree := linkedWorktree(t)
	provider, execer := scriptedProvider(t, t.TempDir(), cliScript{agents: agentsJSONWithCWD(testSessionID, repo)})

	result, err := provider.ResumeSessionIn(context.Background(), testSessionID, "continue", worktree)
	if err != nil {
		t.Fatalf("ResumeSessionIn: %v", err)
	}

	want := append([]string{"claude"}, HandoffArgs(testSessionID, "continue")...)
	if strings.Join(result.Command, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("handoff vector = %v, want %v", result.Command, want)
	}

	calls := resumeCalls(execer)
	if len(calls) != 1 {
		t.Fatalf("handoff calls = %d, want exactly 1", len(calls))
	}
	forbidden := map[string]string{
		"--worktree": "a recovery must not create a worktree (R5)",
		"-w":         "a recovery must not create a worktree (R5)",
		"worktree":   "a recovery must not touch the worktree lifecycle (R6)",
		"git":        "ccresu never runs git (R6)",
		"rm":         "ccresu never removes a worktree (R7)",
	}
	for _, arg := range calls[0].Args {
		if why, bad := forbidden[arg]; bad {
			t.Errorf("handoff vector %v contains %q: %s", calls[0].Args, arg, why)
		}
	}
	// The shape PRD 5 fixed is unchanged: --bg, --resume <id>, --name <label>,
	// then the message.
	if got := calls[0].Args; len(got) < 6 || got[0] != "--bg" || got[1] != "--resume" ||
		got[2] != testSessionID || got[3] != "--name" || got[5] != "continue" {
		t.Errorf("handoff argv = %v, want --bg --resume <id> --name <label> <message>", got)
	}
}

func TestResumeSessionFallsBackWhenTheRecordedWorktreeIsGone(t *testing.T) {
	// R8 end to end: the recorded run directory has been cleared, so the
	// handoff runs in the project directory and the fallback is announced. The
	// session still resumes — a missing worktree is not a verdict.
	repo := t.TempDir()
	gone := filepath.Join(t.TempDir(), "cleared-worktree")
	logger, logs := captureLogger()
	provider, execer := observingProvider(t, t.TempDir(), []string{repo, gone}, logger)
	ctx := context.Background()

	if _, err := provider.Discover(ctx, DiscoverFilter{}); err != nil {
		t.Fatalf("first Discover: %v", err)
	}
	result, err := provider.ResumeSession(ctx, testSessionID, "continue")
	if err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	if result.CWD != repo {
		t.Errorf("Result.CWD = %q, want the project directory %q", result.CWD, repo)
	}
	calls := resumeCalls(execer)
	if len(calls) != 1 {
		t.Fatalf("handoff calls = %d, want exactly 1: the attempt proceeds", len(calls))
	}
	if calls[0].Options.Dir != repo {
		t.Errorf("handoff Dir = %q, want the project directory %q", calls[0].Options.Dir, repo)
	}
	if !strings.Contains(logs.String(), "recorded run directory is gone") {
		t.Errorf("logs = %q, want the fallback announced", logs.String())
	}
	if !strings.Contains(logs.String(), gone) {
		t.Errorf("logs = %q, want the skipped path named", logs.String())
	}
}

func TestResumeSessionRefusesAnUnresolvedWorkspace(t *testing.T) {
	// R2/§6.5: with neither directory available the attempt fails with a named
	// error and no child process is spawned. It is not ErrUnrecoverable: a
	// directory can come back, so this is a failed attempt, not a verdict.
	provider, execer := scriptedProvider(t, t.TempDir(), cliScript{agents: agentsJSONWithCWD(testSessionID, "")})

	_, err := provider.ResumeSession(context.Background(), testSessionID, "continue")
	if !errors.Is(err, ErrWorkspaceUnresolved) {
		t.Fatalf("ResumeSession = %v, want ErrWorkspaceUnresolved", err)
	}
	if errors.Is(err, ErrUnrecoverable) {
		t.Error("error wraps ErrUnrecoverable, want a failed attempt: a directory can come back")
	}
	if calls := resumeCalls(execer); len(calls) != 0 {
		t.Errorf("handoff calls = %d, want 0: nothing may run without a directory", len(calls))
	}
}

func TestResumeSessionInResumesASessionTheListingOmits(t *testing.T) {
	// A session the CLI no longer lists is still resumable by id: the caller
	// already resolved a directory, and the handoff is addressed by session id.
	// Reading the id off a zero-value Session instead built a `--resume ""`
	// vector that the CLI rejected, so this case could never hand off at all.
	dir := t.TempDir()
	provider, execer := scriptedProvider(t, t.TempDir(), cliScript{agents: "[]"})

	result, err := provider.ResumeSessionIn(context.Background(), testSessionID, "continue", dir)
	if err != nil {
		t.Fatalf("ResumeSessionIn: %v", err)
	}
	if result.RequestedSessionID != testSessionID {
		t.Errorf("RequestedSessionID = %q, want %q", result.RequestedSessionID, testSessionID)
	}
	calls := resumeCalls(execer)
	if len(calls) != 1 {
		t.Fatalf("handoff calls = %d, want exactly 1", len(calls))
	}
	if calls[0].Args[2] != testSessionID {
		t.Errorf("handoff argv = %v, want --resume %s", calls[0].Args, testSessionID)
	}
	if calls[0].Options.Dir != dir {
		t.Errorf("handoff Dir = %q, want the caller's directory %q", calls[0].Options.Dir, dir)
	}
}
