package claude

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// testBackgroundJobID is the eight-hex background job name the CLI prints for
// testSessionID ("3f1e2c7a-8b4d-…"): the job is named after the session id's
// first eight characters, which is why the refusal can name it at all.
const testBackgroundJobID = "3f1e2c7a"

func TestStopArgsIsTheVerifiedVector(t *testing.T) {
	t.Parallel()

	got := StopArgs(testBackgroundJobID)
	want := []string{"stop", testBackgroundJobID}
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
	if again := StopArgs(testBackgroundJobID); again[0] != "stop" {
		t.Errorf("StopArgs() shares state between calls: %q", again)
	}
}

// TestStopRejectsAnythingThatIsNotAJobName pins the field failure: ccresu sent
// the session UUID, and the CLI answered "No job matching '<uuid>'" with exit 1,
// so the stop never happened and the session ended PERMANENTLY_FAILED. The UUID
// is now refused before a process starts.
func TestStopRejectsAnythingThatIsNotAJobName(t *testing.T) {
	t.Parallel()

	for _, id := range []string{
		"",
		"not-a-uuid",
		"--force",
		testSessionID,       // the shape that caused the incident
		testSessionID + "x", // and the same shape with a suffix
		"3F1E2C7A",          // the CLI's names are lower-case hex
		"3f1e2c7",           // seven characters is not a job name
		"3f1e2c7a0",         // nine characters is not either
		"3f1e2c7a-8b4d",     // a hyphen is never part of the name
		"../../etc/passwd",  // a path is never a job name
		"3f1e2c7z",          // non-hex
	} {
		execer := NewScriptedExecer(nil, nil, Output{}, errors.New("must not run"))
		cli := NewCLI(CLIOptions{Executable: "claude", Execer: execer})

		err := cli.Stop(context.Background(), id)
		if !errors.Is(err, ErrInvalidBackgroundJobID) {
			t.Errorf("Stop(%q) error = %v, want ErrInvalidBackgroundJobID", id, err)
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

	if err := cli.Stop(context.Background(), testBackgroundJobID); err != nil {
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
	want := []string{"stop", testBackgroundJobID}
	if strings.Join(call.Args, " ") != strings.Join(want, " ") {
		t.Errorf("argv = %q, want %q", call.Args, want)
	}
	if call.Options.Stdin != nil {
		t.Error("stop must never be given stdin: no terminal is attached")
	}
}

func TestStopFailureIsDescribedAndTyped(t *testing.T) {
	t.Parallel()

	// The real CLI answers an unknown job name with exit 1 and "No job
	// matching"; a version that does not know `stop` exits non-zero with a
	// usage error. Both have to surface as a failure naming the exit code and
	// the CLI's own words.
	execer := NewScriptedExecer([]Output{{
		Stderr:   []byte("No job matching '3f1e2c7a'. Run 'claude agents' to list running sessions.\n"),
		ExitCode: 1,
	}}, nil, Output{}, errors.New("unexpected invocation"))
	cli := NewCLI(CLIOptions{Executable: "claude", Execer: execer})

	err := cli.Stop(context.Background(), testBackgroundJobID)
	if !errors.Is(err, ErrStopFailed) {
		t.Fatalf("Stop() error = %v, want ErrStopFailed", err)
	}
	for _, want := range []string{"claude stop failed", "exit 1", "No job matching"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestStopReportsAnExecutorFailure(t *testing.T) {
	t.Parallel()

	execer := NewScriptedExecer([]Output{{ExitCode: -1}}, []error{errors.New("fork/exec: no such file")}, Output{}, nil)
	cli := NewCLI(CLIOptions{Executable: "claude", Execer: execer})

	err := cli.Stop(context.Background(), testBackgroundJobID)
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

// backgroundRefusalFor is the same refusal for a given session: the job the CLI
// names is that session id's first eight characters.
func backgroundRefusalFor(sessionID string) string {
	job := sessionID
	if len(job) > 8 {
		job = job[:8]
	}
	return "Error: That session is running in the background (" + job + "). " +
		"Run `claude attach " + job + "` to open it, or `claude stop " + job + "` first to resume it here. " +
		"Add --fork-session to branch off a copy instead."
}

func TestBackgroundJobID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil error", err: nil},
		{name: "the field message", err: errors.New(backgroundRefusal), want: "837eaaea"},
		{
			name: "per session",
			err:  errors.New(backgroundRefusalFor(testSessionID)),
			want: testBackgroundJobID,
		},
		{
			name: "wrapped the way the log showed it",
			err: errors.New("claude: claude executable unavailable: claude --resume failed (exit 1): " +
				backgroundRefusal + ": exit status 1"),
			want: "837eaaea",
		},
		{
			name: "wrapped across lines and indented",
			err: errors.New("claude --resume failed (exit 1): Error: That session is\n" +
				"    running in the background (837eaaea). Run `claude attach 837eaaea`\n" +
				"    to open it, or `claude stop 837eaaea` first to resume it here.\n"),
			want: "837eaaea",
		},
		{
			name: "ansi colour codes",
			err:  errors.New("\x1b[31mError: That session is running in the background\x1b[0m (837eaaea)"),
			want: "837eaaea",
		},
		{
			name: "only the parenthesised name",
			err:  errors.New("Error: That session is running in the background (deadbeef)."),
			want: "deadbeef",
		},
		{name: "refusal that names no job", err: errors.New("Error: That session is running in the background.")},
		{
			name: "parenthesised name is not a job name",
			err:  errors.New("Error: That session is running in the background (837EAEA)."),
		},
		{name: "unrelated background sentence", err: errors.New("the job will keep running in the background")},
		{name: "auth error", err: errors.New("invalid api key provided")},
		{name: "empty message", err: errors.New("")},
		{name: "typed sentinel", err: ErrSessionRunningInBackground},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := BackgroundJobID(tc.err); got != tc.want {
				t.Errorf("BackgroundJobID(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

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
			err: errors.New("claude: claude executable unavailable: claude --resume failed (exit 1): " +
				backgroundRefusal + ": exit status 1"),
			want: true,
		},
		{
			name: "indented and wrapped across lines",
			err: errors.New("claude --resume failed (exit 1): Error: That session is\n" +
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
		{name: "generic exit 1", err: errors.New("claude --resume failed (exit 1): unknown error")},
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
