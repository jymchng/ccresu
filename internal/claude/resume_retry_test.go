package claude

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// backgroundRefusalOutput is what the CLI printed in the field: exit 1, the
// refusal on stderr, nothing on stdout. The refusal names the job that holds
// testSessionID.
func backgroundRefusalOutput() Output {
	return Output{Stderr: []byte(backgroundRefusalFor(testSessionID) + "\n"), ExitCode: 1}
}

// noJobMatchingOutput is the CLI's answer to a `claude stop` whose argument is
// not one of the eight-hex job names it keeps. It is exit 1 in the field: that
// is how a stop that passes a session UUID fails.
func noJobMatchingOutput(arg string) Output {
	return Output{
		Stderr:   []byte("No job matching '" + arg + "'. Run 'claude agents' to list running sessions.\n"),
		ExitCode: 1,
	}
}

// resumeCalls returns the recorded calls whose argv is a resume invocation.
func resumeCalls(calls []Call) []Call {
	var out []Call
	for _, call := range calls {
		if len(call.Args) > 0 && call.Args[0] == "--resume" {
			out = append(out, call)
		}
	}
	return out
}

// stopCalls returns the recorded calls whose argv is a stop invocation.
func stopCalls(calls []Call) []Call {
	var out []Call
	for _, call := range calls {
		if len(call.Args) > 0 && call.Args[0] == "stop" {
			out = append(out, call)
		}
	}
	return out
}

// resumeScript answers the version probe, the resume invocations and the stop
// invocation from small counters, so a test can describe the CLI's behaviour
// without depending on the exact number of incidental calls.
//
// The stop arm resolves its argument the way the real CLI does: it matches only
// an eight-hex job name it knows about, and answers anything else -- including
// the session UUID -- with "No job matching" and exit 1. A fake that accepted
// any argument is what let the session-UUID stop reach the field.
type resumeScript struct {
	mu       sync.Mutex
	resumes  int
	stops    int
	onResume func(attempt int) (Output, error)
	onStop   func(attempt int) (Output, error)
}

// knownJobs are the background jobs the stub CLI holds; stopping anything else
// is "No job matching".
var knownJobs = map[string]bool{testBackgroundJobID: true}

func (s *resumeScript) handle(call Call) (Output, error) {
	if len(call.Args) == 0 {
		return Output{}, errors.New("unexpected invocation with no arguments")
	}
	switch call.Args[0] {
	case "--version":
		return Output{Stdout: []byte(testVersionOutput)}, nil
	case "stop":
		s.mu.Lock()
		s.stops++
		attempt := s.stops
		hook := s.onStop
		s.mu.Unlock()
		if err := resolveStopArg(call.Args[1:]); err != "" {
			return noJobMatchingOutput(err), nil
		}
		if hook != nil {
			return hook(attempt)
		}
		return Output{}, nil
	default:
		s.mu.Lock()
		s.resumes++
		attempt := s.resumes
		hook := s.onResume
		s.mu.Unlock()
		if hook != nil {
			return hook(attempt)
		}
		return Output{Stdout: []byte(testDispatchOutput)}, nil
	}
}

// resolveStopArg mirrors the CLI's resolver: one argument, and it has to name a
// job the CLI holds. It returns the offending argument, or "" when the stop is
// resolvable.
func resolveStopArg(args []string) string {
	if len(args) != 1 {
		return strings.Join(args, " ")
	}
	arg := args[0]
	if !ValidBackgroundJobID(arg) || !knownJobs[arg] {
		return arg
	}
	return ""
}

func newScriptedResumeCLI(t *testing.T, script *resumeScript, logger *slog.Logger) (*CLI, *FakeExecer) {
	t.Helper()
	execer := NewFakeExecer(script.handle)
	opts := CLIOptions{Executable: "claude", Execer: execer}
	if logger != nil {
		opts.Logger = logger
	}
	return NewCLI(opts), execer
}

// TestResumeStopsABackgroundSessionAndResumesAgain is the incident this feature
// exists for: the CLI refuses because the session is attached to a background
// agent, ccresu stops it, and the identical resume then succeeds.
func TestResumeStopsABackgroundSessionAndResumesAgain(t *testing.T) {
	t.Parallel()

	script := &resumeScript{onResume: func(attempt int) (Output, error) {
		if attempt == 1 {
			return backgroundRefusalOutput(), nil
		}
		return Output{Stdout: []byte(testDispatchOutput)}, nil
	}}
	cli, execer := newScriptedResumeCLI(t, script, nil)

	result, err := cli.Resume(context.Background(), testSessionID, "keep going")
	if err != nil {
		t.Fatalf("Resume() error = %v, want success after the stop", err)
	}
	if result.JobID != testBackgroundJobID {
		t.Errorf("JobID = %q, want %q: the retried dispatch must report its own job",
			result.JobID, testBackgroundJobID)
	}
	if result.RequestedSessionID != testSessionID {
		t.Errorf("RequestedSessionID = %q, want %q", result.RequestedSessionID, testSessionID)
	}

	calls := execer.Calls()
	resumes := resumeCalls(calls)
	if len(resumes) != 2 {
		t.Fatalf("resume invoked %d times, want 2: %v", len(resumes), calls)
	}
	// The retry must be byte-identical: same message, same flags, same order.
	first := strings.Join(resumes[0].Args, "\x00")
	second := strings.Join(resumes[1].Args, "\x00")
	if first != second {
		t.Errorf("retried argv differs:\n first = %q\nsecond = %q", first, second)
	}
	want := []string{"--resume", testSessionID, "--bg", "keep going"}
	if first != strings.Join(want, "\x00") {
		t.Errorf("argv = %q, want %q", resumes[0].Args, want)
	}

	stops := stopCalls(calls)
	if len(stops) != 1 {
		t.Fatalf("stop invoked %d times, want exactly 1: %v", len(stops), calls)
	}
	// The stop takes the job name the refusal printed, never the session UUID:
	// the CLI answers a UUID with "No job matching" and exit 1.
	wantStop := []string{"stop", testBackgroundJobID}
	if strings.Join(stops[0].Args, "\x00") != strings.Join(wantStop, "\x00") {
		t.Errorf("stop argv = %q, want %q", stops[0].Args, wantStop)
	}
	if stops[0].Options.Stdin != nil {
		t.Error("stop must never be given stdin")
	}

	// The attempt row is built from result.Command, so it must still show the
	// one resume command and never the stop.
	if got := strings.Join(result.Command, " "); got != "claude --resume "+testSessionID+" --bg keep going" {
		t.Errorf("result.Command = %q, want the single resume command", got)
	}
}

// TestResumeNeverPassesTheSessionIDToStop is the regression test for the field
// failure: with the old code the stop carried the session UUID, the stub CLI
// answered "No job matching" and exited 1, and the session ended
// PERMANENTLY_FAILED. Every recorded stop argument must be a job name.
func TestResumeNeverPassesTheSessionIDToStop(t *testing.T) {
	t.Parallel()

	script := &resumeScript{onResume: func(attempt int) (Output, error) {
		if attempt == 1 {
			return backgroundRefusalOutput(), nil
		}
		return Output{Stdout: []byte(testDispatchOutput)}, nil
	}}
	cli, execer := newScriptedResumeCLI(t, script, nil)

	if _, err := cli.Resume(context.Background(), testSessionID, "keep going"); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	for _, call := range stopCalls(execer.Calls()) {
		if len(call.Args) < 2 {
			t.Fatalf("stop called without an argument: %q", call.Args)
		}
		if !ValidBackgroundJobID(call.Args[1]) {
			t.Errorf("stop argument %q is not an eight-hex job name", call.Args[1])
		}
		if call.Args[1] == testSessionID {
			t.Errorf("stop was given the session UUID %q", call.Args[1])
		}
	}
}

// TestResumeDoesNotStopWhenTheRefusalNamesNoJob keeps the second command
// honest: without a job name there is nothing safe to stop, so the refusal is
// reported unchanged rather than a guessed job being stopped.
func TestResumeDoesNotStopWhenTheRefusalNamesNoJob(t *testing.T) {
	t.Parallel()

	script := &resumeScript{onResume: func(int) (Output, error) {
		return Output{
			Stderr:   []byte("Error: That session is running in the background.\n"),
			ExitCode: 1,
		}, nil
	}}
	cli, execer := newScriptedResumeCLI(t, script, nil)

	_, err := cli.Resume(context.Background(), testSessionID, "keep going")
	if err == nil {
		t.Fatal("Resume() error = nil, want the refusal to surface")
	}
	if !IsSessionRunningInBackground(err) {
		t.Errorf("error %v lost the background marker", err)
	}
	calls := execer.Calls()
	if got := len(stopCalls(calls)); got != 0 {
		t.Errorf("stop invoked %d times, want none: %v", got, calls)
	}
	if got := len(resumeCalls(calls)); got != 1 {
		t.Errorf("resume invoked %d times, want 1: %v", got, calls)
	}
}

// TestResumeStillFailsWhenTheSessionStaysInTheBackground is the honesty case:
// stopping once does not guarantee the session is free, and a second refusal
// must surface as a failure, not a success.
func TestResumeStillFailsWhenTheSessionStaysInTheBackground(t *testing.T) {
	t.Parallel()

	script := &resumeScript{onResume: func(int) (Output, error) {
		return backgroundRefusalOutput(), nil
	}}
	cli, execer := newScriptedResumeCLI(t, script, nil)

	result, err := cli.Resume(context.Background(), testSessionID, "keep going")
	if err == nil {
		t.Fatal("Resume() error = nil, want the refusal to surface")
	}
	if result.JobID != "" {
		t.Errorf("JobID = %q on a failed dispatch, want empty", result.JobID)
	}
	if !IsSessionRunningInBackground(err) {
		t.Errorf("error %v lost the background marker", err)
	}

	calls := execer.Calls()
	if got := len(resumeCalls(calls)); got != 2 {
		t.Errorf("resume invoked %d times, want exactly 2 (one retry, never a loop): %v", got, calls)
	}
	if got := len(stopCalls(calls)); got != 1 {
		t.Errorf("stop invoked %d times, want exactly 1: %v", got, calls)
	}
}

// TestResumeReportsTheOriginalErrorWhenStopFails pins the ordering the user
// asked for: a stop that fails leaves the session in the way, so the resume
// refusal — not the stop failure — is what the caller sees and classifies.
func TestResumeReportsTheOriginalErrorWhenStopFails(t *testing.T) {
	t.Parallel()

	script := &resumeScript{
		onResume: func(int) (Output, error) {
			return backgroundRefusalOutput(), nil
		},
		onStop: func(int) (Output, error) {
			return Output{Stderr: []byte("error: unknown command 'stop'\n"), ExitCode: 2}, nil
		},
	}
	cli, execer := newScriptedResumeCLI(t, script, nil)

	result, err := cli.Resume(context.Background(), testSessionID, "keep going")
	if err == nil {
		t.Fatal("Resume() error = nil, want a failure")
	}
	if errors.Is(err, ErrStopFailed) {
		t.Errorf("error = %v, want the original resume refusal, not the stop failure", err)
	}
	if !strings.Contains(err.Error(), "running in the background") {
		t.Errorf("error %q does not carry the original refusal", err)
	}

	calls := execer.Calls()
	if got := len(resumeCalls(calls)); got != 1 {
		t.Errorf("resume invoked %d times, want 1: with the session still in the way a retry would only fail again", got)
	}
	if got := len(stopCalls(calls)); got != 1 {
		t.Errorf("stop invoked %d times, want 1: %v", got, calls)
	}
	if result.JobID != "" {
		t.Errorf("JobID = %q on a failed dispatch, want empty", result.JobID)
	}
}

// TestResumeNeverStopsForAnOrdinaryFailure keeps the recovery narrow: only the
// background refusal may add a command, so a plain failure costs one invocation.
func TestResumeNeverStopsForAnOrdinaryFailure(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		out  Output
	}{
		{name: "auth error", out: Output{Stderr: []byte("Error: invalid api key\n"), ExitCode: 1}},
		{name: "generic exit 1", out: Output{Stderr: []byte("Error: something went wrong\n"), ExitCode: 1}},
		{name: "network text", out: Output{Stderr: []byte("connection reset by peer\n"), ExitCode: 1}},
		{
			// Mentions a background job but is not the refusal.
			name: "unrelated background sentence",
			out:  Output{Stderr: []byte("note: the job will keep running in the background\n"), ExitCode: 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			script := &resumeScript{onResume: func(int) (Output, error) { return tc.out, nil }}
			cli, execer := newScriptedResumeCLI(t, script, nil)

			if _, err := cli.Resume(context.Background(), testSessionID, "keep going"); err == nil {
				t.Fatal("Resume() error = nil, want a failure")
			}
			calls := execer.Calls()
			if got := len(stopCalls(calls)); got != 0 {
				t.Errorf("stop invoked %d times, want none: %v", got, calls)
			}
			if got := len(resumeCalls(calls)); got != 1 {
				t.Errorf("resume invoked %d times, want 1: %v", got, calls)
			}
		})
	}
}

// TestResumeLogsTheStop proves the extra command is recorded somewhere: it is
// in the log, not in the attempt row.
func TestResumeLogsTheStop(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	script := &resumeScript{onResume: func(attempt int) (Output, error) {
		if attempt == 1 {
			return backgroundRefusalOutput(), nil
		}
		return Output{Stdout: []byte(testDispatchOutput)}, nil
	}}
	cli, _ := newScriptedResumeCLI(t, script, logger)

	if _, err := cli.Resume(context.Background(), testSessionID, "keep going"); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	logged := buf.String()
	if !strings.Contains(logged, "background") {
		t.Errorf("log %q does not explain the background session", logged)
	}
	if !strings.Contains(logged, testSessionID) {
		t.Errorf("log %q does not name the session", logged)
	}
	if !strings.Contains(logged, "dispatching again") {
		t.Errorf("log %q does not record the stop-then-resume decision", logged)
	}
	// The job name is what was actually passed to `claude stop`, so the log has
	// to carry it: the two ids differ and one is not the other.
	if !strings.Contains(logged, testBackgroundJobID) {
		t.Errorf("log %q does not name the stopped background job", logged)
	}
}

// TestScriptedCLIRejectsASessionUUIDStop keeps the fake honest. If the stub ever
// accepts a session UUID again, every assertion built on it becomes worthless --
// which is exactly how the field failure passed 125 tests.
func TestScriptedCLIRejectsASessionUUIDStop(t *testing.T) {
	t.Parallel()

	script := &resumeScript{}
	cli, execer := newScriptedResumeCLI(t, script, nil)

	err := cli.Stop(context.Background(), testBackgroundJobID)
	if err != nil {
		t.Fatalf("Stop(job name) error = %v, want success", err)
	}

	// Bypass Stop's own validation to prove the stub's resolver, not ccresu,
	// refuses a UUID: run the command the way the old code would have.
	out, runErr := execer.Run(context.Background(), "claude", []string{"stop", testSessionID}, RunOptions{})
	if runErr != nil {
		t.Fatalf("direct stop run failed: %v", runErr)
	}
	if out.ExitCode == 0 {
		t.Fatal("the stub accepted a session UUID as a `claude stop` argument; the real CLI does not")
	}
	if !strings.Contains(string(out.Stderr), "No job matching") {
		t.Errorf("stderr = %q, want the CLI's \"No job matching\" wording", out.Stderr)
	}
}
