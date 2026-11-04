package config

import (
	"os"
	"path/filepath"
	"testing"
)

// isolateConfigDirs points the user config directory somewhere harmless so a
// test that expects the fallback cannot pick up the developer's real
// ~/.config/ccresu.
func isolateConfigDirs(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config"))
	return dir
}

// TestTokenPathFollowsTheConfigurationFile is the point of the --config fix: the
// bearer token lives next to the configuration file in use, not always in
// ~/.config/ccresu, so `--config /elsewhere/config.yaml` does not silently read a
// token that belongs to a different configuration.
func TestTokenPathFollowsTheConfigurationFile(t *testing.T) {
	isolateConfigDirs(t)

	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.MkdirAll(elsewhere, 0o700); err != nil {
		t.Fatalf("create %s: %v", elsewhere, err)
	}
	path := filepath.Join(elsewhere, "config.yaml")
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}

	lookup := func(string) (string, bool) { return "", false }

	cfg, err := LoadWithEnv(path, lookup)
	if err != nil {
		t.Fatalf("LoadWithEnv(%s): %v", path, err)
	}
	want := filepath.Join(elsewhere, "api-token")
	if cfg.Server.TokenPath != want {
		t.Errorf("server.token_path = %q, want %q (beside the --config file)", cfg.Server.TokenPath, want)
	}
}

// TestTokenPathUsesTheExplicitSourcePath checks the same rule through Load, which
// resolves the path itself, and through ExpandPathsWithin.
func TestTokenPathUsesTheExplicitSourcePath(t *testing.T) {
	isolateConfigDirs(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "custom.yaml")
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%s): %v", path, err)
	}
	if want := filepath.Join(dir, "api-token"); cfg.Server.TokenPath != want {
		t.Errorf("Load: server.token_path = %q, want %q", cfg.Server.TokenPath, want)
	}

	expanded := Defaults()
	expanded.Server.TokenPath = ""
	expanded.Storage.Path = ""
	if err := ExpandPathsWithin(&expanded, "/elsewhere/config.yaml"); err != nil {
		t.Fatalf("ExpandPathsWithin: %v", err)
	}
	if want := filepath.Join("/elsewhere", "api-token"); expanded.Server.TokenPath != want {
		t.Errorf("ExpandPathsWithin: server.token_path = %q, want %q", expanded.Server.TokenPath, want)
	}
}

// TestTokenPathFallbackWithoutAConfigurationFile keeps the old behaviour for the
// case that has no file to sit next to: a bare stream (Parse) or an empty source
// path still resolves under the user config directory.
func TestTokenPathFallbackWithoutAConfigurationFile(t *testing.T) {
	home := isolateConfigDirs(t)

	cfg, err := Parse([]byte(""))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := filepath.Join(home, ".config", DefaultDirName, "api-token")
	if cfg.Server.TokenPath != want {
		t.Errorf("Parse: server.token_path = %q, want %q", cfg.Server.TokenPath, want)
	}

	got, err := DefaultTokenPath("   ")
	if err != nil {
		t.Fatalf("DefaultTokenPath: %v", err)
	}
	if got != want {
		t.Errorf("DefaultTokenPath(\"\") = %q, want %q", got, want)
	}
}

// TestTokenPathExplicitSettingWins proves the new default does not override a
// configured server.token_path.
func TestTokenPathExplicitSettingWins(t *testing.T) {
	isolateConfigDirs(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "server:\n  token_path: " + filepath.Join("/run/secrets", "ccresu-token") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}

	cfg, err := LoadWithEnv(path, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("LoadWithEnv: %v", err)
	}
	if want := filepath.Join("/run/secrets", "ccresu-token"); cfg.Server.TokenPath != want {
		t.Errorf("server.token_path = %q, want the configured %q", cfg.Server.TokenPath, want)
	}
}

// TestTokenPathRelativeSourcePathResolvesBesideTheFile keeps the rule sane when
// the configuration is named relatively: the token is a sibling of the file.
func TestTokenPathRelativeSourcePathResolvesBesideTheFile(t *testing.T) {
	isolateConfigDirs(t)

	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("config.yaml", []byte(""), 0o600); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	cfg, err := LoadWithEnv("config.yaml", func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("LoadWithEnv: %v", err)
	}
	if cfg.Server.TokenPath != "api-token" {
		t.Errorf("server.token_path = %q, want %q", cfg.Server.TokenPath, "api-token")
	}
}

// TestDefaultTokenPathFailsWithoutAHome keeps the existing failure mode: with no
// configuration file and no user config directory there is nowhere to put the
// token, and that must be an error rather than an empty path.
func TestDefaultTokenPathFailsWithoutAHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	if got, err := DefaultTokenPath(""); err == nil {
		t.Errorf("DefaultTokenPath(\"\") = %q, want an error", got)
	}
}
