package detector

import (
	"strings"
	"testing"
)

func TestExtractPausedGoal(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		in      string
		want    bool
		message string
	}{
		{
			name:    "canonical paused goal",
			in:      "Goal paused : usage limit reached : send a message after it resets",
			want:    true,
			message: "Goal paused : usage limit reached : send a message after it resets",
		},
		{
			name:    "session paused",
			in:      "the session has been paused by the usage limit",
			want:    true,
			message: "the session has been paused by the usage limit",
		},
		{
			name:    "task paused",
			in:      "your task was paused",
			want:    true,
			message: "your task was paused",
		},
		{
			name:    "work paused",
			in:      "work paused until the limit resets",
			want:    true,
			message: "work paused until the limit resets",
		},
		{
			name:    "paused until",
			in:      "paused until the limit resets at 12pm",
			want:    true,
			message: "paused until the limit resets at 12pm",
		},
		{
			name:    "paused due to",
			in:      "paused due to a usage limit",
			want:    true,
			message: "paused due to a usage limit",
		},
		{
			name: "running is not paused",
			in:   "the goal is running",
			want: false,
		},
		{
			name: "pause button prose",
			in:   "press pause to stop",
			want: false,
		},
		{
			name: "empty",
			in:   "",
			want: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, message := extractPausedGoal(tc.in)
			if got != tc.want {
				t.Fatalf("paused = %v, want %v", got, tc.want)
			}
			if !tc.want {
				if message != "" {
					t.Fatalf("message = %q, want empty", message)
				}
				return
			}
			if message != tc.message {
				t.Fatalf("message = %q, want %q", message, tc.message)
			}
		})
	}
}

func TestExtractPausedGoalTruncates(t *testing.T) {
	t.Parallel()

	long := "Goal paused : " + strings.Repeat("x", 500)
	paused, message := extractPausedGoal(long)
	if !paused {
		t.Fatal("expected a paused goal")
	}
	if len([]rune(message)) > 240 {
		t.Fatalf("message has %d runes, want at most 240", len([]rune(message)))
	}
}

func TestExtractPausedGoalKeepsMultiByteIntact(t *testing.T) {
	t.Parallel()

	long := "Goal paused : " + strings.Repeat("é", 400)
	_, message := extractPausedGoal(long)
	if !strings.HasPrefix(message, "Goal paused : ") {
		t.Fatalf("message = %q", message)
	}
	for _, r := range message {
		if r == '\uFFFD' {
			t.Fatalf("message contains a replacement rune: %q", message)
		}
	}
}
