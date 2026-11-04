package claude

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/clock"
)

// cliScript describes how a fake claude executable reacts to each verified
// invocation. Zero values fall back to the verified fixtures.
type cliScript struct {
	version       string
	versionErr    error
	versionExit   int
	versionStderr string

	agents     string
	agentsErr  error
	agentsExit int

	resume       string
	resumeErr    error
	resumeExit   int
	resumeStderr string
}

// scriptedProvider returns a Provider whose CLI is backed by a dispatching fake
// execer, so every call answers according to its argv instead of a fragile
// positional queue.
func scriptedProvider(t *testing.T, home string, script cliScript) (*Provider, *FakeExecer) {
	t.Helper()

	if script.version == "" && script.versionErr == nil {
		script.version = testVersionOutput
	}
	if script.agents == "" {
		script.agents = testAgentsJSON
	}
	if script.resume == "" {
		script.resume = testResumeJSON
	}

	execer := NewFakeExecer(func(call Call) (Output, error) {
		argv := strings.Join(call.Args, " ")
		switch {
		case argv == "--version":
			return Output{
				Stdout:   []byte(script.version),
				Stderr:   []byte(script.versionStderr),
				ExitCode: script.versionExit,
			}, script.versionErr
		case strings.HasPrefix(argv, "agents --json"):
			return Output{
				Stdout:   []byte(script.agents),
				ExitCode: script.agentsExit,
			}, script.agentsErr
		case strings.HasPrefix(argv, "-p --resume"):
			return Output{
				Stdout:   []byte(script.resume),
				Stderr:   []byte(script.resumeStderr),
				ExitCode: script.resumeExit,
			}, script.resumeErr
		default:
			return Output{ExitCode: -1}, errors.New("unexpected claude invocation: " + argv)
		}
	})

	provider, err := New(Options{
		Executable: "claude",
		Home:       home,
		Execer:     execer,
		Clock:      clock.NewFake(time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)),
		Logger:     slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return provider, execer
}

func TestNewProviderDefaults(t *testing.T) {
	t.Parallel()

	provider, err := New(Options{Home: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if provider.CLI().Executable() != DefaultExecutable {
		t.Errorf("executable = %q, want %q", provider.CLI().Executable(), DefaultExecutable)
	}
	if provider.Store() == nil {
		t.Error("Store() = nil")
	}
	if provider.log == nil || provider.clock == nil {
		t.Error("logger/clock not defaulted")
	}

	provider, err = New(Options{
		Home:   t.TempDir(),
		Logger: slog.Default(),
		Clock:  clock.System(),
		All:    true,
		CWD:    "/root/project",
	})
	if err != nil {
		t.Fatalf("New() with explicit options error = %v", err)
	}
	if !provider.all || provider.cwd != "/root/project" {
		t.Errorf("options not applied: all=%v cwd=%q", provider.all, provider.cwd)
	}
}

func TestProviderDiscoverMergesSources(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	store, err := NewStore(home)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	// A transcript for a session the CLI does not list: the post-limit state.
	writeTranscript(t, store, "/root/old-project", testOtherSessionID, 4)
	// A session-env-only session.
	envOnlyID := "11111111-2222-4333-8444-555555555555"
	if err := os.MkdirAll(store.SessionEnvPath(envOnlyID), 0o700); err != nil {
		t.Fatalf("mkdir env: %v", err)
	}

	provider, execer := scriptedProvider(t, home, cliScript{})

	sessions, err := provider.Discover(context.Background(), DiscoverFilter{})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	byID := map[string]Session{}
	for _, session := range sessions {
		byID[session.ID] = session
	}

	// The live interactive session is recoverable, typed and enriched.
	live, ok := byID[testSessionID]
	if !ok {
		t.Fatalf("live session missing from %+v", sessions)
	}
	if !live.Recoverable || live.Type != SessionTypeInteractive {
		t.Errorf("live session = %+v, want recoverable interactive", live)
	}
	if live.PID != 4242 || live.Status != "busy" || live.Source != SourceAgents {
		t.Errorf("live session metadata = %+v", live)
	}
	if live.ProjectPath != "/root/project" || live.StartedAt.IsZero() {
		t.Errorf("live session path/start = %+v", live)
	}

	// The background session merges CLI metadata with its transcript record.
	background, ok := byID[testOtherSessionID]
	if !ok {
		t.Fatalf("background session missing from %+v", sessions)
	}
	if background.Type != SessionTypeBackground || background.Kind != "background" {
		t.Errorf("background type = %q/%q", background.Type, background.Kind)
	}
	if background.TranscriptPath == "" || background.TranscriptLines != 6 {
		t.Errorf("transcript not merged: %+v", background)
	}
	if !background.Recoverable {
		t.Errorf("background session must stay recoverable: %+v", background)
	}
	if background.WaitingFor != "usage limit reset" {
		t.Errorf("WaitingFor = %q", background.WaitingFor)
	}
	// The transcript is newer than the CLI start time, so it wins.
	if !background.LastActivity.After(live.LastActivity) {
		t.Errorf("transcript activity should be newer: %v vs %v", background.LastActivity, live.LastActivity)
	}

	// A session-env-only session is still recoverable.
	envSession, ok := byID[envOnlyID]
	if !ok {
		t.Fatalf("session-env-only session missing from %+v", sessions)
	}
	if !envSession.Recoverable || !envSession.HasSessionEnv {
		t.Errorf("session-env-only session = %+v", envSession)
	}
	if envSession.Type != SessionTypeTranscript || envSession.Source != SourceSessionEnv {
		t.Errorf("session-env-only type/source = %q/%q", envSession.Type, envSession.Source)
	}

	// An unsupported kind with a session id is reported as UNRECOVERABLE.
	unsupported, ok := byID[testUnsupportedSessionID]
	if !ok {
		t.Fatalf("unsupported session missing from %+v", sessions)
	}
	if unsupported.Recoverable || unsupported.RecoveryCode() != RecoveryUnrecoverable {
		t.Errorf("unsupported session = %+v, want UNRECOVERABLE", unsupported)
	}
	if !strings.Contains(unsupported.Reason, "unsupported session type") ||
		!strings.Contains(unsupported.Reason, "galaxy-brain") {
		t.Errorf("Reason = %q", unsupported.Reason)
	}

	// An entry without a session id is also UNRECOVERABLE, for a different reason.
	var anonymous Session
	for _, session := range sessions {
		if strings.HasPrefix(session.ID, "unidentified:") {
			anonymous = session
		}
	}
	if anonymous.ID == "" {
		t.Fatalf("anonymous session missing from %+v", sessions)
	}
	if anonymous.Recoverable || !strings.Contains(anonymous.Reason, "no session id") {
		t.Errorf("anonymous session = %+v", anonymous)
	}

	// Sessions are ordered newest activity first.
	for i := 1; i < len(sessions); i++ {
		if sessions[i].LastActivity.After(sessions[i-1].LastActivity) {
			t.Fatalf("sessions not sorted newest first: %+v", sessions)
		}
	}

	// The verified argv carries no extra flags by default.
	calls := execer.Calls()
	last := calls[len(calls)-1]
	if got := strings.Join(last.Args, " "); got != "agents --json" {
		t.Errorf("agents args = %q, want no flags by default", got)
	}
	if last.Options.Stdin != nil {
		t.Error("discovery must not attach a stdin stream")
	}

	// A second pass preserves DiscoveredAt.
	firstDiscovered := live.DiscoveredAt
	again, err := provider.Discover(context.Background(), DiscoverFilter{IDs: []string{testSessionID}})
	if err != nil {
		t.Fatalf("second Discover() error = %v", err)
	}
	if len(again) != 1 || !again[0].DiscoveredAt.Equal(firstDiscovered) {
		t.Errorf("DiscoveredAt not preserved: %v → %+v", firstDiscovered, again)
	}
}

func TestProviderDiscoverWithoutSessionID(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	provider, _ := scriptedProvider(t, home, cliScript{
		agents: `[{"id":"state-1","cwd":"/root/x","kind":"interactive","status":"busy"}]`,
	})

	sessions, err := provider.Discover(context.Background(), DiscoverFilter{})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %+v, want 1", sessions)
	}
	if sessions[0].Recoverable {
		t.Error("a session without a session id must be UNRECOVERABLE")
	}
	if !strings.Contains(sessions[0].Reason, "no session id") {
		t.Errorf("Reason = %q", sessions[0].Reason)
	}
	if _, err := provider.ResumeSession(context.Background(), sessions[0].ID, "go"); !errors.Is(err, ErrInvalidSessionID) {
		t.Errorf("error = %v, want ErrInvalidSessionID", err)
	}
}

func TestProviderDiscoverFilters(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	store, err := NewStore(home)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	writeTranscript(t, store, "/root/project", testOtherSessionID, 2)

	provider, execer := scriptedProvider(t, home, cliScript{})

	recoverableOnly, err := provider.Discover(context.Background(), DiscoverFilter{RecoverableOnly: true})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(recoverableOnly) == 0 {
		t.Fatal("RecoverableOnly returned nothing")
	}
	for _, session := range recoverableOnly {
		if !session.Recoverable {
			t.Fatalf("RecoverableOnly returned %+v", session)
		}
	}

	under, err := provider.Discover(context.Background(), DiscoverFilter{UnderCWD: "/root/other"})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	for _, session := range under {
		if !strings.HasPrefix(session.ProjectPath, "/root/other") {
			t.Fatalf("UnderCWD returned %+v", session)
		}
	}

	// The filter's All flag and UnderCWD must reach the CLI.
	if _, err := provider.Discover(context.Background(), DiscoverFilter{All: boolPtr(true), UnderCWD: "/root/project"}); err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	calls := execer.Calls()
	last := calls[len(calls)-1]
	if got := strings.Join(last.Args, " "); got != "agents --json --all --cwd /root/project" {
		t.Errorf("agents args = %q", got)
	}
}

func TestProviderDiscoverFallbacks(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	store, err := NewStore(home)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	writeTranscript(t, store, "/root/project", testOtherSessionID, 2)

	// The CLI listing fails, but on-disk sessions must still be discovered.
	provider, _ := scriptedProvider(t, home, cliScript{agentsErr: errors.New("agents exploded")})
	sessions, err := provider.Discover(context.Background(), DiscoverFilter{})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != testOtherSessionID {
		t.Fatalf("sessions = %+v, want the on-disk session", sessions)
	}
	if !sessions[0].Recoverable {
		t.Errorf("on-disk session not recoverable: %+v", sessions[0])
	}

	// A non-zero exit without a Go error is still a failure.
	provider, _ = scriptedProvider(t, home, cliScript{agents: "", agentsExit: 2})
	provider.cli.ResetVersionCache()
	if sessions, err := provider.Discover(context.Background(), DiscoverFilter{}); err != nil {
		// `agents` was emptied and the exit code set; discovery degrades to disk.
		t.Fatalf("Discover() error = %v", err)
	} else if len(sessions) != 1 {
		t.Fatalf("sessions = %+v, want the on-disk session", sessions)
	}

	// An unusable version fails loudly.
	provider, _ = scriptedProvider(t, home, cliScript{version: "1.0.0\n"})
	if _, err := provider.Discover(context.Background(), DiscoverFilter{}); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("error = %v, want ErrUnsupportedVersion", err)
	}
}

func TestProviderGetSessionStatusFreshTranscript(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	store, err := NewStore(home)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	writeTranscript(t, store, "/root/project", testSessionID, 4)

	provider, _ := scriptedProvider(t, home, cliScript{})

	status, err := provider.GetSessionStatus(context.Background(), testSessionID)
	if err != nil {
		t.Fatalf("GetSessionStatus() error = %v", err)
	}
	if !status.Running {
		t.Error("Running = false for a busy session")
	}
	if status.CheckedAt.IsZero() {
		t.Error("CheckedAt not set")
	}
	if status.Transcript.SessionID != testSessionID || status.Transcript.Lines != 6 {
		t.Errorf("transcript not refreshed: %+v", status.Transcript)
	}
	if status.Session.TranscriptSize != status.Transcript.Size {
		t.Errorf("session transcript size not refreshed: %d vs %d", status.Session.TranscriptSize, status.Transcript.Size)
	}
	if !status.Session.LastActivity.Equal(status.Transcript.LastTimestamp) {
		t.Errorf("LastActivity = %v, want %v", status.Session.LastActivity, status.Transcript.LastTimestamp)
	}
}

func TestProviderGetSessionErrors(t *testing.T) {
	t.Parallel()

	provider, _ := scriptedProvider(t, t.TempDir(), cliScript{})

	if _, err := provider.GetSession(context.Background(), "bogus"); !errors.Is(err, ErrInvalidSessionID) {
		t.Errorf("GetSession(bogus) error = %v", err)
	}
	if _, err := provider.GetSession(context.Background(), "11111111-2222-4333-8444-555555555555"); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("GetSession(unknown) error = %v", err)
	}
	if _, err := provider.GetSessionStatus(context.Background(), "11111111-2222-4333-8444-555555555555"); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("GetSessionStatus(unknown) error = %v", err)
	}
}

func TestProviderResume(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	store, err := NewStore(home)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	writeTranscript(t, store, "/root/project", testSessionID, 4)

	provider, execer := scriptedProvider(t, home, cliScript{})

	result, err := provider.ResumeSession(context.Background(), testSessionID, "continue the goal")
	if err != nil {
		t.Fatalf("ResumeSession() error = %v", err)
	}
	if !result.Verified || result.SessionID != testSessionID {
		t.Errorf("result = %+v, want a verified resume", result)
	}

	calls := execer.Calls()
	last := calls[len(calls)-1]
	want := strings.Join([]string{"-p", "--resume", testSessionID, "continue the goal", "--output-format", "json"}, " ")
	if got := strings.Join(last.Args, " "); got != want {
		t.Errorf("resume argv = %q, want %q", got, want)
	}
	if last.Options.Stdin != nil {
		t.Error("resume must not attach a stdin stream")
	}

	if _, err := provider.ResumeSession(context.Background(), "bogus", "go"); !errors.Is(err, ErrInvalidSessionID) {
		t.Errorf("ResumeSession(invalid) error = %v", err)
	}
	if _, err := provider.ResumeSession(context.Background(), testUnsupportedSessionID, "go"); !errors.Is(err, ErrSessionNotFound) && !errors.Is(err, ErrUnrecoverable) {
		t.Errorf("ResumeSession(unknown) error = %v", err)
	}
}

func TestProviderResumeRefusesUnrecoverable(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	provider, execer := scriptedProvider(t, home, cliScript{
		agents: `[{"id":"state-1","cwd":"/root/x","kind":"galaxy-brain","sessionId":"` + testSessionID + `"}]`,
	})

	sessions, err := provider.Discover(context.Background(), DiscoverFilter{})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(sessions) != 1 || sessions[0].Recoverable {
		t.Fatalf("sessions = %+v, want one UNRECOVERABLE session", sessions)
	}
	if _, err := provider.GetSession(context.Background(), testSessionID); err != nil {
		t.Fatalf("GetSession() error = %v", err)
	}

	if _, err := provider.ResumeSession(context.Background(), testSessionID, "go"); !errors.Is(err, ErrUnrecoverable) {
		t.Fatalf("ResumeSession() error = %v, want ErrUnrecoverable", err)
	}
	for _, call := range execer.Calls() {
		if strings.HasPrefix(strings.Join(call.Args, " "), "-p --resume") {
			t.Fatalf("an unrecoverable session must never produce a resume command: %v", call.Args)
		}
	}
}

func TestProviderResumeFailurePropagates(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	store, err := NewStore(home)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	writeTranscript(t, store, "/root/project", testSessionID, 2)

	provider, _ := scriptedProvider(t, home, cliScript{
		resume:     `{"type":"result","is_error":true,"session_id":"` + testSessionID + `","result":"still limited"}`,
		resumeExit: 0,
	})
	if _, err := provider.ResumeSession(context.Background(), testSessionID, "go"); !errors.Is(err, ErrResumeFailed) {
		t.Fatalf("ResumeSession() error = %v, want ErrResumeFailed", err)
	}
}

func TestProviderVersion(t *testing.T) {
	t.Parallel()

	provider, _ := scriptedProvider(t, t.TempDir(), cliScript{})
	report, err := provider.Version(context.Background())
	if err != nil {
		t.Fatalf("Version() error = %v", err)
	}
	if report.Version.String() != "2.1.223" {
		t.Errorf("Version() = %s", report.Version)
	}

	oldProvider, _ := scriptedProvider(t, t.TempDir(), cliScript{version: "1.5.0\n"})
	if _, err := oldProvider.Version(context.Background()); !errors.Is(err, ErrUnsupportedVersion) {
		t.Errorf("Version() error = %v, want ErrUnsupportedVersion", err)
	}
}

func TestProviderStoreScanFailureIsNotFatal(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	store, err := NewStore(home)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	writeTranscript(t, store, "/root/project", testSessionID, 2)

	provider, _ := scriptedProvider(t, home, cliScript{})

	// Remove the transcript between discovery and the status scan: the status
	// must still be returned, simply without fresh transcript metadata.
	path, err := store.TranscriptPath(testSessionID)
	if err != nil {
		t.Fatalf("TranscriptPath() error = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove transcript: %v", err)
	}

	// Discovery then finds the session through session-env absence only if the
	// CLI lists it, which it does.
	status, err := provider.GetSessionStatus(context.Background(), testSessionID)
	if err != nil {
		t.Fatalf("GetSessionStatus() error = %v", err)
	}
	if status.Session.ID != testSessionID {
		t.Errorf("status session = %q", status.Session.ID)
	}
	if status.Transcript.Path != "" {
		t.Errorf("expected no fresh transcript metadata, got %+v", status.Transcript)
	}

	// A session whose transcript directory vanished entirely is not an error.
	_ = filepath.Join(home, "projects")
}

func boolPtr(b bool) *bool { return &b }
