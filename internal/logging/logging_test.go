package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestParseLevel(t *testing.T) {
	t.Parallel()

	tests := map[string]slog.Level{
		"":        slog.LevelInfo,
		"info":    slog.LevelInfo,
		"INFO":    slog.LevelInfo,
		"debug":   slog.LevelDebug,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
	}
	for input, want := range tests {
		got, err := ParseLevel(input)
		if err != nil {
			t.Errorf("ParseLevel(%q): %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", input, got, want)
		}
	}

	if _, err := ParseLevel("verbose"); err == nil {
		t.Error("ParseLevel should reject an unknown level")
	}
}

func TestNewRejectsUnknownFormat(t *testing.T) {
	t.Parallel()

	if _, err := New(Options{Format: "xml"}); err == nil {
		t.Fatal("expected an error for an unknown format")
	}
}

func TestNewRejectsUnknownLevel(t *testing.T) {
	t.Parallel()

	if _, err := New(Options{Level: "loud"}); err == nil {
		t.Fatal("expected an error for an unknown level")
	}
}

func TestJSONLoggerRedactsSensitiveKeys(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger, err := New(Options{Level: "debug", Format: "json", Output: &buf})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	logger.Info("config loaded",
		slog.String("bot_token", "123456789:supersecretvalue"),
		slog.String("chat_id", "-100200300"),
		slog.String("api_key", "sk-live-abcdef"),
		slog.String("path", "/home/user/.config/ccresu/config.yaml"),
	)

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("decode log line %q: %v", buf.String(), err)
	}

	for _, key := range []string{"bot_token", "chat_id", "api_key"} {
		if got := record[key]; got != RedactedValue {
			t.Errorf("%s = %v, want %s", key, got, RedactedValue)
		}
	}
	if got := record["path"]; got != "/home/user/.config/ccresu/config.yaml" {
		t.Errorf("non-sensitive attribute was altered: %v", got)
	}
	if record["msg"] != "config loaded" {
		t.Errorf("msg = %v", record["msg"])
	}
}

func TestRedactorScrubsRegisteredSecret(t *testing.T) {
	t.Parallel()

	secret := "123456789:AAH-super-secret-token"
	redactor := NewRedactor()
	redactor.Add(secret)

	var buf bytes.Buffer
	logger, err := New(Options{Format: "json", Output: &buf, Redactor: redactor})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	logger.Error("telegram call failed: "+secret, slog.String("detail", "url contained "+secret))
	logger.Error("wrapped", slog.Any("error", errors.New("boom "+secret)))

	output := buf.String()
	if strings.Contains(output, secret) {
		t.Fatalf("registered secret leaked into logs: %s", output)
	}
	if strings.Count(output, RedactedValue) < 3 {
		t.Fatalf("expected the secret to be redacted in message, attribute and error; got %s", output)
	}
}

func TestRedactorIgnoresShortAndDuplicateSecrets(t *testing.T) {
	t.Parallel()

	redactor := NewRedactor()
	redactor.Add("abc")        // too short to be a secret
	redactor.Add("   ")        // blank
	redactor.Add("longsecret") // registered
	redactor.Add("longsecret") // duplicate
	if got := redactor.Redact("abc longsecret"); got != "abc "+RedactedValue {
		t.Errorf("Redact = %q", got)
	}
}

func TestRedactorNilSafety(t *testing.T) {
	t.Parallel()

	var redactor *Redactor
	redactor.Add("secret-value")
	if got := redactor.Redact("secret-value"); got != "secret-value" {
		t.Errorf("nil redactor should be a no-op, got %q", got)
	}
	if got := redactor.Redact(""); got != "" {
		t.Errorf("empty string should stay empty, got %q", got)
	}
}

func TestIsSensitiveKey(t *testing.T) {
	t.Parallel()

	sensitive := []string{"bot_token", "BOT_TOKEN", "api_key", "apikey", "Authorization", "password", "passwd", "client_secret", "session_cookie", "chat_id", "credentials"}
	for _, key := range sensitive {
		if !IsSensitiveKey(key) {
			t.Errorf("IsSensitiveKey(%q) = false, want true", key)
		}
	}

	benign := []string{"session_id", "path", "level", "msg", "duration"}
	for _, key := range benign {
		if IsSensitiveKey(key) {
			t.Errorf("IsSensitiveKey(%q) = true, want false", key)
		}
	}
}

func TestTextFormatAndLevelFiltering(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger, err := New(Options{Level: "warn", Format: "text", Output: &buf})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	logger.Info("should not appear")
	logger.Warn("should appear")

	output := buf.String()
	if strings.Contains(output, "should not appear") {
		t.Errorf("info record should be filtered out: %s", output)
	}
	if !strings.Contains(output, "should appear") {
		t.Errorf("warn record should be present: %s", output)
	}
}

func TestHandlerWithAttrsAndGroupKeepRedaction(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger, err := New(Options{Format: "json", Output: &buf})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	logger.With("bot_token", "secret-token-value").
		WithGroup("telegram").
		Info("grouped", slog.String("auth_token", "another-secret"))

	output := buf.String()
	if strings.Contains(output, "secret-token-value") || strings.Contains(output, "another-secret") {
		t.Fatalf("grouped attributes leaked: %s", output)
	}
}

func TestHandlerEnabledRespectsLevel(t *testing.T) {
	t.Parallel()

	handler, err := NewHandler(Options{Level: "error", Format: "json", Output: &bytes.Buffer{}})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	if handler.Enabled(t.Context(), slog.LevelInfo) {
		t.Error("info should be disabled at error level")
	}
	if !handler.Enabled(t.Context(), slog.LevelError) {
		t.Error("error should be enabled")
	}
}

func TestTimeSourceOverridesRecordTime(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	fixed := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	logger, err := New(Options{
		Format:     "json",
		Output:     &buf,
		TimeSource: func() time.Time { return fixed },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	logger.Info("pinned")

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if record["time"] != "2026-11-04T12:00:00.000Z" && record["time"] != fixed.Format(time.RFC3339) {
		t.Errorf("time = %v, want %v", record["time"], fixed.Format(time.RFC3339))
	}
}

func TestMaskSecret(t *testing.T) {
	t.Parallel()

	if got := MaskSecret(""); got != "" {
		t.Errorf("MaskSecret(\"\") = %q", got)
	}
	if got := MaskSecret("short"); got != "*****" {
		t.Errorf("MaskSecret(short) = %q", got)
	}
	got := MaskSecret("123456789:AAH-super-secret")
	if !strings.HasSuffix(got, "cret") {
		t.Errorf("MaskSecret should keep the last four characters, got %q", got)
	}
	if strings.Contains(got, "123456789") {
		t.Errorf("MaskSecret leaked the token prefix: %q", got)
	}
}
