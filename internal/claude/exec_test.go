package claude

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// TestHelperProcess is not a real test. It is the child the execer tests spawn
// to exercise the real os/exec path without depending on an external binary.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("CCRESU_EXEC_HELPER") != "1" {
		t.Skip("helper process for the execer tests")
	}

	mode := os.Getenv("CCRESU_EXEC_HELPER_MODE")
	switch mode {
	case "echo":
		// Echo the argv so the parent can assert it survived untouched.
		_, _ = os.Stdout.WriteString(strings.Join(os.Args, "|"))
	case "stdin":
		buf := make([]byte, 64)
		n, _ := os.Stdin.Read(buf)
		if n == 0 {
			_, _ = os.Stdout.WriteString("stdin-EOF")
			os.Exit(0)
		}
		_, _ = os.Stdout.Write(buf[:n])
	case "flood":
		chunk := strings.Repeat("x", 1024)
		for i := 0; i < 64; i++ {
			_, _ = os.Stdout.WriteString(chunk)
		}
	case "fail":
		_, _ = os.Stderr.WriteString("helper failed on purpose\n")
		os.Exit(3)
	case "sleep":
		time.Sleep(5 * time.Second)
	}

	// The test binary would otherwise run the remaining tests as the child.
	os.Exit(0)
}

// helperEnv returns the environment that turns the test binary into a helper.
func helperEnv(mode string) []string {
	return []string{"CCRESU_EXEC_HELPER=1", "CCRESU_EXEC_HELPER_MODE=" + mode}
}

// helperArgs builds the argv that runs TestHelperProcess in the child.
func helperArgs(mode string) []string {
	return []string{"-test.run=^TestHelperProcess$", "--", mode}
}

func TestOSExecerRunsWithoutAShell(t *testing.T) {
	t.Parallel()

	execer := OSExecer{}
	// A message full of shell metacharacters must reach the child verbatim, as
	// one argv element, with no shell in between.
	message := "continue; rm -rf / && echo pwned"
	out, err := execer.Run(context.Background(), os.Args[0], append(helperArgs("echo"), message), RunOptions{
		Env: helperEnv("echo"),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	fields := strings.Split(string(out.Stdout), "|")
	found := false
	for _, field := range fields {
		if field == message {
			found = true
		}
	}
	if !found {
		t.Errorf("message split across argv: %q", out.Stdout)
	}
}

func TestOSExecerStdinIsClosedByDefault(t *testing.T) {
	t.Parallel()

	execer := OSExecer{}
	out, err := execer.Run(context.Background(), os.Args[0], helperArgs("stdin"), RunOptions{
		Env: helperEnv("stdin"),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := strings.TrimSpace(string(out.Stdout)); got != "stdin-EOF" {
		t.Fatalf("stdout = %q, want an immediate EOF: the child must never get a TTY", got)
	}

	out, err = execer.Run(context.Background(), os.Args[0], helperArgs("stdin"), RunOptions{
		Env:   helperEnv("stdin"),
		Stdin: strings.NewReader("explicit input"),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := strings.TrimSpace(string(out.Stdout)); got != "explicit input" {
		t.Fatalf("stdout = %q, want the supplied stdin", got)
	}
}

func TestOSExecerTruncatesOutput(t *testing.T) {
	t.Parallel()

	execer := OSExecer{}
	out, err := execer.Run(context.Background(), os.Args[0], helperArgs("flood"), RunOptions{
		Env:            helperEnv("flood"),
		MaxOutputBytes: 100,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(out.Stdout) != 100 {
		t.Errorf("captured %d bytes, want 100", len(out.Stdout))
	}
	if !out.Truncated {
		t.Error("Truncated = false, want true")
	}
}

func TestOSExecerExitCodeAndStderr(t *testing.T) {
	t.Parallel()

	execer := OSExecer{}
	out, err := execer.Run(context.Background(), os.Args[0], helperArgs("fail"), RunOptions{
		Env: helperEnv("fail"),
	})
	if err == nil {
		t.Fatal("expected an error for a non-zero exit")
	}
	if out.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", out.ExitCode)
	}
	if !strings.Contains(string(out.Stderr), "helper failed on purpose") {
		t.Errorf("stderr = %q", out.Stderr)
	}
}

func TestOSExecerErrorDoesNotEchoTheArguments(t *testing.T) {
	t.Parallel()

	// The continuation message is a positional argument, so an error that quoted
	// argv would put the prompt in the log and in the error stored on the
	// session. Only the binary and the exit status belong there.
	const prompt = "CONTINUATION-MESSAGE-MARKER"

	execer := OSExecer{}
	_, err := execer.Run(context.Background(), os.Args[0], append(helperArgs("fail"), prompt), RunOptions{
		Env: helperEnv("fail"),
	})
	if err == nil {
		t.Fatal("expected an error for a non-zero exit")
	}
	if strings.Contains(err.Error(), prompt) {
		t.Fatalf("error = %q, must not echo the argument vector", err)
	}
	if !strings.Contains(err.Error(), "exit status 3") {
		t.Errorf("error = %q, want the exit status", err)
	}
}

func TestOSExecerMissingBinary(t *testing.T) {
	t.Parallel()

	execer := OSExecer{}
	out, err := execer.Run(context.Background(), "/nonexistent/ccresu-test-binary", nil, RunOptions{})
	if err == nil {
		t.Fatal("expected an error for a missing binary")
	}
	if out.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1", out.ExitCode)
	}

	out, err = execer.Run(context.Background(), "  ", nil, RunOptions{})
	if !errors.Is(err, ErrCLIUnavailable) {
		t.Errorf("error = %v, want ErrCLIUnavailable", err)
	}
	if out.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1", out.ExitCode)
	}
}

func TestOSExecerContextCancellation(t *testing.T) {
	t.Parallel()

	execer := OSExecer{}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := execer.Run(ctx, os.Args[0], helperArgs("sleep"), RunOptions{Env: helperEnv("sleep")})
	if err == nil {
		t.Fatal("expected an error when the context expires")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("child was not terminated promptly: %s", elapsed)
	}

	// An already-cancelled context never starts a process.
	cancelled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	out, err := execer.Run(cancelled, os.Args[0], helperArgs("echo"), RunOptions{Env: helperEnv("echo")})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	if out.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1", out.ExitCode)
	}
}

func TestOSExecerWorkingDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	script := "pwd"
	out, err := OSExecer{}.Run(context.Background(), "/bin/sh", []string{"-c", script}, RunOptions{Dir: dir})
	if err != nil {
		t.Skipf("shell unavailable: %v", err)
	}
	if got := strings.TrimSpace(string(out.Stdout)); got != dir {
		t.Errorf("pwd = %q, want %q", got, dir)
	}
}
