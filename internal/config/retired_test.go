package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A configuration file written for 0.8.x carries telegram.notify_needs_input.
// 0.9.0 removed the knob, and the loader rejects unknown keys, so without a
// tolerant path the upgrade refuses to start over a key that means nothing. These
// tests pin both halves of that bargain: the retired key is tolerated, and every
// key that was never a key is still refused.

// retiredDoc wraps telegram fragments into a document that otherwise validates.
func retiredDoc(telegram string) []byte {
	return []byte("app:\n  name: ok\ntelegram:\n" + telegram)
}

func TestRetiredKeyStillLoads(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"block style, false":   "  notify_needs_input: false\n",
		"block style, true":    "  notify_needs_input: true\n",
		"first key in section": "  notify_needs_input: false\n  notify_waiting: false\n",
		"last key in section":  "  notify_waiting: false\n  notify_needs_input: false\n",
		"quoted key":           "  \"notify_needs_input\": false\n",
		"repeated key":         "  notify_needs_input: false\n  notify_needs_input: true\n",
		"inline flow mapping":  "",
		"with siblings kept":   "  enabled: false\n  notify_needs_input: false\n  chat_id: \"42\"\n",
	}
	for name, telegram := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			doc := retiredDoc(telegram)
			if name == "inline flow mapping" {
				// The exact shape an operator's file takes after the key was
				// documented as a one-liner.
				doc = []byte("app:\n  name: ok\ntelegram: {notify_needs_input: false}\n")
			}

			cfg, err := parse(doc, noEnv)
			if err != nil {
				t.Fatalf("retired key refused the document: %v", err)
			}

			got := cfg.RetiredKeys()
			if len(got) != 1 || got[0] != "telegram.notify_needs_input" {
				t.Fatalf("RetiredKeys() = %v, want [telegram.notify_needs_input]", got)
			}

			// Tolerated is not the same as honoured: nothing may be enabled by
			// the retired key. Its own meaning is gone, so the notify defaults
			// decide every switch.
			want := Defaults().Telegram
			if got := cfg.Telegram; got.NotifyLimitDetected != want.NotifyLimitDetected ||
				got.NotifyWaiting != want.NotifyWaiting ||
				got.NotifyRecoveryStarted != want.NotifyRecoveryStarted ||
				got.NotifyRecoverySuccess != want.NotifyRecoverySuccess ||
				got.NotifyRecoveryFailure != want.NotifyRecoveryFailure ||
				got.NotifyPermanentFailure != want.NotifyPermanentFailure ||
				got.NotifySessionCompleted != want.NotifySessionCompleted {
				t.Fatalf("retired key changed a notification switch: %+v, want defaults", got)
			}

			warnings := strings.Join(cfg.Warnings(), "\n")
			if !strings.Contains(warnings, "telegram.notify_needs_input") {
				t.Fatalf("no warning names the ignored key: %q", warnings)
			}
			if !strings.Contains(warnings, "no longer a configuration key") {
				t.Fatalf("warning does not say the key is gone: %q", warnings)
			}
		})
	}
}

// TestRetiredKeyLeavesNeighboursIntact proves the pruning pass drops one key and
// nothing else: a value that sits next to the retired key still lands where the
// document put it.
func TestRetiredKeyLeavesNeighboursIntact(t *testing.T) {
	t.Parallel()

	cfg, err := parse([]byte(`
app:
  name: neighbour
telegram:
  notify_needs_input: false
  notify_waiting: true
  enabled: true
  bot_token: "123456789:AAHsecretvalue_that_is_long"
  chat_id: "-1001234567890"
  parse_mode: HTML
scheduler:
  poll_interval: 45s
`), noEnv)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.App.Name != "neighbour" {
		t.Fatalf("app.name = %q, want neighbour", cfg.App.Name)
	}
	if !cfg.Telegram.NotifyWaiting {
		t.Fatal("notify_waiting was not applied")
	}
	if !cfg.Telegram.Enabled || cfg.Telegram.ChatID != "-1001234567890" {
		t.Fatalf("telegram section was disturbed: %+v", cfg.Telegram)
	}
	if cfg.Scheduler.PollInterval.Duration.String() != "45s" {
		t.Fatalf("poll_interval = %s, want 45s", cfg.Scheduler.PollInterval)
	}
	// The retired key cannot switch anything on: the switches it might have
	// controlled keep their defaults.
	if cfg.Telegram.NotifyLimitDetected != Defaults().Telegram.NotifyLimitDetected {
		t.Fatal("a retired key changed notify_limit_detected")
	}
}

// TestRetiredKeyOnlyMatchesItsOwnPath proves the tolerance is not a blanket
// "ignore notify_needs_input anywhere": the key is retired under telegram and
// nowhere else.
func TestRetiredKeyOnlyMatchesItsOwnPath(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"top level":   "notify_needs_input: false\n",
		"recovery":    "recovery:\n  notify_needs_input: false\n",
		"app":         "app:\n  notify_needs_input: false\n",
		"nested deep": "recovery:\n  guardrails:\n    notify_needs_input: false\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := parse([]byte(doc), noEnv)
			if err == nil {
				t.Fatalf("%q was silently ignored", doc)
			}
			if !strings.Contains(err.Error(), "notify_needs_input") {
				t.Fatalf("error does not name the key: %v", err)
			}
		})
	}
}

// TestRetiredKeyTyposStillRejected is the other half of the bargain: a key that
// was never a key must never be tolerated, or a typo would leave a default in
// place while the operator believes it was set.
func TestRetiredKeyTyposStillRejected(t *testing.T) {
	t.Parallel()

	for _, key := range []string{
		"notify_needs_inputt",
		"notify_needsinput",
		"notify_needs",
		"notify_needs_input_",
		"Notify_Needs_Input",
		"notify_needs_inputs",
		"notify_need_input",
	} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()

			cfg, err := parse(retiredDoc("  "+key+": false\n"), noEnv)
			if err == nil {
				t.Fatalf("typo %q was accepted: %+v", key, cfg.Telegram)
			}
			if !strings.Contains(err.Error(), key) {
				t.Fatalf("error does not name %q: %v", key, err)
			}
		})
	}
}

// TestRetiredKeyDoesNotTouchACleanDocument proves the pass is a no-op for every
// file that carries no retired key, so it cannot regress the strict decode.
func TestRetiredKeyDoesNotTouchACleanDocument(t *testing.T) {
	t.Parallel()

	doc := retiredDoc("  notify_waiting: false\n")
	pruned, removed := pruneRetiredKeys(doc)
	if removed != nil {
		t.Fatalf("clean document reported retired keys: %v", removed)
	}
	if string(pruned) != string(doc) {
		t.Fatalf("clean document was rewritten:\n%s", pruned)
	}

	cfg, err := parse(doc, noEnv)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.RetiredKeys() != nil {
		t.Fatalf("RetiredKeys() = %v, want nil", cfg.RetiredKeys())
	}
	if warnings := strings.Join(cfg.Warnings(), "\n"); strings.Contains(warnings, "no longer a configuration key") {
		t.Fatalf("clean document warned about a retired key: %q", warnings)
	}
}

// TestPruneLeavesMalformedDocumentsToTheStrictDecode proves the pass never hides
// a syntax error: bytes it cannot understand come back untouched so the decoder
// reports them.
func TestPruneLeavesMalformedDocumentsToTheStrictDecode(t *testing.T) {
	t.Parallel()

	for _, doc := range []string{"telegram: [unclosed\n", "\ttelegram:\n", "---\n---\n"} {
		pruned, removed := pruneRetiredKeys([]byte(doc))
		if removed != nil {
			t.Fatalf("%q reported retired keys: %v", doc, removed)
		}
		if string(pruned) != doc {
			t.Fatalf("malformed document was rewritten: %q -> %q", doc, pruned)
		}
	}
	if _, err := parse([]byte("telegram: [unclosed\n"), noEnv); err == nil {
		t.Fatal("malformed yaml parsed without an error")
	}
}

// TestRetiredKeyLoadsFromDisk is the upgrade itself: a file on disk carrying the
// knob loads through LoadWithEnv, and ccresu leaves the operator's file alone.
func TestRetiredKeyLoadsFromDisk(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")
	body := `app:
  name: from-disk
telegram:
  enabled: false
  notify_needs_input: false
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadWithEnv(path, noEnv)
	if err != nil {
		t.Fatalf("LoadWithEnv refused a file with the retired key: %v", err)
	}
	if cfg.App.Name != "from-disk" {
		t.Fatalf("app.name = %q, want from-disk", cfg.App.Name)
	}
	if got := cfg.RetiredKeys(); len(got) != 1 || got[0] != "telegram.notify_needs_input" {
		t.Fatalf("RetiredKeys() = %v", got)
	}
	if cfg.Source != path {
		t.Fatalf("Source = %q, want %q", cfg.Source, path)
	}

	// The loader reads; it must not rewrite a file it was asked to load.
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(after) != body {
		t.Fatalf("loader rewrote the configuration file:\n%s", after)
	}
}

// TestRetiredKeysAreDocumentedAsPermanent keeps the list honest: a retired key is
// withdrawn for good, so the loader never stops tolerating the releases that
// shipped before the removal.
func TestRetiredKeysAreDocumentedAsPermanent(t *testing.T) {
	t.Parallel()

	if len(retiredKeys) == 0 {
		t.Fatal("retiredKeys is empty: the withdrawal this loader exists for is not recorded")
	}
	for _, path := range retiredKeys {
		if len(path) < 2 {
			t.Fatalf("retired key %v must name its section, not a bare top-level key", path)
		}
		for _, part := range path {
			if strings.TrimSpace(part) != part || part == "" {
				t.Fatalf("retired path %v has a malformed segment", path)
			}
		}
	}
	// The teardown of 0.9.0 is the reason the list exists at all.
	found := false
	for _, path := range retiredKeys {
		if strings.Join(path, ".") == "telegram.notify_needs_input" {
			found = true
		}
	}
	if !found {
		t.Fatal("telegram.notify_needs_input is no longer tolerated: an 0.8.x file will not load")
	}
}
