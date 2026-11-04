package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// TestRunMapsTheArgumentVectorOntoTheCLI covers the whole of this package: the
// build metadata it injects, the arguments it forwards and the exit code it
// returns. No configuration file is involved, so the cases are the ones that
// must work before anything else does.
func TestRunMapsTheArgumentVectorOntoTheCLI(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout []string
		wantStderr []string
	}{
		{
			name:       "version command",
			args:       []string{"version"},
			wantCode:   0,
			wantStdout: []string{"ccresu dev", "commit", "none", "platform"},
		},
		{
			name:       "version flag",
			args:       []string{"--version"},
			wantCode:   0,
			wantStdout: []string{"ccresu dev"},
		},
		{
			name:       "help flag",
			args:       []string{"--help"},
			wantCode:   0,
			wantStdout: []string{"Usage:", "Commands:", "Exit codes:"},
		},
		{
			name:       "help command",
			args:       []string{"help"},
			wantCode:   0,
			wantStdout: []string{"Commands:"},
		},
		{
			name:       "unknown command is a usage error",
			args:       []string{"definitely-not-a-command"},
			wantCode:   2,
			wantStderr: []string{"unknown command", "Run 'ccresu --help' for usage."},
		},
		{
			name:       "unknown global flag",
			args:       []string{"--not-a-flag", "version"},
			wantCode:   2,
			wantStderr: []string{"unknown flag"},
		},
		{
			name:       "no command prints the help page",
			args:       nil,
			wantCode:   2,
			wantStdout: []string{"Commands:"},
			wantStderr: []string{"no command given"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tc.args, &stdout, &stderr)
			if code != tc.wantCode {
				t.Fatalf("exit code = %d, want %d (stdout %q, stderr %q)",
					code, tc.wantCode, stdout.String(), stderr.String())
			}
			for _, want := range tc.wantStdout {
				if !strings.Contains(stdout.String(), want) {
					t.Fatalf("stdout %q does not contain %q", stdout.String(), want)
				}
			}
			for _, want := range tc.wantStderr {
				if !strings.Contains(stderr.String(), want) {
					t.Fatalf("stderr %q does not contain %q", stderr.String(), want)
				}
			}
		})
	}
}

// TestMainPassesTheProcessArgumentsAndExitCodeThrough proves main itself does
// its two jobs: it hands os.Args[1:] to the CLI rather than os.Args, and it
// exits with the code the CLI returned. os.Exit is swapped for a recorder, which
// is what makes the one process-terminating line testable.
func TestMainPassesTheProcessArgumentsAndExitCodeThrough(t *testing.T) {
	originalExit := osExit
	originalArgs := os.Args
	t.Cleanup(func() {
		osExit = originalExit
		os.Args = originalArgs
	})

	var gotCode int
	called := false
	osExit = func(code int) {
		called = true
		gotCode = code
	}
	// The program name at index 0 must not reach the CLI: 'version' would
	// otherwise be read as an argument to the binary's own path.
	os.Args = []string{"ccresu", "version"}

	// main writes to the real process streams; the version page is small and a
	// test binary's stdout is captured by the tooling.
	main()

	if !called {
		t.Fatal("main did not call os.Exit")
	}
	if gotCode != 0 {
		t.Fatalf("main exited %d, want 0", gotCode)
	}

	// And a failing invocation propagates its code rather than always exiting 0.
	os.Args = []string{"ccresu", "definitely-not-a-command"}
	called = false
	main()
	if !called {
		t.Fatal("main did not call os.Exit on the failing path")
	}
	if gotCode != 2 {
		t.Fatalf("main exited %d for an unknown command, want 2", gotCode)
	}
}
