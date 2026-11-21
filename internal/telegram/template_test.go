package telegram

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/storage"
)

// fullEventData is a maximal payload, so a template that forgets a field is
// visible in the rendering tests.
func fullEventData() EventData {
	return EventData{
		SessionID:   "11111111-1111-4111-8111-111111111111",
		ProjectPath: "/home/u/project",
		// PRD 6 R12: a session that has moved into a worktree carries two
		// directories, so the maximal payload carries both.
		RunsInPath:    "/home/u/project/.claude/worktrees/fix-auth",
		LimitID:       7,
		LimitType:     storage.LimitTypeFiveHour,
		ResetAt:       time.Date(2026, 1, 5, 11, 0, 0, 0, time.UTC),
		ResetTimezone: "Europe/Berlin",
		ResetRaw:      "12pm (Europe/Berlin)",
		AttemptID:     3,
		Attempt:       2,
		MaxAttempts:   10,
		Delay:         90 * time.Second,
		Reason:        "claude: claude executable unavailable\na second line",
		State:         "PERMANENTLY_FAILED",
		Duration:      4 * time.Minute,
	}
}

func TestEventsCoversEveryNotification(t *testing.T) {
	events := Events()
	// The count is exact on purpose: adding a message class is a decision, and
	// this line is where the decision has to be restated.
	if len(events) != 7 {
		t.Fatalf("Events() = %d events, want 7", len(events))
	}
	seen := map[storage.NotificationEvent]bool{}
	for _, event := range events {
		if !event.Valid() {
			t.Errorf("event %q is not a valid storage event", event)
		}
		if seen[event] {
			t.Errorf("event %q is listed twice", event)
		}
		seen[event] = true
		if Subject(event) == "" {
			t.Errorf("event %q has no subject", event)
		}
	}
}

func TestRenderProducesEveryTemplate(t *testing.T) {
	data := fullEventData()
	for _, event := range Events() {
		t.Run(string(event), func(t *testing.T) {
			body, err := Render(event, data)
			if err != nil {
				t.Fatalf("Render = %v, want nil", err)
			}
			if body == "" {
				t.Fatal("Render returned an empty body")
			}
			if !strings.Contains(body, Subject(event)) {
				t.Errorf("body %q does not carry the subject %q", body, Subject(event))
			}
			if !strings.Contains(body, "<code>"+data.SessionID+"</code>") {
				t.Errorf("body %q does not carry the session id", body)
			}
			if strings.Contains(body, "\n\n\n") {
				t.Errorf("body %q has a triple newline", body)
			}
			if !strings.HasSuffix(body, "\n") {
				t.Errorf("body %q does not end with a newline", body)
			}
		})
	}
}

func TestRenderEscapesEveryDynamicValue(t *testing.T) {
	hostile := "<script>alert('x')</script> & \"quoted\""
	data := EventData{
		SessionID:   hostile,
		ProjectPath: hostile,
		// Distinct from ProjectPath on purpose: writeProject only renders the
		// second directory when the two differ, so an equal value would leave
		// the new field untested for escaping (PRD 6 R12).
		RunsInPath: hostile + "/worktree",
		ResetRaw:   hostile,
		Reason:     hostile,
		State:      hostile,
	}
	for _, event := range Events() {
		body, err := Render(event, data)
		if err != nil {
			t.Fatalf("Render(%s) = %v", event, err)
		}
		if strings.Contains(body, "<script>") {
			t.Errorf("Render(%s) leaked unescaped markup: %q", event, body)
		}
		if !strings.Contains(body, "&lt;script&gt;") {
			t.Errorf("Render(%s) did not escape the hostile value: %q", event, body)
		}
	}
}

func TestRenderRejectsAnUnknownEvent(t *testing.T) {
	if _, err := Render(storage.NotificationEvent("nope"), fullEventData()); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("Render error = %v, want ErrInvalidEvent", err)
	}
}

func TestRenderResolvesTheResetInstantInItsOwnZone(t *testing.T) {
	body, err := Render(storage.EventLimitDetected, fullEventData())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(body, "12:00 CET") {
		t.Errorf("body %q does not show the reset in Europe/Berlin", body)
	}
	if !strings.Contains(body, "12pm (Europe/Berlin)") {
		t.Errorf("body %q does not preserve the original phrase", body)
	}
	if !strings.Contains(body, "5-hour window") {
		t.Errorf("body %q does not name the limit kind", body)
	}

	unknown := fullEventData()
	unknown.ResetAt = time.Time{}
	body, err = Render(storage.EventLimitDetected, unknown)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(body, "unknown") || !strings.Contains(body, "poll") {
		t.Errorf("body %q does not explain that the reset is unknown", body)
	}

	badZone := fullEventData()
	badZone.ResetTimezone = "Not/AZone"
	body, err = Render(storage.EventLimitDetected, badZone)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(body, "UTC") {
		t.Errorf("body %q does not fall back to UTC", body)
	}
}

func TestRenderOmitsUnknownDetails(t *testing.T) {
	body, err := Render(storage.EventRecoveryFailure, EventData{SessionID: "s"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(body, "Reason:") {
		t.Errorf("body %q invented a reason", body)
	}
	if strings.Contains(body, "Next attempt in") {
		t.Errorf("body %q invented a retry delay", body)
	}
	if !strings.Contains(body, "unknown") {
		t.Errorf("body %q does not report an unknown attempt", body)
	}

	body, err = Render(storage.EventPermanentFailure, EventData{SessionID: "s"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(body, "ccresu retry s") {
		t.Errorf("body %q does not tell the operator how to retry", body)
	}

	body, err = Render(storage.EventSessionCompleted, EventData{SessionID: "s"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(body, "Project:") || strings.Contains(body, "State:") {
		t.Errorf("body %q invented optional details", body)
	}
}

func TestFirstLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"   ", ""},
		{"one", "one"},
		{"\n\n  first  \nsecond", "first"},
		{"  \n second \n third ", "second"},
	}
	for _, tc := range cases {
		if got := firstLine(tc.in); got != tc.want {
			t.Errorf("firstLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAttemptText(t *testing.T) {
	cases := []struct {
		data EventData
		want string
	}{
		{EventData{Attempt: 2, MaxAttempts: 10}, "2/10"},
		{EventData{Attempt: 3}, "3"},
		{EventData{}, "unknown"},
	}
	for _, tc := range cases {
		if got := attemptText(tc.data); got != tc.want {
			t.Errorf("attemptText(%+v) = %q, want %q", tc.data, got, tc.want)
		}
	}
}

func TestDedupeKeyIdentifiesOneLogicalEvent(t *testing.T) {
	session := "11111111-1111-4111-8111-111111111111"

	limit := EventData{SessionID: session, LimitID: 7}
	if got := DedupeKey(storage.EventLimitDetected, limit); got != string(storage.EventLimitDetected)+":"+session+":7" {
		t.Errorf("limit key = %q", got)
	}
	waiting := DedupeKey(storage.EventWaiting, limit)
	if waiting == DedupeKey(storage.EventLimitDetected, limit) {
		t.Error("different events must have different keys")
	}
	if waiting != DedupeKey(storage.EventWaiting, limit) {
		t.Error("the key must be stable")
	}

	attempt := EventData{SessionID: session, AttemptID: 3, Attempt: 2}
	first := DedupeKey(storage.EventRecoveryStarted, attempt)
	if first != string(storage.EventRecoveryStarted)+":"+session+":3" {
		t.Errorf("attempt key = %q", first)
	}
	byNumber := EventData{SessionID: session, Attempt: 2}
	if got := DedupeKey(storage.EventRecoveryStarted, byNumber); got != string(storage.EventRecoveryStarted)+":"+session+":2" {
		t.Errorf("attempt-number key = %q", got)
	}

	completed := DedupeKey(storage.EventSessionCompleted, EventData{SessionID: session})
	if completed != string(storage.EventSessionCompleted)+":"+session {
		t.Errorf("completed key = %q", completed)
	}

	noLimit := DedupeKey(storage.EventLimitDetected, EventData{SessionID: session})
	if noLimit == DedupeKey(storage.EventLimitDetected, limit) {
		t.Error("a limit id must change the key")
	}

	if key := DedupeKey(storage.EventSessionCompleted, EventData{SessionID: session}); key == DedupeKey(storage.EventLimitDetected, limit) {
		t.Error("session_completed must not share a limit_detected key")
	}
}

func TestSubjectOfUnknownEvent(t *testing.T) {
	if got := Subject(storage.NotificationEvent("nope")); got != "" {
		t.Errorf("Subject = %q, want an empty string", got)
	}
}

// TestRenderShowsTheTwoDirectoriesDistinctly pins PRD 6 R12 at the notification
// surface: a message names where the session started and, when that is not where
// it runs, where it runs as well.
//
// The three cases are the three facts the template has to keep apart — a session
// that moved, a session that never moved, and a session nobody has observed yet.
// The last two must not print a second line, because repeating one path under two
// labels is noise and an unobserved directory is not the project directory.
func TestRenderShowsTheTwoDirectoriesDistinctly(t *testing.T) {
	// EventLimitDetected is one of the two messages that picks up the
	// directories from the payload (the other is session_completed).
	const event = storage.EventLimitDetected

	moved := fullEventData()
	body, err := Render(event, moved)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if want := "Project: " + Code(moved.ProjectPath); !strings.Contains(body, want) {
		t.Errorf("body %q does not carry %q", body, want)
	}
	if want := "Runs in: " + Code(moved.RunsInPath); !strings.Contains(body, want) {
		t.Errorf("body %q does not carry the run directory as %q", body, want)
	}

	stayed := fullEventData()
	stayed.RunsInPath = stayed.ProjectPath
	body, err = Render(event, stayed)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(body, "Runs in:") {
		t.Errorf("body %q repeats the project directory under a second label", body)
	}
	if got := strings.Count(body, Code(stayed.ProjectPath)); got != 1 {
		t.Errorf("the project path appears %d times, want once: %q", got, body)
	}

	unobserved := fullEventData()
	unobserved.RunsInPath = ""
	body, err = Render(event, unobserved)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(body, "Runs in:") {
		t.Errorf("body %q invents a run directory nobody observed", body)
	}
	if want := "Project: " + Code(unobserved.ProjectPath); !strings.Contains(body, want) {
		t.Errorf("body %q lost the project directory", body)
	}
}

// TestRecoveryMessagesDescribeTheBlockingResume pins the two recovery bodies to
// the resume ccresu actually runs.
//
// A recovery is one blocking command, `claude --resume <session-id>
// "<continuation message>"`, and its exit status is the verdict. The started
// message therefore names the invocation without the continuation message — the
// prompt can be long or sensitive, and the session id is the part a reader
// copies or checks — and the success message says how the outcome was decided
// rather than describing a background dispatch that no longer exists. The
// success sentence is quoted verbatim in docs/telegram.md, so a template that
// drifted from it would leave the manual wrong as well.
func TestRecoveryMessagesDescribeTheBlockingResume(t *testing.T) {
	data := fullEventData()

	started, err := Render(storage.EventRecoveryStarted, data)
	if err != nil {
		t.Fatalf("Render(recovery_started) = %v, want nil", err)
	}
	invocation := "<code>claude --resume " + data.SessionID + "</code>."
	if !strings.Contains(started, invocation) {
		t.Errorf("recovery_started body %q does not name the invocation %q", started, invocation)
	}
	if !strings.Contains(started, "exit status") {
		t.Errorf("recovery_started body %q does not say the exit status decides", started)
	}

	succeeded, err := Render(storage.EventRecoverySuccess, data)
	if err != nil {
		t.Fatalf("Render(recovery_success) = %v, want nil", err)
	}
	want := "The original conversation continued in its own session, and the resume exited 0."
	if !strings.Contains(succeeded, want) {
		t.Errorf("recovery_success body %q does not state the verdict as %q", succeeded, want)
	}
}
