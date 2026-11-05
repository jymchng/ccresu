package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jymchng/ccresu/internal/storage"
)

// globalFlagForms is every documented global flag, in the shape a user types it.
// Each entry is inserted before, and after, every command in the position table
// below, because "accepted in one position only" is the defect this covers.
func globalFlagForms(configPath string) []struct {
	name string
	args []string
} {
	return []struct {
		name string
		args []string
	}{
		{"json", []string{"--json"}},
		{"config", []string{"--config", configPath}},
		{"config shorthand", []string{"-c", configPath}},
		{"config equals", []string{"--config=" + configPath}},
		{"log level", []string{"--log-level", "debug"}},
		{"log level equals", []string{"--log-level=debug"}},
		{"log format", []string{"--log-format", "json"}},
		{"log format equals", []string{"--log-format=json"}},
		{"verbose long", []string{"--verbose"}},
		{"verbose short", []string{"-v"}},
		{"quiet long", []string{"--quiet"}},
		{"quiet short", []string{"-q"}},
	}
}

// TestEveryCommandAcceptsTheGlobalFlagsInEitherPosition walks the real command
// table with every documented global flag, before the command and after it, and
// requires that neither position is rejected as a usage error.
func TestEveryCommandAcceptsTheGlobalFlagsInEitherPosition(t *testing.T) {
	h := newHarness(t)
	// A session in RESET_TIME_REACHED is the state a command is written for, so
	// the commands that need an id get past their argument checks and the exit
	// code reflects the command, not the missing argument.
	const id = "11111111-1111-4111-8111-111111111111"
	h.seedSession(t, id, func(s *storage.Session) { s.State = storage.StateResetTimeReached })

	commands := []struct {
		name string
		args []string
	}{
		{"daemon", []string{"daemon", "--once"}},
		{"status", []string{"status"}},
		{"sessions", []string{"sessions"}},
		{"inspect", []string{"inspect", id}},
		{"recover", []string{"recover", id}},
		{"retry", []string{"retry", id}},
		{"enable", []string{"enable", id}},
		{"reset", []string{"reset", id}},
		{"disable", []string{"disable", id}},
		{"history", []string{"history", id}},
		{"config validate", []string{"config", "validate"}},
		{"config show", []string{"config", "show"}},
		{"doctor", []string{"doctor"}},
		{"serve", []string{"serve"}},
		{"version", []string{"version"}},
	}
	assertCommandsCovered(t, commands)

	configPath := h.cfg.Source
	for _, command := range commands {
		for _, flagForm := range globalFlagForms(configPath) {
			t.Run(command.name+"/"+flagForm.name, func(t *testing.T) {
				for _, position := range []string{"before", "after"} {
					args := make([]string, 0, len(command.args)+len(flagForm.args))
					if position == "before" {
						args = append(args, flagForm.args...)
						args = append(args, command.args...)
					} else {
						args = append(args, command.args...)
						args = append(args, flagForm.args...)
					}

					code, _, stderr := h.exec(t, args...)
					if code == ExitUsage {
						t.Fatalf("ccresu %s (%s) = exit %d, want anything but a usage error: %s",
							strings.Join(args, " "), position, code, strings.TrimSpace(stderr))
					}
					if strings.Contains(stderr, "flag provided but not defined") {
						t.Fatalf("ccresu %s (%s) rejected the flag: %s",
							strings.Join(args, " "), position, strings.TrimSpace(stderr))
					}
				}
			})
		}
	}
}

// TestGlobalFlagsAfterTheCommandTakeEffect proves the accepted flag is not merely
// tolerated: `status --json` has to print the same JSON `--json status` does.
func TestGlobalFlagsAfterTheCommandTakeEffect(t *testing.T) {
	h := newHarness(t)

	beforeCode, beforeOut, beforeErr := h.exec(t, "--json", "status")
	afterCode, afterOut, afterErr := h.exec(t, "status", "--json")
	if beforeCode != ExitOK || afterCode != ExitOK {
		t.Fatalf("exit codes = %d/%d, want 0/0 (%s%s)", beforeCode, afterCode, beforeErr, afterErr)
	}

	var before, after map[string]any
	if err := json.Unmarshal([]byte(beforeOut), &before); err != nil {
		t.Fatalf("--json status is not JSON: %v (%q)", err, beforeOut)
	}
	if err := json.Unmarshal([]byte(afterOut), &after); err != nil {
		t.Fatalf("status --json is not JSON: %v (%q)", err, afterOut)
	}
	// "now" is the only field that may differ between two invocations.
	delete(before, "now")
	delete(after, "now")
	beforeJSON, err := json.Marshal(before)
	if err != nil {
		t.Fatalf("marshal --json status: %v", err)
	}
	afterJSON, err := json.Marshal(after)
	if err != nil {
		t.Fatalf("marshal status --json: %v", err)
	}
	if string(beforeJSON) != string(afterJSON) {
		t.Errorf("status --json = %s, want %s (the same payload as --json status)",
			afterJSON, beforeJSON)
	}

	// The same command without the flag is the human page, which is the contrast
	// that shows the flag did something rather than being ignored.
	code, plainOut, _ := h.exec(t, "status")
	if code != ExitOK {
		t.Fatalf("status = exit %d, want 0", code)
	}
	if json.Valid([]byte(plainOut)) || strings.HasPrefix(strings.TrimSpace(plainOut), "{") {
		t.Errorf("status without --json printed JSON: %q", plainOut)
	}
}

// TestParseFlagsInterleavesFlagsAndPositionals states the parsing rule directly:
// flags and positionals may be in any order, a flag's value is never mistaken for
// a global flag, and the collected positionals reach the command intact.
func TestParseFlagsInterleavesFlagsAndPositionals(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantArgs   []string
		wantJSON   bool
		wantLevel  string
		wantConfig string
		wantMsg    string
		wantCode   int
	}{
		{
			name:     "flags after the positional",
			args:     []string{"id-1", "--json", "-v"},
			wantArgs: []string{"id-1"},
			wantJSON: true, wantLevel: "debug",
		},
		{
			name:     "flags before and after the positional",
			args:     []string{"--log-level", "warn", "id-1", "--json", "--message", "hi"},
			wantArgs: []string{"id-1"},
			wantJSON: true, wantLevel: "warn", wantMsg: "hi",
		},
		{
			name:     "flag between two positionals",
			args:     []string{"id-1", "--json", "id-2"},
			wantArgs: []string{"id-1", "id-2"},
			wantJSON: true,
		},
		{
			name:     "a command flag value is never read as a global flag",
			args:     []string{"id-1", "--message", "--json"},
			wantArgs: []string{"id-1"},
			wantJSON: false, wantMsg: "--json",
		},
		{
			name:       "the config shorthand works after the positional",
			args:       []string{"id-1", "-c", "/tmp/other.yaml"},
			wantArgs:   []string{"id-1"},
			wantConfig: "/tmp/other.yaml",
		},
		{
			name:      "the last shorthand wins",
			args:      []string{"id-1", "-v", "-q"},
			wantArgs:  []string{"id-1"},
			wantLevel: "error",
		},
		{
			name:      "the last shorthand wins in reverse",
			args:      []string{"id-1", "-q", "-v"},
			wantArgs:  []string{"id-1"},
			wantLevel: "debug",
		},
		{
			name:     "a terminator keeps a dash-led positional",
			args:     []string{"id-1", "--", "-not-a-flag"},
			wantArgs: []string{"id-1", "-not-a-flag"},
		},
		{
			name:     "an unknown flag is still a usage error",
			args:     []string{"id-1", "--nope"},
			wantCode: ExitUsage,
		},
		{
			name:     "a missing value is still a usage error",
			args:     []string{"id-1", "--log-level"},
			wantCode: ExitUsage,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			app := newApp(h.deps(), BuildInfo{Version: "test"}, &h.stdout, &h.stderr)
			fs := app.flags("recover", "recover <session-id> [--message TEXT] [--force]")
			message := fs.String("message", "", "continuation message")

			err := app.parseFlags(fs, test.args)
			if test.wantCode != ExitOK {
				if err == nil {
					t.Fatalf("parseFlags(%q) = nil, want a usage error", test.args)
				}
				if code := exitCode(err); code != test.wantCode {
					t.Errorf("parseFlags(%q) = exit %d (%v), want %d", test.args, code, err, test.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseFlags(%q) = %v, want nil", test.args, err)
			}
			if got := strings.Join(fs.Args(), ","); got != strings.Join(test.wantArgs, ",") {
				t.Errorf("positionals = %q, want %q", got, strings.Join(test.wantArgs, ","))
			}
			if app.json != test.wantJSON {
				t.Errorf("json = %v, want %v", app.json, test.wantJSON)
			}
			if test.wantLevel != "" && app.logLevel != test.wantLevel {
				t.Errorf("log level = %q, want %q", app.logLevel, test.wantLevel)
			}
			if test.wantConfig != "" && app.configPath != test.wantConfig {
				t.Errorf("config path = %q, want %q", app.configPath, test.wantConfig)
			}
			if *message != test.wantMsg {
				t.Errorf("message = %q, want %q", *message, test.wantMsg)
			}
		})
	}
}

// TestCommandHelpListsTheGlobalFlags documents the trade-off of registering the
// global flags on every command: they are visible in that command's own help, so
// a user reading `ccresu status --help` still discovers them.
func TestCommandHelpListsTheGlobalFlags(t *testing.T) {
	h := newHarness(t)
	code, _, stderr := h.exec(t, "status", "--help")
	if code != ExitOK {
		t.Fatalf("status --help = exit %d, want 0", code)
	}
	// PrintDefaults renders both spellings with a single dash, which is what the
	// standard library does; the double-dash form the user types still parses.
	for _, want := range []string{"-json", "-log-level", "-log-format", "-config", "-quiet", "-verbose"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("status --help does not mention %s:\n%s", want, stderr)
		}
	}
}

// assertCommandsCovered fails when the table above stops covering the real command
// surface, so a new command cannot quietly skip the position check.
func assertCommandsCovered(t *testing.T, covered []struct {
	name string
	args []string
}) {
	t.Helper()
	names := make(map[string]bool, len(covered))
	for _, command := range covered {
		names[strings.Fields(command.name)[0]] = true
	}
	for _, spec := range newApp(Deps{}, BuildInfo{}, nil, nil).commands() {
		if !names[spec.name] {
			t.Errorf("command %q is not covered by the global-flag position table", spec.name)
		}
	}
}
