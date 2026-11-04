package server

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/jymchng/ccresu/internal/config"
	"github.com/jymchng/ccresu/internal/recovery"
	"github.com/jymchng/ccresu/internal/storage"
)

// limitQuery caps how many rows one request may ask for.
const (
	defaultListLimit = 100
	maxListLimit     = 500
	// detailListLimit bounds the nested lists of one session document.
	detailListLimit = 20
)

// handleHealth answers liveness. It touches nothing, so it stays useful when the
// database is busy.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request, _ params) {
	s.writeJSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"api":            APIPrefix,
		"now":            s.now(),
		"uptime_seconds": s.uptime().Seconds(),
	})
}

// handleVersion reports build metadata and, best effort, the Claude Code version.
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request, _ params) {
	body := versionResponse{
		Version:   s.cfg.Build.Version,
		Commit:    s.cfg.Build.Commit,
		BuildDate: s.cfg.Build.BuildDate,
		Go:        runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
		API:       APIPrefix,
	}
	if body.Version == "" {
		body.Version = "dev"
	}
	if body.Commit == "" {
		body.Commit = "none"
	}
	if body.BuildDate == "" {
		body.BuildDate = "unknown"
	}
	body.Claude = s.probeClaudeVersion(r)
	s.writeJSON(w, http.StatusOK, body)
}

// handleStatus summarises everything an operator wants at a glance, matching the
// numbers `ccresu status` prints.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request, _ params) {
	sessions, err := s.cfg.Store.ListSessions(r.Context(), storage.SessionFilter{})
	if err != nil {
		s.fail(w, "list sessions", err)
		return
	}
	states := make(map[string]int, len(stateOrder))
	enabledCount := 0
	for _, session := range sessions {
		states[string(session.State)]++
		if session.Enabled {
			enabledCount++
		}
	}
	report := statusReport{
		Status:          "ok",
		Version:         s.cfg.Build.Version,
		Now:             s.now(),
		UptimeSeconds:   s.uptime().Seconds(),
		ConfigPath:      s.cfg.ConfigPath,
		Sessions:        len(sessions),
		EnabledSessions: enabledCount,
		States:          sortedStates(states),
		RecoveryEnabled: s.recoveryEnabled(),
		Monitoring:      s.cfg.Config != nil && s.cfg.Config.Monitoring.Enabled,
	}
	if report.Version == "" {
		report.Version = "dev"
	}
	if s.cfg.Store != nil {
		report.DatabasePath = s.cfg.Store.Path()
	}
	if s.cfg.Config != nil {
		report.TelegramEnabled = s.cfg.Config.Telegram.Enabled
	}
	if s.cfg.Recovery != nil {
		report.MaxAttempts = s.cfg.Recovery.MaxAttempts()
	}

	if schedules, err := s.cfg.Store.LoadSchedules(r.Context()); err == nil {
		report.Schedules = len(schedules)
		report.NextScheduleAt = nextDue(schedules, s.now())
	}
	if pending, err := s.cfg.Recovery.Pending(r.Context()); err == nil {
		report.PendingRecoveries = len(pending)
	}
	if notifications, err := s.cfg.Store.ListPendingNotifications(r.Context(), 0); err == nil {
		report.PendingNotifications = len(notifications)
	}
	s.writeJSON(w, http.StatusOK, report)
}

// nextDue returns the earliest due instant among schedules, or nil.
func nextDue(schedules []storage.Schedule, now time.Time) *time.Time {
	var earliest *time.Time
	for _, schedule := range schedules {
		due := schedule.DueAt()
		if due.IsZero() {
			continue
		}
		if earliest == nil || due.Before(*earliest) {
			copied := due.UTC()
			earliest = &copied
		}
	}
	return earliest
}

// handleSessions lists sessions, newest first, with the documented filters.
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request, _ params) {
	query := r.URL.Query()
	filter := storage.SessionFilter{NewestFirst: true}

	limit, apiErr := queryInt(query, "limit", defaultListLimit, 1, maxListLimit)
	if apiErr != nil {
		s.writeError(w, apiErr)
		return
	}
	filter.Limit = limit

	if enabled, apiErr := queryBool(query, "enabled"); apiErr != nil {
		s.writeError(w, apiErr)
		return
	} else if enabled != nil {
		filter.Enabled = enabled
	}
	if recoverable, apiErr := queryBool(query, "recoverable"); apiErr != nil {
		s.writeError(w, apiErr)
		return
	} else if recoverable != nil {
		filter.Recoverable = recoverable
	}
	for _, raw := range query["state"] {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		state, apiErr := parseState(raw)
		if apiErr != nil {
			s.writeError(w, apiErr)
			return
		}
		filter.States = append(filter.States, state)
	}

	sessions, err := s.cfg.Store.ListSessions(r.Context(), filter)
	if err != nil {
		s.fail(w, "list sessions", err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"count":    len(sessions),
		"sessions": toSessionDTOs(sessions),
	})
}

// handleSession returns one session with its recent context.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request, path params) {
	session, ok := s.requireSession(w, r, path)
	if !ok {
		return
	}
	detail := map[string]any{"session": toSessionDTO(session)}

	if limits, err := s.cfg.Store.ListUsageLimits(r.Context(), session.ID, detailListLimit); err == nil {
		converted := make([]usageLimitDTO, 0, len(limits))
		for _, limit := range limits {
			converted = append(converted, toUsageLimitDTO(limit))
		}
		detail["usage_limits"] = converted
	}
	if schedule, err := s.cfg.Store.GetSchedule(r.Context(), session.ID); err == nil {
		detail["schedule"] = toScheduleDTO(schedule)
	}
	if attempts, err := s.cfg.Store.ListAttempts(r.Context(), session.ID, detailListLimit); err == nil {
		converted := make([]attemptDTO, 0, len(attempts))
		for _, attempt := range attempts {
			converted = append(converted, toAttemptDTO(attempt))
		}
		detail["attempts"] = converted
	}
	if notifications, err := s.cfg.Store.ListNotificationsForSession(r.Context(), session.ID, detailListLimit); err == nil {
		converted := make([]notificationDTO, 0, len(notifications))
		for _, notification := range notifications {
			converted = append(converted, toNotificationDTO(notification))
		}
		detail["notifications"] = converted
	}
	s.writeJSON(w, http.StatusOK, detail)
}

// handleHistory returns the persisted audit trail of one session.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request, path params) {
	session, ok := s.requireSession(w, r, path)
	if !ok {
		return
	}
	limit, apiErr := queryInt(r.URL.Query(), "limit", 0, 1, maxListLimit)
	if apiErr != nil {
		s.writeError(w, apiErr)
		return
	}
	attempts, err := s.cfg.Store.ListAttempts(r.Context(), session.ID, limit)
	if err != nil {
		s.fail(w, "list attempts", err)
		return
	}
	transitions, err := s.cfg.Store.ListTransitions(r.Context(), session.ID, limit)
	if err != nil {
		s.fail(w, "list transitions", err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"session_id":  session.ID,
		"state":       string(session.State),
		"attempts":    toAttemptDTOs(attempts),
		"transitions": toTransitionDTOs(transitions),
	})
}

// toAttemptDTOs converts a list.
func toAttemptDTOs(attempts []storage.RecoveryAttempt) []attemptDTO {
	out := make([]attemptDTO, 0, len(attempts))
	for _, attempt := range attempts {
		out = append(out, toAttemptDTO(attempt))
	}
	return out
}

// toTransitionDTOs converts a list.
func toTransitionDTOs(transitions []storage.Transition) []transitionDTO {
	out := make([]transitionDTO, 0, len(transitions))
	for _, transition := range transitions {
		out = append(out, toTransitionDTO(transition))
	}
	return out
}

// recoverRequest is the optional body of POST /sessions/{id}/recover.
//
// Note what is absent: there is no field naming a command, a binary or an
// argument vector. The continuation message is data handed to the provider, which
// runs the fixed `claude -p --resume` invocation.
type recoverRequest struct {
	Message string `json:"message"`
}

// handleRecover attempts one recovery of a session.
func (s *Server) handleRecover(w http.ResponseWriter, r *http.Request, path params) {
	session, ok := s.requireSession(w, r, path)
	if !ok {
		return
	}
	if s.cfg.Recovery == nil {
		s.writeError(w, &apiError{
			Status:  http.StatusServiceUnavailable,
			Code:    codeUnavailable,
			Message: "recovery is not available in this process",
		})
		return
	}
	var body recoverRequest
	if apiErr := s.decodeJSON(r, &body, true); apiErr != nil {
		s.writeError(w, apiErr)
		return
	}
	if len(body.Message) > maxContinuationMessage {
		s.writeError(w, errInvalid(fmt.Sprintf("message must be at most %d bytes", maxContinuationMessage)))
		return
	}

	outcome, err := s.cfg.Recovery.RecoverRequest(r.Context(), recovery.Request{
		SessionID: session.ID,
		Message:   strings.TrimSpace(body.Message),
	})
	if err != nil {
		s.fail(w, "recover session", err)
		return
	}
	s.publishMutation("recovery", session.ID, "recovery requested by the API", toOutcomeDTO(outcome))
	s.writeJSON(w, http.StatusOK, toOutcomeDTO(outcome))
}

// handleEnable allows ccresu to recover a session.
func (s *Server) handleEnable(w http.ResponseWriter, r *http.Request, path params) {
	s.setEnabled(w, r, path, true)
}

// handleDisable stops ccresu from recovering a session.
func (s *Server) handleDisable(w http.ResponseWriter, r *http.Request, path params) {
	s.setEnabled(w, r, path, false)
}

// setEnabled applies an enable/disable request and reports the new state.
func (s *Server) setEnabled(w http.ResponseWriter, r *http.Request, path params, enabled bool) {
	session, ok := s.requireSession(w, r, path)
	if !ok {
		return
	}
	// Accept and ignore an empty body so a plain POST works from a script.
	if r.ContentLength > 0 {
		if apiErr := s.decodeJSON(r, &struct{}{}, true); apiErr != nil {
			s.writeError(w, apiErr)
			return
		}
	}
	if err := s.cfg.Store.SetSessionEnabled(r.Context(), session.ID, enabled); err != nil {
		s.fail(w, "set session enabled", err)
		return
	}
	updated, err := s.cfg.Store.GetSession(r.Context(), session.ID)
	if err != nil {
		s.fail(w, "read session", err)
		return
	}
	kind := "disable"
	reason := "recovery disabled through the API"
	if enabled {
		kind = "enable"
		reason = "recovery enabled through the API"
	}
	s.publishMutation(kind, session.ID, reason, map[string]any{"enabled": enabled})
	s.writeJSON(w, http.StatusOK, map[string]any{
		"session": toSessionDTO(updated),
		"enabled": updated.Enabled,
	})
}

// handleRecoveries lists the sessions a restart would continue.
func (s *Server) handleRecoveries(w http.ResponseWriter, r *http.Request, _ params) {
	if s.cfg.Recovery == nil {
		s.writeError(w, &apiError{
			Status:  http.StatusServiceUnavailable,
			Code:    codeUnavailable,
			Message: "recovery is not available in this process",
		})
		return
	}
	pending, err := s.cfg.Recovery.Pending(r.Context())
	if err != nil {
		s.fail(w, "list pending recoveries", err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"count":        len(pending),
		"max_attempts": s.cfg.Recovery.MaxAttempts(),
		"pending":      toSessionDTOs(pending),
	})
}

// handleSettings lists the persisted key/value settings.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request, _ params) {
	settings, err := s.cfg.Store.ListSettings(r.Context())
	if err != nil {
		s.fail(w, "list settings", err)
		return
	}
	type settingDTO struct {
		Key       string    `json:"key"`
		Value     string    `json:"value"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	out := make([]settingDTO, 0, len(settings))
	for _, setting := range settings {
		out = append(out, settingDTO{Key: setting.Key, Value: setting.Value, UpdatedAt: setting.UpdatedAt})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"count": len(out), "settings": out})
}

// settingRequest is the body of PUT /settings.
type settingRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// handleUpdateSetting writes one setting.
func (s *Server) handleUpdateSetting(w http.ResponseWriter, r *http.Request, _ params) {
	var body settingRequest
	if apiErr := s.decodeJSON(r, &body, false); apiErr != nil {
		s.writeError(w, apiErr)
		return
	}
	key := strings.TrimSpace(body.Key)
	if key == "" {
		s.writeError(w, errInvalid("key is required"))
		return
	}
	if len(key) > 64 {
		s.writeError(w, errInvalid("key must be at most 64 characters"))
		return
	}
	for _, char := range key {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9':
		case char == '.', char == '_', char == '-':
		default:
			s.writeError(w, errInvalid("key may only contain lowercase letters, digits, dot, underscore and dash"))
			return
		}
	}
	if len(body.Value) > maxSettingValue {
		s.writeError(w, errInvalid(fmt.Sprintf("value must be at most %d bytes", maxSettingValue)))
		return
	}
	if err := s.cfg.Store.SetSetting(r.Context(), key, body.Value); err != nil {
		s.fail(w, "set setting", err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"key": key, "value": body.Value})
}

// handleTelegram reports notification configuration and backlog.
func (s *Server) handleTelegram(w http.ResponseWriter, r *http.Request, _ params) {
	body := map[string]any{
		"enabled":      s.cfg.Config != nil && s.cfg.Config.Telegram.Enabled,
		"max_attempts": 0,
		"pending":      0,
		"events":       map[string]bool{},
	}
	enabledEvents := map[string]bool{}
	for _, kind := range []storage.NotificationEvent{
		storage.EventLimitDetected, storage.EventWaiting, storage.EventRecoveryStarted,
		storage.EventRecoverySuccess, storage.EventRecoveryFailure,
		storage.EventPermanentFailure, storage.EventSessionCompleted,
	} {
		enabled := false
		if s.cfg.Notifier != nil {
			enabled = s.cfg.Notifier.Enabled(kind)
		}
		enabledEvents[string(kind)] = enabled
	}
	body["events"] = enabledEvents
	if s.cfg.Notifier != nil {
		body["max_attempts"] = s.cfg.Notifier.MaxAttempts()
	}
	if pending, err := s.cfg.Store.ListPendingNotifications(r.Context(), 0); err == nil {
		body["pending"] = len(pending)
	}
	s.writeJSON(w, http.StatusOK, body)
}

// configView is the redacted configuration the API reports.
//
// Secrets are not part of the document at all: the Telegram bot token and chat id
// never appear, and the API bearer token only appears as a note that a file
// exists. An operator reads the file to see the values, which is a deliberate
// second step.
type configView struct {
	ConfigPath string `json:"config_path,omitempty"`
	App        struct {
		Name      string `json:"name"`
		LogLevel  string `json:"log_level"`
		LogFormat string `json:"log_format"`
		Timezone  string `json:"timezone"`
	} `json:"app"`
	Claude struct {
		Executable        string `json:"executable"`
		DiscoveryInterval string `json:"discovery_interval"`
		ResumeEnabled     bool   `json:"resume_enabled"`
	} `json:"claude"`
	Monitoring struct {
		Enabled               bool `json:"enabled"`
		DetectUsageLimits     bool `json:"detect_usage_limits"`
		DetectSessionComplete bool `json:"detect_session_completion"`
	} `json:"monitoring"`
	Recovery struct {
		Enabled        bool   `json:"enabled"`
		MaxAttempts    int    `json:"max_attempts"`
		InitialDelay   string `json:"initial_delay"`
		MaxDelay       string `json:"max_delay"`
		MaxConcurrent  int    `json:"max_concurrent_recoveries"`
		ContinuationID string `json:"continuation_message_source"`
	} `json:"recovery"`
	Telegram struct {
		Enabled    bool   `json:"enabled"`
		BotToken   string `json:"bot_token"`
		ChatID     string `json:"chat_id"`
		ParseMode  string `json:"parse_mode"`
		TokenNote  string `json:"token_note"`
		ChatIDNote string `json:"chat_id_note"`
	} `json:"telegram"`
	Server struct {
		Enabled        bool     `json:"enabled"`
		Host           string   `json:"host"`
		Port           int      `json:"port"`
		AllowedOrigins []string `json:"allowed_origins"`
		TokenPath      string   `json:"token_path"`
	} `json:"server"`
	Storage struct {
		Driver string `json:"driver"`
		Path   string `json:"path"`
	} `json:"storage"`
}

// handleConfig reports the effective configuration with secrets removed.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request, _ params) {
	cfg := s.cfg.Config
	if cfg == nil {
		s.writeError(w, &apiError{
			Status:  http.StatusServiceUnavailable,
			Code:    codeUnavailable,
			Message: "no configuration is loaded in this process",
		})
		return
	}
	var view configView
	view.ConfigPath = s.cfg.ConfigPath
	view.App.Name = cfg.App.Name
	view.App.LogLevel = cfg.App.LogLevel
	view.App.LogFormat = cfg.App.LogFormat
	view.App.Timezone = cfg.App.Timezone
	view.Claude.Executable = cfg.Claude.Executable
	view.Claude.DiscoveryInterval = cfg.Claude.DiscoveryInterval.Duration.String()
	view.Claude.ResumeEnabled = cfg.Claude.ResumeEnabled
	view.Monitoring.Enabled = cfg.Monitoring.Enabled
	view.Monitoring.DetectUsageLimits = cfg.Monitoring.DetectUsageLimits
	view.Monitoring.DetectSessionComplete = cfg.Monitoring.DetectSessionCompletion
	view.Recovery.Enabled = cfg.Recovery.Enabled
	view.Recovery.MaxAttempts = cfg.Recovery.MaxAttempts
	view.Recovery.InitialDelay = cfg.Recovery.InitialDelay.Duration.String()
	view.Recovery.MaxDelay = cfg.Recovery.MaxDelay.Duration.String()
	view.Recovery.MaxConcurrent = cfg.Recovery.MaxConcurrentRecoveries
	view.Recovery.ContinuationID = "configured"
	view.Telegram.Enabled = cfg.Telegram.Enabled
	view.Telegram.BotToken = unsafeValue()
	view.Telegram.ChatID = unsafeValue()
	view.Telegram.ParseMode = cfg.Telegram.ParseMode
	view.Telegram.TokenNote = "the bot token is never returned by the API; read telegram.bot_token in the configuration file"
	view.Telegram.ChatIDNote = "the chat id is never returned by the API; read telegram.chat_id in the configuration file"
	view.Server.Enabled = cfg.Server.Enabled
	view.Server.Host = cfg.Server.Host
	view.Server.Port = cfg.Server.Port
	view.Server.AllowedOrigins = append([]string(nil), cfg.Server.AllowedOrigins...)
	view.Server.TokenPath = cfg.Server.TokenPath
	view.Storage.Driver = cfg.Storage.Driver
	view.Storage.Path = cfg.Storage.Path
	s.writeJSON(w, http.StatusOK, view)
}

// testRequest is the body of POST /test.
type testRequest struct {
	// Telegram asks for the notification wiring to be described in the report.
	// Nothing is sent: a self-test must be safe to run at any time.
	Telegram bool `json:"telegram"`
}

// testCheck is one line of the self-test report.
type testCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// handleTest runs a read-only self-diagnosis.
//
// It proves the API can reach the database, the recovery engine and the version
// probe. It never sends a message, never resumes a session and never runs a
// process of its own, so calling it cannot change state beyond a read.
func (s *Server) handleTest(w http.ResponseWriter, r *http.Request, _ params) {
	var body testRequest
	if apiErr := s.decodeJSON(r, &body, true); apiErr != nil {
		s.writeError(w, apiErr)
		return
	}
	checks := make([]testCheck, 0, 5)
	ok := true

	version, err := s.cfg.Store.Version(r.Context())
	if err != nil {
		ok = false
		checks = append(checks, testCheck{Name: "database", OK: false, Detail: err.Error()})
	} else {
		checks = append(checks, testCheck{Name: "database", OK: true,
			Detail: fmt.Sprintf("schema version %d at %s", version, s.cfg.Store.Path())})
	}

	if s.cfg.Recovery == nil {
		ok = false
		checks = append(checks, testCheck{Name: "recovery", OK: false, Detail: "no recovery manager"})
	} else {
		pending, err := s.cfg.Recovery.Pending(r.Context())
		if err != nil {
			ok = false
			checks = append(checks, testCheck{Name: "recovery", OK: false, Detail: err.Error()})
		} else {
			checks = append(checks, testCheck{Name: "recovery", OK: true,
				Detail: fmt.Sprintf("%d pending, max %d attempts", len(pending), s.cfg.Recovery.MaxAttempts())})
		}
	}

	if body.Telegram {
		check := testCheck{Name: "telegram", OK: true, Detail: "disabled"}
		if s.cfg.Config != nil && s.cfg.Config.Telegram.Enabled {
			if s.cfg.Notifier == nil {
				ok = false
				check = testCheck{Name: "telegram", OK: false, Detail: "enabled but no notifier is wired"}
			} else {
				check.Detail = fmt.Sprintf("enabled, max %d attempts, no message sent by this test", s.cfg.Notifier.MaxAttempts())
			}
		}
		checks = append(checks, check)
	}

	if s.cfg.VersionProbe != nil {
		report, err := s.cfg.VersionProbe.Version(r.Context())
		if err != nil {
			checks = append(checks, testCheck{Name: "claude", OK: false, Detail: err.Error()})
		} else {
			checks = append(checks, testCheck{Name: "claude", OK: true, Detail: report.Version.String()})
		}
	}

	status := http.StatusOK
	if !ok {
		status = http.StatusServiceUnavailable
	}
	s.writeJSON(w, status, map[string]any{"ok": ok, "checks": checks, "now": s.now()})
}

// requireSession resolves the id in the path, answering 404/400 itself.
func (s *Server) requireSession(w http.ResponseWriter, r *http.Request, path params) (storage.Session, bool) {
	id := path["id"]
	if apiErr := validateSessionID(id); apiErr != nil {
		s.writeError(w, apiErr)
		return storage.Session{}, false
	}
	session, err := s.cfg.Store.GetSession(r.Context(), strings.TrimSpace(id))
	if err != nil {
		if mapped := mapError(err); mapped.Code == codeNotFound {
			s.writeError(w, errNotFound(fmt.Sprintf("session %s does not exist", id)))
			return storage.Session{}, false
		}
		s.fail(w, "read session", err)
		return storage.Session{}, false
	}
	return session, true
}

// recoveryEnabled reports whether the recovery engine is active.
func (s *Server) recoveryEnabled() bool {
	return s.cfg.Recovery != nil && s.cfg.Recovery.Enabled()
}

// fail maps a domain error onto the API contract, logging an unexpected one.
func (s *Server) fail(w http.ResponseWriter, operation string, err error) {
	mapped := mapError(err)
	if mapped.Code == codeInternal {
		s.cfg.Log.Error("api operation failed", "operation", operation, "error", err)
	}
	s.writeError(w, mapped)
}

// compile-time guards that the config package still exposes what the view reads.
var _ = config.Config{}
var _ = context.Background
