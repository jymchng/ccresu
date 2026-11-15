package claude

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// exitError mimics what OSExecer returns for a non-zero exit: both a status and
// an error, because a caller that checks only one of them is the bug this file
// exists to prevent.
func exitError(code int) error { return fmt.Errorf("claude: exit status %d", code) }

// TestDaemonStatusExitOneMeansNoSupervisor pins PRD 3 R1's first clause. Exit 1
// is the documented "the supervisor isn't running" answer, and on a machine that
// has never run `claude --bg` it is the *normal* state, so it must not be a
// failure. Exit 2 is a different fact and must be named as one.
func TestDaemonStatusExitOneMeansNoSupervisor(t *testing.T) {
	t.Parallel()

	t.Run("exit 1 is a normal answer", func(t *testing.T) {
		t.Parallel()
		execer := NewFakeExecer(func(Call) (Output, error) {
			return Output{ExitCode: 1}, exitError(1)
		})
		cli := NewCLI(CLIOptions{Executable: "claude", Execer: execer})

		status, err := cli.DaemonStatus(context.Background())
		if err != nil {
			t.Fatalf("DaemonStatus() error = %v, want nil: exit 1 means no supervisor, not a failure", err)
		}
		if status.Running {
			t.Error("Running = true, want false when no supervisor answered")
		}
		if got, want := strings.Join(status.Argv, " "), "daemon status"; got != want {
			t.Errorf("Argv = %q, want %q", got, want)
		}
		if status.ExitCode != 1 {
			t.Errorf("ExitCode = %d, want 1 recorded", status.ExitCode)
		}
	})

	t.Run("exit 2 is a distinct named failure", func(t *testing.T) {
		t.Parallel()
		execer := NewFakeExecer(func(Call) (Output, error) {
			return Output{ExitCode: 2, Stderr: []byte("unknown command \"daemon\"")}, exitError(2)
		})
		cli := NewCLI(CLIOptions{Executable: "claude", Execer: execer})

		status, err := cli.DaemonStatus(context.Background())
		if err == nil {
			t.Fatal("DaemonStatus() error = nil, want a named failure for exit 2")
		}
		if !errors.Is(err, ErrDaemonStatusFailed) {
			t.Errorf("error = %v, want it to wrap ErrDaemonStatusFailed", err)
		}
		if errors.Is(err, ErrSupervisorVersionMismatch) {
			t.Error("exit 2 reported as a version mismatch, want its own condition")
		}
		// The distinction matters: "no supervisor" is carried on a nil error, so
		// a non-nil error must never be read as that normal answer.
		if status.Running {
			t.Error("Running = true, want false")
		}
		if !strings.Contains(err.Error(), "unknown command") {
			t.Errorf("error = %v, want the CLI's own stderr carried out", err)
		}
	})

	t.Run("a timeout is not read as an absent supervisor", func(t *testing.T) {
		t.Parallel()
		execer := NewFakeExecer(func(Call) (Output, error) {
			return Output{ExitCode: -1}, fmt.Errorf("claude: %w", context.DeadlineExceeded)
		})
		cli := NewCLI(CLIOptions{Executable: "claude", Execer: execer})

		_, err := cli.DaemonStatus(context.Background())
		if !errors.Is(err, ErrDaemonStatusFailed) {
			t.Fatalf("error = %v, want ErrDaemonStatusFailed for an unresponsive supervisor", err)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("error = %v, want the timeout preserved", err)
		}
	})
}

// TestDaemonStatusParsesTheDocumentedFields keeps the parser tolerant without
// letting it invent anything: the output format is only promised in prose, so
// four plausible shapes all have to read the same.
func TestDaemonStatusParsesTheDocumentedFields(t *testing.T) {
	t.Parallel()

	const colon = "state: running\nversion: 2.1.257 (Claude Code)\nsocket dir: /tmp/claude-1000\nworkers: 3\n"
	const spaced = "State   running\nVersion   2.1.257\nSocket Directory   /tmp/claude-1000\nWorker Count   3\n"
	const equals = "status=running\nversion=2.1.257\nsocket=/tmp/claude-1000\nworkers=3\n"
	const noisy = "\x1b[32mstate\x1b[0m: RUNNING\n\n- version: v2.1.257\n- socket: /tmp/claude-1000\n- workers: 3 sessions\n"

	for _, raw := range []string{colon, spaced, equals, noisy} {
		status := ParseSupervisorStatus(raw)
		if status.State == "" {
			t.Errorf("ParseSupervisorStatus(%q).State is empty", raw)
			continue
		}
		if !status.Version.Parsed || status.Version.String() != "2.1.257" {
			t.Errorf("version = %+v, want 2.1.257 parsed", status.Version)
		}
		if status.SocketDir != "/tmp/claude-1000" {
			t.Errorf("SocketDir = %q, want /tmp/claude-1000", status.SocketDir)
		}
		if !status.WorkersKnown || status.Workers != 3 {
			t.Errorf("Workers = %d (known %v), want 3", status.Workers, status.WorkersKnown)
		}
	}
}

// An unrecognised format is reported as unrecognised, never as an empty
// supervisor: "the supervisor answered in prose ccresu does not know" and "there
// is no supervisor" are different facts.
func TestAnUnrecognisedStatusIsNotAnEmptySupervisor(t *testing.T) {
	t.Parallel()

	status := ParseSupervisorStatus("Claude Code background service is healthy\n")
	if status.Recognised() {
		t.Errorf("Recognised() = true for prose, want false: %+v", status)
	}
	if status.Raw == "" {
		t.Error("Raw is empty, want the supervisor's own words preserved")
	}

	unparsed := ParseSupervisorStatus("state: running\nversion: unknown-build\n")
	if unparsed.Version.Parsed {
		t.Error("Version.Parsed = true for a non-version, want false")
	}
	if unparsed.Version.Raw != "unknown-build" {
		t.Errorf("Version.Raw = %q, want the raw value kept", unparsed.Version.Raw)
	}
}

// TestAVersionMismatchCarriesBothVersionsAndTheRemedy pins R2's content
// requirements at the unit level: both versions, the floor, the documented
// remedy, and a condition that is distinguishable from every other failure.
func TestAVersionMismatchCarriesBothVersionsAndTheRemedy(t *testing.T) {
	t.Parallel()

	cli := Version{Major: 2, Minor: 1, Patch: 300, Parsed: true}
	stale := Version{Major: 2, Minor: 1, Patch: 257, Parsed: true}
	floor := Version{Major: 2, Minor: 1, Patch: 257, Parsed: true}
	running := SupervisorStatus{Running: true, State: "running", Version: stale}

	err := CompareSupervisorVersion(running, cli, floor)
	if err == nil {
		t.Fatal("CompareSupervisorVersion() = nil, want a mismatch between 2.1.257 and 2.1.300")
	}
	if !errors.Is(err, ErrSupervisorVersionMismatch) {
		t.Errorf("error = %v, want it to wrap ErrSupervisorVersionMismatch", err)
	}
	var mismatch *SupervisorVersionMismatch
	if !errors.As(err, &mismatch) {
		t.Fatalf("error = %v, want *SupervisorVersionMismatch", err)
	}
	if mismatch.Supervisor.String() != "2.1.257" || mismatch.CLI.String() != "2.1.300" {
		t.Errorf("versions = supervisor %s / cli %s, want 2.1.257 / 2.1.300",
			mismatch.Supervisor, mismatch.CLI)
	}
	if got, want := mismatch.Remedy, "claude daemon stop --any --keep-workers"; got != want {
		t.Errorf("Remedy = %q, want %q", got, want)
	}
	for _, fragment := range []string{"2.1.257", "2.1.300", "daemon stop --any --keep-workers", "floor"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("error text %q is missing %q", err.Error(), fragment)
		}
	}

	// The three cases that are deliberately not mismatches.
	if err := CompareSupervisorVersion(SupervisorStatus{Running: false, Version: stale}, cli, floor); err != nil {
		t.Errorf("no supervisor reported a mismatch: %v", err)
	}
	if err := CompareSupervisorVersion(SupervisorStatus{Running: true, Version: Version{Raw: "?"}}, cli, floor); err != nil {
		t.Errorf("an unparsed supervisor version reported a mismatch: %v", err)
	}
	if err := CompareSupervisorVersion(SupervisorStatus{Running: true, Version: cli}, cli, floor); err != nil {
		t.Errorf("identical versions reported a mismatch: %v", err)
	}
	// A supervisor that is old enough to matter against the floor is a mismatch
	// even if the CLI happens to match it.
	if err := CompareSupervisorVersion(SupervisorStatus{Running: true, Version: stale},
		stale, Version{Major: 2, Minor: 2, Patch: 0, Parsed: true}); err == nil {
		t.Error("a supervisor below the floor reported no mismatch")
	}
}

// TestCcresuNeverRespawns is the guard for PRD 3 R3 and R4.
//
// The guarantee is an absence, so it is asserted against everything that can
// build a command line: no constructor in this package may emit `respawn`, and
// `daemon` may only ever appear with the read-only verbs. If a future edit adds
// a mutating lifecycle verb, this test is what notices.
func TestCcresuNeverRespawns(t *testing.T) {
	t.Parallel()

	constructors := map[string][]string{
		"DaemonStatusArgs":   DaemonStatusArgs(),
		"DaemonLogsArgs":     DaemonLogsArgs(),
		"SessionLogsArgs":    SessionLogsArgs("3f1e2c7a"),
		"StopArgs":           StopArgs("3f1e2c7a"),
		"HandoffArgs":        HandoffArgs(testSessionID, "continue"),
		"AgentsQuery.Args":   (AgentsQuery{}).Args(),
		"VersionArgs-if-any": nil,
	}

	mutating := map[string]bool{
		"stop":    true, // permitted, and only for an operator's termination (R5)
		"respawn": false,
		"kill":    false,
		"rm":      false,
		"restart": false,
	}
	daemonVerbs := map[string]bool{"status": true, "logs": true}

	for name, argv := range constructors {
		if argv == nil {
			continue
		}
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "respawn") {
			t.Errorf("%s emits `respawn`, which a recovery must never run (R4): %q", name, joined)
		}
		if strings.Contains(argv[0], "respawn") {
			t.Errorf("%s uses respawn as its verb: %q", name, joined)
		}
		for i, token := range argv {
			if token == "daemon" && i+1 < len(argv) && !daemonVerbs[argv[i+1]] {
				t.Errorf("%s emits the mutating supervisor verb %q; only `daemon status` and `daemon logs` are read-only: %q",
					name, argv[i+1], joined)
			}
			if token == "daemon" && i+1 < len(argv) && argv[i+1] == "stop" {
				t.Errorf("%s emits `daemon stop`, which stops every hosted session (R3): %q", name, joined)
			}
			if mutating[token] && token != "stop" {
				t.Errorf("%s emits the lifecycle verb %q: %q", name, token, joined)
			}
		}
	}
}

// TestStopStillTakesTheEightHexJobID keeps PRD 1's R10 contract green while the
// rest of the lifecycle surface changes: the stop path is untouched, a UUID is
// still refused before a process starts, and an eight-hex name is still what
// reaches the CLI.
func TestStopStillTakesTheEightHexJobID(t *testing.T) {
	t.Parallel()

	execer := NewScriptedExecer([]Output{{ExitCode: 0}}, nil, Output{ExitCode: 0}, nil)
	cli := NewCLI(CLIOptions{Executable: "claude", Execer: execer})

	if err := cli.Stop(context.Background(), testBackgroundJobID); err != nil {
		t.Fatalf("Stop(%q) error = %v", testBackgroundJobID, err)
	}
	call, ok := execer.LastCall()
	if !ok {
		t.Fatal("Stop() ran no command")
	}
	if got, want := strings.Join(call.Args, " "), "stop "+testBackgroundJobID; got != want {
		t.Errorf("argv = %q, want %q", got, want)
	}

	// The shape that caused the original incident: a UUID is refused before any
	// process is started, so the caller is told why instead of reading "No job
	// matching".
	refusing := NewScriptedExecer(nil, nil, Output{}, errors.New("must not run"))
	guard := NewCLI(CLIOptions{Executable: "claude", Execer: refusing})
	if err := guard.Stop(context.Background(), testSessionID); !errors.Is(err, ErrInvalidBackgroundJobID) {
		t.Fatalf("Stop(uuid) error = %v, want ErrInvalidBackgroundJobID", err)
	}
	if calls := refusing.Calls(); len(calls) != 0 {
		t.Errorf("Stop(uuid) ran %d commands, want 0: %q", len(calls), calls[0].Args)
	}
}

// Logs are read on demand (R10), and a failed read is not an empty log.
func TestLogsAreReadOnDemandAndFailuresAreNotEmptied(t *testing.T) {
	t.Parallel()

	t.Run("session logs", func(t *testing.T) {
		t.Parallel()
		execer := NewScriptedExecer([]Output{{ExitCode: 0, Stdout: []byte("line one\nline two\n")}}, nil, Output{}, nil)
		cli := NewCLI(CLIOptions{Executable: "claude", Execer: execer})

		result, err := cli.SessionLogs(context.Background(), "3f1e2c7a")
		if err != nil {
			t.Fatalf("SessionLogs() error = %v", err)
		}
		call, _ := execer.LastCall()
		if got, want := strings.Join(call.Args, " "), "logs 3f1e2c7a"; got != want {
			t.Errorf("argv = %q, want %q", got, want)
		}
		if got, want := strings.Join(result.Argv, " "), "logs 3f1e2c7a"; got != want {
			t.Errorf("result Argv = %q, want the executed vector %q", got, want)
		}
		if result.Text != "line one\nline two" {
			t.Errorf("Text = %q, want both lines without the trailing newline", result.Text)
		}
	})

	t.Run("daemon logs", func(t *testing.T) {
		t.Parallel()
		execer := NewScriptedExecer([]Output{{ExitCode: 0, Stdout: []byte("supervisor up\n")}}, nil, Output{}, nil)
		cli := NewCLI(CLIOptions{Executable: "claude", Execer: execer})

		result, err := cli.DaemonLogs(context.Background())
		if err != nil {
			t.Fatalf("DaemonLogs() error = %v", err)
		}
		if got, want := strings.Join(result.Argv, " "), "daemon logs"; got != want {
			t.Errorf("Argv = %q, want %q", got, want)
		}
		if result.Text != "supervisor up" {
			t.Errorf("Text = %q", result.Text)
		}
	})

	t.Run("a failed read is reported, not returned as empty", func(t *testing.T) {
		t.Parallel()
		execer := NewScriptedExecer([]Output{{ExitCode: 3, Stderr: []byte("no such session")}}, []error{exitError(3)}, Output{}, nil)
		cli := NewCLI(CLIOptions{Executable: "claude", Execer: execer})

		result, err := cli.SessionLogs(context.Background(), "3f1e2c7a")
		if !errors.Is(err, ErrLogsFailed) {
			t.Fatalf("error = %v, want ErrLogsFailed", err)
		}
		if result.Text != "" {
			t.Errorf("Text = %q, want no text from a failed read", result.Text)
		}
		if !strings.Contains(err.Error(), "no such session") {
			t.Errorf("error = %v, want the CLI's stderr carried out", err)
		}
	})

	t.Run("an identifier that is not one is refused", func(t *testing.T) {
		t.Parallel()
		for _, id := range []string{"", "   ", "--all", "-p", "two words", "with\nnewline"} {
			execer := NewScriptedExecer(nil, nil, Output{}, errors.New("must not run"))
			cli := NewCLI(CLIOptions{Executable: "claude", Execer: execer})
			if _, err := cli.SessionLogs(context.Background(), id); !errors.Is(err, ErrInvalidSessionID) {
				t.Errorf("SessionLogs(%q) error = %v, want ErrInvalidSessionID", id, err)
			}
			if calls := execer.Calls(); len(calls) != 0 {
				t.Errorf("SessionLogs(%q) ran %s, want no process", id, strconv.Quote(strings.Join(calls[0].Args, " ")))
			}
		}
	})
}

// The argv vectors are exported so tests and logs can assert them exactly
// (R11); a caller must not be able to corrupt a shared slice.
func TestSupervisorArgvVectorsAreExactAndIndependent(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		got  []string
		want []string
	}{
		{name: "daemon status", got: DaemonStatusArgs(), want: []string{"daemon", "status"}},
		{name: "daemon logs", got: DaemonLogsArgs(), want: []string{"daemon", "logs"}},
		{name: "session logs", got: SessionLogsArgs("3f1e2c7a"), want: []string{"logs", "3f1e2c7a"}},
	} {
		if strings.Join(tc.got, " ") != strings.Join(tc.want, " ") {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
		tc.got[0] = "mutated"
	}

	if again := DaemonStatusArgs(); again[0] != "daemon" {
		t.Errorf("DaemonStatusArgs() shares state between calls: %q", again)
	}
	if again := SessionLogsArgs("3f1e2c7a"); again[0] != "logs" {
		t.Errorf("SessionLogsArgs() shares state between calls: %q", again)
	}
}
