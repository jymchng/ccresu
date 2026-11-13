package claude

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// limitBanner is the refusal shape the CLI produces when the account's usage
// window has not reset. The reset phrase is the one recorded for session
// 1347e5db-a9fb-42eb-b92a-82256bd5aa68, whose 60 refused resumes were all
// reported as "claude executable unavailable".
const limitBanner = "Claude usage limit reached. Your limit will reset at 10am (Europe/Berlin)."

// TestDescribeRunError pins the contract of a failed invocation: the output the
// CLI produced is carried, and the condition is named honestly.
//
// The defect this guards against: a refusal was wrapped in ErrCLIUnavailable,
// so the retry policy treated "wait until 10am" as "the binary is gone", and the
// banner — which lived on stdout — was thrown away before anything could read
// it.
func TestDescribeRunError(t *testing.T) {
	t.Parallel()

	crash := errors.New("exit status 2")

	cases := []struct {
		name string
		what string
		out  Output
		err  error

		wantKind     error   // the sentinel errors.Is must find, nil for none
		wantNotKinds []error // sentinels that must NOT match
		wantExit     int
		wantMessage  []string // fragments the rendered message must contain
		wantNoOutput []string // fragments the rendered message must NOT contain
	}{
		{
			name:         "limit banner on stdout",
			what:         "claude -p --resume",
			out:          Output{Stdout: []byte(limitBanner), ExitCode: 1},
			err:          errors.New("exit status 1"),
			wantKind:     ErrUsageLimitInForce,
			wantNotKinds: []error{ErrCLIUnavailable},
			wantExit:     1,
			// The banner is the whole point: it must reach the operator.
			wantMessage: []string{"failed (exit 1)", "usage limit reached", "10am (Europe/Berlin)"},
		},
		{
			name:         "limit banner on stderr",
			what:         "claude -p --resume",
			out:          Output{Stderr: []byte(limitBanner), ExitCode: 1},
			err:          errors.New("exit status 1"),
			wantKind:     ErrUsageLimitInForce,
			wantNotKinds: []error{ErrCLIUnavailable},
			wantExit:     1,
			wantMessage:  []string{"usage limit reached", "10am (Europe/Berlin)"},
		},
		{
			name:         "json refusal on stdout",
			what:         "claude -p --resume",
			out:          Output{Stdout: []byte(`{"type":"result","subtype":"error","is_error":true,"result":"You've hit your 5-hour limit"}`), ExitCode: 1},
			err:          errors.New("exit status 1"),
			wantKind:     ErrUsageLimitInForce,
			wantNotKinds: []error{ErrCLIUnavailable},
			wantExit:     1,
			wantMessage:  []string{"5-hour limit"},
		},
		{
			name:         "plain crash",
			what:         "claude agents --json",
			out:          Output{ExitCode: 2, Stderr: []byte("Error: not logged in\nsecond line")},
			err:          crash,
			wantKind:     nil,
			wantNotKinds: []error{ErrCLIUnavailable, ErrUsageLimitInForce},
			wantExit:     2,
			wantMessage:  []string{"failed (exit 2)", "not logged in", "exit status 2"},
		},
		{
			name:         "binary missing",
			what:         "claude --version",
			out:          Output{ExitCode: -1},
			err:          errors.New("exec: claude: executable file not found"),
			wantKind:     ErrCLIUnavailable,
			wantNotKinds: []error{ErrUsageLimitInForce},
			wantExit:     -1,
			wantMessage:  []string{"failed (exit -1)", "executable file not found"},
		},
		{
			name:         "cancelled",
			what:         "claude -p --resume",
			out:          Output{ExitCode: -1},
			err:          context.Canceled,
			wantKind:     ErrCLIUnavailable,
			wantNotKinds: []error{ErrUsageLimitInForce},
			wantExit:     -1,
			wantMessage:  []string{"context canceled"},
		},
		{
			name:         "deadline exceeded",
			what:         "claude -p --resume",
			out:          Output{ExitCode: -1},
			err:          context.DeadlineExceeded,
			wantKind:     ErrCLIUnavailable,
			wantNotKinds: []error{ErrUsageLimitInForce},
			wantExit:     -1,
			wantMessage:  []string{"context deadline exceeded"},
		},
		{
			// A limit noun with no decisive verb is not a refusal: prose that
			// merely mentions the usage limit must not park a session.
			name:         "a passing mention is not a refusal",
			what:         "claude -p --resume",
			out:          Output{Stdout: []byte("the usage limit is documented in the README"), ExitCode: 1},
			err:          errors.New("exit status 1"),
			wantKind:     nil,
			wantNotKinds: []error{ErrUsageLimitInForce, ErrCLIUnavailable},
			wantExit:     1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := describeRunError(tc.what, tc.out, tc.err)
			if err == nil {
				t.Fatal("describeRunError returned nil")
			}

			if tc.wantKind != nil && !errors.Is(err, tc.wantKind) {
				t.Errorf("errors.Is(%v) = false, want true", tc.wantKind)
			}
			for _, forbidden := range tc.wantNotKinds {
				if errors.Is(err, forbidden) {
					t.Errorf("errors.Is(%v) = true, must be false", forbidden)
				}
			}
			if tc.wantKind == nil {
				// No sentinel means a crash: the typed error is the only handle.
				var cliErr *CLIError
				if !errors.As(err, &cliErr) {
					t.Fatalf("error = %v, want *CLIError", err)
				}
				if cliErr.Kind != nil {
					t.Errorf("Kind = %v, want nil", cliErr.Kind)
				}
				if cliErr.ExitCode != tc.wantExit {
					t.Errorf("ExitCode = %d, want %d", cliErr.ExitCode, tc.wantExit)
				}
				if string(cliErr.Stdout) != string(tc.out.Stdout) {
					t.Errorf("Stdout = %q, want %q", cliErr.Stdout, tc.out.Stdout)
				}
				if string(cliErr.Stderr) != string(tc.out.Stderr) {
					t.Errorf("Stderr = %q, want %q", cliErr.Stderr, tc.out.Stderr)
				}
			}

			message := err.Error()
			for _, want := range tc.wantMessage {
				if !strings.Contains(message, want) {
					t.Errorf("message %q missing %q", message, want)
				}
			}
			for _, forbidden := range tc.wantNoOutput {
				if strings.Contains(message, forbidden) {
					t.Errorf("message %q must not contain %q", message, forbidden)
				}
			}
		})
	}
}

// TestResumeRefusalIsARecognisableCondition drives the production path: the CLI
// adapter must report a refused resume as a usage-limit condition, with the
// banner in the message and the captured streams on the error.
func TestResumeRefusalIsARecognisableCondition(t *testing.T) {
	t.Parallel()

	cli, _ := newTestCLI(t,
		Output{Stdout: []byte(testVersionOutput)},
		Output{Stdout: []byte(limitBanner), ExitCode: 1},
	)
	_, err := cli.Resume(context.Background(), testSessionID, "continue")
	if !errors.Is(err, ErrUsageLimitInForce) {
		t.Fatalf("error = %v, want ErrUsageLimitInForce", err)
	}
	if errors.Is(err, ErrCLIUnavailable) {
		t.Errorf("error = %v, must not claim the executable was unavailable", err)
	}
	if !strings.Contains(err.Error(), "10am (Europe/Berlin)") {
		t.Errorf("error = %v, want the reset banner", err)
	}

	var cliErr *CLIError
	if !errors.As(err, &cliErr) {
		t.Fatalf("error = %v, want *CLIError", err)
	}
	if cliErr.What != "claude -p --resume" {
		t.Errorf("What = %q, want %q", cliErr.What, "claude -p --resume")
	}
	if cliErr.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", cliErr.ExitCode)
	}
	if !strings.Contains(string(cliErr.Stdout), "usage limit reached") {
		t.Errorf("Stdout = %q, want the banner", cliErr.Stdout)
	}
}

// TestUsageLimitRecognition is the guard on the two-signal rule: a limit noun
// alone never identifies a refusal.
func TestUsageLimitRecognition(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		text string
		want bool
	}{
		{"canonical banner", "Claude usage limit reached. Your limit will reset at 10am.", true},
		{"five hour hit", "You've hit your 5-hour limit", true},
		{"weekly resets", "Your weekly limit resets in 2 hours", true},
		{"legacy epoch", "Claude AI usage limit reached|1730000000", true},
		{"paused goal", "Goal paused · usage limit reached · send a message after it resets", true},
		{"opus limit", "You've hit your Opus limit", true},
		{"noun only", "the usage limit is documented in the README", false},
		{"verb only", "the request was rejected", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isUsageLimitRefusal(Output{Stdout: []byte(tc.text)}); got != tc.want {
				t.Errorf("isUsageLimitRefusal(%q) = %v, want %v", tc.text, got, tc.want)
			}
			if got := isUsageLimitRefusal(Output{Stderr: []byte(tc.text)}); got != tc.want {
				t.Errorf("isUsageLimitRefusal(stderr %q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}
