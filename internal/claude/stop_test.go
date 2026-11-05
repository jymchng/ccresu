package claude

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestStopArgsIsTheVerifiedVector(t *testing.T) {
	t.Parallel()

	got := StopArgs(testSessionID)
	want := []string{"stop", testSessionID}
	if len(got) != len(want) {
		t.Fatalf("StopArgs() = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("StopArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// A caller must not be able to corrupt the vector other callers build.
	got[0] = "mutated"
	if again := StopArgs(testSessionID); again[0] != "stop" {
		t.Errorf("StopArgs() shares state between calls: %q", again)
	}
	// The short id form the CLI prints in its own error must be rejected by
	// ValidateSessionID, so it can never reach the command line.
	if err := ValidateSessionID(testSessionID[:8]); !errors.Is(err, ErrInvalidSessionID) {
		t.Errorf("ValidateSessionID(short id) = %v, want ErrInvalidSessionID", err)
	}
}

func TestStopRejectsAnInvalidSessionIDBeforeExecuting(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"", "not-a-uuid", "--force", testSessionID + "x"} {
		execer := NewScriptedExecer(nil, nil, Output{}, errors.New("must not run"))
		cli := NewCLI(CLIOptions{Executable: "claude", Execer: execer})

		err := cli.Stop(context.Background(), id)
		if !errors.Is(err, ErrInvalidSessionID) {
			t.Errorf("Stop(%q) error = %v, want ErrInvalidSessionID", id, err)
		}
		if calls := execer.Calls(); len(calls) != 0 {
			t.Errorf("Stop(%q) ran %v, want no invocation", id, calls)
		}
	}
}

func TestStopRunsTheVerifiedCommand(t *testing.T) {
	t.Parallel()

	execer := NewScriptedExecer([]Output{{}}, nil, Output{}, errors.New("unexpected invocation"))
	cli := NewCLI(CLIOptions{Executable: "claude", Execer: execer})

	if err := cli.Stop(context.Background(), testSessionID); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	calls := execer.Calls()
	if len(calls) != 1 {
		t.Fatalf("Stop() made %d calls, want 1: %v", len(calls), calls)
	}
	call := calls[0]
	if call.Name != "claude" {
		t.Errorf("command = %q, want the configured executable", call.Name)
	}
	want := []string{"stop", testSessionID}
	if strings.Join(call.Args, " ") != strings.Join(want, " ") {
		t.Errorf("argv = %q, want %q", call.Args, want)
	}
	if call.Options.Stdin != nil {
		t.Error("stop must never be given stdin: no terminal is attached")
	}
}

func TestStopFailureIsDescribedAndTyped(t *testing.T) {
	t.Parallel()

	// The real CLI answers an unknown id with exit 0 and a message, but a
	// version that does not know `stop` exits non-zero: that has to surface as
	// a failure naming the exit code and the CLI's own words.
	execer := NewScriptedExecer([]Output{{
		Stderr:   []byte("error: unknown command 'stop'\n"),
		ExitCode: 2,
	}}, nil, Output{}, errors.New("unexpected invocation"))
	cli := NewCLI(CLIOptions{Executable: "claude", Execer: execer})

	err := cli.Stop(context.Background(), testSessionID)
	if !errors.Is(err, ErrStopFailed) {
		t.Fatalf("Stop() error = %v, want ErrStopFailed", err)
	}
	for _, want := range []string{"claude stop failed", "exit 2", "unknown command"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestStopReportsAnExecutorFailure(t *testing.T) {
	t.Parallel()

	execer := NewScriptedExecer([]Output{{ExitCode: -1}}, []error{errors.New("fork/exec: no such file")}, Output{}, nil)
	cli := NewCLI(CLIOptions{Executable: "claude", Execer: execer})

	err := cli.Stop(context.Background(), testSessionID)
	if !errors.Is(err, ErrStopFailed) {
		t.Fatalf("Stop() error = %v, want ErrStopFailed", err)
	}
	if !strings.Contains(err.Error(), "no such file") {
		t.Errorf("error %q does not carry the executor failure", err)
	}
}

// backgroundRefusal is the exact message Claude Code 2.1.223 produced in the
// field, including the short id, the backticks and the trailing remedy.
const backgroundRefusal = "Error: That session is running in the background (837eaaea). " +
	"Run `claude attach 837eaaea` to open it, or `claude stop 837eaaea` first to resume it here. " +
	"Add --fork-session to branch off a copy instead."

func TestIsSessionRunningInBackground(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil error", err: nil},
		{name: "bare refusal", err: errors.New(backgroundRefusal), want: true},
		{
			name: "wrapped the way the log showed it",
			err: errors.New("claude: claude executable unavailable: claude -p --resume failed (exit 1): " +
				backgroundRefusal + ": exit status 1"),
			want: true,
		},
		{
			name: "indented and wrapped across lines",
			err: errors.New("claude -p --resume failed (exit 1): Error: That session is\n" +
				"    running in the background (837eaaea).\n"),
			want: true,
		},
		{
			name: "ansi colour codes",
			err:  errors.New("\x1b[31mError: That session is running in the background\x1b[0m (837eaaea)"),
			want: true,
		},
		{name: "upper case", err: errors.New("THAT SESSION IS RUNNING IN THE BACKGROUND"), want: true},
		{name: "typed sentinel", err: ErrSessionRunningInBackground, want: true},
		{
			name: "wrapped typed sentinel",
			err:  errors.Join(errors.New("context"), ErrSessionRunningInBackground),
			want: true,
		},
		{name: "auth error", err: errors.New("invalid api key provided")},
		{name: "generic exit 1", err: errors.New("claude -p --resume failed (exit 1): unknown error")},
		{name: "network text", err: errors.New("connection reset by peer")},
		{name: "mentions background but not the refusal", err: errors.New("the job will keep running in the background")},
		{name: "empty error", err: errors.New("")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsSessionRunningInBackground(tc.err); got != tc.want {
				t.Errorf("IsSessionRunningInBackground(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestNormalizeMessage(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"case", "ABC Def", "abc def"},
		{"ansi", "\x1b[1mBold\x1b[0m text", "bold text"},
		{"newlines and indentation", "one\n\ttwo   three", "one two three"},
		{"already normalised", "abc def", "abc def"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := normalizeMessage(tc.in)
			if got != tc.want {
				t.Errorf("normalizeMessage(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if again := normalizeMessage(got); again != got {
				t.Errorf("normalizeMessage is not idempotent: %q -> %q", got, again)
			}
		})
	}
}
