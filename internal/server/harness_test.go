package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/clock"
	"github.com/jymchng/ccresu/internal/config"
	"github.com/jymchng/ccresu/internal/recovery"
	"github.com/jymchng/ccresu/internal/storage"
)

// baseTime is the fixed instant every test starts from.
var baseTime = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

// sessionID is a canonical Claude Code session id: a v4 UUID, because the
// recovery engine validates the id before it will resume anything.
const sessionID = "3f9a7c1e-2b44-4d8a-9f10-5c6d7e8f9a01"

// otherSessionID is a second canonical id for list and filter tests.
const otherSessionID = "7c2b1a90-4e33-4f21-8b7d-1a2c3d4e5f60"

// harness is one API instance over an in-memory database and fakes.
type harness struct {
	t         *testing.T
	server    *Server
	token     string
	tokenPath string
	store     *storage.Store
	provider  *claude.FakeProvider
	recovery  *recovery.Manager
	clock     *clock.Fake
	cfg       *config.Config
	http      *httptest.Server
}

// harnessOption customises the harness.
type harnessOption func(*Config, *config.Config)

// newHarness builds the API. The defaults are deliberately permissive for rate
// limiting so a test is never accidentally throttled; the limiter is tested
// explicitly.
func newHarness(t *testing.T, options ...harnessOption) *harness {
	t.Helper()
	clk := clock.NewFake(baseTime)
	store, err := storage.Open(context.Background(), storage.Options{
		Memory: true,
		Clock:  clk.Now,
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	provider := claude.NewFakeProvider()
	manager, err := recovery.New(recovery.Config{
		Store:       store,
		Provider:    provider,
		Clock:       clk,
		Logger:      slog.New(slog.DiscardHandler),
		Enabled:     true,
		MaxAttempts: 3,
		// A fake clock never advances by itself, so verification must be
		// "measure once" rather than "wait for growth".
		VerificationTimeout: -1,
		HealthCheckInterval: -1,
	})
	if err != nil {
		t.Fatalf("recovery manager: %v", err)
	}

	cfg := config.Defaults()
	cfg.Telegram.BotToken = "123456:SUPER-SECRET-BOT-TOKEN"
	cfg.Telegram.ChatID = "-100200300"
	cfg.Server.AllowedOrigins = []string{"https://docs.example.test", "http://localhost:8787"}

	tokenPath := filepath.Join(t.TempDir(), "api-token")

	h := &harness{
		t:         t,
		tokenPath: tokenPath,
		store:     store,
		provider:  provider,
		recovery:  manager,
		clock:     clk,
		cfg:       &cfg,
	}

	serverCfg := Config{
		Addr:           "127.0.0.1:0",
		AllowedOrigins: cfg.Server.AllowedOrigins,
		TokenPath:      tokenPath,
		Store:          store,
		Provider:       provider,
		Recovery:       manager,
		Clock:          clk,
		Log:            slog.New(slog.DiscardHandler),
		Config:         &cfg,
		ConfigPath:     "/tmp/ccresu/config.yaml",
		Build:          BuildInfo{Version: "1.4.0", Commit: "cafebabe", BuildDate: "2026-03-01"},
		RateLimit:      10_000,
		RateBurst:      10_000,
		Heartbeat:      50 * time.Millisecond,
	}
	for _, option := range options {
		option(&serverCfg, &cfg)
	}

	server, err := New(serverCfg)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	h.server = server
	h.token = server.token
	return h
}

// serve starts the API on a real port and returns the base URL. It is started on
// first use so tests that never make a request do not pay for a listener.
func (h *harness) serve() string {
	h.t.Helper()
	if h.http != nil {
		return h.http.URL
	}
	h.http = httptest.NewServer(h.server.Handler())
	h.t.Cleanup(h.http.Close)
	return h.http.URL
}

// seedSession persists one session and applies mutate first.
func (h *harness) seedSession(id string, mutate func(*storage.Session)) storage.Session {
	h.t.Helper()
	session := storage.Session{
		ID:              id,
		ProjectPath:     "/work/project",
		TranscriptPath:  "/home/u/.claude/projects/-work-project/" + id + ".jsonl",
		Provider:        "claude",
		Type:            storage.SessionTypeInteractive,
		State:           storage.StateRunning,
		Enabled:         true,
		Recoverable:     true,
		TranscriptSize:  1024,
		TranscriptLines: 10,
	}
	if mutate != nil {
		mutate(&session)
	}
	if _, err := h.store.UpsertSession(context.Background(), session); err != nil {
		h.t.Fatalf("seed %s: %v", id, err)
	}
	stored, err := h.store.GetSession(context.Background(), id)
	if err != nil {
		h.t.Fatalf("read back %s: %v", id, err)
	}
	return stored
}

// makeRecoverable turns a seeded session into one the state machine will resume:
// the reset time is reached and the provider reports a transcript that grows when
// the resume runs.
func (h *harness) makeRecoverable(id string) {
	h.t.Helper()
	h.seedSession(id, func(s *storage.Session) {
		s.State = storage.StateResetTimeReached
		s.TranscriptSize = 10
		s.TranscriptLines = 1
	})
	resumed := claude.Session{
		ID:          id,
		ProjectPath: "/work/project",
		Recoverable: true,
	}
	h.provider.SetSession(resumed)
	h.provider.Statuses[id] = claude.SessionStatus{
		Session:    resumed,
		Transcript: claude.TranscriptInfo{Path: "/tmp/t.jsonl", Size: 400, Lines: 9},
	}
	h.provider.ResumeResults[id] = claude.ResumeResult{
		RequestedSessionID: id,
		SessionID:          id,
		Verified:           true,
		Result:             "continued",
		Subtype:            "success",
	}
	h.provider.OnResume = func(context.Context, string, string) (claude.ResumeResult, error) {
		h.provider.Statuses[id] = claude.SessionStatus{
			Session:    resumed,
			Transcript: claude.TranscriptInfo{Path: "/tmp/t.jsonl", Size: 8192, Lines: 64},
		}
		return h.provider.ResumeResults[id], nil
	}
}

// response is one decoded API reply.
type response struct {
	t      *testing.T
	status int
	header http.Header
	body   []byte
}

// expect fails unless the status matches.
func (r response) expect(status int) response {
	r.t.Helper()
	if r.status != status {
		r.t.Fatalf("status = %d, want %d (body %s)", r.status, status, r.body)
	}
	return r
}

// decode unmarshals the body.
func (r response) decode(dst any) response {
	r.t.Helper()
	if err := json.Unmarshal(r.body, dst); err != nil {
		r.t.Fatalf("decode %s: %v", r.body, err)
	}
	return r
}

// text returns the body as a string.
func (r response) text() string { return string(r.body) }

// do performs a request against the API with the harness token attached.
func (h *harness) do(method, path string, body any) response {
	h.t.Helper()
	return h.doWithToken(method, path, body, h.token)
}

// doWithToken performs a request with an explicit token ("" means no header).
func (h *harness) doWithToken(method, path string, body any, token string) response {
	h.t.Helper()
	return h.doRequest(method, path, body, func(req *http.Request) {
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	})
}

// doRequest performs a request and lets the caller set headers.
func (h *harness) doRequest(method, path string, body any, mutate func(*http.Request)) response {
	h.t.Helper()
	var reader io.Reader
	switch value := body.(type) {
	case nil:
		reader = nil
	case string:
		reader = strings.NewReader(value)
	case []byte:
		reader = bytes.NewReader(value)
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			h.t.Fatalf("encode body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}

	url := h.serve() + APIPrefix + path
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		h.t.Fatalf("build request: %v", err)
	}
	if reader != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if mutate != nil {
		mutate(req)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatalf("read body: %v", err)
	}
	return response{t: h.t, status: resp.StatusCode, header: resp.Header.Clone(), body: payload}
}

// get is shorthand for a GET.
func (h *harness) get(path string) response { return h.do(http.MethodGet, path, nil) }

// errorOf decodes the error envelope and returns its code.
func (r response) errorOf() apiError {
	r.t.Helper()
	var envelope errorEnvelope
	r.decode(&envelope)
	if envelope.Error == nil {
		r.t.Fatalf("no error envelope in %s", r.body)
	}
	return *envelope.Error
}

// fakeProbe is a VersionProbe that returns a fixed report.
type fakeProbe struct {
	report claude.VersionReport
	err    error
}

// Version implements VersionProbe.
func (p fakeProbe) Version(context.Context) (claude.VersionReport, error) {
	return p.report, p.err
}

// fakeNotifier reports a fixed notification configuration.
type fakeNotifier struct {
	enabled     map[storage.NotificationEvent]bool
	maxAttempts int
}

// Enabled implements Notifier.
func (n fakeNotifier) Enabled(event storage.NotificationEvent) bool { return n.enabled[event] }

// MaxAttempts implements Notifier.
func (n fakeNotifier) MaxAttempts() int { return n.maxAttempts }

// waitFor polls until condition holds or the deadline passes.
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// readToken reads the token file the harness server created.
func readToken(t *testing.T, path string) (string, os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat token: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read token: %v", err)
	}
	return strings.TrimSpace(string(data)), info.Mode().Perm()
}

// String helps failure messages identify a harness.
func (h *harness) String() string { return fmt.Sprintf("server(%s)", h.tokenPath) }
