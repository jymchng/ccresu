// Package logging provides structured logging with secret redaction.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// Options configures the logger produced by New.
type Options struct {
	// Level is one of debug, info, warn, error. Empty means info.
	Level string
	// Format is json or text. Empty means json.
	Format string
	// Output receives the log records. Empty means os.Stderr.
	Output io.Writer
	// Redactor is consulted for every attribute and message. Nil means a new
	// empty redactor, so New never returns a logger that leaks secrets.
	Redactor *Redactor
	// TimeSource supplies record timestamps; nil means the system clock.
	TimeSource func() time.Time
}

// New builds a slog.Logger with the requested level, format and redaction.
//
// It returns an error for an unknown level or format rather than silently
// falling back, so a typo in the configuration cannot hide log output.
func New(opts Options) (*slog.Logger, error) {
	handler, err := NewHandler(opts)
	if err != nil {
		return nil, err
	}
	return slog.New(handler), nil
}

// NewHandler builds the redacting slog.Handler used by New.
func NewHandler(opts Options) (slog.Handler, error) {
	level, err := ParseLevel(opts.Level)
	if err != nil {
		return nil, err
	}

	out := opts.Output
	if out == nil {
		out = os.Stderr
	}

	redactor := opts.Redactor
	if redactor == nil {
		redactor = NewRedactor()
	}

	handlerOpts := &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: redactor.replaceAttr,
	}

	var base slog.Handler
	switch strings.ToLower(strings.TrimSpace(opts.Format)) {
	case "", "json":
		base = slog.NewJSONHandler(out, handlerOpts)
	case "text":
		base = slog.NewTextHandler(out, handlerOpts)
	default:
		return nil, fmt.Errorf("unsupported log format %q: expected json or text", opts.Format)
	}

	return &redactingHandler{inner: base, redactor: redactor, now: opts.TimeSource}, nil
}

// ParseLevel converts a configured level name into a slog.Level.
func ParseLevel(level string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unsupported log level %q: expected debug, info, warn or error", level)
	}
}

// MaskSecret renders a secret for logs and API responses without revealing it.
func MaskSecret(secret string) string {
	trimmed := strings.TrimSpace(secret)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) <= 8 {
		return strings.Repeat("*", len(trimmed))
	}
	return strings.Repeat("*", 8) + trimmed[len(trimmed)-4:]
}

// RedactedValue is substituted for anything that must not be logged.
const RedactedValue = "[REDACTED]"

// sensitiveFragments identify attribute keys whose values are never logged.
var sensitiveFragments = []string{
	"token",
	"secret",
	"password",
	"passwd",
	"authorization",
	"api_key",
	"apikey",
	"credential",
	"cookie",
	"chat_id",
}

// Redactor masks registered secrets and secret-shaped values wherever they
// appear: in attribute keys, string values, error text and log messages.
//
// A Redactor is safe for concurrent use.
type Redactor struct {
	mu      sync.RWMutex
	secrets []string
}

// NewRedactor returns an empty Redactor.
func NewRedactor() *Redactor { return &Redactor{} }

// Add registers a literal secret to be scrubbed from all output. Values shorter
// than 6 bytes are ignored: masking them would mangle ordinary words without
// protecting anything meaningful.
func (r *Redactor) Add(secret string) {
	trimmed := strings.TrimSpace(secret)
	if r == nil || len(trimmed) < 6 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.secrets {
		if existing == trimmed {
			return
		}
	}
	r.secrets = append(r.secrets, trimmed)
}

// Redact replaces every registered secret in s with RedactedValue.
func (r *Redactor) Redact(s string) string {
	if r == nil || s == "" {
		return s
	}
	r.mu.RLock()
	secrets := r.secrets
	r.mu.RUnlock()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(s, secret) {
			s = strings.ReplaceAll(s, secret, RedactedValue)
		}
	}
	return s
}

// IsSensitiveKey reports whether an attribute key names a secret-bearing field.
func IsSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	for _, fragment := range sensitiveFragments {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return false
}

func (r *Redactor) replaceAttr(_ []string, attr slog.Attr) slog.Attr {
	if IsSensitiveKey(attr.Key) && attr.Value.Kind() != slog.KindGroup {
		return slog.String(attr.Key, RedactedValue)
	}
	switch attr.Value.Kind() {
	case slog.KindString:
		return slog.String(attr.Key, r.Redact(attr.Value.String()))
	case slog.KindAny:
		switch v := attr.Value.Any().(type) {
		case error:
			return slog.String(attr.Key, r.Redact(v.Error()))
		case fmt.Stringer:
			return slog.String(attr.Key, r.Redact(v.String()))
		}
	}
	return attr
}

// redactingHandler scrubs the record message before delegating to the inner
// handler, so secrets embedded in formatted messages are caught too.
type redactingHandler struct {
	inner    slog.Handler
	redactor *Redactor
	now      func() time.Time
}

func (h *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *redactingHandler) Handle(ctx context.Context, record slog.Record) error {
	if h.now != nil {
		record.Time = h.now()
	}
	record.Message = h.redactor.Redact(record.Message)
	return h.inner.Handle(ctx, record)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &redactingHandler{inner: h.inner.WithAttrs(attrs), redactor: h.redactor, now: h.now}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{inner: h.inner.WithGroup(name), redactor: h.redactor, now: h.now}
}
