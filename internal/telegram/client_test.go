package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/clock"
	"github.com/jymchng/ccresu/internal/storage"
)

// testToken looks like a real bot token and is long enough for MaskSecret to
// keep its tail.
const testToken = "123456:TEST-token-value"

// discardLogger keeps test output readable.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestClient builds a client with a fast, deterministic retry policy.
func newTestClient(t *testing.T, baseURL string, mutate func(*Options)) *Client {
	t.Helper()
	opts := Options{
		Token:          testToken,
		ChatID:         "42",
		BaseURL:        baseURL,
		Timeout:        2 * time.Second,
		MaxAttempts:    3,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     5 * time.Millisecond,
		Logger:         discardLogger(),
	}
	if mutate != nil {
		mutate(&opts)
	}
	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func TestNewClientValidatesItsOptions(t *testing.T) {
	cases := []struct {
		name string
		opts Options
		want error
	}{
		{"no token", Options{ChatID: "42"}, ErrInvalidConfig},
		{"whitespace token", Options{Token: "12 34", ChatID: "42"}, ErrInvalidConfig},
		{"no chat", Options{Token: testToken}, ErrInvalidConfig},
		{"bad parse mode", Options{Token: testToken, ChatID: "42", ParseMode: "LaTeX"}, ErrInvalidConfig},
		{"bad base url", Options{Token: testToken, ChatID: "42", BaseURL: "api.telegram.org"}, ErrInvalidConfig},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewClient(tc.opts); !errors.Is(err, tc.want) {
				t.Fatalf("NewClient = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestNewClientAppliesDefaultsAndMasksTheToken(t *testing.T) {
	client, err := NewClient(Options{Token: "  " + testToken + "  ", ChatID: " 42 "})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if client.ChatID() != "42" {
		t.Errorf("ChatID = %q, want it trimmed", client.ChatID())
	}
	if client.baseURL != DefaultBaseURL {
		t.Errorf("baseURL = %q, want %q", client.baseURL, DefaultBaseURL)
	}
	if client.parseMode != ParseModeHTML {
		t.Errorf("parseMode = %q, want HTML", client.parseMode)
	}
	if client.timeout != DefaultTimeout || client.attempts != DefaultMaxAttempts ||
		client.initial != DefaultInitialBackoff || client.max != DefaultMaxBackoff {
		t.Errorf("defaults = %+v, want the documented policy", client)
	}
	if client.clock == nil || client.log == nil || client.http == nil {
		t.Error("clock, logger and HTTP client must always be set")
	}
	masked := client.MaskedToken()
	if strings.Contains(masked, "TEST-token") || !strings.HasSuffix(masked, "alue") {
		t.Errorf("MaskedToken = %q, want it masked but recognisable", masked)
	}
	if strings.Contains(masked, testToken) {
		t.Error("MaskedToken leaked the token")
	}
}

// capture is one recorded Bot API request.
type capture struct {
	mu       sync.Mutex
	requests int
	path     string
	method   string
	ctype    string
	payload  map[string]any
}

func (c *capture) record(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var payload map[string]any
	_ = json.Unmarshal(body, &payload)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests++
	c.path = r.URL.Path
	c.method = r.Method
	c.ctype = r.Header.Get("Content-Type")
	c.payload = payload
}

func (c *capture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requests
}

func TestSendMessageDeliversHTMLAndReturnsTheMessage(t *testing.T) {
	seen := &capture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.record(r)
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":9,"date":1767225600,"text":"hello","chat":{"id":42}}}`)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, nil)
	message, err := client.SendMessage(context.Background(), "hello <b>world</b>")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	if message.MessageID != 9 || message.ChatID != 42 || message.Text != "hello" {
		t.Errorf("message = %+v, want the API result", message)
	}
	if !message.Date.Equal(time.Unix(1767225600, 0).UTC()) {
		t.Errorf("message date = %s, want the API timestamp", message.Date)
	}

	if seen.path != "/bot"+testToken+"/sendMessage" {
		t.Errorf("path = %q, want the sendMessage endpoint", seen.path)
	}
	if seen.method != http.MethodPost {
		t.Errorf("method = %q, want POST", seen.method)
	}
	if seen.ctype != "application/json" {
		t.Errorf("content type = %q, want application/json", seen.ctype)
	}
	if seen.payload["chat_id"] != "42" || seen.payload["text"] != "hello <b>world</b>" {
		t.Errorf("payload = %+v, want the chat and text", seen.payload)
	}
	if seen.payload["parse_mode"] != ParseModeHTML {
		t.Errorf("payload parse_mode = %v, want HTML", seen.payload["parse_mode"])
	}
	if seen.payload["disable_web_page_preview"] != true {
		t.Errorf("payload disable_web_page_preview = %v, want true", seen.payload["disable_web_page_preview"])
	}
}

func TestSendMessageToOverridesTheChat(t *testing.T) {
	seen := &capture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.record(r)
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1,"chat":{"id":-100}}}`)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, nil)
	message, err := client.SendMessageTo(context.Background(), "-100", "hi")
	if err != nil {
		t.Fatalf("SendMessageTo: %v", err)
	}
	if seen.payload["chat_id"] != "-100" {
		t.Errorf("payload chat_id = %v, want -100", seen.payload["chat_id"])
	}
	if message.ChatID != -100 {
		t.Errorf("message chat = %d, want -100", message.ChatID)
	}
}

func TestSendMessageValidatesItsArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("an invalid request must not reach the network")
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, nil)

	if _, err := client.SendMessage(context.Background(), "   "); !errors.Is(err, ErrEmptyMessage) {
		t.Errorf("empty text = %v, want ErrEmptyMessage", err)
	}
	long := strings.Repeat("a", MaxMessageLength+1)
	if _, err := client.SendMessage(context.Background(), long); !errors.Is(err, ErrTooLong) {
		t.Errorf("long text = %v, want ErrTooLong", err)
	}
	if _, err := client.SendMessageTo(context.Background(), " ", "hi"); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("empty chat = %v, want ErrInvalidConfig", err)
	}
	var nilCtx context.Context
	if _, err := client.SendMessage(nilCtx, "hi"); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("nil context = %v, want ErrInvalidConfig", err)
	}
}

func TestSendMessageRetriesRateLimits(t *testing.T) {
	seen := &capture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.record(r)
		if seen.count() == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":0}}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":2}}`)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, nil)
	if _, err := client.SendMessage(context.Background(), "hi"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if seen.count() != 2 {
		t.Errorf("requests = %d, want the rate limit retried once", seen.count())
	}
}

func TestSendMessageRetriesServerFaults(t *testing.T) {
	seen := &capture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.record(r)
		if seen.count() < 3 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":502,"description":"Bad Gateway"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":3}}`)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, func(o *Options) { o.MaxAttempts = 3 })
	if _, err := client.SendMessage(context.Background(), "hi"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if seen.count() != 3 {
		t.Errorf("requests = %d, want 3", seen.count())
	}
}

func TestSendMessageStopsAfterTheAttemptBudget(t *testing.T) {
	seen := &capture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.record(r)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":500,"description":"Internal Server Error"}`)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, func(o *Options) { o.MaxAttempts = 2 })
	_, err := client.SendMessage(context.Background(), "hi")
	if !errors.Is(err, ErrNotDelivered) {
		t.Fatalf("error = %v, want ErrNotDelivered", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !apiErr.Retryable() {
		t.Errorf("error = %v, want a retryable APIError", err)
	}
	if seen.count() != 2 {
		t.Errorf("requests = %d, want the budget of 2", seen.count())
	}
}

func TestSendMessageDoesNotRetryRejectedCredentials(t *testing.T) {
	seen := &capture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.record(r)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, nil)
	_, err := client.SendMessage(context.Background(), "hi")
	if !errors.Is(err, ErrNotDelivered) {
		t.Fatalf("error = %v, want ErrNotDelivered", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want an APIError", err)
	}
	if apiErr.Retryable() {
		t.Error("a rejected token must not be retryable")
	}
	if apiErr.StatusCode != http.StatusUnauthorized || apiErr.ErrorCode != 401 {
		t.Errorf("api error = %+v, want the status and code", apiErr)
	}
	if seen.count() != 1 {
		t.Errorf("requests = %d, want no retry", seen.count())
	}
}

func TestSendMessageRejectsAMalformedReply(t *testing.T) {
	seen := &capture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.record(r)
		_, _ = io.WriteString(w, "this is not json")
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, func(o *Options) { o.MaxAttempts = 2 })
	_, err := client.SendMessage(context.Background(), "hi")
	if !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("error = %v, want ErrMalformedResponse", err)
	}
	if seen.count() != 2 {
		t.Errorf("requests = %d, want the malformed reply retried to the budget", seen.count())
	}
}

func TestSendMessageReportsATransportFailureWithoutTheToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	baseURL := server.URL
	server.Close() // nothing is listening any more

	client := newTestClient(t, baseURL, func(o *Options) { o.MaxAttempts = 2 })
	_, err := client.SendMessage(context.Background(), "hi")
	if !errors.Is(err, ErrNotDelivered) {
		t.Fatalf("error = %v, want ErrNotDelivered", err)
	}
	if strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "TEST-token") {
		t.Errorf("error leaks the token: %q", err)
	}
	var transportErr *transportError
	if !errors.As(err, &transportErr) || !transportErr.Retryable() {
		t.Errorf("error = %v, want a retryable transportError", err)
	}
}

func TestSendMessageHonoursACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	seen := &capture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.record(r)
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, nil)
	if _, err := client.SendMessage(ctx, "hi"); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	if seen.count() != 0 {
		t.Errorf("requests = %d, want none", seen.count())
	}
}

func TestSendMessageAbortsTheBackoffWhenCancelled(t *testing.T) {
	seen := &capture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.record(r)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":500,"description":"boom"}`)
	}))
	defer server.Close()

	clk := clock.NewFake(time.Now())
	client := newTestClient(t, server.URL, func(o *Options) {
		o.Clock = clk
		o.MaxAttempts = 5
		o.InitialBackoff = time.Minute // only the fake clock could advance past this
		o.MaxBackoff = time.Minute
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := client.SendMessage(ctx, "hi")
		done <- err
	}()

	deadline := time.Now().Add(2 * time.Second)
	for seen.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SendMessage ignored the cancel during its backoff")
	}
}

func TestSendMessageAppliesItsTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer func() {
		close(release)
		server.Close()
	}()

	client := newTestClient(t, server.URL, func(o *Options) {
		o.Timeout = 20 * time.Millisecond
		o.MaxAttempts = 1
	})
	if _, err := client.SendMessage(context.Background(), "hi"); err == nil {
		t.Fatal("SendMessage = nil error, want a timeout")
	}
}

func TestInterpretValidatesTheEnvelope(t *testing.T) {
	client := newTestClient(t, DefaultBaseURL, nil)
	header := http.Header{}

	if _, err := client.interpret("sendMessage", 200, header, nil); !errors.Is(err, ErrMalformedResponse) {
		t.Errorf("empty body = %v, want ErrMalformedResponse", err)
	}
	if _, err := client.interpret("sendMessage", 200, header, []byte("{")); !errors.Is(err, ErrMalformedResponse) {
		t.Errorf("broken JSON = %v, want ErrMalformedResponse", err)
	}

	result, err := client.interpret("sendMessage", 200, header, []byte(`{"ok":true,"result":{"message_id":5}}`))
	if err != nil || !strings.Contains(string(result), "message_id") {
		t.Fatalf("interpret = %q, %v, want the result payload", result, err)
	}

	_, err = client.interpret("sendMessage", 200, header, []byte(`{"ok":false,"description":"bad chat"}`))
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("ok=false = %v, want an APIError", err)
	}
	if apiErr.Description != "bad chat" {
		t.Errorf("description = %q, want the API text", apiErr.Description)
	}

	header.Set("Retry-After", "7")
	_, err = client.interpret("sendMessage", 429, header,
		[]byte(`{"ok":false,"description":"slow down"}`))
	if !errors.As(err, &apiErr) || apiErr.RetryAfter != 7*time.Second {
		t.Errorf("error = %v, want the Retry-After header honoured", err)
	}

	_, err = client.interpret("sendMessage", 429, http.Header{},
		[]byte(`{"ok":false,"description":"slow down","parameters":{"retry_after":30}}`))
	if !errors.As(err, &apiErr) || apiErr.RetryAfter != 30*time.Second {
		t.Errorf("error = %v, want the retry_after parameter honoured", err)
	}
}

func TestDelay(t *testing.T) {
	client := newTestClient(t, DefaultBaseURL, func(o *Options) {
		o.InitialBackoff = 2 * time.Second
		o.MaxBackoff = 10 * time.Second
	})

	if got := client.delay(1, errors.New("boom")); got != 2*time.Second {
		t.Errorf("delay(1) = %s, want the initial backoff", got)
	}
	if got := client.delay(2, errors.New("boom")); got != 4*time.Second {
		t.Errorf("delay(2) = %s, want the doubled backoff", got)
	}
	if got := client.delay(9, errors.New("boom")); got != 10*time.Second {
		t.Errorf("delay(9) = %s, want the cap", got)
	}
	if got := client.delay(1, &APIError{RetryAfter: 3 * time.Second}); got != 3*time.Second {
		t.Errorf("delay with retry_after = %s, want 3s", got)
	}
	if got := client.delay(1, &APIError{RetryAfter: time.Hour}); got != 10*time.Second {
		t.Errorf("delay with an enormous retry_after = %s, want the cap", got)
	}
}

func TestRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"api 429", &APIError{StatusCode: 429}, true},
		{"api 500", &APIError{StatusCode: 500}, true},
		{"api 400", &APIError{StatusCode: 400}, false},
		{"transport", &transportError{}, true},
		{"malformed", ErrMalformedResponse, true},
		{"unknown", errors.New("boom"), true},
	}
	for _, tc := range cases {
		if got := retryable(tc.err); got != tc.want {
			t.Errorf("%s: retryable = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAPIErrorMessage(t *testing.T) {
	plain := &APIError{Method: "sendMessage", StatusCode: 400, Description: "chat not found"}
	if got := plain.Error(); !strings.Contains(got, "sendMessage") ||
		!strings.Contains(got, "400") || !strings.Contains(got, "chat not found") {
		t.Errorf("Error = %q, want method, status and description", got)
	}
	if got := (&APIError{Method: "m", StatusCode: 500}).Error(); !strings.Contains(got, "no description") {
		t.Errorf("Error = %q, want a placeholder description", got)
	}
	limited := &APIError{Method: "m", StatusCode: 429, Description: "slow", RetryAfter: time.Second}
	if got := limited.Error(); !strings.Contains(got, "retry after 1s") {
		t.Errorf("Error = %q, want the retry hint", got)
	}
	if got := (&transportError{Method: "sendMessage", Cause: "connection refused"}).Error(); !strings.Contains(got, "connection refused") {
		t.Errorf("Error = %q, want the cause", got)
	}
}

// TestRenderThenSendEscapesAHostileValueOnTheWire is the escaping path end to
// end: a hostile session id and CLI error go through the real template and the
// real HTTP client, and the bytes that reach the Bot API have already been
// escaped. Asserting this only against Render would miss a client that re-encodes
// the body or drops parse_mode.
func TestRenderThenSendEscapesAHostileValueOnTheWire(t *testing.T) {
	seen := &capture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.record(r)
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1,"date":1767225600,"chat":{"id":42}}}`)
	}))
	defer server.Close()

	hostile := `<script>alert("x")</script> & co`
	body, err := Render(storage.EventRecoveryFailure, EventData{
		SessionID:   hostile,
		Reason:      hostile,
		Attempt:     2,
		MaxAttempts: 5,
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	client := newTestClient(t, server.URL, nil)
	if _, err := client.SendMessage(context.Background(), body); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	text, ok := seen.payload["text"].(string)
	if !ok {
		t.Fatalf("payload text = %v, want a string", seen.payload["text"])
	}
	if strings.Contains(text, "<script>") || strings.Contains(text, "& co") {
		t.Errorf("the wire body leaked unescaped input: %q", text)
	}
	if !strings.Contains(text, "&lt;script&gt;") || !strings.Contains(text, "&amp; co") {
		t.Errorf("the wire body is not escaped: %q", text)
	}
	if seen.payload["parse_mode"] != ParseModeHTML {
		t.Errorf("parse_mode = %v, want %s, or escaping means nothing", seen.payload["parse_mode"], ParseModeHTML)
	}
}
