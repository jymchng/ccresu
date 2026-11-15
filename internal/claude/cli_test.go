package claude

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestParseVersion(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		input    string
		want     string
		pre      string
		wantErr  bool
		supports bool
	}{
		{name: "verified banner", input: testVersionOutput, want: "2.1.257", supports: true},
		{name: "v prefix", input: "v2.1.257", want: "2.1.257", supports: true},
		{name: "program prefix", input: "claude 2.2.0", want: "2.2.0", supports: true},
		{name: "prerelease", input: "2.2.0-beta.2 (Claude Code)", want: "2.2.0-beta.2", pre: "beta.2", supports: true},
		{name: "too old", input: "1.9.9 (Claude Code)", want: "1.9.9", supports: false},
		{name: "empty", input: "  ", wantErr: true},
		{name: "garbage", input: "not a version", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v, err := ParseVersion(tc.input)
			if tc.wantErr {
				if !errors.Is(err, ErrUnrecognizedVersion) {
					t.Fatalf("error = %v, want ErrUnrecognizedVersion", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseVersion(%q) error = %v", tc.input, err)
			}
			if got := v.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
			if v.PreRelease != tc.pre {
				t.Errorf("PreRelease = %q, want %q", v.PreRelease, tc.pre)
			}
			if v.Supported() != tc.supports {
				t.Errorf("Supported() = %v, want %v", v.Supported(), tc.supports)
			}
			if tc.supports && v.checkSupported() != nil {
				t.Errorf("checkSupported() = %v, want nil", v.checkSupported())
			}
			if !tc.supports {
				err := v.checkSupported()
				if !errors.Is(err, ErrUnsupportedVersion) || !strings.Contains(err.Error(), MinimumVersion.String()) {
					t.Errorf("checkSupported() = %v, want ErrUnsupportedVersion naming the minimum", err)
				}
			}
		})
	}
}

func TestVersionCompare(t *testing.T) {
	t.Parallel()

	build := func(major, minor, patch int) Version {
		return Version{Major: major, Minor: minor, Patch: patch, Parsed: true}
	}
	cases := []struct {
		a, b Version
		want int
	}{
		{build(2, 1, 0), build(2, 1, 0), 0},
		{build(2, 0, 1), build(2, 1, 0), -1},
		{build(3, 0, 0), build(2, 9, 9), 1},
		{build(2, 1, 0), build(2, 1, 1), -1},
		{Version{Major: 2, Parsed: true, PreRelease: "rc1"}, build(2, 0, 0), -1},
		{build(2, 0, 0), Version{Major: 2, Parsed: true, PreRelease: "rc1"}, 1},
		{
			Version{Major: 2, Parsed: true, PreRelease: "alpha"},
			Version{Major: 2, Parsed: true, PreRelease: "beta"},
			-1,
		},
	}
	for _, tc := range cases {
		if got := tc.a.Compare(tc.b); got != tc.want {
			t.Errorf("%s.Compare(%s) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
		if got := tc.b.Compare(tc.a); got != -tc.want {
			t.Errorf("%s.Compare(%s) = %d, want %d", tc.b, tc.a, got, -tc.want)
		}
	}

	unparsed := Version{Raw: "garbage"}
	if unparsed.String() != "garbage" {
		t.Errorf("String() = %q, want the raw text", unparsed.String())
	}
	if unparsed.Supported() {
		t.Error("an unparsed version must not be supported")
	}
	if err := unparsed.checkSupported(); !errors.Is(err, ErrUnrecognizedVersion) {
		t.Errorf("checkSupported() = %v, want ErrUnrecognizedVersion", err)
	}
}

func TestVersionProbeAndCaching(t *testing.T) {
	t.Parallel()

	cli, execer := newTestCLI(t,
		Output{Stdout: []byte(testVersionOutput)},
		Output{Stdout: []byte(testVersionOutput)},
	)
	first, err := cli.Version(context.Background())
	if err != nil {
		t.Fatalf("Version() error = %v", err)
	}
	if first.String() != "2.1.257" {
		t.Errorf("Version() = %s, want 2.1.257", first)
	}
	if _, err := cli.Version(context.Background()); err != nil {
		t.Fatalf("cached Version() error = %v", err)
	}
	if got := len(execer.Calls()); got != 1 {
		t.Fatalf("execer calls = %d, want 1 (version must be cached)", got)
	}
	call := execer.Calls()[0]
	if call.Name != "claude" || strings.Join(call.Args, " ") != "--version" {
		t.Fatalf("unexpected version command: %+v", call)
	}

	report, err := cli.ProbeVersion(context.Background())
	if err != nil {
		t.Fatalf("ProbeVersion() error = %v", err)
	}
	if report.ExitCode != 0 || strings.Join(report.Command, " ") != "claude --version" {
		t.Errorf("unexpected report: %+v", report)
	}

	cli.ResetVersionCache()
	if _, err := cli.Version(context.Background()); err == nil {
		t.Fatal("expected the scripted execer to run out of responses")
	}
}

func TestVersionProbeFailures(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		execer  *FakeExecer
		wantErr error
	}{
		{
			name: "binary missing",
			execer: NewFakeExecer(func(Call) (Output, error) {
				return Output{ExitCode: -1}, errors.New("exec: claude: executable file not found")
			}),
			wantErr: ErrCLIUnavailable,
		},
		{
			name: "timeout",
			execer: NewFakeExecer(func(Call) (Output, error) {
				return Output{ExitCode: -1}, context.DeadlineExceeded
			}),
			wantErr: ErrCLIUnavailable,
		},
		{
			name: "garbage version",
			execer: NewFakeExecer(func(Call) (Output, error) {
				return Output{Stdout: []byte("who knows")}, nil
			}),
			wantErr: ErrUnrecognizedVersion,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cli := NewCLI(CLIOptions{Executable: "claude", Execer: tc.execer})
			report, err := cli.ProbeVersion(context.Background())
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if report.ExitCode != -1 && tc.name != "garbage version" {
				t.Errorf("ExitCode = %d, want -1", report.ExitCode)
			}
		})
	}
}

func TestVersionProbeNonZeroExitIsNotUnavailable(t *testing.T) {
	t.Parallel()

	// `claude --version` that ran and exited non-zero is a crash with output,
	// not a missing binary; doctor must be able to show what it said.
	execer := NewFakeExecer(func(Call) (Output, error) {
		return Output{Stderr: []byte("Error: broken installation"), ExitCode: 1}, errors.New("exit status 1")
	})
	cli := NewCLI(CLIOptions{Executable: "claude", Execer: execer})
	report, err := cli.ProbeVersion(context.Background())
	if err == nil {
		t.Fatal("ProbeVersion() error = nil, want a failure")
	}
	if errors.Is(err, ErrCLIUnavailable) {
		t.Errorf("error = %v, must not claim the executable was unavailable", err)
	}
	var cliErr *CLIError
	if !errors.As(err, &cliErr) {
		t.Fatalf("error = %v, want *CLIError", err)
	}
	if cliErr.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", cliErr.ExitCode)
	}
	if !strings.Contains(err.Error(), "broken installation") {
		t.Errorf("error = %v, want the captured stderr", err)
	}
	if report.ExitCode != 1 || report.Stderr != "Error: broken installation" {
		t.Errorf("report = %+v, want the probe outcome recorded", report)
	}
}

func TestAgentsQueryArgs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		query AgentsQuery
		want  string
	}{
		{AgentsQuery{}, "agents --json"},
		{AgentsQuery{All: true}, "agents --json --all"},
		{AgentsQuery{CWD: "/w"}, "agents --json --cwd /w"},
		{AgentsQuery{All: true, CWD: " /w "}, "agents --json --all --cwd /w"},
	}
	for _, tc := range cases {
		if got := strings.Join(tc.query.Args(), " "); got != tc.want {
			t.Errorf("Args() = %q, want %q", got, tc.want)
		}
	}
}

func TestCLIAgents(t *testing.T) {
	t.Parallel()

	cli, execer := newTestCLI(t,
		Output{Stdout: []byte(testVersionOutput)},
		Output{Stdout: []byte(testAgentsJSON)},
	)
	entries, err := cli.Agents(context.Background(), AgentsQuery{All: true, CWD: "/root/project"})
	if err != nil {
		t.Fatalf("Agents() error = %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("entries = %d, want 4", len(entries))
	}
	calls := execer.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	want := "agents --json --all --cwd /root/project"
	if got := strings.Join(calls[1].Args, " "); got != want {
		t.Errorf("agents args = %q, want %q", got, want)
	}
}

func TestCLIAgentsErrors(t *testing.T) {
	t.Parallel()

	t.Run("non zero exit", func(t *testing.T) {
		t.Parallel()
		cli, _ := newTestCLI(t,
			Output{Stdout: []byte(testVersionOutput)},
			Output{ExitCode: 2, Stderr: []byte("Error: not logged in\nsecond line")},
		)
		_, err := cli.Agents(context.Background(), AgentsQuery{})
		// The command ran and exited non-zero, so the executable was available:
		// this is a crash with output, not a missing binary.
		var cliErr *CLIError
		if !errors.As(err, &cliErr) {
			t.Fatalf("error = %v, want *CLIError", err)
		}
		if cliErr.ExitCode != 2 {
			t.Errorf("ExitCode = %d, want 2", cliErr.ExitCode)
		}
		if !strings.Contains(string(cliErr.Stderr), "not logged in") {
			t.Errorf("Stderr = %q, want the captured detail", cliErr.Stderr)
		}
		if errors.Is(err, ErrCLIUnavailable) {
			t.Errorf("error = %v, must not be ErrCLIUnavailable: the binary ran", err)
		}
		if errors.Is(err, ErrUsageLimitInForce) {
			t.Errorf("error = %v, must not be a usage-limit refusal", err)
		}
		if !strings.Contains(err.Error(), "exit 2") || !strings.Contains(err.Error(), "not logged in") {
			t.Errorf("error = %v, want exit code and stderr detail", err)
		}
	})

	t.Run("old version", func(t *testing.T) {
		t.Parallel()
		cli, _ := newTestCLI(t, Output{Stdout: []byte("1.0.0\n")})
		if _, err := cli.Agents(context.Background(), AgentsQuery{}); !errors.Is(err, ErrUnsupportedVersion) {
			t.Fatalf("error = %v, want ErrUnsupportedVersion", err)
		}
	})

	t.Run("malformed body", func(t *testing.T) {
		t.Parallel()
		cli, _ := newTestCLI(t,
			Output{Stdout: []byte(testVersionOutput)},
			Output{Stdout: []byte("<html>")},
		)
		if _, err := cli.Agents(context.Background(), AgentsQuery{}); !errors.Is(err, ErrMalformedOutput) {
			t.Fatalf("error = %v, want ErrMalformedOutput", err)
		}
	})
}

func TestHandoffArgsExact(t *testing.T) {
	t.Parallel()

	got := strings.Join(HandoffArgs(testSessionID, "continue"), " ")
	want := "--bg --resume " + testSessionID + " --name ccresu-3f1e2c7a continue"
	if got != want {
		t.Fatalf("HandoffArgs() = %q, want %q", got, want)
	}
}

// TestNoRecoveryArgumentCarriesPrintMode is the guard on R1 and R12: print mode
// is retired, not kept beside --bg. The two cannot be combined, so a vector
// carrying both would be rejected before a session existed.
func TestNoRecoveryArgumentCarriesPrintMode(t *testing.T) {
	t.Parallel()

	vectors := map[string][]string{
		"handoff": HandoffArgs(testSessionID, "keep going"),
		"stop":    StopArgs("837eaaea"),
	}
	for name, argv := range vectors {
		for _, arg := range argv {
			switch arg {
			case "-p", "--print", "--output-format":
				t.Errorf("%s argv = %q, want no print-mode flag", name, argv)
			}
		}
		if len(argv) > 1 && argv[0] == "--" {
			t.Errorf("%s argv = %q, want no bare -- separator first", name, argv)
		}
	}
}

func TestCLIHandoffSuccess(t *testing.T) {
	t.Parallel()

	cli, execer := newTestCLI(t,
		Output{Stdout: []byte(testVersionOutput)},
		Output{Stdout: []byte(testHandoffBlock)},
	)
	result, err := cli.Resume(context.Background(), testSessionID, "keep going", testSessionDir)
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if !result.Backgrounded {
		t.Error("Backgrounded = false, want true")
	}
	if result.Verified {
		t.Error("Verified = true, want false: a handoff cannot prove the conversation continued")
	}
	if result.StateID != "3f1e2c7a" {
		t.Errorf("StateID = %q, want 3f1e2c7a", result.StateID)
	}
	if result.Name != "ccresu-3f1e2c7a" {
		t.Errorf("Name = %q, want ccresu-3f1e2c7a", result.Name)
	}
	if result.SessionID != testSessionID {
		t.Errorf("SessionID = %q, want %q", result.SessionID, testSessionID)
	}
	if result.CWD != testSessionDir {
		t.Errorf("CWD = %q, want %q", result.CWD, testSessionDir)
	}
	if result.Handoff.Raw == "" {
		t.Error("Handoff.Raw = \"\", want the output the block was parsed from")
	}

	calls := execer.Calls()
	last := calls[len(calls)-1]
	if last.Name != "claude" {
		t.Errorf("command name = %q, want claude", last.Name)
	}
	want := HandoffArgs(testSessionID, "keep going")
	if strings.Join(last.Args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("argv = %v, want %v", last.Args, want)
	}
	if result.Command[0] != "claude" || strings.Join(result.Command[1:], " ") != strings.Join(want, " ") {
		t.Errorf("recorded command = %v", result.Command)
	}
	// The message must be the final argv element and a single one, so no shell
	// ever sees it and no flag parser can mistake it for a flag.
	if message := last.Args[len(last.Args)-1]; message != "keep going" {
		t.Errorf("message argv = %q, want %q", message, "keep going")
	}
}

// TestTheHandoffRunsInTheSessionsDirectory is R8: the cwd is chosen, never
// inherited. A handoff issued from the daemon's cwd resumes the wrong tree.
func TestTheHandoffRunsInTheSessionsDirectory(t *testing.T) {
	t.Parallel()

	cli, execer := newTestCLI(t,
		Output{Stdout: []byte(testVersionOutput)},
		Output{Stdout: []byte(testHandoffBlock)},
	)
	if _, err := cli.Resume(context.Background(), testSessionID, "go", "/w/session-worktree"); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	calls := execer.Calls()
	last := calls[len(calls)-1]
	if last.Options.Dir != "/w/session-worktree" {
		t.Errorf("child dir = %q, want /w/session-worktree", last.Options.Dir)
	}
	if wd, err := os.Getwd(); err == nil && last.Options.Dir == wd {
		t.Errorf("child dir = %q, which is the test process's own directory", last.Options.Dir)
	}
	// PRD 6 R2: the query commands pass ccresu's own directory explicitly rather
	// than inheriting it, so no invocation depends on how the daemon was started.
	if calls[0].Options.Dir == "" {
		t.Error("version probe dir is empty, want ccresu's own directory passed explicitly")
	}
	if calls[0].Options.Dir == "/w/session-worktree" {
		t.Error("version probe ran in the session's directory, want ccresu's own")
	}
}

// TestACopiedContinuationIsRecorded is R4: a copy is an expected outcome, not a
// failure, and the copy becomes the conversation the recovery is reported
// against.
func TestACopiedContinuationIsRecorded(t *testing.T) {
	t.Parallel()

	output := "backgrounded \u00b7 3f1e2c7a \u00b7 ccresu-3f1e2c7a\n" +
		"note: started a copy of the conversation in session " + testOtherSessionID + "\n"
	cli, _ := newTestCLI(t,
		Output{Stdout: []byte(testVersionOutput)},
		Output{Stdout: []byte(output)},
	)
	result, err := cli.Resume(context.Background(), testSessionID, "go", testSessionDir)
	if err != nil {
		t.Fatalf("Resume() error = %v, want nil: a copy is not a failure", err)
	}
	if !result.Copied {
		t.Error("Copied = false, want true")
	}
	if result.CopySessionID != testOtherSessionID {
		t.Errorf("CopySessionID = %q, want %q", result.CopySessionID, testOtherSessionID)
	}
	if result.SessionID != testOtherSessionID {
		t.Errorf("SessionID = %q, want the copy %q: the recovery is reported against it", result.SessionID, testOtherSessionID)
	}
	if result.Note == "" {
		t.Error("Note = \"\", want the CLI's explanation")
	}
	if !result.Backgrounded {
		t.Error("Backgrounded = false, want true")
	}
}

// TestAStartedSupervisorIsRecognised covers the missing-supervisor case: the CLI
// prints `Starting background service...` above the block when it had to start
// one first, and parsing must survive that line.
func TestAStartedSupervisorIsRecognised(t *testing.T) {
	t.Parallel()

	output := "Starting background service\u2026\n" + testHandoffBlock
	cli, _ := newTestCLI(t,
		Output{Stdout: []byte(testVersionOutput)},
		Output{Stdout: []byte(output)},
	)
	result, err := cli.Resume(context.Background(), testSessionID, "go", testSessionDir)
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if !result.SupervisorStarted {
		t.Error("SupervisorStarted = false, want true")
	}
	if !result.Backgrounded || result.StateID != "3f1e2c7a" {
		t.Errorf("the prefix line broke parsing: %+v", result.Handoff)
	}
}

func TestCLIResumeRejectsBadInput(t *testing.T) {
	t.Parallel()

	cli, execer := newTestCLI(t)
	cases := []struct {
		name      string
		sessionID string
		message   string
		dir       string
		wantErr   error
	}{
		{"flag injection", "--resume", "go", testSessionDir, ErrInvalidSessionID},
		{"path traversal", "../session", "go", testSessionDir, ErrInvalidSessionID},
		{"empty message", testSessionID, "   ", testSessionDir, ErrEmptyMessage},
		{"long message", testSessionID, strings.Repeat("x", MaxMessageBytes+1), testSessionDir, ErrMessageTooLong},
		{"no directory", testSessionID, "go", "  ", ErrHandoffDirectoryMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := cli.Resume(context.Background(), tc.sessionID, tc.message, tc.dir); !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
		})
	}
	if got := len(execer.Calls()); got != 0 {
		t.Fatalf("execer calls = %d, want 0: validation must run before any command", got)
	}
}

func TestCLIHandoffFailures(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		output  Output
		runErr  error
		wantErr error
		// wantNoSentinel asserts a *CLIError with no sentinel: the command ran
		// and crashed, so it is neither a missing binary nor a named refusal.
		wantNoSentinel bool
	}{
		{
			name:    "no backgrounded block",
			output:  Output{Stdout: []byte("resumed in place\n")},
			wantErr: ErrHandoffNotAcknowledged,
		},
		{
			name:    "workspace not trusted",
			output:  Output{Stdout: []byte("Workspace not trusted. Run claude in this directory first.\n"), ExitCode: 1},
			wantErr: ErrWorkspaceNotTrusted,
		},
		{
			name:           "non zero exit",
			output:         Output{Stdout: []byte("boom"), Stderr: []byte("boom"), ExitCode: 3},
			runErr:         errors.New("exit status 3"),
			wantNoSentinel: true,
		},
		{
			name:    "timeout",
			output:  Output{ExitCode: -1},
			runErr:  context.DeadlineExceeded,
			wantErr: ErrCLIUnavailable,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := 0
			execer := NewFakeExecer(func(Call) (Output, error) {
				call++
				if call == 1 {
					return Output{Stdout: []byte(testVersionOutput)}, nil
				}
				return tc.output, tc.runErr
			})
			cli := NewCLI(CLIOptions{Executable: "claude", Execer: execer})
			_, err := cli.Resume(context.Background(), testSessionID, "go", testSessionDir)
			if tc.wantNoSentinel {
				var cliErr *CLIError
				if !errors.As(err, &cliErr) {
					t.Fatalf("error = %v, want *CLIError", err)
				}
				if cliErr.ExitCode != 3 {
					t.Errorf("ExitCode = %d, want 3", cliErr.ExitCode)
				}
				if errors.Is(err, ErrCLIUnavailable) || errors.Is(err, ErrUsageLimitInForce) {
					t.Errorf("error = %v, want no sentinel: the binary ran and crashed", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestPreHandoffCLIIsANamedError is R12's blunt edge: there is no fallback, so a
// CLI below 2.1.257 is a named, explainable refusal rather than a mystery
// failure, and it is raised before the handoff is attempted.
func TestPreHandoffCLIIsANamedError(t *testing.T) {
	t.Parallel()

	cli, execer := newTestCLI(t, Output{Stdout: []byte("2.1.223 (Claude Code)\n")})
	_, err := cli.Resume(context.Background(), testSessionID, "go", testSessionDir)
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("error = %v, want ErrUnsupportedVersion", err)
	}
	var requirement *VersionRequirementError
	if !errors.As(err, &requirement) {
		t.Fatalf("error = %v, want *VersionRequirementError: a generic failure is not explainable", err)
	}
	if requirement.Need.String() != "2.1.257" {
		t.Errorf("Need = %s, want 2.1.257", requirement.Need)
	}
	if requirement.Have.String() != "2.1.223" {
		t.Errorf("Have = %s, want 2.1.223", requirement.Have)
	}
	for _, want := range []string{"upgrade Claude Code", "v0.6.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
	if calls := execer.Calls(); len(calls) != 1 {
		t.Fatalf("execer calls = %d, want 1: only the version probe may run", len(calls))
	}
}

func TestCLIResumeContextCancelled(t *testing.T) {
	t.Parallel()

	cli, _ := newTestCLI(t, Output{Stdout: []byte(testVersionOutput)})
	if _, err := cli.Version(context.Background()); err != nil {
		t.Fatalf("Version() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cli.Resume(ctx, testSessionID, "go", testSessionDir); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestCLIRunAppliesTimeout(t *testing.T) {
	t.Parallel()

	execer := &deadlineRecorder{output: Output{Stdout: []byte(testVersionOutput)}}
	cli := NewCLI(CLIOptions{Executable: "claude", Execer: execer, QueryTimeout: time.Second})
	if _, err := cli.Version(context.Background()); err != nil {
		t.Fatalf("Version() error = %v", err)
	}
	if !execer.sawDeadline {
		t.Error("expected the CLI to apply a query timeout")
	}
	if execer.timeout <= 0 || execer.timeout > time.Second {
		t.Errorf("timeout = %v, want at most 1s", execer.timeout)
	}

	// A caller-supplied deadline must win over the internal one.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	execer2 := &deadlineRecorder{output: Output{Stdout: []byte(testVersionOutput)}}
	cli2 := NewCLI(CLIOptions{Executable: "claude", Execer: execer2, QueryTimeout: time.Minute})
	if _, err := cli2.Version(ctx); err != nil {
		t.Fatalf("Version() error = %v", err)
	}
	if !execer2.sawDeadline || execer2.timeout > 6*time.Second {
		t.Errorf("caller deadline ignored: deadline=%v timeout=%v", execer2.sawDeadline, execer2.timeout)
	}
}

// TestCLIHandoffTimeoutBoundsTheHandoffInvocation proves the other half of
// recovery.startup_timeout's wiring: the value the composition root puts in
// claude.Options.HandoffTimeout is the deadline the `claude --bg --resume`
// invocation actually runs under, distinct from the query bound. The recorder
// sees the last invocation, which is the handoff, so a 47s handoff bound beside
// a 5s query bound cannot be confused for one another.
func TestCLIHandoffTimeoutBoundsTheHandoffInvocation(t *testing.T) {
	t.Parallel()

	execer := &deadlineRecorder{output: Output{Stdout: []byte(testVersionOutput + testHandoffBlock)}}
	cli := NewCLI(CLIOptions{
		Executable:     "claude",
		Execer:         execer,
		QueryTimeout:   5 * time.Second,
		HandoffTimeout: 47 * time.Second,
	})
	if _, err := cli.Resume(context.Background(), testSessionID, "continue", testSessionDir); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if !execer.sawDeadline {
		t.Fatal("the handoff ran with no deadline, so nothing bounds it")
	}
	if execer.timeout <= 30*time.Second || execer.timeout > 47*time.Second {
		t.Fatalf("handoff deadline = %s, want at most 47s and clearly the handoff bound rather than the 5s query one", execer.timeout)
	}
}

// deadlineRecorder records the deadline the CLI applied.
type deadlineRecorder struct {
	output      Output
	sawDeadline bool
	timeout     time.Duration
}

func (d *deadlineRecorder) Run(ctx context.Context, _ string, _ []string, _ RunOptions) (Output, error) {
	if deadline, ok := ctx.Deadline(); ok {
		d.sawDeadline = true
		d.timeout = time.Until(deadline)
	}
	return d.output, nil
}

func TestTail(t *testing.T) {
	t.Parallel()

	if got := tail("  ", 10); got != "" {
		t.Errorf("tail(blank) = %q, want empty", got)
	}
	if got := tail("line one\nline two", 100); got != "line one line two" {
		t.Errorf("tail() = %q", got)
	}
	long := strings.Repeat("a", 50)
	got := tail(long, 10)
	if !strings.HasPrefix(got, "…") || utf8.RuneCountInString(got) != 11 {
		t.Errorf("tail(long) = %q", got)
	}
}
