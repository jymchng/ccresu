package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jymchng/ccresu/internal/config"
)

// tokenDeps is the minimum environment needed to load a configuration through
// the real loader, exactly as the CLI does.
func tokenDeps(home string) Deps {
	var stdout, stderr bytes.Buffer
	return Deps{
		Stdout:    &stdout,
		Stderr:    &stderr,
		LookupEnv: func(string) (string, bool) { return "", false },
		LoadConfig: func(path string, lookup func(string) (string, bool)) (*config.Config, error) {
			return config.LoadWithEnv(path, lookup)
		},
		DefaultConfigPath: func() (string, error) {
			return filepath.Join(home, ".config", "ccresu", "config.yaml"), nil
		},
	}
}

// TestTokenPathFollowsTheConfigFlag pins the rule the docs promise: the bearer
// token lives beside the configuration file that is actually in use, so
// `--config /elsewhere/config.yaml` does not keep reading the token from
// ~/.config/ccresu.
func TestTokenPathFollowsTheConfigFlag(t *testing.T) {
	home := t.TempDir()
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.MkdirAll(elsewhere, 0o700); err != nil {
		t.Fatalf("create %s: %v", elsewhere, err)
	}
	configPath := filepath.Join(elsewhere, "config.yaml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write %s: %v", configPath, err)
	}

	deps := tokenDeps(home)
	code := RunWith(deps, []string{"--config", configPath, "config", "show"}, BuildInfo{}, nil, nil)
	if code != ExitOK {
		t.Fatalf("config show --config %s = exit %d", configPath, code)
	}

	var shown config.Config
	if err := json.Unmarshal(deps.Stdout.(*bytes.Buffer).Bytes(), &shown); err != nil {
		t.Fatalf("decode config show output: %v", err)
	}
	if want := filepath.Join(elsewhere, "api-token"); shown.Server.TokenPath != want {
		t.Errorf("server.token_path = %q, want %q", shown.Server.TokenPath, want)
	}
}

// TestTokenPathUsesTheDefaultConfigLocationWhenNoFlagIsGiven is the other half of
// the rule: with no --config, the token still lands next to the default
// configuration file.
func TestTokenPathUsesTheDefaultConfigLocationWhenNoFlagIsGiven(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, ".config", "ccresu", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write %s: %v", configPath, err)
	}

	deps := tokenDeps(home)
	if code := RunWith(deps, []string{"config", "show"}, BuildInfo{}, nil, nil); code != ExitOK {
		t.Fatalf("config show = exit %d", code)
	}

	var shown config.Config
	if err := json.Unmarshal(deps.Stdout.(*bytes.Buffer).Bytes(), &shown); err != nil {
		t.Fatalf("decode config show output: %v", err)
	}
	if want := filepath.Join(filepath.Dir(configPath), "api-token"); shown.Server.TokenPath != want {
		t.Errorf("server.token_path = %q, want %q", shown.Server.TokenPath, want)
	}
}
