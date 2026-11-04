package integration_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jymchng/ccresu/internal/storage"
)

// TestCLIReportsVersionBuildMetadataAndHelp covers the two invocations that must
// work before a configuration, a database or Claude Code exists at all.
func TestCLIReportsVersionBuildMetadataAndHelp(t *testing.T) {
	f := newFixture(t)

	for _, args := range [][]string{{"version"}, {"-V"}, {"--version"}} {
		got := f.mustRun(0, args...)
		for _, want := range []string{"ccresu dev", "commit", "go1.", "linux/", "built"} {
			if !strings.Contains(got.stdout, want) {
				t.Fatalf("ccresu %s stdout %q does not contain %q",
					strings.Join(args, " "), got.stdout, want)
			}
		}
		if got.stderr != "" {
			t.Fatalf("ccresu %s wrote to stderr: %q", strings.Join(args, " "), got.stderr)
		}
	}

	// --version must not need the configuration file, which is exactly what makes
	// it useful from a broken installation.
	missing := filepath.Join(f.Dir, "does-not-exist.yaml")
	got := f.mustRun(0, "--config", missing, "--version")
	if !strings.Contains(got.stdout, "ccresu dev") {
		t.Fatalf("--version with a missing config printed %q", got.stdout)
	}

	for _, args := range [][]string{{"--help"}, {"help"}} {
		got := f.mustRun(0, args...)
		for _, want := range []string{"Commands:", "Global flags:", "Exit codes:", "serve"} {
			if !strings.Contains(got.stdout, want) {
				t.Fatalf("ccresu %s stdout %q does not contain %q",
					strings.Join(args, " "), got.stdout, want)
			}
		}
	}
}

// TestCLIGlobalFlagsWorkBeforeAndAfterTheCommand is the shipped behaviour of goal
// #19 exercised in a real process: both positions must be accepted and produce
// the same document.
func TestCLIGlobalFlagsWorkBeforeAndAfterTheCommand(t *testing.T) {
	f := newFixture(t)

	before := f.mustRun(0, "--json", "--config", f.ConfigPath, "version")
	after := f.mustRun(0, "version", "--json", "--config", f.ConfigPath)
	if before.stdout != after.stdout {
		t.Fatalf("the global flags changed the output depending on position:\nbefore: %s\nafter:  %s",
			before.stdout, after.stdout)
	}
	var version map[string]string
	if err := json.Unmarshal([]byte(after.stdout), &version); err != nil {
		t.Fatalf("version --json is not JSON: %v (%q)", err, after.stdout)
	}
	if version["version"] != "dev" {
		t.Fatalf("version = %q, want dev", version["version"])
	}

	// --config after the subcommand has to be honoured too, not merely accepted.
	beforeStatus := f.mustRun(0, "--config", f.ConfigPath, "status", "--json")
	afterStatus := f.mustRun(0, "status", "--config", f.ConfigPath, "--json")
	for _, got := range []result{beforeStatus, afterStatus} {
		var report struct {
			ConfigPath   string `json:"config_path"`
			DatabasePath string `json:"database_path"`
			Sessions     int    `json:"sessions"`
		}
		if err := json.Unmarshal([]byte(got.stdout), &report); err != nil {
			t.Fatalf("status --json is not JSON: %v (%q)", err, got.stdout)
		}
		if report.ConfigPath != f.ConfigPath {
			t.Fatalf("config_path = %q, want %q", report.ConfigPath, f.ConfigPath)
		}
		if report.DatabasePath != f.DBPath {
			t.Fatalf("database_path = %q, want %q", report.DatabasePath, f.DBPath)
		}
		if report.Sessions != 0 {
			t.Fatalf("sessions = %d on a fresh installation, want 0", report.Sessions)
		}
	}

	// -v and -q are shorthands with a defined precedence, in either position.
	if got := f.mustRun(0, "-q", "version", "-v"); got.stdout == "" {
		t.Fatal("-q/-v before and after the command produced no output")
	}
}

// TestCLILoadsAndValidatesTheConfigurationFromDisk proves the real loader, the
// real validation rules and the real error rendering are wired to the commands
// named validate and show.
func TestCLILoadsAndValidatesTheConfigurationFromDisk(t *testing.T) {
	f := newFixture(t)

	// Positional path, and the --config flag before the subcommand.
	byPositional := f.mustRun(0, "config", "validate", f.ConfigPath)
	if !strings.Contains(byPositional.stdout, "is valid") {
		t.Fatalf("config validate printed %q", byPositional.stdout)
	}
	if !strings.Contains(byPositional.stdout, f.DBPath) {
		t.Fatalf("config validate did not report the database in use: %q", byPositional.stdout)
	}
	f.mustRun(0, "config", "validate", "--json", f.ConfigPath)

	shown := f.mustRun(0, "config", "show", "--config", f.ConfigPath)
	var effective map[string]any
	if err := json.Unmarshal([]byte(shown.stdout), &effective); err != nil {
		t.Fatalf("config show is not JSON: %v (%q)", err, shown.stdout)
	}
	// Every section is present, and the credential field is masked rather than
	// omitted: an inspection command must show that a token exists without
	// revealing it.
	telegram, ok := effective["Telegram"].(map[string]any)
	if !ok {
		t.Fatalf("config show has no telegram section: %s", shown.stdout)
	}
	if _, present := telegram["BotToken"]; !present {
		t.Fatalf("config show omitted the telegram bot token field: %s", shown.stdout)
	}

	// A broken file is a validation failure the operator can read: exit 1 with
	// every problem named, not a stack trace.
	broken := filepath.Join(f.Dir, "broken.yaml")
	if err := os.WriteFile(broken, []byte("server:\n  port: 99999\nstorage:\n  driver: mysql\n"), 0o600); err != nil {
		t.Fatalf("write broken config: %v", err)
	}
	got := f.mustRun(1, "config", "validate", broken)
	for _, want := range []string{"server.port", "storage.driver"} {
		if !strings.Contains(got.stderr, want) {
			t.Fatalf("config validate stderr %q does not name %q", got.stderr, want)
		}
	}

	// A missing file is reported as a load failure, not as a valid empty config.
	missing := f.mustRun(1, "config", "validate", filepath.Join(f.Dir, "nope.yaml"))
	if !strings.Contains(missing.stderr, "configuration") {
		t.Fatalf("config validate on a missing file printed %q", missing.stderr)
	}
}

// TestCLIReadsAndWritesThePersistedState exercises the commands that open the
// database for real, including the cross-process contract with the fixture: a
// session written by the test process is visible to the binary.
func TestCLIReadsAndWritesThePersistedState(t *testing.T) {
	f := newFixture(t)
	f.seedSession(t, seededSessionID, nil)

	status := f.mustRun(0, "status", "--json", "--config", f.ConfigPath)
	var report struct {
		Sessions        int            `json:"sessions"`
		EnabledSessions int            `json:"enabled_sessions"`
		States          map[string]int `json:"states"`
		MaxAttempts     int            `json:"max_attempts"`
		RecoveryEnabled bool           `json:"recovery_enabled"`
	}
	if err := json.Unmarshal([]byte(status.stdout), &report); err != nil {
		t.Fatalf("status --json: %v (%q)", err, status.stdout)
	}
	if report.Sessions != 1 || report.EnabledSessions != 1 {
		t.Fatalf("status reports %d sessions (%d enabled), want 1 (1)",
			report.Sessions, report.EnabledSessions)
	}
	if report.States[string(storage.StateRunning)] != 1 {
		t.Fatalf("state histogram = %v, want one RUNNING session", report.States)
	}
	if !report.RecoveryEnabled || report.MaxAttempts != 10 {
		t.Fatalf("recovery configuration = %t / %d attempts, want true / 10",
			report.RecoveryEnabled, report.MaxAttempts)
	}

	sessions := f.mustRun(0, "sessions", "--json", "--config", f.ConfigPath)
	var listed []map[string]any
	if err := json.Unmarshal([]byte(sessions.stdout), &listed); err != nil {
		t.Fatalf("sessions --json: %v (%q)", err, sessions.stdout)
	}
	if len(listed) != 1 || listed[0]["ID"] != seededSessionID {
		t.Fatalf("sessions listed %v, want the one seeded session %s", listed, seededSessionID)
	}

	inspect := f.mustRun(0, "inspect", seededSessionID, "--config", f.ConfigPath)
	for _, want := range []string{seededSessionID, string(storage.StateRunning), "/home/dev/src/widget"} {
		if !strings.Contains(inspect.stdout, want) {
			t.Fatalf("inspect stdout %q does not contain %q", inspect.stdout, want)
		}
	}

	history := f.mustRun(0, "history", seededSessionID, "--json", "--config", f.ConfigPath)
	if !json.Valid([]byte(history.stdout)) {
		t.Fatalf("history --json is not JSON: %q", history.stdout)
	}

	// disable then enable through the CLI changes the persisted row, which is the
	// read-back proof rather than a look at the printed text.
	f.mustRun(0, "disable", seededSessionID, "--config", f.ConfigPath)
	if session := f.readSession(t, seededSessionID); session.Enabled {
		t.Fatal("disable left the session enabled in the database")
	}
	f.mustRun(0, "enable", seededSessionID, "--config", f.ConfigPath)
	if session := f.readSession(t, seededSessionID); !session.Enabled {
		t.Fatal("enable left the session disabled in the database")
	}

	// The database the binary created is the configured one, with restrictive
	// permissions on the file.
	info, err := os.Stat(f.DBPath)
	if err != nil {
		t.Fatalf("stat %s: %v", f.DBPath, err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("database mode = %04o, want 0600", mode)
	}
}

// TestCLIAppliesEnvironmentOverridesLast proves the documented precedence holds
// in a real process: CCRESU_* wins over the file, so a test or a container can
// relocate state without editing the configuration.
func TestCLIAppliesEnvironmentOverridesLast(t *testing.T) {
	f := newFixture(t)
	override := filepath.Join(f.Dir, "override", "state.db")

	got := f.runWithEnv([]string{"CCRESU_STORAGE__PATH=" + override},
		"status", "--json", "--config", f.ConfigPath)
	if got.code != 0 {
		t.Fatalf("status exited %d: %s", got.code, got.stderr)
	}
	var report struct {
		DatabasePath string `json:"database_path"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &report); err != nil {
		t.Fatalf("status --json: %v (%q)", err, got.stdout)
	}
	if report.DatabasePath != override {
		t.Fatalf("database_path = %q, want the environment override %q",
			report.DatabasePath, override)
	}
	if _, err := os.Stat(override); err != nil {
		t.Fatalf("the environment override did not create its database: %v", err)
	}
}

// TestCLIReportsUsageAndNotFoundErrors pins the exit-code contract scripts depend
// on: 2 for a wrong invocation, 3 for a missing row, 4 for a session whose state
// forbids the operation.
func TestCLIReportsUsageAndNotFoundErrors(t *testing.T) {
	f := newFixture(t)

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStderr string
	}{
		{
			name:       "unknown command",
			args:       []string{"frobnicate", "--config", f.ConfigPath},
			wantCode:   2,
			wantStderr: "unknown command",
		},
		{
			name:       "unknown flag on a real command",
			args:       []string{"status", "--definitely-not-a-flag", "--config", f.ConfigPath},
			wantCode:   2,
			wantStderr: "not defined",
		},
		{
			name:       "missing flag value before the command",
			args:       []string{"--config"},
			wantCode:   2,
			wantStderr: "needs a path",
		},
		{
			name:       "missing flag value after the command",
			args:       []string{"status", "--config"},
			wantCode:   2,
			wantStderr: "needs an argument",
		},
		{
			name:       "no command",
			args:       nil,
			wantCode:   2,
			wantStderr: "no command given",
		},
		{
			name:       "missing session argument",
			args:       []string{"inspect", "--config", f.ConfigPath},
			wantCode:   2,
			wantStderr: "needs a session id",
		},
		{
			name:       "unknown config subcommand",
			args:       []string{"config", "frobnicate"},
			wantCode:   2,
			wantStderr: "unknown config subcommand",
		},
		{
			name:       "unknown session",
			args:       []string{"inspect", "0d6b1e34-9a51-4f7c-8b2e-5c8d7a1f6e09", "--config", f.ConfigPath},
			wantCode:   3,
			wantStderr: "not found",
		},
		{
			name:       "missing configuration file",
			args:       []string{"status", "--config", filepath.Join(f.Dir, "absent.yaml")},
			wantCode:   1,
			wantStderr: "load configuration",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := f.mustRun(tc.wantCode, tc.args...)
			if !strings.Contains(got.stderr, tc.wantStderr) {
				t.Fatalf("stderr %q does not contain %q", got.stderr, tc.wantStderr)
			}
			if tc.wantCode == 2 && !strings.Contains(got.stderr, "Run 'ccresu --help' for usage.") {
				t.Fatalf("a usage error did not point at the help page: %q", got.stderr)
			}
		})
	}

	// A usage error must not have created anything: nothing was attempted. The
	// not-found cases legitimately open the database, so they are excluded.
	usageOnly := newFixture(t)
	usageOnly.mustRun(2, "frobnicate", "--config", usageOnly.ConfigPath)
	usageOnly.mustRun(2, "status", "--definitely-not-a-flag")
	if _, err := os.Stat(usageOnly.DBPath); !os.IsNotExist(err) {
		t.Fatalf("a usage error created the database (%v)", err)
	}
}

// TestCLIDoctorProbesTheEnvironmentAndTheClaudeCLI runs the real version probe:
// the command shells out to the configured executable and classifies every check
// from what came back.
func TestCLIDoctorProbesTheEnvironmentAndTheClaudeCLI(t *testing.T) {
	f := newFixture(t)

	got := f.mustRun(0, "doctor", "--json", "--config", f.ConfigPath)
	var report struct {
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Detail string `json:"detail"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &report); err != nil {
		t.Fatalf("doctor --json: %v (%q)", err, got.stdout)
	}
	if len(report.Checks) < 5 {
		t.Fatalf("doctor reported %d checks, want at least 5: %s", len(report.Checks), got.stdout)
	}
	seen := make(map[string]string, len(report.Checks))
	for _, check := range report.Checks {
		if check.Status == "fail" {
			t.Fatalf("doctor check %q failed: %s", check.Name, check.Detail)
		}
		seen[check.Name] = check.Detail
	}
	if detail := seen["config"]; detail != f.ConfigPath {
		t.Fatalf("config check = %q, want %q", detail, f.ConfigPath)
	}
	// The stub answers `claude --version`; a passing check therefore proves the
	// adapter parsed real process output rather than trusting a default.
	if detail := seen["claude"]; !strings.Contains(detail, "2.1.223") {
		t.Fatalf("claude check = %q, want the stub's reported version", detail)
	}
	if detail := seen["database"]; !strings.Contains(detail, f.DBPath) {
		t.Fatalf("database check = %q, want %q", detail, f.DBPath)
	}

	// And a stub that cannot answer is a failed check with a non-zero exit, so a
	// broken installation is never silently reported as healthy.
	brokenStub := newFixture(t, withClaudeScript("#!/bin/sh\necho 'unsupported' >&2\nexit 1\n"))
	failed := brokenStub.mustRun(1, "doctor", "--json", "--config", brokenStub.ConfigPath)
	if !strings.Contains(failed.stdout, `"status": "fail"`) {
		t.Fatalf("doctor with a broken claude reported no failure: %q", failed.stdout)
	}
	if !strings.Contains(failed.stderr, "check(s) failed") || !strings.Contains(failed.stderr, "claude") {
		t.Fatalf("doctor with a broken claude printed %q", failed.stderr)
	}
}

// TestCLIDaemonRunsASingleReconcilePass covers the daemon entry point end to end:
// configuration, storage, discovery through the real provider (which runs the
// stub CLI), detection, scheduling, recovery and delivery, then a clean exit.
func TestCLIDaemonRunsASingleReconcilePass(t *testing.T) {
	f := newFixture(t)

	got := f.mustRun(0, "daemon", "--once", "--config", f.ConfigPath)
	if !strings.Contains(got.stdout, "reconcile pass complete") {
		t.Fatalf("daemon --once printed %q", got.stdout)
	}
	if !strings.Contains(got.stdout, "(0 sessions)") {
		t.Fatalf("daemon --once reported the wrong session count: %q", got.stdout)
	}

	// The pass migrated and initialised the database, which is what makes the
	// next command able to read it.
	if _, err := os.Stat(f.DBPath); err != nil {
		t.Fatalf("daemon --once did not create the database: %v", err)
	}

	// Monitoring disabled is a refusal, not a silent no-op.
	disabled := newFixture(t, withMonitoringEnabled(false))
	refusal := disabled.mustRun(1, "daemon", "--once", "--config", disabled.ConfigPath)
	if !strings.Contains(refusal.stderr, "monitoring is disabled") {
		t.Fatalf("daemon with monitoring disabled printed %q", refusal.stderr)
	}

	// And an invalid pass argument is a usage error before anything is opened.
	f.mustRun(2, "daemon", "--interval", "-1s")
}

// TestCLIServeRefusesWhenTheServerIsDisabled covers the guard that keeps a
// disabled API from binding a port.
func TestCLIServeRefusesWhenTheServerIsDisabled(t *testing.T) {
	f := newFixture(t, withServerEnabled(false))

	got := f.mustRun(1, "serve", "--config", f.ConfigPath)
	if !strings.Contains(got.stderr, "server is disabled") {
		t.Fatalf("serve with the server disabled printed %q", got.stderr)
	}
}
