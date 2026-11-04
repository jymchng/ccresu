package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/jymchng/ccresu/configs"
	"github.com/jymchng/ccresu/internal/config"
)

// cmdConfig implements `ccresu config init`, `validate` and `show`.
func (a *App) cmdConfig(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usagef("config needs a subcommand: init, validate or show")
	}
	switch args[0] {
	case "init":
		return a.cmdConfigInit(args[1:])
	case "validate":
		return a.cmdConfigValidate(args[1:])
	case "show":
		return a.cmdConfigShow(ctx, args[1:])
	default:
		return usagef("unknown config subcommand %q (want init, validate or show)", args[0])
	}
}

// cmdConfigInit writes the shipped example configuration to the resolved path.
//
// It exists because the alternative is a `mkdir`, a `cp` out of a release tarball
// and a `chmod`: three commands that are easy to get wrong, and whose failure
// mode is a silently world-readable file holding a bot token. Here the mode is
// fixed at 0600, the parent directory is created, and the contents are the same
// bytes the installer would have copied.
func (a *App) cmdConfigInit(args []string) error {
	set := a.flags("config init", "config init [PATH] [--force]")
	force := set.Bool("force", false, "overwrite an existing configuration file")
	if err := a.parseFlags(set, args); err != nil {
		return err
	}
	explicit := a.configPath
	switch rest := set.Args(); len(rest) {
	case 0:
	case 1:
		explicit = rest[0]
	default:
		return usagef("config init takes at most one path, got %d arguments", len(rest))
	}

	path, err := a.resolveConfigPath(explicit)
	if err != nil {
		return err
	}

	replaced := false
	switch _, statErr := os.Stat(path); {
	case statErr == nil:
		if !*force {
			// Refusing is the point: silently replacing a configuration would
			// throw away the Telegram token and every tuned setting in it. The
			// message names all three ways forward so the operator never has to
			// guess at a destructive flag.
			return withCode(ExitFailure, fmt.Errorf(
				"configuration %s already exists; it was left untouched\n"+
					"to keep it:          edit it, then run 'ccresu config validate'\n"+
					"to replace it:       ccresu config init %s --force\n"+
					"to use another file: ccresu --config PATH config init", path, path))
		}
		replaced = true
	case errors.Is(statErr, fs.ErrNotExist):
		// The normal case: nothing there yet.
	default:
		return withCode(ExitFailure, fmt.Errorf("configuration %s cannot be inspected: %w", path, statErr))
	}

	// Prove the example still loads before writing it, so a bad embedded file is
	// a failed command instead of an installed broken configuration.
	if err := a.checkExample(); err != nil {
		return err
	}

	if dir := filepath.Dir(path); dir != "" {
		// 0700: the directory holds the API token too, so it is not a place to
		// leave world-traversable.
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return withCode(ExitFailure, fmt.Errorf("create %s: %w", dir, err))
		}
	}
	if err := os.WriteFile(path, configs.Example, 0o600); err != nil {
		return withCode(ExitFailure, fmt.Errorf("write configuration %s: %w", path, err))
	}
	// WriteFile only applies the mode when it creates the file, and even then the
	// process umask can narrow or widen what lands on disk. Chmod afterwards so
	// the mode is the one that was promised, including on a --force overwrite of
	// a file that had been left 0644.
	if err := os.Chmod(path, 0o600); err != nil {
		return withCode(ExitFailure, fmt.Errorf("set mode 0600 on %s: %w", path, err))
	}

	if a.json {
		return a.writeJSON(a.stdout, map[string]any{
			"path":       path,
			"created":    !replaced,
			"replaced":   replaced,
			"mode":       "0600",
			"valid":      true,
			"next_steps": []string{"edit " + path, "ccresu config validate", "ccresu doctor"},
		})
	}
	verb := "created"
	if replaced {
		verb = "replaced with the shipped example"
	}
	fmt.Fprintf(a.stdout, "configuration %s %s (mode 0600)\n", path, verb)
	fmt.Fprintln(a.stdout)
	fmt.Fprintln(a.stdout, "Next steps:")
	fmt.Fprintf(a.stdout, "  1. edit %s\n", path)
	fmt.Fprintln(a.stdout, "  2. ccresu config validate")
	fmt.Fprintln(a.stdout, "  3. ccresu doctor")
	return nil
}

// checkExample validates the embedded example through the same loader
// `config validate` uses, against a throwaway path.
//
// The environment is deliberately not applied: the question is whether the file
// the binary carries is valid on its own, and an operator's CCRESU_* overrides
// must not be able to make the check fail for a reason that has nothing to do
// with the file being written.
func (a *App) checkExample() error {
	dir, err := os.MkdirTemp("", "ccresu-config-init-")
	if err != nil {
		return withCode(ExitFailure, fmt.Errorf("check the shipped example: %w", err))
	}
	defer func() { _ = os.RemoveAll(dir) }()

	probe := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(probe, configs.Example, 0o600); err != nil {
		return withCode(ExitFailure, fmt.Errorf("check the shipped example: %w", err))
	}
	if _, err := a.deps.LoadConfig(probe, nil); err != nil {
		return withCode(ExitFailure, fmt.Errorf("the shipped example configuration does not validate: %w", err))
	}
	return nil
}

// cmdConfigValidate loads the configuration and reports every problem it finds.
//
// It is deliberately usable before anything else works: it needs no database, no
// Claude Code install and no network, which is what makes it the first thing an
// operator runs after editing the file.
func (a *App) cmdConfigValidate(args []string) error {
	fs := a.flags("config validate", "config validate [PATH]")
	if err := a.parseFlags(fs, args); err != nil {
		return err
	}
	explicit := a.configPath
	switch rest := fs.Args(); len(rest) {
	case 0:
	case 1:
		explicit = rest[0]
	default:
		return usagef("config validate takes at most one path, got %d arguments", len(rest))
	}

	path, err := a.resolveConfigPath(explicit)
	if err != nil {
		return err
	}
	cfg, err := a.deps.LoadConfig(path, a.deps.LookupEnv)
	if err != nil {
		// A validation failure is reported as the loader rendered it: every rule
		// that failed, not just the first.
		return withCode(ExitFailure, fmt.Errorf("configuration %s is invalid:\n%w", path, err))
	}
	if cfg == nil {
		return withCode(ExitFailure, fmt.Errorf("configuration %s is empty", path))
	}

	if a.json {
		return a.writeJSON(a.stdout, map[string]any{
			"path":     path,
			"valid":    true,
			"app":      cfg.App.Name,
			"timezone": cfg.App.Timezone,
			"storage":  cfg.Storage.Path,
		})
	}
	fmt.Fprintf(a.stdout, "configuration %s is valid\n", path)
	a.field(a.stdout, "app", cfg.App.Name)
	a.field(a.stdout, "timezone", cfg.App.Timezone)
	a.field(a.stdout, "database", cfg.Storage.Path)
	a.field(a.stdout, "monitoring", yesNo(cfg.Monitoring.Enabled))
	a.field(a.stdout, "recovery", fmt.Sprintf("%s (max attempts %d)", yesNo(cfg.Recovery.Enabled), cfg.Recovery.MaxAttempts))
	a.field(a.stdout, "telegram", yesNo(cfg.Telegram.Enabled))
	a.field(a.stdout, "server", fmt.Sprintf("%s (%s)", yesNo(cfg.Server.Enabled), cfg.Server.Addr()))
	return nil
}

// cmdConfigShow prints the effective configuration with the bot token masked.
func (a *App) cmdConfigShow(_ context.Context, args []string) error {
	fs := a.flags("config show", "config show")
	if err := a.parseFlags(fs, args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return usagef("config show takes no arguments, got %q", rest[0])
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	// A secret must never be printed, not even by an inspection command.
	masked := *cfg
	masked.Telegram.BotToken = config.MaskSecret(cfg.Telegram.BotToken)

	encoder := json.NewEncoder(a.stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(masked); err != nil {
		return fmt.Errorf("encode configuration: %w", err)
	}
	return nil
}

// check is one doctor result.
type check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// Doctor statuses.
const (
	checkOK   = "ok"
	checkWarn = "warn"
	checkFail = "fail"
)

// cmdDoctor checks the environment ccresu runs in and reports every result,
// including the ones that failed, before exiting non-zero.
//
// It is the command an operator runs when "nothing happens": it distinguishes a
// missing configuration from an unreadable database, a missing Claude Code CLI and
// an unreachable Telegram bot.
func (a *App) cmdDoctor(ctx context.Context, args []string) error {
	fs := a.flags("doctor", "doctor")
	if err := a.parseFlags(fs, args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return usagef("doctor takes no arguments, got %q", rest[0])
	}

	var checks []check
	add := func(name, status, detail string) {
		checks = append(checks, check{Name: name, Status: status, Detail: detail})
	}

	path, err := a.resolveConfigPath(a.configPath)
	if err != nil {
		add("config", checkFail, err.Error())
	} else if _, statErr := os.Stat(path); statErr != nil {
		add("config", checkFail, fmt.Sprintf("%s is not readable: %v", path, statErr))
	} else if cfg, loadErr := a.deps.LoadConfig(path, a.deps.LookupEnv); loadErr != nil {
		add("config", checkFail, loadErr.Error())
	} else {
		add("config", checkOK, path)
		if loc, locErr := cfg.DisplayLocation(); locErr != nil {
			add("timezone", checkFail, locErr.Error())
		} else {
			add("timezone", checkOK, loc.String())
		}
		if dir, dirErr := config.ExpandPath(cfg.Storage.Path); dirErr != nil {
			add("data directory", checkFail, dirErr.Error())
		} else {
			status, detail := writableCheck(filepath.Dir(dir))
			add("data directory", status, detail)
		}
	}

	rt, release, runtimeErr := a.openRuntime(ctx)
	if runtimeErr != nil {
		add("database", checkFail, runtimeErr.Error())
		add("claude", checkWarn, "not checked: the database could not be opened")
		add("telegram", checkWarn, "not checked: the database could not be opened")
		add("sessions", checkWarn, "not checked: the database could not be opened")
	} else {
		version, versionErr := rt.Store.Version(ctx)
		if versionErr != nil {
			add("database", checkFail, versionErr.Error())
		} else {
			add("database", checkOK, fmt.Sprintf("%s (schema version %d)", rt.Store.Path(), version))
		}
		checks = append(checks, a.claudeChecks(ctx, rt)...)
		checks = append(checks, a.telegramChecks(rt)...)
		count := countSessions(ctx, rt)
		status := checkOK
		if count == 0 {
			status = checkWarn
			add("sessions", status, "no sessions discovered yet")
		} else {
			add("sessions", checkOK, fmt.Sprintf("%d discovered", count))
		}
		release()
	}

	return a.reportChecks(checks)
}

// claudeChecks probes the Claude Code CLI and its on-disk store.
func (a *App) claudeChecks(ctx context.Context, rt *Runtime) []check {
	var checks []check
	reporter, ok := rt.Provider.(VersionReporter)
	if !ok || reporter == nil {
		return append(checks, check{Name: "claude", Status: checkWarn, Detail: "no version probe available"})
	}
	report, err := reporter.Version(ctx)
	if err != nil {
		return append(checks, check{Name: "claude", Status: checkFail, Detail: err.Error()})
	}
	checks = append(checks, check{
		Name:   "claude",
		Status: checkOK,
		Detail: fmt.Sprintf("%s (%s)", report.Version, strings.Join(report.Command, " ")),
	})
	if rt.Transcripts == nil {
		return append(checks, check{Name: "transcripts", Status: checkWarn, Detail: "transcript store unavailable"})
	}
	return append(checks, check{Name: "transcripts", Status: checkOK, Detail: "session transcripts are readable"})
}

// telegramChecks reports whether notifications are configured, with the token
// masked.
func (a *App) telegramChecks(rt *Runtime) []check {
	cfg := rt.Config
	if cfg == nil || !cfg.Telegram.Enabled {
		return []check{{Name: "telegram", Status: checkOK, Detail: "disabled"}}
	}
	detail := fmt.Sprintf("chat %s, token %s", cfg.Telegram.ChatID, config.MaskSecret(cfg.Telegram.BotToken))
	status := checkOK
	if rt.Notifier == nil {
		status = checkFail
		detail += " (notifier not built)"
	}
	return []check{{Name: "telegram", Status: status, Detail: detail}}
}

// writableCheck verifies that a directory exists (or can be created) and accepts
// a write.
func writableCheck(dir string) (string, string) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return checkFail, fmt.Sprintf("%s is not usable: %v", dir, err)
	}
	probe, err := os.CreateTemp(dir, ".ccresu-doctor-*")
	if err != nil {
		return checkFail, fmt.Sprintf("%s is not writable: %v", dir, err)
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return checkOK, dir
}

// reportChecks prints the doctor table and turns any failure into a non-zero exit.
func (a *App) reportChecks(checks []check) error {
	if a.json {
		if err := a.writeJSON(a.stdout, map[string]any{"checks": checks}); err != nil {
			return err
		}
	} else {
		rows := make([][]string, 0, len(checks))
		for _, item := range checks {
			rows = append(rows, []string{item.Status, item.Name, item.Detail})
		}
		a.table(a.stdout, []string{"STATUS", "CHECK", "DETAIL"}, rows)
	}

	var failed []string
	for _, item := range checks {
		if item.Status == checkFail {
			failed = append(failed, item.Name)
		}
	}
	if len(failed) > 0 {
		return withCode(ExitFailure, fmt.Errorf("%d check(s) failed: %s", len(failed), strings.Join(failed, ", ")))
	}
	return nil
}
