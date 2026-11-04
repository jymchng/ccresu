package logging_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/logging"
)

const secret = "123456789:AAHsuper-secret-bot-token_value"

type stringer struct{ v string }

func (s stringer) String() string { return s.v }

// TestVerifySecretsNeverEscape proves a registered secret cannot reach any
// output path: message, attribute, nested group, error, Stringer, or key shapes,
// including after WithAttrs/WithGroup.
func TestVerifySecretsNeverEscape(t *testing.T) {
	var buf bytes.Buffer
	redactor := logging.NewRedactor()
	redactor.Add(secret)

	logger, err := logging.New(logging.Options{
		Level:      "debug",
		Format:     "json",
		Output:     &buf,
		Redactor:   redactor,
		TimeSource: func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	child := logger.With("component", "worker").WithGroup("net")
	child.Info("connecting with "+secret, "detail", "url="+secret)
	child.Error("request failed", "err", errors.New("401 for "+secret))
	child.Warn("stringer", "value", stringer{v: secret})
	child.Debug("nested", "payload", map[string]any{"note": secret})
	logger.Info("keys", "bot_token", secret, "Authorization", "Bearer "+secret,
		"api_key", secret, "chat_id", "12345", "password", secret, "cookie", secret)

	out := buf.String()
	if strings.Contains(out, "AAHsuper-secret") || strings.Contains(out, "bot-token_value") {
		t.Fatalf("secret leaked into log output:\n%s", out)
	}
	if !strings.Contains(out, logging.RedactedValue) {
		t.Fatalf("expected %q markers in output:\n%s", logging.RedactedValue, out)
	}

	// Every line must be valid JSON: the handler must not corrupt records.
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("invalid JSON log line %q: %v", line, err)
		}
		if ts, ok := rec["time"].(string); !ok || !strings.HasPrefix(ts, "2026-01-02T03:04:05") {
			t.Fatalf("TimeSource not honoured: %v", rec["time"])
		}
	}
}

// TestVerifyUnknownLevelAndFormatRejected proves a typo cannot silently fall
// back to a working default.
func TestVerifyUnknownLevelAndFormatRejected(t *testing.T) {
	if _, err := logging.New(logging.Options{Level: "verbose", Output: &bytes.Buffer{}}); err == nil {
		t.Fatal("unknown level was accepted")
	}
	if _, err := logging.New(logging.Options{Format: "logfmt", Output: &bytes.Buffer{}}); err == nil {
		t.Fatal("unknown format was accepted")
	}
	if _, err := logging.New(logging.Options{Level: "debug", Format: "text", Output: &bytes.Buffer{}}); err != nil {
		t.Fatalf("valid text logger: %v", err)
	}
}

// TestVerifyLevelFiltering proves the configured level is honoured.
func TestVerifyLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	logger, err := logging.New(logging.Options{Level: "warn", Output: &buf})
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("info-hidden")
	logger.Warn("warn-shown")
	out := buf.String()
	if strings.Contains(out, "info-hidden") {
		t.Fatalf("info record emitted at warn level: %s", out)
	}
	if !strings.Contains(out, "warn-shown") {
		t.Fatalf("warn record missing: %s", out)
	}
}

// TestVerifyDefaultRedactorIsNotNil proves New never yields a logger without a
// redactor (default protects nothing registered, but nil pointer is impossible).
func TestVerifyDefaultLoggerWorks(t *testing.T) {
	var buf bytes.Buffer
	logger, err := logging.New(logging.Options{Output: &buf})
	if err != nil {
		t.Fatalf("default New: %v", err)
	}
	logger.Info("hello", "session", "abc")
	if !strings.Contains(buf.String(), "hello") {
		t.Fatalf("default logger swallowed output: %s", buf.String())
	}
	var _ *slog.Logger = logger
}

type logValuer struct{ v string }

func (l logValuer) LogValue() slog.Value { return slog.StringValue(l.v) }

// TestVerifyAnyAndLogValuerPaths covers the non-string attribute paths that can
// otherwise smuggle a secret past redaction.
func TestVerifyAnyAndLogValuerPaths(t *testing.T) {
	var buf bytes.Buffer
	redactor := logging.NewRedactor()
	redactor.Add(secret)
	logger, err := logging.New(logging.Options{Level: "debug", Output: &buf, Redactor: redactor})
	if err != nil {
		t.Fatal(err)
	}

	logger.Info("nil-payload", "payload", nil)
	logger.Info("logvaluer", "value", logValuer{v: secret})
	logger.Info("slice", "args", []string{"--token", secret})
	logger.Info("struct", "session", struct {
		ID    string
		Token string
	}{ID: "abc", Token: secret})
	logger.Info("clean-map", "meta", map[string]any{"ok": true})

	out := buf.String()
	if strings.Contains(out, "AAHsuper-secret") || strings.Contains(out, "bot-token_value") {
		t.Fatalf("secret leaked:\n%s", out)
	}
	if !strings.Contains(out, `"nil-payload"`) {
		t.Fatalf("nil payload record missing:\n%s", out)
	}
	if !strings.Contains(out, `"clean-map"`) {
		t.Fatalf("clean map record missing:\n%s", out)
	}
	redactor.Add("not-present-anywhere")
	if redactor.Redact("nothing here") != "nothing here" {
		t.Fatal("Redact altered a secret-free string")
	}
	if logging.MaskSecret("") != "" {
		t.Fatal("MaskSecret of empty string must be empty")
	}
}
