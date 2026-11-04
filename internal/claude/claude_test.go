package claude

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const testSessionID = "3f1e2c7a-8b4d-4c5e-9f01-2a3b4c5d6e7f"

const testOtherSessionID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"

const testUnsupportedSessionID = "cccccccc-1111-4222-8333-444444444444"

const testAnonymousStateID = "agent-state-4"

// testVersionOutput is the verified `claude --version` shape.
const testVersionOutput = "2.1.223 (Claude Code)\n"

// testAgentsJSON is the verified `claude agents --json` shape.
const testAgentsJSON = `[
  {
    "pid": 4242,
    "id": "agent-state-1",
    "cwd": "/root/project",
    "kind": "interactive",
    "startedAt": 1750000000000,
    "sessionId": "` + testSessionID + `",
    "name": "refactor things",
    "status": "busy",
    "state": "working"
  },
  {
    "id": "agent-state-2",
    "cwd": "/root/project",
    "kind": "background",
    "startedAt": 1749999999000,
    "sessionId": "` + testOtherSessionID + `",
    "status": "waiting",
    "waitingFor": "usage limit reset",
    "state": "blocked"
  },
  {
    "id": "agent-state-3",
    "cwd": "/root/other",
    "kind": "galaxy-brain",
    "startedAt": 1749999998000,
    "sessionId": "` + testUnsupportedSessionID + `"
  },
  {
    "id": "agent-state-4",
    "cwd": "/root/anonymous",
    "kind": "interactive",
    "startedAt": 1749999997000,
    "status": "busy"
  }
]`

// testResumeJSON is the verified `-p --output-format json` shape.
const testResumeJSON = `{"type":"result","subtype":"success","is_error":false,` +
	`"duration_ms":1234,"num_turns":2,"result":"continued the goal",` +
	`"session_id":"` + testSessionID + `","total_cost_usd":0.0123,` +
	`"modelUsage":{"claude-sonnet-4-5":{"inputTokens":10}}}`

// newTestCLI builds a CLI backed by a scripted execer.
func newTestCLI(t *testing.T, outputs ...Output) (*CLI, *FakeExecer) {
	t.Helper()
	execer := NewScriptedExecer(outputs, nil, Output{}, errors.New("unexpected extra invocation"))
	cli := NewCLI(CLIOptions{Executable: "claude", Execer: execer})
	return cli, execer
}

func TestValidSessionID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		id   string
		want bool
	}{
		{testSessionID, true},
		{strings.ToUpper(testSessionID), true},
		{"", false},
		{"not-a-uuid", false},
		{"--dangerously-skip-permissions", false},
		{"../etc/passwd", false},
		{testSessionID + "x", false},
	}
	for _, tc := range cases {
		if got := ValidSessionID(tc.id); got != tc.want {
			t.Errorf("ValidSessionID(%q) = %v, want %v", tc.id, got, tc.want)
		}
		if err := ValidateSessionID(tc.id); (err == nil) != tc.want {
			t.Errorf("ValidateSessionID(%q) error = %v, want valid=%v", tc.id, err, tc.want)
		}
	}
}

func TestSessionHelpers(t *testing.T) {
	t.Parallel()

	session := Session{ID: testSessionID, Type: SessionTypeInteractive, Kind: "interactive", Recoverable: true}
	if got := session.RecoveryCode(); got != RecoveryRecoverable {
		t.Errorf("RecoveryCode() = %q, want %q", got, RecoveryRecoverable)
	}
	if session.Unrecoverable() {
		t.Error("recoverable session reported as unrecoverable")
	}
	if !strings.Contains(session.String(), RecoveryRecoverable) {
		t.Errorf("String() = %q, want it to mention the recovery code", session.String())
	}

	broken := Session{
		ID:          "unidentified:x",
		Type:        SessionTypeUnknown,
		Kind:        "galaxy-brain",
		ProjectPath: "/root/other",
		Reason:      "unsupported session type",
	}
	if got := broken.RecoveryCode(); got != RecoveryUnrecoverable {
		t.Errorf("RecoveryCode() = %q, want %q", got, RecoveryUnrecoverable)
	}
	if !broken.Unrecoverable() {
		t.Error("unrecoverable session reported as recoverable")
	}
	if !strings.Contains(broken.String(), "UNRECOVERABLE") || !strings.Contains(broken.String(), "unsupported session type") {
		t.Errorf("String() = %q, want it to include the code and reason", broken.String())
	}
}

func TestDiscoverFilterMatches(t *testing.T) {
	t.Parallel()

	recoverable := Session{ID: testSessionID, ProjectPath: "/root/project", Recoverable: true}
	broken := Session{ID: testOtherSessionID, ProjectPath: "/root/project/sub", Recoverable: false}

	cases := []struct {
		name   string
		filter DiscoverFilter
		want   bool
	}{
		{"zero value keeps everything", DiscoverFilter{}, true},
		{"recoverable only keeps recoverable", DiscoverFilter{RecoverableOnly: true}, true},
		{"cwd prefix keeps nested", DiscoverFilter{UnderCWD: "/root/project"}, true},
		{"cwd mismatch drops", DiscoverFilter{UnderCWD: "/elsewhere"}, false},
		{"cwd trailing slash normalised", DiscoverFilter{UnderCWD: "/root/project/"}, true},
		{"ids keep match", DiscoverFilter{IDs: []string{testSessionID}}, true},
		{"ids drop mismatch", DiscoverFilter{IDs: []string{"nope"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.filter.matches(recoverable); got != tc.want {
				t.Errorf("matches(%v) = %v, want %v", recoverable, got, tc.want)
			}
		})
	}

	if (DiscoverFilter{RecoverableOnly: true}).matches(broken) {
		t.Error("RecoverableOnly kept an unrecoverable session")
	}
	if (DiscoverFilter{UnderCWD: "/root/project"}).matches(Session{ProjectPath: ""}) {
		t.Error("UnderCWD kept a session with no project path")
	}
}

func TestUnderPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		p, root string
		want    bool
	}{
		{"/a/b", "/a/b", true},
		{"/a/b/c", "/a/b", true},
		{"/a/bc", "/a/b", false},
		{"/a", "/a/b", false},
		{"", "/a", false},
	}
	for _, tc := range cases {
		if got := underPath(tc.p, tc.root); got != tc.want {
			t.Errorf("underPath(%q, %q) = %v, want %v", tc.p, tc.root, got, tc.want)
		}
	}
}

func TestSortSessions(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	sessions := []Session{
		{ID: "b", LastActivity: base},
		{ID: "a", LastActivity: base},
		{ID: "c", LastActivity: base.Add(time.Minute)},
		{ID: "d"},
		{ID: "e", StartedAt: base.Add(-time.Hour)},
	}
	sortSessions(sessions)

	got := make([]string, len(sessions))
	for i, session := range sessions {
		got[i] = session.ID
	}
	want := []string{"c", "a", "b", "e", "d"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sorted ids = %v, want %v", got, want)
		}
	}
}

func TestParseAgentsJSON(t *testing.T) {
	t.Parallel()

	entries, err := ParseAgentsJSON([]byte(testAgentsJSON))
	if err != nil {
		t.Fatalf("ParseAgentsJSON() error = %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("entries = %d, want 4", len(entries))
	}
	first := entries[0]
	if first.SessionID != testSessionID {
		t.Errorf("SessionID = %q, want %q", first.SessionID, testSessionID)
	}
	if first.StateID != "agent-state-1" {
		t.Errorf("StateID = %q, want agent-state-1", first.StateID)
	}
	if first.Kind != "interactive" || first.Status != "busy" || first.State != "working" {
		t.Errorf("unexpected mapping: %+v", first)
	}
	if first.PID != 4242 {
		t.Errorf("PID = %d, want 4242", first.PID)
	}
	if first.CWD != "/root/project" || first.Name != "refactor things" {
		t.Errorf("unexpected path/name: %+v", first)
	}
	wantStart := time.UnixMilli(1750000000000).UTC()
	if !first.StartedAt.Equal(wantStart) {
		t.Errorf("StartedAt = %v, want %v", first.StartedAt, wantStart)
	}
	if entries[1].WaitingFor != "usage limit reset" || entries[1].State != "blocked" {
		t.Errorf("second entry not mapped: %+v", entries[1])
	}
	if entries[2].Kind != "galaxy-brain" || entries[2].SessionID != testUnsupportedSessionID {
		t.Errorf("third entry not mapped: %+v", entries[2])
	}
	if entries[3].SessionID != "" || entries[3].CWD != "/root/anonymous" {
		t.Errorf("fourth entry not mapped: %+v", entries[3])
	}
	if len(first.Raw) == 0 {
		t.Error("Raw was not preserved")
	}
}

func TestParseAgentsJSONTolerant(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		input     string
		wantCount int
		wantErr   bool
	}{
		{name: "empty array", input: "[]", wantCount: 0},
		{name: "null", input: "null", wantCount: 0},
		{name: "blank", input: "   ", wantErr: true},
		{name: "truncated", input: `[{"id":`, wantErr: true},
		{name: "not json", input: "no sessions", wantErr: true},
		{
			name:      "single object",
			input:     `{"sessionId":"` + testSessionID + `","kind":"interactive"}`,
			wantCount: 1,
		},
		{
			name:      "sessions envelope",
			input:     `{"sessions":[{"sessionId":"` + testSessionID + `"}]}`,
			wantCount: 1,
		},
		{
			name:      "agents envelope",
			input:     `{"agents":[{"sessionId":"` + testSessionID + `"},{"sessionId":"` + testOtherSessionID + `"}]}`,
			wantCount: 2,
		},
		{
			name:      "alternate keys and types",
			input:     `[{"session_id":"` + testSessionID + `","workingDirectory":"/w","type":"BACKGROUND","processId":"77","createdAt":"2026-06-01T10:00:00Z"}]`,
			wantCount: 1,
		},
		{
			name:      "epoch seconds and microseconds",
			input:     `[{"sessionId":"` + testSessionID + `","startedAt":1750000000},{"sessionId":"` + testOtherSessionID + `","startedAt":1750000000000000}]`,
			wantCount: 2,
		},
		{
			name:      "garbage elements are skipped",
			input:     `[null, 3, {"sessionId":"` + testSessionID + `"}]`,
			wantCount: 1,
		},
		{
			name:      "nested id object",
			input:     `[{"id":{"id":"state-9"},"sessionId":"` + testSessionID + `"}]`,
			wantCount: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			entries, err := ParseAgentsJSON([]byte(tc.input))
			if tc.wantErr {
				if !errors.Is(err, ErrMalformedOutput) {
					t.Fatalf("error = %v, want ErrMalformedOutput", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(entries) != tc.wantCount {
				t.Fatalf("entries = %d, want %d (%+v)", len(entries), tc.wantCount, entries)
			}
		})
	}
}

func TestFlexTimeAndInt(t *testing.T) {
	t.Parallel()

	var payload struct {
		Time  flexTime `json:"t"`
		Count flexInt  `json:"c"`
	}
	if err := json.Unmarshal([]byte(`{"t":1750000000000,"c":"12"}`), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Count.Int() != 12 {
		t.Errorf("Count = %d, want 12", payload.Count.Int())
	}
	if !payload.Time.Time().Equal(time.UnixMilli(1750000000000).UTC()) {
		t.Errorf("Time = %v", payload.Time.Time())
	}

	for _, input := range []string{`{"t":null,"c":null}`, `{"t":true,"c":{}}`, `{"t":"nonsense","c":"x"}`, `{"t":-5,"c":false}`} {
		var probe struct {
			Time  flexTime `json:"t"`
			Count flexInt  `json:"c"`
		}
		if err := json.Unmarshal([]byte(input), &probe); err != nil {
			t.Fatalf("unmarshal(%s): %v", input, err)
		}
		if !probe.Time.Time().IsZero() {
			t.Errorf("unmarshal(%s): expected zero time, got %v", input, probe.Time.Time())
		}
		if probe.Count.Int() != 0 {
			t.Errorf("unmarshal(%s): expected zero count, got %d", input, probe.Count.Int())
		}
	}
}

func TestEpochTime(t *testing.T) {
	t.Parallel()
	if _, ok := epochTime(0); ok {
		t.Error("epochTime(0) should be rejected")
	}
	if _, ok := epochTime(42); ok {
		t.Error("epochTime(42) should be rejected")
	}
	if t2, ok := epochTime(1750000000); !ok || !t2.Equal(time.Unix(1750000000, 0).UTC()) {
		t.Errorf("epoch seconds mapping wrong: %v %v", t2, ok)
	}
	if _, ok := epochTime(17_500_000_000_000_000); !ok {
		t.Error("microseconds should map")
	}
	if _, ok := epochTime(1_750_000_000_000_000_000); !ok {
		t.Error("nanoseconds should map") //nolint:staticcheck // deliberate out-of-range probe
	}
}

func TestSessionRunning(t *testing.T) {
	t.Parallel()

	cases := []struct {
		session Session
		want    bool
	}{
		{Session{State: "working"}, true},
		{Session{State: "blocked"}, true},
		{Session{State: "done"}, false},
		{Session{State: "failed"}, false},
		{Session{State: "stopped"}, false},
		{Session{Status: "busy"}, true},
		{Session{Status: "waiting"}, true},
		{Session{Status: "idle"}, false},
		{Session{PID: 12}, true},
		{Session{}, false},
	}
	for _, tc := range cases {
		if got := sessionRunning(tc.session); got != tc.want {
			t.Errorf("sessionRunning(%+v) = %v, want %v", tc.session, got, tc.want)
		}
	}
}

func TestFakeProviderBehaviour(t *testing.T) {
	t.Parallel()

	session := Session{ID: testSessionID, Type: SessionTypeInteractive, Recoverable: true, Status: "busy"}
	fake := NewFakeProvider(session)
	ctx := context.Background()

	sessions, err := fake.Discover(ctx, DiscoverFilter{})
	if err != nil || len(sessions) != 1 {
		t.Fatalf("Discover() = %v, %v", sessions, err)
	}
	if _, err := fake.GetSession(ctx, testSessionID); err != nil {
		t.Fatalf("GetSession() error = %v", err)
	}
	if _, err := fake.GetSession(ctx, "unknown"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("GetSession(unknown) error = %v, want ErrSessionNotFound", err)
	}
	status, err := fake.GetSessionStatus(ctx, testSessionID)
	if err != nil {
		t.Fatalf("GetSessionStatus() error = %v", err)
	}
	if !status.Running {
		t.Error("expected the busy session to be running")
	}
	result, err := fake.ResumeSession(ctx, testSessionID, "go")
	if err != nil {
		t.Fatalf("ResumeSession() error = %v", err)
	}
	if !result.Verified || result.SessionID != testSessionID {
		t.Errorf("resume result not verified: %+v", result)
	}

	if got := len(fake.Calls()); got != 5 {
		t.Errorf("recorded %d calls, want 5", got)
	}

	// Unrecoverable sessions refuse to resume.
	broken := Session{ID: testOtherSessionID, Reason: "unsupported session type", Recoverable: false}
	fake.SetSession(broken)
	if _, err := fake.ResumeSession(ctx, testOtherSessionID, "go"); !errors.Is(err, ErrUnrecoverable) {
		t.Errorf("ResumeSession(unrecoverable) error = %v, want ErrUnrecoverable", err)
	}

	// Injected failures surface unchanged.
	fake.DiscoverErr = errors.New("boom")
	if _, err := fake.Discover(ctx, DiscoverFilter{}); err == nil {
		t.Error("expected injected discover error")
	}

	// A cancelled context is reported before anything else.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fake.GetSession(cancelled, testSessionID); !errors.Is(err, context.Canceled) {
		t.Errorf("GetSession(cancelled) error = %v, want context.Canceled", err)
	}
}

func TestFakeExecerScripted(t *testing.T) {
	t.Parallel()

	execer := NewScriptedExecer(
		[]Output{{Stdout: []byte("one")}},
		[]error{nil},
		Output{Stdout: []byte("fallback")},
		nil,
	)
	out, err := execer.Run(context.Background(), "claude", []string{"--version"}, RunOptions{})
	if err != nil || string(out.Stdout) != "one" {
		t.Fatalf("first run = %q, %v", out.Stdout, err)
	}
	out, err = execer.Run(context.Background(), "claude", []string{"--version"}, RunOptions{})
	if err != nil || string(out.Stdout) != "fallback" {
		t.Fatalf("second run = %q, %v", out.Stdout, err)
	}
	calls := execer.Calls()
	if len(calls) != 2 || calls[0].Name != "claude" || calls[0].Args[0] != "--version" {
		t.Fatalf("unexpected calls: %+v", calls)
	}
	if _, ok := execer.LastCall(); !ok {
		t.Error("LastCall() reported no calls")
	}
}
