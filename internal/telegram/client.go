package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jymchng/ccresu/internal/clock"
	"github.com/jymchng/ccresu/internal/logging"
)

// The Bot API contract ccresu relies on.
const (
	// DefaultBaseURL is the public Bot API endpoint.
	DefaultBaseURL = "https://api.telegram.org"
	// DefaultTimeout bounds one HTTP round trip.
	DefaultTimeout = 30 * time.Second
	// DefaultMaxAttempts bounds the retry loop for one message.
	DefaultMaxAttempts = 4
	// DefaultInitialBackoff is the wait after the first retryable failure.
	DefaultInitialBackoff = 2 * time.Second
	// DefaultMaxBackoff caps the computed wait, including a requested retry_after.
	DefaultMaxBackoff = 2 * time.Minute
	// MaxMessageLength is the Bot API's hard limit on message text.
	MaxMessageLength = 4096
	// maxResponseBytes caps how much of a reply is read, so a hostile or broken
	// endpoint cannot exhaust memory.
	maxResponseBytes = 1 << 20
)

// Message is a delivered Telegram message.
type Message struct {
	// MessageID is the Bot API's identifier for the message.
	MessageID int64
	// ChatID is the chat the message landed in.
	ChatID int64
	// Date is when Telegram accepted the message.
	Date time.Time
	// Text is the text Telegram stored, when it echoes it back.
	Text string
}

// Options configures a Client.
type Options struct {
	// Token is the bot token from BotFather. Required.
	Token string
	// ChatID is the default destination. Required.
	ChatID string
	// BaseURL overrides the API root; tests point it at httptest.
	BaseURL string
	// HTTPClient runs the requests. Nil means a client with no timeout of its
	// own, because Timeout is applied per request through the context.
	HTTPClient *http.Client
	// Timeout bounds one request. Zero means DefaultTimeout.
	Timeout time.Duration
	// MaxAttempts bounds the retry loop. Zero means DefaultMaxAttempts.
	MaxAttempts int
	// InitialBackoff and MaxBackoff shape the retry wait.
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	// ParseMode is the Bot API parse mode. Only "HTML" is supported: the
	// templates emit HTML spans and escape for the HTML parser, so any other
	// mode is rejected here instead of shipping markup Telegram cannot read.
	ParseMode string
	// Clock drives the retry waits. Nil means the system clock.
	Clock clock.Clock
	// Logger receives delivery diagnostics. Nil means slog.Default().
	Logger *slog.Logger
	// Redactor is shared with the rest of the application so the token is
	// scrubbed from every log line. Nil means a private redactor.
	Redactor *logging.Redactor
}

// Client is a Bot API client for one bot token.
//
// It is safe for concurrent use. The token is never included in an error
// message or a log record: the only place it appears is the request URL, and
// every error is scrubbed before it is returned.
type Client struct {
	token     string
	chatID    string
	baseURL   string
	parseMode string
	http      *http.Client
	timeout   time.Duration
	attempts  int
	initial   time.Duration
	max       time.Duration
	clock     clock.Clock
	log       *slog.Logger
	redactor  *logging.Redactor
}

// NewClient validates opts and builds a Client. It performs no I/O, so it is
// safe to construct before the network (or the token) works.
func NewClient(opts Options) (*Client, error) {
	token := strings.TrimSpace(opts.Token)
	if token == "" {
		return nil, fmt.Errorf("%w: bot token is required", ErrInvalidConfig)
	}
	if strings.ContainsAny(token, " \t\r\n") {
		return nil, fmt.Errorf("%w: bot token must not contain whitespace", ErrInvalidConfig)
	}
	chatID := strings.TrimSpace(opts.ChatID)
	if chatID == "" {
		return nil, fmt.Errorf("%w: chat id is required", ErrInvalidConfig)
	}

	parseMode := strings.TrimSpace(opts.ParseMode)
	switch strings.ToLower(parseMode) {
	case "", "html":
		parseMode = ParseModeHTML
	default:
		// A per-mode escaper and per-mode templates would be needed to send
		// anything else; ccresu has neither, so the mode is refused rather than
		// sent and silently mangled.
		return nil, fmt.Errorf("%w: unsupported parse mode %q: ccresu only sends %s",
			ErrInvalidConfig, opts.ParseMode, ParseModeHTML)
	}

	baseURL := strings.TrimSpace(opts.BaseURL)
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		return nil, fmt.Errorf("%w: base URL %q must be http or https", ErrInvalidConfig, opts.BaseURL)
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	attempts := opts.MaxAttempts
	if attempts <= 0 {
		attempts = DefaultMaxAttempts
	}
	initial := opts.InitialBackoff
	if initial <= 0 {
		initial = DefaultInitialBackoff
	}
	maxBackoff := opts.MaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = DefaultMaxBackoff
	}
	if maxBackoff < initial {
		maxBackoff = initial
	}

	clk := opts.Clock
	if clk == nil {
		clk = clock.System()
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	redactor := opts.Redactor
	if redactor == nil {
		redactor = logging.NewRedactor()
	}
	// Registering the token is what guarantees no error, log line or API
	// response can echo it back to the user.
	redactor.Add(token)

	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}

	return &Client{
		token:     token,
		chatID:    chatID,
		baseURL:   strings.TrimRight(baseURL, "/"),
		parseMode: parseMode,
		http:      httpClient,
		timeout:   timeout,
		attempts:  attempts,
		initial:   initial,
		max:       maxBackoff,
		clock:     clk,
		log:       logger,
		redactor:  redactor,
	}, nil
}

// ChatID reports the configured default destination.
func (c *Client) ChatID() string { return c.chatID }

// MaskedToken renders the token for logs and `ccresu doctor`.
func (c *Client) MaskedToken() string { return logging.MaskSecret(c.token) }

// sendMessagePayload is the Bot API request body.
type sendMessagePayload struct {
	ChatID string `json:"chat_id"`
	Text   string `json:"text"`
	// ParseMode is omitted when empty so Telegram treats the text as plain.
	ParseMode string `json:"parse_mode,omitempty"`
	// DisableWebPagePreview keeps a URL in an error message from turning into a
	// preview card.
	DisableWebPagePreview bool `json:"disable_web_page_preview"`
}

// apiMessage is the Bot API message object, reduced to what ccresu uses.
type apiMessage struct {
	MessageID int64  `json:"message_id"`
	Date      int64  `json:"date"`
	Text      string `json:"text"`
	Chat      struct {
		ID int64 `json:"id"`
	} `json:"chat"`
}

// toMessage converts the API object into the package's Message.
func (m apiMessage) toMessage() Message {
	message := Message{MessageID: m.MessageID, ChatID: m.Chat.ID, Text: m.Text}
	if m.Date > 0 {
		message.Date = time.Unix(m.Date, 0).UTC()
	}
	return message
}

// SendMessage delivers text to the configured chat.
func (c *Client) SendMessage(ctx context.Context, text string) (Message, error) {
	return c.SendMessageTo(ctx, c.chatID, text)
}

// SendMessageTo delivers text to chatID.
//
// The call is retried on rate limits, server faults and transport failures, and
// every wait honours ctx so a shutdown is never delayed by a backoff.
func (c *Client) SendMessageTo(ctx context.Context, chatID, text string) (Message, error) {
	if ctx == nil {
		return Message{}, fmt.Errorf("%w: nil context", ErrInvalidConfig)
	}
	if strings.TrimSpace(chatID) == "" {
		return Message{}, fmt.Errorf("%w: chat id is required", ErrInvalidConfig)
	}
	if strings.TrimSpace(text) == "" {
		return Message{}, ErrEmptyMessage
	}
	if length := utf8.RuneCountInString(text); length > MaxMessageLength {
		return Message{}, fmt.Errorf("%w: %d characters, limit %d", ErrTooLong, length, MaxMessageLength)
	}

	payload := sendMessagePayload{
		ChatID:                chatID,
		Text:                  text,
		ParseMode:             c.parseMode,
		DisableWebPagePreview: true,
	}
	var sent apiMessage
	if err := c.call(ctx, "sendMessage", payload, &sent); err != nil {
		return Message{}, err
	}
	return sent.toMessage(), nil
}

// call performs one Bot API method with the retry policy applied.
func (c *Client) call(ctx context.Context, method string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("%w: encode %s payload: %v", ErrInvalidConfig, method, err)
	}

	attempts := c.attempts
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}

		status, header, raw, postErr := c.post(ctx, method, body)
		switch {
		case postErr != nil:
			// A cancelled context is the caller's decision, not a delivery
			// failure, so it is reported as itself.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			lastErr = &transportError{Method: method, Cause: c.redact(postErr.Error())}
		default:
			result, apiErr := c.interpret(method, status, header, raw)
			if apiErr == nil {
				if out != nil && len(result) > 0 {
					if decodeErr := json.Unmarshal(result, out); decodeErr != nil {
						return fmt.Errorf("%w: %s result: %v", ErrMalformedResponse, method, decodeErr)
					}
				}
				return nil
			}
			lastErr = apiErr
		}

		if attempt == attempts || !retryable(lastErr) {
			break
		}
		delay := c.delay(attempt, lastErr)
		c.log.Debug("retrying telegram call",
			"method", method, "attempt", attempt, "delay", delay.String(), "error", lastErr.Error())
		if delay <= 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.clock.After(delay):
		}
	}
	return lastErr
}

// post performs one HTTP round trip and returns the status, headers and body.
func (c *Client) post(ctx context.Context, method string, body []byte) (int, http.Header, []byte, error) {
	requestCtx := ctx
	cancel := func() {}
	if _, ok := ctx.Deadline(); !ok && c.timeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, c.timeout)
	}
	defer cancel()

	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, c.endpoint(method), bytes.NewReader(body))
	if err != nil {
		// This is the one error built from the request URL, and the URL carries
		// the bot token. Scrubbing it at the origin keeps the guarantee local
		// instead of relying on every caller to remember.
		return 0, nil, nil, errors.New(c.redact(err.Error()))
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := c.http.Do(request)
	if err != nil {
		return 0, nil, nil, err
	}
	defer func() {
		// Drain a bounded amount so the connection can be reused, then close.
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<16))
		_ = response.Body.Close()
	}()

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return response.StatusCode, response.Header, nil, err
	}
	return response.StatusCode, response.Header, raw, nil
}

// interpret turns one reply into either a result or an APIError.
func (c *Client) interpret(method string, status int, header http.Header, raw []byte) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: %s returned an empty body with status %d",
			ErrMalformedResponse, method, status)
	}

	var envelope struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
		ErrorCode   int             `json:"error_code"`
		Parameters  *struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("%w: %s returned invalid JSON with status %d",
			ErrMalformedResponse, method, status)
	}

	if status >= 400 || status == http.StatusTooManyRequests || !envelope.OK {
		apiErr := &APIError{
			Method:      method,
			StatusCode:  status,
			ErrorCode:   envelope.ErrorCode,
			Description: c.redact(envelope.Description),
		}
		if envelope.Parameters != nil && envelope.Parameters.RetryAfter > 0 {
			apiErr.RetryAfter = time.Duration(envelope.Parameters.RetryAfter) * time.Second
		}
		if apiErr.RetryAfter == 0 {
			if seconds, convErr := strconv.Atoi(strings.TrimSpace(header.Get("Retry-After"))); convErr == nil && seconds > 0 {
				apiErr.RetryAfter = time.Duration(seconds) * time.Second
			}
		}
		return nil, apiErr
	}
	return envelope.Result, nil
}

// delay computes the wait before the next attempt.
//
// A retry_after from the API wins because it is the server's own instruction;
// otherwise the backoff doubles from Initial and is capped by Max.
func (c *Client) delay(attempt int, err error) time.Duration {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
		if apiErr.RetryAfter > c.max {
			return c.max
		}
		return apiErr.RetryAfter
	}
	delay := c.initial
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= c.max {
			return c.max
		}
	}
	if delay > c.max {
		return c.max
	}
	return delay
}

// endpoint builds the method URL. The token appears only here.
func (c *Client) endpoint(method string) string {
	return c.baseURL + "/bot" + c.token + "/" + method
}

// redact scrubs the bot token (and anything else registered) from s.
func (c *Client) redact(s string) string {
	if c.redactor == nil {
		return s
	}
	return c.redactor.Redact(s)
}
