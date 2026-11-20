package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRetiredConfig writes a configuration file carrying the retired
// telegram.notify_needs_input key plus whatever extra telegram lines the test
// wants, and returns its path.
func writeRetiredConfig(t *testing.T, telegram string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "app:\n  name: stale\ntelegram:\n" + telegram
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write configuration: %v", err)
	}
	return path
}

// TestConfigValidateAcceptsARetiredKey is the upgrade path an operator actually
// walks: an 0.8.x file still sets telegram.notify_needs_input, and the command
// they run first — `ccresu config validate` — must say the file is valid while
// telling them the key is ignored.
func TestConfigValidateAcceptsARetiredKey(t *testing.T) {
	path := writeRetiredConfig(t, "  notify_needs_input: false\n")
	deps, stdout, stderr := initDeps(&path)

	if code := RunWith(deps, []string{"config", "validate", path}, BuildInfo{}, nil, nil); code != ExitOK {
		t.Fatalf("config validate on a file with the retired key = exit %d, want 0: %s%s",
			code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "is valid") {
		t.Errorf("validate did not report the file valid:\n%s", stdout.String())
	}
	for _, want := range []string{"telegram.notify_needs_input", "no longer a configuration key", "warning:"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("validate stayed silent about the retired key (%q missing):\n%s", want, stderr.String())
		}
	}
}

// TestConfigValidateStillRejectsATypo proves the tolerance did not become a
// blanket: an unknown key next to the retired one is still fatal, and the message
// still names it.
func TestConfigValidateStillRejectsATypo(t *testing.T) {
	for _, key := range []string{"notify_needs_inputt", "notify_need_input"} {
		t.Run(key, func(t *testing.T) {
			path := writeRetiredConfig(t, "  "+key+": false\n")
			deps, stdout, stderr := initDeps(&path)

			if code := RunWith(deps, []string{"config", "validate", path}, BuildInfo{}, nil, nil); code != ExitFailure {
				t.Fatalf("config validate with %q = exit %d, want %d: %s%s",
					key, code, ExitFailure, stdout.String(), stderr.String())
			}
			if !strings.Contains(stderr.String(), key) {
				t.Fatalf("the refusal does not name %q:\n%s", key, stderr.String())
			}
		})
	}
}

// TestConfigValidateIsQuietAboutACleanFile keeps the warning meaningful: a file
// that carries no retired key prints nothing.
func TestConfigValidateIsQuietAboutACleanFile(t *testing.T) {
	path := writeRetiredConfig(t, "  notify_waiting: false\n")
	deps, stdout, stderr := initDeps(&path)

	if code := RunWith(deps, []string{"config", "validate", path}, BuildInfo{}, nil, nil); code != ExitOK {
		t.Fatalf("config validate = exit %d, want 0: %s", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "no longer a configuration key") {
		t.Fatalf("a clean file warned about a retired key:\n%s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "is valid") {
		t.Errorf("validate did not report the file valid:\n%s", stdout.String())
	}
}
