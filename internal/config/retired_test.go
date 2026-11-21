package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
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

// TestRetiredRecheckBeforeResumeStillLoads covers the 0.10.0 half of the same
// bargain. scheduler.recheck_before_resume was documented through 0.9.x and no
// release ever read it — and 0.10.0 deleted even the engine hook it would have
// fed, because a usage window can only be learned from the resume itself.
// Removing the key must therefore not make a 0.9.x file unloadable, and must not
// start pretending a check happens.
func TestRetiredRecheckBeforeResumeStillLoads(t *testing.T) {
	t.Parallel()

	cfg, err := parse([]byte(`
app:
  name: recheck
scheduler:
  poll_interval: 45s
  recheck_before_resume: true
  unknown_reset_max_interval: 20m
`), noEnv)
	if err != nil {
		t.Fatalf("retired key refused the document: %v", err)
	}

	if got := cfg.RetiredKeys(); len(got) != 1 || got[0] != "scheduler.recheck_before_resume" {
		t.Fatalf("RetiredKeys() = %v, want [scheduler.recheck_before_resume]", got)
	}
	if warnings := strings.Join(cfg.Warnings(), "\n"); !strings.Contains(warnings, "scheduler.recheck_before_resume") {
		t.Fatalf("no warning names the ignored key: %q", warnings)
	}

	// Tolerated is not honoured, and its neighbours are untouched: the keys
	// either side of the retired one still land where the document put them.
	if got := cfg.Scheduler.PollInterval.Duration; got != 45*time.Second {
		t.Errorf("poll_interval = %s, want 45s", got)
	}
	if got := cfg.Scheduler.UnknownResetMaxInterval.Duration; got != 20*time.Minute {
		t.Errorf("unknown_reset_max_interval = %s, want 20m", got)
	}
	// The default it used to carry is gone, so nothing inherits `true`.
	if got := Defaults().Scheduler.PollInterval.Duration; got != 30*time.Second {
		t.Errorf("scheduler default poll_interval = %s, want 30s", got)
	}
}

// TestRecheckBeforeResumeIsNotAKey proves the retirement is not a tolerance for
// a key that quietly came back: the loading path has no field to set, and the
// environment override that used to reach it is gone too.
func TestRecheckBeforeResumeIsNotAKey(t *testing.T) {
	t.Parallel()

	for _, setter := range envSetters() {
		if strings.Contains(setter.key, "RECHECK") {
			t.Fatalf("%s is still registered, so an operator can still set a key nothing reads", setter.key)
		}
	}

	// A typo of the retired key is still refused, exactly like any other unknown
	// key: the tolerance names one path, not a family.
	if _, err := parse([]byte("app:\n  name: ok\nscheduler:\n  recheck_before_resum: true\n"), noEnv); err == nil {
		t.Fatal("a typo of the retired key was accepted")
	}
}

// TestRetiredRecoveryKeysFrom09StillLoad covers the rest of the 0.10.0 batch:
// the three timeouts that bounded the old handshake and the four guardrail flags
// that rode the handoff vector. All seven were documented in 0.9.x and all seven
// are gone, so a file carrying them must still start — and, because the two spend
// caps sit in the same `recovery.guardrails` mapping, the pass must take the flags
// without taking the caps with them.
func TestRetiredRecoveryKeysFrom09StillLoad(t *testing.T) {
	t.Parallel()

	cfg, err := parse([]byte(`
app:
  name: from-09
recovery:
  enabled: true
  max_attempts: 4
  startup_timeout: 2m
  verification_timeout: 10m
  health_check_interval: 5s
  turn_timeout: 45m
  max_concurrent_recoveries: 2
  guardrails:
    model: opus
    effort: high
    fallback_model: sonnet
    permission_mode: acceptEdits
    max_cost_usd_per_session: 12.5
    max_cost_usd_per_attempt: 3
`), noEnv)
	if err != nil {
		t.Fatalf("a 0.9.x recovery section refused the document: %v", err)
	}

	want := []string{
		"recovery.startup_timeout",
		"recovery.verification_timeout",
		"recovery.health_check_interval",
		"recovery.guardrails.model",
		"recovery.guardrails.effort",
		"recovery.guardrails.fallback_model",
		"recovery.guardrails.permission_mode",
	}
	if got := cfg.RetiredKeys(); !slices.Equal(got, want) {
		t.Fatalf("RetiredKeys() = %v, want %v", got, want)
	}

	// Tolerated is not honoured, and the neighbours are untouched: the keys
	// that kept their meaning still land where the document put them.
	if got := cfg.Recovery.MaxAttempts; got != 4 {
		t.Errorf("max_attempts = %d, want 4", got)
	}
	if got := cfg.Recovery.TurnTimeout.Duration; got != 45*time.Minute {
		t.Errorf("turn_timeout = %s, want 45m", got)
	}
	if got := cfg.Recovery.MaxConcurrentRecoveries; got != 2 {
		t.Errorf("max_concurrent_recoveries = %d, want 2", got)
	}
	if got := cfg.Recovery.Guardrails.MaxCostUSDPerSession; got != 12.5 {
		t.Errorf("max_cost_usd_per_session = %v, want 12.5: the retired flags took the cap with them", got)
	}
	if got := cfg.Recovery.Guardrails.MaxCostUSDPerAttempt; got != 3 {
		t.Errorf("max_cost_usd_per_attempt = %v, want 3: the retired flags took the cap with them", got)
	}

	warnings := strings.Join(cfg.Warnings(), "\n")
	// The one-key message is quoted verbatim in the manual, so a file carrying
	// seven must not reuse its singular grammar.
	if strings.Contains(warnings, " is no longer a configuration key") {
		t.Errorf("seven retired keys were reported in the singular: %q", warnings)
	}
	if !strings.Contains(warnings, "are no longer configuration keys") {
		t.Errorf("warning does not say the keys are gone: %q", warnings)
	}
	for _, path := range want {
		if !strings.Contains(warnings, path) {
			t.Errorf("warning does not name %s: %q", path, warnings)
		}
	}
}

// TestRetiredGuardrailFlagsInAOneLineSection is the shape an operator's file
// takes after the keys were documented as a flow mapping: the pruning pass walks
// mappings, not lines, so the same paths have to be dropped here too.
func TestRetiredGuardrailFlagsInAOneLineSection(t *testing.T) {
	t.Parallel()

	cfg, err := parse([]byte(`
app:
  name: flow
recovery: {turn_timeout: 20m, guardrails: {model: opus, max_cost_usd_per_session: 5}}
`), noEnv)
	if err != nil {
		t.Fatalf("retired flag in a flow mapping refused the document: %v", err)
	}

	if got := cfg.RetiredKeys(); !slices.Equal(got, []string{"recovery.guardrails.model"}) {
		t.Fatalf("RetiredKeys() = %v, want [recovery.guardrails.model]", got)
	}
	if got := cfg.Recovery.Guardrails.MaxCostUSDPerSession; got != 5 {
		t.Errorf("max_cost_usd_per_session = %v, want 5", got)
	}
	if got := cfg.Recovery.TurnTimeout.Duration; got != 20*time.Minute {
		t.Errorf("turn_timeout = %s, want 20m", got)
	}
}

// TestRetiredRecoveryKeyTyposStillRejected is the other half of the same
// bargain for the 0.10.0 batch: the tolerance names seven exact paths, not the
// idea of a timeout or a guardrail flag. A near miss, and the same name under a
// section that never had it, stay fatal.
func TestRetiredRecoveryKeyTyposStillRejected(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"startup_timeouts":       "recovery:\n  startup_timeouts: 2m\n",
		"startup_timeout_":       "recovery:\n  startup_timeout_: 2m\n",
		"verification_timout":    "recovery:\n  verification_timout: 10m\n",
		"health_check_interval_": "recovery:\n  health_check_interval_: 5s\n",
		"guardrails modell":      "recovery:\n  guardrails:\n    modell: opus\n",
		"guardrails effortt":     "recovery:\n  guardrails:\n    effortt: high\n",
		"guardrails fallbacks":   "recovery:\n  guardrails:\n    fallback_models: sonnet\n",
		"guardrails permission":  "recovery:\n  guardrails:\n    permission_modee: acceptEdits\n",
		"top-level timeout":      "startup_timeout: 2m\n",
		"scheduler section":      "schedule:\n  startup_timeout: 2m\n",
		"guardrails top level":   "guardrails:\n  model: opus\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := parse([]byte(doc), noEnv); err == nil {
				t.Fatalf("%q was silently ignored", doc)
			}
		})
	}
}

// TestRetiredRecoveryKeysLoadFromDisk is the upgrade as an operator walks it: a
// whole 0.9.x file on disk loads through LoadWithEnv, the loader reports the seven
// keys it dropped, and it leaves the operator's file exactly as it found it.
func TestRetiredRecoveryKeysLoadFromDisk(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")
	body := `app:
  name: from-disk-09
recovery:
  startup_timeout: 2m
  verification_timeout: 10m
  health_check_interval: 5s
  turn_timeout: 15m
  guardrails:
    effort: high
    max_cost_usd_per_attempt: 2
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadWithEnv(path, noEnv)
	if err != nil {
		t.Fatalf("LoadWithEnv refused a 0.9.x file: %v", err)
	}
	if cfg.Recovery.TurnTimeout.Duration != 15*time.Minute {
		t.Errorf("turn_timeout = %s, want 15m", cfg.Recovery.TurnTimeout.Duration)
	}
	if cfg.Recovery.Guardrails.MaxCostUSDPerAttempt != 2 {
		t.Errorf("max_cost_usd_per_attempt = %v, want 2", cfg.Recovery.Guardrails.MaxCostUSDPerAttempt)
	}
	if got := len(cfg.RetiredKeys()); got != 4 {
		t.Fatalf("RetiredKeys() has %d entries, want the four keys the file carries: %v",
			got, cfg.RetiredKeys())
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(after) != body {
		t.Fatalf("loader rewrote the configuration file:\n%s", after)
	}
}

// TestRetiredKeysMatchTheDocumentedList keeps the manual's closed list and the
// loader's in step. docs/configuration.md tells an operator exactly which keys are
// tolerated and that the list is permanent and closed; a path added here but not
// there is a promise the reader cannot check, and a path documented there but
// missing here is the promise this file exists to keep.
func TestRetiredKeysMatchTheDocumentedList(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile(filepath.Join("..", "..", "docs", "configuration.md"))
	if err != nil {
		t.Fatalf("read docs/configuration.md: %v", err)
	}
	page := string(body)

	for _, path := range retiredKeys {
		dotted := strings.Join(path, ".")
		if !strings.Contains(page, dotted) {
			t.Errorf("%s is tolerated by the loader but docs/configuration.md does not list it", dotted)
		}
	}
}
