package claude_test

// Independent verification of the acceptance criteria for internal/claude.
//
// These tests deliberately do not reuse the package's own fixtures or helpers:
// they assert the externally visible contract of the confirmed interfaces and
// try to break it from the outside.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
)

const (
	sessionA = "0f6a4b6c-1111-4a2b-9c3d-00000000000a"
	sessionB = "0f6a4b6c-2222-4a2b-9c3d-00000000000b"
	sessionC = "0f6a4b6c-3333-4a2b-9c3d-00000000000c"
)

// fakeCLI is an independent execer that replies by argv.
type fakeCLI struct {
	t        *testing.T
	version  string
	agents   string
	resume   string
	exit     int
	resumeNo int
	seen     [][]string
}

func (f *fakeCLI) Run(_ context.Context, name string, args []string, _ claude.RunOptions) (claude.Output, error) {
	f.seen = append(f.seen, append([]string{name}, args...))
	argv := strings.Join(args, " ")
	switch {
	case argv == "--version":
		return claude.Output{Stdout: []byte(f.version)}, nil
	case strings.HasPrefix(argv, "agents"):
		return claude.Output{Stdout: []byte(f.agents), ExitCode: f.exit}, nil
	case strings.HasPrefix(argv, "--resume"):
		return claude.Output{Stdout: []byte(f.resume), ExitCode: f.resumeNo}, nil
	}
	f.t.Fatalf("unexpected invocation: %q", argv)
	return claude.Output{}, nil
}

func newProvider(t *testing.T, exec claude.Execer, home string) claude.SessionProvider {
	t.Helper()
	provider, err := claude.New(claude.Options{Executable: "claude", Home: home, Execer: exec})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return provider
}

// TestInterfaceSurface asserts the exact four methods the rest of ccresu depends
// on, with the documented signatures.
func TestInterfaceSurface(t *testing.T) {
	t.Parallel()

	var (
		_ func(context.Context, claude.DiscoverFilter) ([]claude.Session, error) = newProvider(t, &fakeCLI{t: t, version: "2.1.0 (Claude Code)"}, t.TempDir()).Discover
		_ func(context.Context, string) (claude.Session, error)                  = newProvider(t, &fakeCLI{t: t, version: "2.1.0 (Claude Code)"}, t.TempDir()).GetSession
		_ func(context.Context, string) (claude.SessionStatus, error)            = newProvider(t, &fakeCLI{t: t, version: "2.1.0 (Claude Code)"}, t.TempDir()).GetSessionStatus
		_ func(context.Context, string, string) (claude.ResumeResult, error)     = newProvider(t, &fakeCLI{t: t, version: "2.1.0 (Claude Code)"}, t.TempDir()).ResumeSession
	)

	// The fake must satisfy the same interface so recovery can be tested without
	// a CLI.
	var _ claude.SessionProvider = claude.NewFakeProvider()
}

// TestUnsupportedSessionTypesAreUnrecoverable checks the acceptance criterion
// that anything ccresu cannot resume is labelled UNRECOVERABLE with a reason,
// and that no command is ever constructed for it.
func TestUnsupportedSessionTypesAreUnrecoverable(t *testing.T) {
	t.Parallel()

	agents := `[
	  {"id":"1","kind":"cloud","sessionId":"` + sessionA + `","cwd":"/w"},
	  {"id":"2","kind":"","sessionId":"` + sessionB + `","cwd":"/w"},
	  {"id":"3","kind":"interactive","cwd":"/w"}
	]`
	exec := &fakeCLI{t: t, version: "2.1.0 (Claude Code)", agents: agents}
	provider := newProvider(t, exec, t.TempDir())

	sessions, err := provider.Discover(context.Background(), claude.DiscoverFilter{})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("sessions = %d, want 3", len(sessions))
	}
	for _, session := range sessions {
		if session.Recoverable {
			t.Errorf("session %+v must not be recoverable", session)
		}
		if session.RecoveryCode() != claude.RecoveryUnrecoverable {
			t.Errorf("RecoveryCode() = %q, want %q", session.RecoveryCode(), claude.RecoveryUnrecoverable)
		}
		if strings.TrimSpace(session.Reason) == "" {
			t.Errorf("session %+v has no reason", session)
		}
	}

	// Resuming must fail before executing anything.
	before := len(exec.seen)
	_, err = provider.ResumeSession(context.Background(), sessionA, "continue")
	if !errors.Is(err, claude.ErrUnrecoverable) {
		t.Fatalf("ResumeSession() error = %v, want ErrUnrecoverable", err)
	}
	for _, argv := range exec.seen[before:] {
		if len(argv) > 1 && argv[1] == "-p" {
			t.Fatalf("a resume command was constructed for an unrecoverable session: %v", argv)
		}
	}
}

// TestResumeCommandIsExactlyTheVerifiedOne asserts the only state-changing
// command, byte for byte, with hostile input.
func TestResumeCommandIsExactlyTheVerifiedOne(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	exec := &fakeCLI{
		t:       t,
		version: "2.1.223 (Claude Code)",
		agents:  `[]`,
		resume:  "backgrounded \u00b7 3f1e2c7a\n  claude stop 3f1e2c7a      stop this session\n",
	}
	provider := newProvider(t, exec, home)

	// A transcript makes the session discoverable without the CLI listing.
	dir := filepath.Join(home, "projects", claude.SlugifyCWD("/w"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	line := `{"type":"user","sessionId":"` + sessionA + `","cwd":"/w","timestamp":"2026-06-01T10:00:00Z","message":{"role":"user","content":"hello"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, sessionA+".jsonl"), []byte(line), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	message := `continue; touch /tmp/pwned && $(whoami) "quoted" 'single'` + "\n--output-format text"
	result, err := provider.ResumeSession(context.Background(), sessionA, message)
	if err != nil {
		t.Fatalf("ResumeSession() error = %v", err)
	}
	if result.JobID != "3f1e2c7a" {
		t.Errorf("JobID = %q, want the job the dispatch printed", result.JobID)
	}

	var resumeArgv []string
	for _, argv := range exec.seen {
		if len(argv) > 1 && argv[1] == "--resume" {
			resumeArgv = argv
		}
	}
	if resumeArgv == nil {
		t.Fatal("no resume command was executed")
	}
	want := []string{"claude", "--resume", sessionA, "--bg", message}
	if len(resumeArgv) != len(want) {
		t.Fatalf("argv = %q, want exactly %q", resumeArgv, want)
	}
	for i := range want {
		if resumeArgv[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q (full argv %q)", i, resumeArgv[i], want[i], resumeArgv)
		}
	}
}

// TestResumeDispatchThatReportsNoJobIDIsNeverCountedAsSuccess checks that a
// background dispatch which exits 0 without naming the job it created is a
// failure: with `--output-format json` gone, the acknowledgement line is the only
// thing the dispatch can report, so a missing job id means there is no job to
// verify and nothing to claim.
func TestResumeDispatchThatReportsNoJobIDIsNeverCountedAsSuccess(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dir := filepath.Join(home, "projects", claude.SlugifyCWD("/w"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	line := `{"type":"user","sessionId":"` + sessionA + `","cwd":"/w","timestamp":"2026-06-01T10:00:00Z","message":{"role":"user","content":"hello"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, sessionA+".jsonl"), []byte(line), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cases := []struct {
		name    string
		resume  string
		exit    int
		wantErr error
	}{
		{
			name:    "no acknowledgement line",
			resume:  "starting a background service\n",
			wantErr: claude.ErrNoBackgroundJob,
		},
		{
			name:    "acknowledgement without a job id",
			resume:  "backgrounded \u00b7\n",
			wantErr: claude.ErrNoBackgroundJob,
		},
		{
			name:    "dispatch refused",
			resume:  "",
			exit:    1,
			wantErr: claude.ErrCLIUnavailable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			exec := &fakeCLI{t: t, version: "2.1.223 (Claude Code)", agents: "[]", resume: tc.resume, resumeNo: tc.exit}
			provider := newProvider(t, exec, home)
			result, err := provider.ResumeSession(context.Background(), sessionA, "continue")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if result.JobID != "" {
				t.Errorf("JobID = %q, want empty on a failed dispatch", result.JobID)
			}
		})
	}
}

// TestInvalidSessionIDsNeverReachTheCommandLine checks the anti-flag-injection
// and anti-path-traversal guarantees.
func TestInvalidSessionIDsNeverReachTheCommandLine(t *testing.T) {
	t.Parallel()

	exec := &fakeCLI{t: t, version: "2.1.223 (Claude Code)", agents: "[]"}
	provider := newProvider(t, exec, t.TempDir())

	hostile := []string{
		"--dangerously-skip-permissions",
		"-p",
		"../../etc/passwd",
		"3f1e2c7a-8b4d-4c5e-9f01-2a3b4c5d6e7f\n-p",
		"$(id)",
		"`id`",
		"3f1e2c7a8b4d4c5e9f012a3b4c5d6e7f",
	}
	for _, id := range hostile {
		if _, err := provider.ResumeSession(context.Background(), id, "continue"); !errors.Is(err, claude.ErrInvalidSessionID) {
			t.Errorf("ResumeSession(%q) error = %v, want ErrInvalidSessionID", id, err)
		}
		if _, err := provider.GetSession(context.Background(), id); !errors.Is(err, claude.ErrInvalidSessionID) {
			t.Errorf("GetSession(%q) error = %v, want ErrInvalidSessionID", id, err)
		}
	}
	for _, argv := range exec.seen {
		for _, arg := range argv {
			if arg == "--dangerously-skip-permissions" || strings.Contains(arg, "passwd") {
				t.Fatalf("hostile id reached the command line: %v", argv)
			}
		}
	}
}

// TestVersionParsingOfRealBanners checks that the adapter handles the banners
// actually produced by the CLI, and refuses what it cannot understand.
func TestVersionParsingOfRealBanners(t *testing.T) {
	t.Parallel()

	accepted := []struct {
		banner string
		want   string
	}{
		{"2.1.223 (Claude Code)\n", "2.1.223"},
		{"2.1.223 (Claude Code)", "2.1.223"},
		{"claude 2.0.0", "2.0.0"},
		{"v2.5.1", "2.5.1"},
	}
	for _, tc := range accepted {
		version, err := claude.ParseVersion(tc.banner)
		if err != nil {
			t.Errorf("ParseVersion(%q) error = %v", tc.banner, err)
			continue
		}
		if version.String() != tc.want {
			t.Errorf("ParseVersion(%q) = %q, want %q", tc.banner, version, tc.want)
		}
	}

	rejected := []string{"", "   ", "unknown", "2.1", "Claude Code"}
	for _, banner := range rejected {
		if _, err := claude.ParseVersion(banner); !errors.Is(err, claude.ErrUnrecognizedVersion) {
			t.Errorf("ParseVersion(%q) error = %v, want ErrUnrecognizedVersion", banner, err)
		}
	}
}

// TestOldCLIIsRefusedBeforeAnyCommand checks that a too-old CLI never gets an
// agents or resume invocation.
func TestOldCLIIsRefusedBeforeAnyCommand(t *testing.T) {
	t.Parallel()

	exec := &fakeCLI{t: t, version: "1.9.0\n", agents: "[]"}
	provider := newProvider(t, exec, t.TempDir())
	if _, err := provider.Discover(context.Background(), claude.DiscoverFilter{}); !errors.Is(err, claude.ErrUnsupportedVersion) {
		t.Fatalf("Discover() error = %v, want ErrUnsupportedVersion", err)
	}
	for _, argv := range exec.seen {
		if len(argv) > 1 && (argv[1] == "agents" || argv[1] == "-p") {
			t.Fatalf("an unsupported CLI was invoked with %v", argv)
		}
	}
}

// TestDiscoveryUsesOnlyDocumentedFlags asserts the discovery argv.
func TestDiscoveryUsesOnlyDocumentedFlags(t *testing.T) {
	t.Parallel()

	exec := &fakeCLI{t: t, version: "2.1.223 (Claude Code)", agents: "[]"}
	provider := newProvider(t, exec, t.TempDir())
	if _, err := provider.Discover(context.Background(), claude.DiscoverFilter{UnderCWD: "/w"}); err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	all := boolPointer(true)
	if _, err := provider.Discover(context.Background(), claude.DiscoverFilter{All: all, UnderCWD: "/w"}); err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	var agentsCalls [][]string
	for _, argv := range exec.seen {
		if len(argv) > 1 && argv[1] == "agents" {
			agentsCalls = append(agentsCalls, argv)
		}
	}
	if len(agentsCalls) != 2 {
		t.Fatalf("agents calls = %d, want 2", len(agentsCalls))
	}
	if got := strings.Join(agentsCalls[0], " "); got != "claude agents --json --cwd /w" {
		t.Errorf("first agents argv = %q", got)
	}
	if got := strings.Join(agentsCalls[1], " "); got != "claude agents --json --all --cwd /w" {
		t.Errorf("second agents argv = %q", got)
	}
}

// TestTranscriptDiscoveryWithoutTheCLI proves the post-limit case: the process is
// gone, the CLI lists nothing, and the session must still be found and resumable.
func TestTranscriptDiscoveryWithoutTheCLI(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	projects := filepath.Join(home, "projects")
	dir := filepath.Join(projects, claude.SlugifyCWD("/root/example"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	lines := `{"type":"user","sessionId":"` + sessionC + `","cwd":"/root/example","version":"2.1.223","gitBranch":"main","timestamp":"2026-06-01T09:00:00Z","message":{"role":"user","content":"do the thing"}}` + "\n" +
		`{"type":"assistant","sessionId":"` + sessionC + `","cwd":"/root/example","timestamp":"2026-06-01T09:01:00Z","message":{"role":"assistant","content":[{"type":"text","text":"You've hit your 5-hour limit"}]}}` + "\n"
	path := filepath.Join(dir, sessionC+".jsonl")
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	// session-env alone must also be enough.
	if err := os.MkdirAll(filepath.Join(home, "session-env", sessionB), 0o700); err != nil {
		t.Fatalf("mkdir env: %v", err)
	}

	exec := &fakeCLI{t: t, version: "2.1.223 (Claude Code)", agents: "[]"}
	provider := newProvider(t, exec, home)

	sessions, err := provider.Discover(context.Background(), claude.DiscoverFilter{})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	found := map[string]claude.Session{}
	for _, session := range sessions {
		found[session.ID] = session
	}
	rec, ok := found[sessionC]
	if !ok {
		t.Fatalf("transcript-only session missing: %+v", sessions)
	}
	if !rec.Recoverable || rec.TranscriptPath != path || rec.ProjectPath != "/root/example" {
		t.Errorf("transcript-only session = %+v", rec)
	}
	if rec.TranscriptSize == 0 || rec.TranscriptLines != 2 {
		t.Errorf("transcript metrics = %d bytes, %d lines", rec.TranscriptSize, rec.TranscriptLines)
	}
	if _, ok := found[sessionB]; !ok {
		t.Errorf("session-env-only session missing: %+v", sessions)
	}

	status, err := provider.GetSessionStatus(context.Background(), sessionC)
	if err != nil {
		t.Fatalf("GetSessionStatus() error = %v", err)
	}
	if status.Running {
		t.Error("Running = true although no process exists")
	}
	if status.Transcript.LastAssistantText != "You've hit your 5-hour limit" {
		t.Errorf("LastAssistantText = %q", status.Transcript.LastAssistantText)
	}
}

// TestTranscriptScanIsBounded checks that a long transcript does not blow up the
// scan and that the newest turn is still reported.
func TestTranscriptScanIsBounded(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dir := filepath.Join(home, "projects", claude.SlugifyCWD("/w"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	var b strings.Builder
	for i := 0; i < 5000; i++ {
		entry := map[string]any{
			"type":      "assistant",
			"sessionId": sessionA,
			"cwd":       "/w",
			"timestamp": time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Second).Format(time.RFC3339),
			"message": map[string]any{
				"role":    "assistant",
				"content": []any{map[string]any{"type": "text", "text": "turn " + strings.Repeat("z", 40)}},
			},
		}
		encoded, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		b.Write(encoded)
		b.WriteByte('\n')
	}
	path := filepath.Join(dir, sessionA+".jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	store, err := claude.NewStore(home)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	start := time.Now()
	info, err := store.ScanTranscript(path)
	if err != nil {
		t.Fatalf("ScanTranscript() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("scan took %s, suspiciously slow", elapsed)
	}
	if info.Lines != 5000 {
		t.Errorf("Lines = %d, want 5000", info.Lines)
	}
	if info.EntryCount >= 5000 {
		t.Errorf("EntryCount = %d: the scan must not decode the whole file", info.EntryCount)
	}
	if !strings.HasPrefix(info.LastAssistantText, "turn zzz") {
		t.Errorf("LastAssistantText = %q", info.LastAssistantText)
	}
	if !info.LastTimestamp.After(info.FirstTimestamp) {
		t.Errorf("timestamps = %v → %v", info.FirstTimestamp, info.LastTimestamp)
	}
}

// TestPartialTranscriptLineIsNotLost checks that a half-written trailing line is
// re-read on the next poll rather than skipped, which recovery relies on when it
// proves the conversation grew.
func TestPartialTranscriptLineIsNotLost(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dir := filepath.Join(home, "projects", claude.SlugifyCWD("/w"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, sessionA+".jsonl")
	first := `{"type":"user","sessionId":"` + sessionA + `","cwd":"/w","timestamp":"2026-06-01T10:00:00Z","message":{"role":"user","content":"hi"}}` + "\n"
	if err := os.WriteFile(path, []byte(first), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	store, err := claude.NewStore(home)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	_, offset, err := store.ReadEntriesSince(path, 0)
	if err != nil {
		t.Fatalf("ReadEntriesSince() error = %v", err)
	}

	// The CLI is mid-write: no trailing newline yet.
	// Note the deliberately unclosed string: the line is half written.
	partial := `{"type":"assistant","sessionId":"` + sessionA + `","cwd":"/w","timestamp":"2026-06-01T10:01:00Z","message":{"role":"assistant","content":[{"type":"text","text":"resum`

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := file.WriteString(partial); err != nil {
		t.Fatalf("write partial: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	entries, offsetAfterPartial, err := store.ReadEntriesSince(path, offset)
	if err != nil {
		t.Fatalf("ReadEntriesSince() error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %+v, want none for an incomplete line", entries)
	}
	if offsetAfterPartial != offset {
		t.Fatalf("offset advanced past an incomplete line: %d → %d", offset, offsetAfterPartial)
	}

	// The write completes: the line must now be delivered exactly once.
	file, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := file.WriteString(`ed"}]}}` + "\n"); err != nil {
		t.Fatalf("write rest: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	entries, _, err = store.ReadEntriesSince(path, offset)
	if err != nil {
		t.Fatalf("ReadEntriesSince() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Text != "resumed" {
		t.Fatalf("entries = %+v, want the completed turn", entries)
	}
}

// TestDiscoverFilterIsCrossPlatform guards against a separator assumption that
// would silently hide every session on Windows.
func TestDiscoverFilterIsCrossPlatform(t *testing.T) {
	t.Parallel()

	fake := claude.NewFakeProvider(
		claude.Session{ID: sessionA, ProjectPath: filepath.Join("root", "proj"), Recoverable: true},
		claude.Session{ID: sessionB, ProjectPath: filepath.Join("root", "proj", "sub"), Recoverable: true},
		claude.Session{ID: sessionC, ProjectPath: filepath.Join("root", "other"), Recoverable: true},
	)
	got, err := fake.Discover(context.Background(), claude.DiscoverFilter{UnderCWD: filepath.Join("root", "proj")})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d sessions, want 2: %+v", len(got), got)
	}
	for _, session := range got {
		if session.ID == sessionC {
			t.Errorf("sibling directory leaked into the result: %+v", session)
		}
	}
}

func boolPointer(b bool) *bool { return &b }
