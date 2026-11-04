package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/config"
	"github.com/jymchng/ccresu/internal/storage"
)

// TestHealth proves the liveness endpoint answers without touching anything.
func TestHealth(t *testing.T) {
	h := newHarness(t)
	var body map[string]any
	h.get("/health").expect(http.StatusOK).decode(&body)
	if body["status"] != "ok" {
		t.Errorf("status = %v", body["status"])
	}
	if body["api"] != APIPrefix {
		t.Errorf("api = %v, want %q", body["api"], APIPrefix)
	}
	if _, ok := body["uptime_seconds"]; !ok {
		t.Error("uptime_seconds is missing")
	}
}

// TestVersionWithoutAProbe proves the endpoint degrades instead of failing when
// no version probe is wired.
func TestVersionWithoutAProbe(t *testing.T) {
	h := newHarness(t)
	var body versionResponse
	h.get("/version").expect(http.StatusOK).decode(&body)
	if body.Version != "1.4.0" || body.Commit != "cafebabe" || body.BuildDate != "2026-03-01" {
		t.Errorf("build metadata = %+v", body)
	}
	if body.API != APIPrefix || body.Go == "" || body.Platform == "" {
		t.Errorf("body = %+v", body)
	}
	if body.Claude != nil {
		t.Errorf("claude = %+v, want it omitted without a probe", body.Claude)
	}
}

// TestVersionWithAProbe covers both a working and a failing probe.
func TestVersionWithAProbe(t *testing.T) {
	h := newHarness(t, func(c *Config, _ *configConfigAlias) {
		c.VersionProbe = fakeProbe{report: claude.VersionReport{Version: parsedVersion("2.1.223")}}
	})
	var body versionResponse
	h.get("/version").expect(http.StatusOK).decode(&body)
	if body.Claude == nil || !body.Claude.Available {
		t.Fatalf("claude = %+v, want it available", body.Claude)
	}
	if body.Claude.Version != "2.1.223" {
		t.Errorf("claude version = %q", body.Claude.Version)
	}

	failing := newHarness(t, func(c *Config, _ *configConfigAlias) {
		c.VersionProbe = fakeProbe{err: errors.New("claude is not installed")}
	})
	var failed versionResponse
	failing.get("/version").expect(http.StatusOK).decode(&failed)
	if failed.Claude == nil || failed.Claude.Available {
		t.Fatalf("claude = %+v, want it unavailable", failed.Claude)
	}
	if !strings.Contains(failed.Claude.Error, "not installed") {
		t.Errorf("error = %q", failed.Claude.Error)
	}
}

// TestStatus proves the summary reports the session histogram and the queue
// counts.
func TestStatus(t *testing.T) {
	h := newHarness(t)
	h.seedSession(sessionID, func(s *storage.Session) { s.State = storage.StateRunning })
	h.seedSession(otherSessionID, func(s *storage.Session) {
		s.State = storage.StateWaitingForReset
		s.Enabled = false
	})

	// One persisted schedule and one queued notification.
	if _, err := h.store.SaveSchedule(t.Context(), storage.Schedule{
		SessionID: otherSessionID,
		ResetAt:   baseTime.Add(2 * time.Hour),
		Unknown:   false,
	}); err != nil {
		t.Fatalf("save schedule: %v", err)
	}
	if _, _, err := h.store.EnqueueNotification(t.Context(), storage.Notification{
		Event:     storage.EventLimitDetected,
		SessionID: sessionID,
		DedupeKey: "limit:" + sessionID,
		Body:      "hello",
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	var body statusReport
	h.get("/status").expect(http.StatusOK).decode(&body)
	if body.Sessions != 2 || body.EnabledSessions != 1 {
		t.Errorf("sessions = %d enabled = %d", body.Sessions, body.EnabledSessions)
	}
	if body.States[string(storage.StateRunning)] != 1 || body.States[string(storage.StateWaitingForReset)] != 1 {
		t.Errorf("states = %+v", body.States)
	}
	if body.Schedules != 1 {
		t.Errorf("schedules = %d", body.Schedules)
	}
	if body.NextScheduleAt == nil {
		t.Error("next_schedule_at is missing")
	}
	if body.PendingNotifications != 1 {
		t.Errorf("pending notifications = %d", body.PendingNotifications)
	}
	if !body.RecoveryEnabled || body.MaxAttempts != 3 {
		t.Errorf("recovery = %v max attempts = %d", body.RecoveryEnabled, body.MaxAttempts)
	}
	if !body.Monitoring {
		t.Error("monitoring = false")
	}
	if body.ConfigPath != "/tmp/ccresu/config.yaml" {
		t.Errorf("config path = %q", body.ConfigPath)
	}
	if body.DatabasePath == "" {
		t.Error("database path is empty")
	}
	if body.Version != "1.4.0" {
		t.Errorf("version = %q", body.Version)
	}
	if body.UptimeSeconds < 0 {
		t.Errorf("uptime = %v", body.UptimeSeconds)
	}
}

// TestSessionsListAndFilters proves every documented filter works and a bad one
// is refused.
func TestSessionsListAndFilters(t *testing.T) {
	h := newHarness(t)
	h.seedSession(sessionID, func(s *storage.Session) {
		s.State = storage.StateRunning
		s.Enabled = true
		s.Recoverable = true
	})
	h.seedSession(otherSessionID, func(s *storage.Session) {
		s.State = storage.StateDisabled
		s.Enabled = false
		s.Recoverable = false
	})

	var all struct {
		Count    int          `json:"count"`
		Sessions []sessionDTO `json:"sessions"`
	}
	h.get("/sessions").expect(http.StatusOK).decode(&all)
	if all.Count != 2 || len(all.Sessions) != 2 {
		t.Fatalf("count = %d sessions = %d", all.Count, len(all.Sessions))
	}

	var enabledOnly struct {
		Count    int          `json:"count"`
		Sessions []sessionDTO `json:"sessions"`
	}
	h.get("/sessions?enabled=true").expect(http.StatusOK).decode(&enabledOnly)
	if enabledOnly.Count != 1 || enabledOnly.Sessions[0].ID != sessionID {
		t.Fatalf("enabled filter = %+v", enabledOnly)
	}

	var byState struct {
		Count    int          `json:"count"`
		Sessions []sessionDTO `json:"sessions"`
	}
	h.get("/sessions?state=disabled").expect(http.StatusOK).decode(&byState)
	if byState.Count != 1 || byState.Sessions[0].ID != otherSessionID {
		t.Fatalf("state filter = %+v", byState)
	}

	var recoverable struct {
		Count int `json:"count"`
	}
	h.get("/sessions?recoverable=false").expect(http.StatusOK).decode(&recoverable)
	if recoverable.Count != 1 {
		t.Fatalf("recoverable filter count = %d", recoverable.Count)
	}

	h.get("/sessions?limit=1").expect(http.StatusOK)
	h.get("/sessions?state=").expect(http.StatusOK)

	h.get("/sessions?limit=0").expect(http.StatusBadRequest)
	h.get("/sessions?limit=9999").expect(http.StatusBadRequest)
	h.get("/sessions?limit=abc").expect(http.StatusBadRequest)
	h.get("/sessions?state=NOPE").expect(http.StatusBadRequest)
	h.get("/sessions?enabled=maybe").expect(http.StatusBadRequest)
}

// TestSessionDetail proves one session comes back with its context, and that a
// missing or malformed id is distinguished.
func TestSessionDetail(t *testing.T) {
	h := newHarness(t)
	h.seedSession(sessionID, nil)
	if _, err := h.store.RecordUsageLimit(t.Context(), storage.UsageLimit{
		SessionID:  sessionID,
		LimitType:  storage.LimitTypeFiveHour,
		ResetRaw:   "12pm (Europe/Berlin)",
		DetectedAt: baseTime,
	}); err != nil {
		t.Fatalf("record limit: %v", err)
	}

	var detail struct {
		Session     sessionDTO      `json:"session"`
		UsageLimits []usageLimitDTO `json:"usage_limits"`
	}
	h.get("/sessions/" + sessionID).expect(http.StatusOK).decode(&detail)
	if detail.Session.ID != sessionID {
		t.Errorf("session id = %q", detail.Session.ID)
	}
	if len(detail.UsageLimits) != 1 || detail.UsageLimits[0].LimitType != string(storage.LimitTypeFiveHour) {
		t.Errorf("usage limits = %+v", detail.UsageLimits)
	}

	missing := h.get("/sessions/11111111-1111-4111-8111-111111111111")
	missing.expect(http.StatusNotFound)
	if code := missing.errorOf().Code; code != codeNotFound {
		t.Errorf("code = %q", code)
	}

	// A traversal attempt never reaches a lookup: it changes the path shape, so
	// the router does not match it at all.
	h.get("/sessions/" + "%2E%2E%2Fetc%2Fpasswd").expect(http.StatusNotFound)
	// A same-shaped id with a forbidden character is a client error.
	h.get("/sessions/a$b").expect(http.StatusBadRequest)
	h.get("/sessions/" + strings.Repeat("a", 200)).expect(http.StatusBadRequest)
}

// TestHistory proves the audit trail is returned and validated.
func TestHistory(t *testing.T) {
	h := newHarness(t)
	h.seedSession(sessionID, nil)
	if _, err := h.store.TransitionSession(t.Context(), sessionID, storage.StateUsageLimitDetected, "limit found", 0); err != nil {
		t.Fatalf("transition: %v", err)
	}

	var body struct {
		SessionID   string          `json:"session_id"`
		State       string          `json:"state"`
		Attempts    []attemptDTO    `json:"attempts"`
		Transitions []transitionDTO `json:"transitions"`
	}
	h.get("/sessions/" + sessionID + "/history").expect(http.StatusOK).decode(&body)
	if body.SessionID != sessionID {
		t.Errorf("session id = %q", body.SessionID)
	}
	if body.State != string(storage.StateUsageLimitDetected) {
		t.Errorf("state = %q", body.State)
	}
	if len(body.Transitions) != 1 {
		t.Errorf("transitions = %+v, want the recorded limit", body.Transitions)
	}
	if body.Transitions[len(body.Transitions)-1].ToState != string(storage.StateUsageLimitDetected) {
		t.Errorf("last transition = %+v", body.Transitions[len(body.Transitions)-1])
	}

	h.get("/sessions/" + sessionID + "/history?limit=1").expect(http.StatusOK)
	h.get("/sessions/" + sessionID + "/history?limit=x").expect(http.StatusBadRequest)
	h.get("/sessions/11111111-1111-4111-8111-111111111111/history").expect(http.StatusNotFound)
}

// TestRecoverEndpoint proves a real recovery runs and reports the proof.
func TestRecoverEndpoint(t *testing.T) {
	h := newHarness(t)
	h.makeRecoverable(sessionID)

	var body outcomeDTO
	h.do(http.MethodPost, "/sessions/"+sessionID+"/recover", nil).expect(http.StatusOK).decode(&body)
	if body.SessionID != sessionID {
		t.Errorf("session id = %q", body.SessionID)
	}
	if !body.Resumed {
		t.Errorf("resumed = false: %+v", body)
	}
	if !body.Verification.Proven || !body.Verification.SessionIDPreserved || !body.Verification.TranscriptGrew {
		t.Errorf("verification = %+v", body.Verification)
	}
	if body.State != string(storage.StateResumed) {
		t.Errorf("state = %q, want RESUMED", body.State)
	}
	if body.Attempt.ID == 0 {
		t.Error("the attempt was not persisted")
	}

	// The provider was asked to resume exactly once, with the session id.
	calls := h.provider.Calls()
	resumes := 0
	for _, call := range calls {
		if call.Name == "ResumeSession" {
			resumes++
			if call.SessionID != sessionID {
				t.Errorf("resumed %q, want %q", call.SessionID, sessionID)
			}
		}
	}
	if resumes != 1 {
		t.Errorf("resume calls = %d, want 1", resumes)
	}
}

// TestRecoverPassesAContinuationMessage proves the optional message reaches the
// provider verbatim, as data.
func TestRecoverPassesAContinuationMessage(t *testing.T) {
	h := newHarness(t)
	h.makeRecoverable(sessionID)

	message := "continue; do not restart the goal"
	h.do(http.MethodPost, "/sessions/"+sessionID+"/recover", map[string]string{"message": message}).
		expect(http.StatusOK)
	for _, call := range h.provider.Calls() {
		if call.Name == "ResumeSession" {
			if call.Message != message {
				t.Errorf("message = %q, want %q", call.Message, message)
			}
			return
		}
	}
	t.Fatal("no resume call was recorded")
}

// TestRecoverRejectsAnOverlongMessage proves the only free-form field is bounded.
func TestRecoverRejectsAnOverlongMessage(t *testing.T) {
	h := newHarness(t)
	h.makeRecoverable(sessionID)
	h.do(http.MethodPost, "/sessions/"+sessionID+"/recover", map[string]string{
		"message": strings.Repeat("x", maxContinuationMessage+1),
	}).expect(http.StatusBadRequest)
}

// TestRecoverReportsIneligibleAndUnavailable distinguishes a state refusal from a
// missing engine.
func TestRecoverReportsIneligibleAndUnavailable(t *testing.T) {
	h := newHarness(t)
	h.seedSession(sessionID, func(s *storage.Session) {
		s.Enabled = false
		s.State = storage.StateDisabled
	})
	ineligible := h.do(http.MethodPost, "/sessions/"+sessionID+"/recover", nil)
	ineligible.expect(http.StatusConflict)
	if code := ineligible.errorOf().Code; code != codeNotEligible {
		t.Errorf("code = %q, want %q", code, codeNotEligible)
	}

	noEngine := newHarness(t, func(c *Config, _ *configConfigAlias) { c.Recovery = nil })
	noEngine.seedSession(sessionID, nil)
	unavailable := noEngine.do(http.MethodPost, "/sessions/"+sessionID+"/recover", nil)
	unavailable.expect(http.StatusServiceUnavailable)
	if code := unavailable.errorOf().Code; code != codeUnavailable {
		t.Errorf("code = %q, want %q", code, codeUnavailable)
	}
	noEngine.do(http.MethodGet, "/recoveries", nil).expect(http.StatusServiceUnavailable)
}

// TestRecoverUnknownSession proves a 404, not a 409, for an unknown id.
func TestRecoverUnknownSession(t *testing.T) {
	h := newHarness(t)
	h.do(http.MethodPost, "/sessions/11111111-1111-4111-8111-111111111111/recover", nil).
		expect(http.StatusNotFound)
}

// TestEnableAndDisable proves both toggles persist and report the new state.
func TestEnableAndDisable(t *testing.T) {
	h := newHarness(t)
	h.seedSession(sessionID, nil)

	var disabled struct {
		Enabled bool       `json:"enabled"`
		Session sessionDTO `json:"session"`
	}
	h.do(http.MethodPost, "/sessions/"+sessionID+"/disable", nil).expect(http.StatusOK).decode(&disabled)
	if disabled.Enabled || disabled.Session.Enabled {
		t.Fatalf("disable reported enabled = %+v", disabled)
	}
	stored, err := h.store.GetSession(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if stored.Enabled {
		t.Fatal("the database still has the session enabled")
	}

	var enabled struct {
		Enabled bool `json:"enabled"`
	}
	h.do(http.MethodPost, "/sessions/"+sessionID+"/enable", nil).expect(http.StatusOK).decode(&enabled)
	if !enabled.Enabled {
		t.Fatal("enable reported disabled")
	}

	h.do(http.MethodPost, "/sessions/11111111-1111-4111-8111-111111111111/disable", nil).
		expect(http.StatusNotFound)
}

// TestRecoveries proves the pending list reports what a restart would continue.
func TestRecoveries(t *testing.T) {
	h := newHarness(t)
	h.seedSession(sessionID, func(s *storage.Session) { s.State = storage.StateWaitingForReset })
	h.seedSession(otherSessionID, func(s *storage.Session) { s.State = storage.StateRunning })

	var body struct {
		Count       int          `json:"count"`
		MaxAttempts int          `json:"max_attempts"`
		Pending     []sessionDTO `json:"pending"`
	}
	h.get("/recoveries").expect(http.StatusOK).decode(&body)
	if body.Count != 1 || len(body.Pending) != 1 || body.Pending[0].ID != sessionID {
		t.Fatalf("pending = %+v", body)
	}
	if body.MaxAttempts != 3 {
		t.Errorf("max attempts = %d", body.MaxAttempts)
	}
}

// TestSettings proves the settings resource reads and writes, with validation.
func TestSettings(t *testing.T) {
	h := newHarness(t)
	if err := h.store.SetSetting(t.Context(), "ui.theme", "dark"); err != nil {
		t.Fatalf("seed setting: %v", err)
	}

	var listed struct {
		Count    int `json:"count"`
		Settings []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"settings"`
	}
	h.get("/settings").expect(http.StatusOK).decode(&listed)
	if listed.Count != 1 || listed.Settings[0].Key != "ui.theme" || listed.Settings[0].Value != "dark" {
		t.Fatalf("settings = %+v", listed)
	}

	var written struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	h.do(http.MethodPut, "/settings", map[string]string{"key": "poll.interval", "value": "30"}).
		expect(http.StatusOK).decode(&written)
	if written.Key != "poll.interval" {
		t.Errorf("key = %q", written.Key)
	}
	stored, ok, err := h.store.GetSetting(t.Context(), "poll.interval")
	if err != nil || !ok || stored != "30" {
		t.Fatalf("stored = %q ok = %v err = %v", stored, ok, err)
	}

	cases := []struct {
		name string
		body any
	}{
		{"missing key", map[string]string{"value": "x"}},
		{"uppercase key", map[string]string{"key": "NotAllowed", "value": "x"}},
		{"separator key", map[string]string{"key": "a/b", "value": "x"}},
		{"long key", map[string]string{"key": strings.Repeat("a", 65), "value": "x"}},
		{"long value", map[string]string{"key": "ok", "value": strings.Repeat("v", maxSettingValue+1)}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			h.do(http.MethodPut, "/settings", testCase.body).expect(http.StatusBadRequest)
		})
	}
}

// TestTelegramStatus proves the notification configuration is reported without
// its secrets.
func TestTelegramStatus(t *testing.T) {
	h := newHarness(t, func(c *Config, cfg *configConfigAlias) {
		cfg.Telegram.Enabled = true
		c.Notifier = fakeNotifier{
			enabled:     map[storage.NotificationEvent]bool{storage.EventLimitDetected: true},
			maxAttempts: 5,
		}
	})
	h.seedSession(sessionID, nil)
	if _, _, err := h.store.EnqueueNotification(t.Context(), storage.Notification{
		Event: storage.EventLimitDetected, SessionID: sessionID,
		DedupeKey: "k1", Body: "queued",
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	var body struct {
		Enabled     bool            `json:"enabled"`
		MaxAttempts int             `json:"max_attempts"`
		Pending     int             `json:"pending"`
		Events      map[string]bool `json:"events"`
	}
	h.get("/telegram/status").expect(http.StatusOK).decode(&body)
	if !body.Enabled {
		t.Error("enabled = false")
	}
	if body.MaxAttempts != 5 {
		t.Errorf("max attempts = %d", body.MaxAttempts)
	}
	if body.Pending != 1 {
		t.Errorf("pending = %d", body.Pending)
	}
	if !body.Events[string(storage.EventLimitDetected)] {
		t.Error("limit_detected is not reported as enabled")
	}
	if body.Events[string(storage.EventWaiting)] {
		t.Error("waiting_for_reset is reported as enabled but was configured off")
	}
}

// TestConfigIsRedacted proves the endpoint reports the effective configuration
// while never echoing a secret.
func TestConfigIsRedacted(t *testing.T) {
	h := newHarness(t)
	resp := h.get("/config").expect(http.StatusOK)

	for _, secret := range []string{"SUPER-SECRET-BOT-TOKEN", "123456:", "-100200300"} {
		if strings.Contains(resp.text(), secret) {
			t.Fatalf("the configuration response leaks %q:\n%s", secret, resp.text())
		}
	}
	var body map[string]any
	resp.decode(&body)
	if body["config_path"] != "/tmp/ccresu/config.yaml" {
		t.Errorf("config_path = %v", body["config_path"])
	}
	telegram, ok := body["telegram"].(map[string]any)
	if !ok {
		t.Fatalf("telegram = %v", body["telegram"])
	}
	if telegram["bot_token"] != "[redacted]" {
		t.Errorf("bot_token = %v", telegram["bot_token"])
	}
	if telegram["chat_id"] != "[redacted]" {
		t.Errorf("chat_id = %v", telegram["chat_id"])
	}

	unloaded := newHarness(t, func(c *Config, _ *configConfigAlias) { c.Config = nil })
	body = nil
	unloaded.get("/config").expect(http.StatusServiceUnavailable)
}

// TestSelfTest proves the self-test reports each dependency and never sends
// anything.
func TestSelfTest(t *testing.T) {
	h := newHarness(t, func(c *Config, _ *configConfigAlias) {
		c.VersionProbe = fakeProbe{report: claude.VersionReport{Version: parsedVersion("2.1.223")}}
	})
	h.seedSession(sessionID, func(s *storage.Session) { s.State = storage.StateWaitingForReset })

	var body struct {
		OK     bool        `json:"ok"`
		Checks []testCheck `json:"checks"`
	}
	h.do(http.MethodPost, "/test", map[string]bool{"telegram": true}).expect(http.StatusOK).decode(&body)
	if !body.OK {
		t.Fatalf("self-test failed: %+v", body)
	}
	names := map[string]bool{}
	for _, check := range body.Checks {
		names[check.Name] = check.OK
	}
	for _, wanted := range []string{"database", "recovery", "telegram", "claude"} {
		if _, ok := names[wanted]; !ok {
			t.Errorf("check %q is missing from %+v", wanted, body.Checks)
		}
	}

	// Nothing was queued or sent by the self-test.
	pending, err := h.store.ListPendingNotifications(t.Context(), 0)
	if err != nil {
		t.Fatalf("list notifications: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("the self-test queued %d notifications", len(pending))
	}
}

// TestSelfTestReportsAFailure proves a broken dependency turns the report red
// with a 503.
func TestSelfTestReportsAFailure(t *testing.T) {
	h := newHarness(t, func(c *Config, _ *configConfigAlias) { c.Recovery = nil })
	var body struct {
		OK     bool        `json:"ok"`
		Checks []testCheck `json:"checks"`
	}
	h.do(http.MethodPost, "/test", nil).expect(http.StatusServiceUnavailable).decode(&body)
	if body.OK {
		t.Fatal("ok = true with no recovery engine")
	}
}

// TestConfigAliasForOptions keeps the option closures readable.
type configConfigAlias = config.Config

// TestQueryHelpers covers the parsers directly, including the edges a handler
// would otherwise have to reproduce.
func TestQueryHelpers(t *testing.T) {
	values := map[string][]string{"limit": {"5"}, "enabled": {"true"}}
	if got, apiErr := queryInt(values, "limit", 1, 0, 10); apiErr != nil || got != 5 {
		t.Fatalf("limit = %d err = %v", got, apiErr)
	}
	if _, apiErr := queryInt(values, "limit", 1, 0, 3); apiErr == nil {
		t.Fatal("an out-of-range limit was accepted")
	}
	if got, apiErr := queryInt(map[string][]string{}, "limit", 7, 0, 10); apiErr != nil || got != 7 {
		t.Fatalf("fallback = %d err = %v", got, apiErr)
	}
	for _, raw := range []string{"-1", "+1", " 1", "1.5", "99999999"} {
		if _, err := parsePositiveInt(raw); err == nil {
			t.Errorf("parsePositiveInt(%q) accepted a bad value", raw)
		}
	}

	if value, apiErr := queryBool(values, "enabled"); apiErr != nil || value == nil || *value != true {
		t.Fatalf("enabled = %v err = %v", value, apiErr)
	}
	for _, raw := range []string{"true", "1", "yes"} {
		if value, apiErr := queryBool(map[string][]string{"b": {raw}}, "b"); apiErr != nil || value == nil || !*value {
			t.Errorf("%q parsed as %v err %v", raw, value, apiErr)
		}
	}
	for _, raw := range []string{"false", "0", "no"} {
		if value, apiErr := queryBool(map[string][]string{"b": {raw}}, "b"); apiErr != nil || value == nil || *value {
			t.Errorf("%q parsed as %v err %v", raw, value, apiErr)
		}
	}
	if _, apiErr := queryBool(map[string][]string{"b": {"maybe"}}, "b"); apiErr == nil {
		t.Error("maybe was accepted as a boolean")
	}
}

// TestMapError covers the domain-to-HTTP mapping directly.
func TestMapError(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{nil, 0, ""},
		{storage.ErrNotFound, http.StatusNotFound, codeNotFound},
		{storage.ErrInvalid, http.StatusBadRequest, codeInvalid},
		{storage.ErrClosed, http.StatusServiceUnavailable, codeUnavailable},
		{context.Canceled, http.StatusServiceUnavailable, codeUnavailable},
		{context.DeadlineExceeded, http.StatusServiceUnavailable, codeUnavailable},
		{errors.New("something unexpected"), http.StatusInternalServerError, codeInternal},
	}
	for _, testCase := range cases {
		mapped := mapError(testCase.err)
		if testCase.err == nil {
			if mapped != nil {
				t.Errorf("mapError(nil) = %+v", mapped)
			}
			continue
		}
		if mapped.Status != testCase.status || mapped.Code != testCase.code {
			t.Errorf("mapError(%v) = %d/%s, want %d/%s",
				testCase.err, mapped.Status, mapped.Code, testCase.status, testCase.code)
		}
	}
}

// TestValidateSessionID covers the id guard.
func TestValidateSessionID(t *testing.T) {
	for _, good := range []string{sessionID, "abc", "a.b:c_d-e", strings.Repeat("a", 128)} {
		if apiErr := validateSessionID(good); apiErr != nil {
			t.Errorf("%q was rejected: %v", good, apiErr)
		}
	}
	for _, bad := range []string{"", "   ", "a b", "a/b", "../etc", "a$b", strings.Repeat("a", 129)} {
		if apiErr := validateSessionID(bad); apiErr == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// TestNextDue proves the earliest schedule is reported.
func TestNextDue(t *testing.T) {
	soon := baseTime.Add(time.Hour)
	later := baseTime.Add(3 * time.Hour)
	got := nextDue([]storage.Schedule{
		{SessionID: "b", ResetAt: later},
		{SessionID: "a", ResetAt: soon},
		{SessionID: "c", Unknown: true, NextPollAt: baseTime.Add(30 * time.Minute)},
	}, baseTime)
	if got == nil || !got.Equal(baseTime.Add(30*time.Minute)) {
		t.Fatalf("nextDue = %v", got)
	}
	if nextDue(nil, baseTime) != nil {
		t.Fatal("nextDue(nil) is not nil")
	}
	if nextDue([]storage.Schedule{{SessionID: "x"}}, baseTime) != nil {
		t.Fatal("a schedule with no due time produced an instant")
	}
}

// TestSortedStatesKeepsTheDocumentedOrder proves the histogram is ordered and
// includes anything unexpected.
func TestSortedStatesKeepsTheDocumentedOrder(t *testing.T) {
	sorted := sortedStates(map[string]int{
		string(storage.StateRunning):    2,
		string(storage.StateDiscovered): 1,
		"FUTURE_STATE":                  3,
	})
	if len(sorted) != 3 {
		t.Fatalf("sorted = %+v", sorted)
	}
	if sorted[string(storage.StateDiscovered)] != 1 || sorted["FUTURE_STATE"] != 3 {
		t.Fatalf("sorted = %+v", sorted)
	}
	encoded, err := json.Marshal(sorted)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), "FUTURE_STATE") {
		t.Fatalf("an unknown state was dropped: %s", encoded)
	}
}

// parsedVersion parses a version string or fails the test.
func parsedVersion(raw string) claude.Version {
	version, err := claude.ParseVersion(raw)
	if err != nil {
		panic("test version " + raw + ": " + err.Error())
	}
	return version
}
