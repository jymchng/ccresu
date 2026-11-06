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
		SessionID:     "11111111-1111-4111-8111-111111111111",
		ProjectPath:   "/home/u/project",
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
		ResetRaw:    hostile,
		Reason:      hostile,
		State:       hostile,
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

// TestRecoveryStartedNamesTheDispatchThatRuns is the regression test for an
// operator-facing lie: the message told the operator that ccresu was resuming
// with the retired print-mode command, while the resume had become a background
// dispatch. The body has to name the command that is actually executed.
func TestRecoveryStartedNamesTheDispatchThatRuns(t *testing.T) {
	data := fullEventData()
	body, err := Render(storage.EventRecoveryStarted, data)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := "claude --resume " + data.SessionID + " --bg"
	if !strings.Contains(body, want) {
		t.Errorf("body %q does not name the dispatch %q", body, want)
	}
	for _, retired := range []string{"-p ", "--print", "--output-format"} {
		if strings.Contains(body, retired) {
			t.Errorf("body %q still advertises the retired print-mode resume (%q)", body, retired)
		}
	}
	if !strings.Contains(body, "session id of its own") {
		t.Errorf("body %q does not say the job continues under its own session id", body)
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
}

func TestSubjectOfUnknownEvent(t *testing.T) {
	if got := Subject(storage.NotificationEvent("nope")); got != "" {
		t.Errorf("Subject = %q, want an empty string", got)
	}
}
