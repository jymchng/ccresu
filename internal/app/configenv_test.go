package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jymchng/ccresu/internal/config"
)

// envConfigDeps builds the smallest environment that loads a real configuration
// file through the real loader, with the environment lookup injectable so a test
// can set CCRESU_CONFIG without touching the process environment.
//
// defaultPath is what the platform default would resolve to; it is a separate
// file so "the environment won" is detectable.
func envConfigDeps(defaultPath string, env map[string]string) Deps {
	var stdout, stderr bytes.Buffer
	return Deps{
		Stdout: &stdout,
		Stderr: &stderr,
		LookupEnv: func(key string) (string, bool) {
			value, ok := env[key]
			return value, ok
		},
		LoadConfig: func(path string, lookup func(string) (string, bool)) (*config.Config, error) {
			return config.LoadWithEnv(path, lookup)
		},
		DefaultConfigPath: func() (string, error) { return defaultPath, nil },
	}
}

// writeConfig creates a configuration file whose app.name marks which file was
// read, and returns its path.
func writeConfig(t *testing.T, path, name string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(path), err)
	}
	body := "app:\n  name: " + name + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// showApp runs `ccresu config show` and returns the configuration it printed.
func showApp(t *testing.T, deps Deps, args ...string) config.Config {
	t.Helper()
	if code := RunWith(deps, append(args, "config", "show"), BuildInfo{}, nil, nil); code != ExitOK {
		t.Fatalf("config show %v = exit %d, stderr: %s", args, code, deps.Stderr.(*bytes.Buffer).String())
	}
	var shown config.Config
	if err := json.Unmarshal(deps.Stdout.(*bytes.Buffer).Bytes(), &shown); err != nil {
		t.Fatalf("decode config show output: %v", err)
	}
	return shown
}

// TestConfigPathFromEnvironment pins the documented resolution order:
// $CCRESU_CONFIG selects the configuration file when no --config is given, and
// everything derived from the file (its directory, hence the token path) follows
// it too.
func TestConfigPathFromEnvironment(t *testing.T) {
	home := t.TempDir()
	defaultPath := filepath.Join(home, ".config", "ccresu", "config.yaml")
	writeConfig(t, defaultPath, "default-app")

	envDir := t.TempDir()
	envPath := writeConfig(t, filepath.Join(envDir, "other.yaml"), "env-app")

	deps := envConfigDeps(defaultPath, map[string]string{config.EnvConfigPath: envPath})
	shown := showApp(t, deps)

	if shown.App.Name != "env-app" {
		t.Fatalf("app.name = %q, want env-app: CCRESU_CONFIG did not select the file", shown.App.Name)
	}
	if want := filepath.Join(envDir, "api-token"); shown.Server.TokenPath != want {
		t.Errorf("server.token_path = %q, want %q (it must follow the file in use)", shown.Server.TokenPath, want)
	}
}

// TestConfigFlagWinsOverEnvironment is the other half of the precedence: an
// explicit --config beats $CCRESU_CONFIG, and $CCRESU_CONFIG beats the default.
func TestConfigFlagWinsOverEnvironment(t *testing.T) {
	home := t.TempDir()
	defaultPath := writeConfig(t, filepath.Join(home, ".config", "ccresu", "config.yaml"), "default-app")
	envPath := writeConfig(t, filepath.Join(t.TempDir(), "env.yaml"), "env-app")
	flagPath := writeConfig(t, filepath.Join(t.TempDir(), "flag.yaml"), "flag-app")

	deps := envConfigDeps(defaultPath, map[string]string{config.EnvConfigPath: envPath})
	shown := showApp(t, deps, "--config", flagPath)

	if shown.App.Name != "flag-app" {
		t.Fatalf("app.name = %q, want flag-app: --config must win over CCRESU_CONFIG", shown.App.Name)
	}
	if want := filepath.Join(filepath.Dir(flagPath), "api-token"); shown.Server.TokenPath != want {
		t.Errorf("server.token_path = %q, want %q", shown.Server.TokenPath, want)
	}
}

// TestConfigPathEnvironmentFallsBackToDefault covers the two ways the variable
// can be absent: not set at all, and set to blank. Both must leave the default
// resolution exactly as it was.
func TestConfigPathEnvironmentFallsBackToDefault(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"unset": nil,
		"blank": {config.EnvConfigPath: "   "},
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			defaultPath := writeConfig(t, filepath.Join(home, ".config", "ccresu", "config.yaml"), "default-app")

			deps := envConfigDeps(defaultPath, env)
			shown := showApp(t, deps)

			if shown.App.Name != "default-app" {
				t.Fatalf("app.name = %q, want default-app", shown.App.Name)
			}
		})
	}
}

// TestConfigPathEnvironmentIsExpanded checks the value goes through the same
// expansion as --config, so `~/...` in CCRESU_CONFIG is not taken literally.
func TestConfigPathEnvironmentIsExpanded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	defaultPath := writeConfig(t, filepath.Join(home, ".config", "ccresu", "config.yaml"), "default-app")
	writeConfig(t, filepath.Join(home, "custom", "env.yaml"), "env-app")

	deps := envConfigDeps(defaultPath, map[string]string{config.EnvConfigPath: "~/custom/env.yaml"})
	shown := showApp(t, deps)

	if shown.App.Name != "env-app" {
		t.Fatalf("app.name = %q, want env-app: CCRESU_CONFIG with ~ was not expanded", shown.App.Name)
	}
}
