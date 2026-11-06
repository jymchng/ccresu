// Package support holds the fixtures and wiring shared by the integration and
// end-to-end suites.
//
// It is a normal package rather than a _test.go file so that both test
// binaries can import it, which keeps the interruption banners, the session id
// and the store/provider wiring defined exactly once. Tests are the only
// intended caller.
package support

import (
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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/clock"
	"github.com/jymchng/ccresu/internal/detector"
	"github.com/jymchng/ccresu/internal/recovery"
	"github.com/jymchng/ccresu/internal/storage"
	"github.com/jymchng/ccresu/internal/telegram"
)

// The exact interruption Claude Code prints, and the exact paused-goal banner.
// They are the fixtures the whole product exists for, so they are spelled out
// once and every suite replays them.
const (
	// LimitBanner is the canonical five-hour usage-limit message.
	LimitBanner = "You've hit your 5-hour limit \u00b7 resets 12pm (Europe/Berlin)"
	// PausedBanner is the canonical paused-goal message.
	PausedBanner = "Goal paused \u00b7 usage limit reached \u00b7 send a message after it resets"
)

// The session identifiers the suites use. They are lowercase v4 UUIDs because
// the recovery engine validates the id before it will resume anything.
const (
	// SessionID is the primary fixture session.
	SessionID = "4d1f9c2a-7e35-4b18-9a6f-2c8d0e1f3a45"
	// OtherSessionID is a second valid id, for concurrency and list tests.
	OtherSessionID = "9b2e4c71-1a68-4f90-8d33-7e5a2b9c4d10"
	// ProjectPath is the working directory the fixture session belongs to.
	ProjectPath = "/home/dev/src/widget"
)

// BaseTime is the fixed instant every test starts from: 09:00 UTC, which is
// 10:00 in Berlin during winter, so "resets 12pm (Europe/Berlin)" is two hours
// away and the timezone genuinely matters.
var BaseTime = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

// TranscriptPath is the on-disk transcript a fixture session points at.
func TranscriptPath(id string) string {
	return filepath.Join("/home/dev/.claude/projects/-home-dev-src-widget", id+".jsonl")
}

// QuietLogger discards output so a passing test prints nothing.
func QuietLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// storeSeq makes every database path unique even within one test.
var storeSeq atomic.Int64

// OpenStore opens a real, file-backed, migrated database in a temporary
// directory. The file matters: the suites reopen it to prove durability.
func OpenStore(t testing.TB) *storage.Store {
	t.Helper()
	return OpenStoreAt(t, filepath.Join(t.TempDir(), fmt.Sprintf("ccresu-%d.db", storeSeq.Add(1))))
}

// OpenStoreAt opens (or reopens) the database at path.
func OpenStoreAt(t testing.TB, path string) *storage.Store {
	t.Helper()
	store, err := storage.Open(context.Background(), storage.Options{
		Path:  path,
		Clock: func() time.Time { return BaseTime },
	})
	if err != nil {
		t.Fatalf("open store %s: %v", path, err)
	}
	if _, err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate %s: %v", path, err)
	}
	return store
}

// SeedSession inserts a session row in DISCOVERED and walks it to state along a
// legal path, so the state machine is exercised rather than bypassed.
func SeedSession(t testing.TB, store *storage.Store, id string, state storage.State) storage.Session {
	t.Helper()
	ctx := context.Background()
	session := storage.Session{
		ID:              id,
		ProjectPath:     ProjectPath,
		TranscriptPath:  TranscriptPath(id),
		Provider:        "claude",
		Type:            storage.SessionTypeInteractive,
		State:           storage.StateDiscovered,
		Enabled:         true,
		Recoverable:     true,
		TranscriptSize:  2048,
		TranscriptLines: 12,
	}
	if _, err := store.UpsertSession(ctx, session); err != nil {
		t.Fatalf("upsert %s: %v", id, err)
	}
	// DISCOVERED -> RUNNING -> USAGE_LIMIT_DETECTED -> WAITING_FOR_RESET ->
	// RESET_TIME_REACHED. Stopping early is fine; the earlier states are only
	// visited to keep the persisted history truthful.
	for _, next := range []storage.State{
		storage.StateRunning,
		storage.StateUsageLimitDetected,
		storage.StateWaitingForReset,
		storage.StateResetTimeReached,
	} {
		if session.State == state {
			break
		}
		if _, err := store.TransitionSession(ctx, id, next, "test fixture", 0); err != nil {
			t.Fatalf("transition %s -> %s: %v", id, next, err)
		}
		session.State = next
	}
	stored, err := store.GetSession(ctx, id)
	if err != nil {
		t.Fatalf("read back %s: %v", id, err)
	}
	return stored
}

// DetectAndRecord runs the real detector over message, persists the usage limit
// it found and returns both. It fails the test when the message was not
// classified as a usage limit.
func DetectAndRecord(t testing.TB, store *storage.Store, id, message string) (detector.UsageLimitEvent, storage.UsageLimit) {
	t.Helper()
	event := detector.Analyze(message)
	if !event.Detected {
		t.Fatalf("detector did not detect a usage limit in %q: %s", message, event.Reason())
	}
	limitType := storage.LimitTypeFiveHour
	if event.Limit == detector.LimitWeekly {
		limitType = storage.LimitTypeWeekly
	}
	limit, err := store.RecordUsageLimit(context.Background(), storage.UsageLimit{
		SessionID:     id,
		LimitType:     limitType,
		RawMessage:    event.Message,
		ResetTimezone: event.Reset.Timezone,
		ResetRaw:      event.Reset.Raw,
		Confidence:    event.Confidence,
		PausedGoal:    event.PausedGoal,
		DetectedAt:    event.DetectedAt,
	})
	if err != nil {
		t.Fatalf("record usage limit: %v", err)
	}
	return event, limit
}

// TelegramRequest is one decoded sendMessage call the client put on the wire.
type TelegramRequest struct {
	Path        string
	ChatID      string
	Text        string
	ParseMode   string
	NoPreview   bool
	Authorized  string
	ContentType string
}

// TelegramStub is a Bot API stand-in. It records every message the client
// actually sent, so assertions are about real HTTP traffic rather than about
// calls into a fake sender.
type TelegramStub struct {
	mu       sync.Mutex
	requests []TelegramRequest
	statuses []int
	calls    int
}

// NewTelegramStub starts a Bot API stub. The status codes are consumed in
// order; once they run out every call succeeds.
func NewTelegramStub(t testing.TB, statuses ...int) (*TelegramStub, *httptest.Server) {
	t.Helper()
	stub := &TelegramStub{statuses: statuses}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			ChatID                string `json:"chat_id"`
			Text                  string `json:"text"`
			ParseMode             string `json:"parse_mode"`
			DisableWebPagePreview bool   `json:"disable_web_page_preview"`
		}
		_ = json.Unmarshal(body, &payload)

		stub.mu.Lock()
		stub.calls++
		stub.requests = append(stub.requests, TelegramRequest{
			Path:        r.URL.Path,
			ChatID:      payload.ChatID,
			Text:        payload.Text,
			ParseMode:   payload.ParseMode,
			NoPreview:   payload.DisableWebPagePreview,
			Authorized:  r.Header.Get("Authorization"),
			ContentType: r.Header.Get("Content-Type"),
		})
		status := http.StatusOK
		if len(stub.statuses) > 0 {
			status = stub.statuses[0]
			stub.statuses = stub.statuses[1:]
		}
		stub.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch status {
		case http.StatusOK:
			_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1,"date":1772442000,"text":"sent"}}`)
		case http.StatusTooManyRequests:
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 0"}`)
		default:
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":500,"description":"Internal Server Error"}`)
		}
	}))
	t.Cleanup(server.Close)
	return stub, server
}

// Messages returns a copy of every recorded request.
func (s *TelegramStub) Messages() []TelegramRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]TelegramRequest(nil), s.requests...)
}

// Calls reports how many requests the stub received.
func (s *TelegramStub) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// NewTelegramClient builds a real client pointed at the stub. The clock is used
// only for retry waits, so a retry test does not sleep in real time.
func NewTelegramClient(t testing.TB, stubURL string, clk clock.Clock) *telegram.Client {
	t.Helper()
	client, err := telegram.NewClient(telegram.Options{
		Token:     "424242:integration-test-token",
		ChatID:    "-100200300",
		BaseURL:   stubURL,
		Clock:     clk,
		Logger:    QuietLogger(),
		ParseMode: "HTML",
	})
	if err != nil {
		t.Fatalf("telegram client: %v", err)
	}
	return client
}

// ResumeCall records one resume invocation.
type ResumeCall struct {
	SessionID string
	Message   string
}

// GrowingProvider is a claude.SessionProvider that behaves like a real resume:
// the session keeps its id and its transcript grows afterwards.
//
// It is used instead of the package fake because the suites need a provider
// whose post-resume status changes, which is exactly the signal the verifier
// has to observe.
type GrowingProvider struct {
	mu          sync.Mutex
	statusCalls int
	resumes     []ResumeCall
	// LinesBefore and LinesAfter bracket the resume.
	LinesBefore int64
	LinesAfter  int64
	// Session is what discovery and status report.
	Session claude.Session
	// ResumeErr, when set, is returned instead of a successful resume.
	ResumeErr error
	// FailStatus, when set, makes GetSessionStatus fail.
	FailStatus error
}

// NewGrowingProvider builds a provider for one session.
func NewGrowingProvider(id string, linesBefore, linesAfter int64) *GrowingProvider {
	return &GrowingProvider{
		LinesBefore: linesBefore,
		LinesAfter:  linesAfter,
		Session: claude.Session{
			ID:             id,
			ProjectPath:    ProjectPath,
			TranscriptPath: TranscriptPath(id),
			Source:         claude.SourceTranscript,
			Recoverable:    true,
		},
	}
}

// Discover implements claude.SessionProvider.
func (p *GrowingProvider) Discover(context.Context, claude.DiscoverFilter) ([]claude.Session, error) {
	return []claude.Session{p.Session}, nil
}

// GetSession implements claude.SessionProvider.
func (p *GrowingProvider) GetSession(context.Context, string) (claude.Session, error) {
	return p.Session, nil
}

// GetSessionStatus implements claude.SessionProvider.
//
// Before a resume it reports LinesBefore; after one it reports LinesAfter, which
// is what makes the verifier's growth check meaningful.
func (p *GrowingProvider) GetSessionStatus(_ context.Context, _ string) (claude.SessionStatus, error) {
	p.mu.Lock()
	p.statusCalls++
	resumed := len(p.resumes) > 0
	p.mu.Unlock()

	if p.FailStatus != nil {
		return claude.SessionStatus{}, p.FailStatus
	}

	lines := p.LinesBefore
	if resumed {
		lines = p.LinesAfter
	}
	return claude.SessionStatus{
		Session: p.Session,
		Running: resumed,
		Status:  "idle",
		Transcript: claude.TranscriptInfo{
			SessionID: p.Session.ID,
			Path:      p.Session.TranscriptPath,
			Size:      lines * 200,
			Lines:     lines,
		},
		CheckedAt: BaseTime,
	}, nil
}

// ResumeSession implements claude.SessionProvider.
func (p *GrowingProvider) ResumeSession(_ context.Context, id, message string) (claude.ResumeResult, error) {
	if p.ResumeErr != nil {
		return claude.ResumeResult{}, p.ResumeErr
	}
	p.mu.Lock()
	p.resumes = append(p.resumes, ResumeCall{SessionID: id, Message: message})
	p.mu.Unlock()
	// The dispatch creates a background job; the fake reports the job and the
	// conversation it continues, and keeps the same session id so a test's
	// transcript fixtures remain valid.
	return claude.ResumeResult{
		RequestedSessionID:    id,
		JobID:                 claude.FakeJobID(id),
		ContinuationSessionID: id,
		Command:               append([]string{"claude"}, claude.ResumeArgs(id, message)...),
	}, nil
}

// BackgroundJob implements claude.SessionProvider.
//
// The fake's job is already finished when it is asked about, so a verification
// runs straight to the transcript check instead of waiting for a state change.
func (p *GrowingProvider) BackgroundJob(_ context.Context, jobID string) (claude.AgentEntry, error) {
	return claude.AgentEntry{
		StateID:   jobID,
		SessionID: p.Session.ID,
		CWD:       p.Session.ProjectPath,
		Kind:      "background",
		State:     "done",
	}, nil
}

// ResumeCalls returns a copy of the recorded resumes.
func (p *GrowingProvider) ResumeCalls() []ResumeCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ResumeCall(nil), p.resumes...)
}

// NewManager builds a recovery manager with the documented policy and a
// verification pass that measures once instead of waiting on the wall clock.
func NewManager(t testing.TB, store recovery.Store, provider claude.SessionProvider, clk clock.Clock, maxAttempts int) *recovery.Manager {
	t.Helper()
	manager, err := recovery.New(recovery.Config{
		Store:               store,
		Provider:            provider,
		Clock:               clk,
		Logger:              QuietLogger(),
		Enabled:             true,
		MaxAttempts:         maxAttempts,
		VerificationTimeout: -1,
		HealthCheckInterval: -1,
	})
	if err != nil {
		t.Fatalf("recovery manager: %v", err)
	}
	return manager
}

// WriteFile writes content, creating parent directories.
func WriteFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// JSONLTranscript renders a transcript with n user/assistant lines, matching the
// shape of a real ~/.claude/projects transcript.
func JSONLTranscript(id string, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		fmt.Fprintf(&b,
			`{"type":%q,"message":{"role":%q,"content":[{"type":"text","text":"line %d"}]},"timestamp":"2026-03-02T08:%02d:00.000Z","sessionId":%q}`+"\n",
			role, role, i, i%60, id)
	}
	return b.String()
}
