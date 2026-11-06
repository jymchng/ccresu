package claude

import (
	"context"
	"errors"
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
		{name: "verified banner", input: testVersionOutput, want: "2.1.223", supports: true},
		{name: "v prefix", input: "v2.0.1", want: "2.0.1", supports: true},
		{name: "program prefix", input: "claude 2.2.0", want: "2.2.0", supports: true},
		{name: "prerelease", input: "2.1.0-beta.2 (Claude Code)", want: "2.1.0-beta.2", pre: "beta.2", supports: true},
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
	if first.String() != "2.1.223" {
		t.Errorf("Version() = %s, want 2.1.223", first)
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
		if !errors.Is(err, ErrCLIUnavailable) {
			t.Fatalf("error = %v, want ErrCLIUnavailable", err)
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

func TestResumeArgsExact(t *testing.T) {
	t.Parallel()

	got := strings.Join(ResumeArgs(testSessionID, "continue"), " ")
	want := "-p --resume " + testSessionID + " --output-format json continue"
	if got != want {
		t.Fatalf("ResumeArgs() = %q, want %q", got, want)
	}
}

// The resume vector must stay headless. Claude Code refuses `--bg` together with
// the `-p` in it, and the form that would satisfy `--bg` drops
// `--output-format json` -- no result, no cost, and a *new* session id instead of
// the resumed one. A guard here keeps that decision from being mistaken for an
// omission.
func TestResumeArgsNeverPassBackground(t *testing.T) {
	t.Parallel()

	args := ResumeArgs(testSessionID, "continue")
	if args[0] != "-p" {
		t.Fatalf("ResumeArgs()[0] = %q, want -p", args[0])
	}
	for _, arg := range args {
		switch arg {
		case "--bg", "--background", "-bg":
			t.Fatalf("ResumeArgs() = %v; a resume never asks for a background agent", args)
		}
	}
}

func TestCLIResumeSuccess(t *testing.T) {
	t.Parallel()

	cli, execer := newTestCLI(t,
		Output{Stdout: []byte(testVersionOutput)},
		Output{Stdout: []byte(testResumeJSON)},
	)
	result, err := cli.Resume(context.Background(), testSessionID, "keep going")
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if !result.Verified {
		t.Error("Verified = false, want true")
	}
	if result.SessionID != testSessionID {
		t.Errorf("SessionID = %q, want %q", result.SessionID, testSessionID)
	}
	if result.Result != "continued the goal" || result.Subtype != "success" || result.NumTurns != 2 {
		t.Errorf("unexpected result: %+v", result)
	}
	if result.DurationMS != 1234 || result.TotalCostUSD == 0 {
		t.Errorf("metrics not mapped: %+v", result)
	}
	if result.Model != "claude-sonnet-4-5" {
		t.Errorf("Model = %q, want claude-sonnet-4-5", result.Model)
	}

	calls := execer.Calls()
	last := calls[len(calls)-1]
	if last.Name != "claude" {
		t.Errorf("command name = %q, want claude", last.Name)
	}
	want := []string{"-p", "--resume", testSessionID, "--output-format", "json", "keep going"}
	if strings.Join(last.Args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("argv = %v, want %v", last.Args, want)
	}
	if result.Command[0] != "claude" || strings.Join(result.Command[1:], " ") != strings.Join(want, " ") {
		t.Errorf("recorded command = %v", result.Command)
	}
	// The message must be the final argv element and a single one, so no shell
	// ever sees it and no flag parser can mistake it for a flag.
	message := last.Args[len(last.Args)-1]
	if message != "keep going" {
		t.Errorf("message argv = %q, want %q", message, "keep going")
	}
}

func TestCLIResumeRejectsBadInput(t *testing.T) {
	t.Parallel()

	cli, execer := newTestCLI(t)
	cases := []struct {
		name      string
		sessionID string
		message   string
		wantErr   error
	}{
		{"flag injection", "--resume", "go", ErrInvalidSessionID},
		{"path traversal", "../session", "go", ErrInvalidSessionID},
		{"empty message", testSessionID, "   ", ErrEmptyMessage},
		{"long message", testSessionID, strings.Repeat("x", MaxMessageBytes+1), ErrMessageTooLong},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := cli.Resume(context.Background(), tc.sessionID, tc.message); !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
		})
	}
	if got := len(execer.Calls()); got != 0 {
		t.Fatalf("execer calls = %d, want 0: validation must run before any command", got)
	}
}

func TestCLIResumeFailures(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		output  Output
		runErr  error
		wantErr error
	}{
		{
			name:    "is_error result",
			output:  Output{Stdout: []byte(`{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"` + testSessionID + `","result":"limit still active"}`)},
			wantErr: ErrResumeFailed,
		},
		{
			name:    "different session id",
			output:  Output{Stdout: []byte(`{"type":"result","is_error":false,"session_id":"` + testOtherSessionID + `","result":"ok"}`)},
			wantErr: ErrSessionNotPreserved,
		},
		{
			name:    "no session id",
			output:  Output{Stdout: []byte(`{"type":"result","is_error":false,"result":"ok"}`)},
			wantErr: ErrNoSessionIDReported,
		},
		{
			name:    "not json",
			output:  Output{Stdout: []byte("Error: still limited")},
			wantErr: ErrMalformedOutput,
		},
		{
			name:    "non zero exit with json noise",
			output:  Output{Stdout: []byte("{}"), Stderr: []byte("boom"), ExitCode: 3},
			runErr:  errors.New("exit status 3"),
			wantErr: ErrCLIUnavailable,
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
			if _, err := cli.Resume(context.Background(), testSessionID, "go"); !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
		})
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
	if _, err := cli.Resume(ctx, testSessionID, "go"); !errors.Is(err, context.Canceled) {
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

func TestParseResumeJSON(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		input     string
		wantErr   bool
		wantID    string
		wantError bool
	}{
		{name: "verified object", input: testResumeJSON, wantID: testSessionID},
		{
			name:   "stream json takes the result object",
			input:  `{"type":"system","subtype":"init","session_id":"` + testSessionID + `"}` + "\n" + `{"type":"assistant","message":{"content":"hi"}}` + "\n" + testResumeJSON,
			wantID: testSessionID,
		},
		{
			name:   "array takes the result object",
			input:  `[{"type":"assistant","message":{"content":"hi"}},` + testResumeJSON + `]`,
			wantID: testSessionID,
		},
		{
			name:      "error result",
			input:     `{"type":"result","is_error":true,"result":"limit still active"}`,
			wantError: true,
		},
		{
			name:   "camelCase session id",
			input:  `{"type":"result","sessionId":"` + testSessionID + `","result":"ok"}`,
			wantID: testSessionID,
		},
		{
			name:   "string booleans and numbers",
			input:  `{"type":"result","is_error":"false","session_id":"` + testSessionID + `","num_turns":"3","duration_ms":"99","total_cost_usd":"0.5"}`,
			wantID: testSessionID,
		},
		{name: "empty", input: "  ", wantErr: true},
		{name: "not json", input: "plain text", wantErr: true},
		{name: "useless object", input: `{"foo":"bar"}`, wantErr: true},
		{name: "broken array", input: `[`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := ParseResumeJSON([]byte(tc.input))
			if tc.wantErr {
				if !errors.Is(err, ErrMalformedOutput) {
					t.Fatalf("error = %v, want ErrMalformedOutput", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantID != "" && out.SessionID != tc.wantID {
				t.Errorf("SessionID = %q, want %q", out.SessionID, tc.wantID)
			}
			if out.IsError != tc.wantError {
				t.Errorf("IsError = %v, want %v", out.IsError, tc.wantError)
			}
			if tc.wantError && out.Subtype == "" {
				t.Error("an error result should get a subtype")
			}
		})
	}
}

func TestParseResumeJSONMetrics(t *testing.T) {
	t.Parallel()

	out, err := ParseResumeJSON([]byte(testResumeJSON))
	if err != nil {
		t.Fatalf("ParseResumeJSON() error = %v", err)
	}
	if out.NumTurns != 2 || out.DurationMS != 1234 {
		t.Errorf("metrics = %+v", out)
	}
	if out.TotalCostUSD < 0.012 || out.TotalCostUSD > 0.013 {
		t.Errorf("cost = %v", out.TotalCostUSD)
	}
	if out.Type != "result" || out.Model != "claude-sonnet-4-5" {
		t.Errorf("type/model = %q/%q", out.Type, out.Model)
	}
	if len(out.Raw) == 0 {
		t.Error("Raw was not preserved")
	}
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
