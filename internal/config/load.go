package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	// EnvConfigPath names the environment variable that overrides the config path.
	EnvConfigPath = "CCRESU_CONFIG"
	// EnvPrefix is the prefix for every configuration override variable.
	EnvPrefix = "CCRESU_"
	// DefaultDirName is the directory name used under XDG base directories.
	DefaultDirName = "ccresu"
)

// ErrInvalid is returned (wrapped) whenever the configuration cannot be loaded
// or fails validation. Callers can test it with errors.Is.
var ErrInvalid = errors.New("invalid configuration")

// Load reads, parses, overrides and validates the configuration at path.
//
// An empty path resolves to $CCRESU_CONFIG, then the platform user config
// directory (~/.config/ccresu/config.yaml on Linux).
func Load(path string) (*Config, error) {
	resolved, err := ResolvePath(path)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("%w: read %s: %v", ErrInvalid, resolved, err)
	}

	cfg, err := parseWithin(data, envLookup, resolved)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", resolved, err)
	}
	cfg.Source = resolved
	return cfg, nil
}

// LoadWithEnv is Load with an injectable environment lookup, for tests.
func LoadWithEnv(path string, lookup func(string) (string, bool)) (*Config, error) {
	resolved, err := ResolvePath(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("%w: read %s: %v", ErrInvalid, resolved, err)
	}
	cfg, err := parseWithin(data, lookup, resolved)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", resolved, err)
	}
	cfg.Source = resolved
	return cfg, nil
}

// Parse decodes YAML into defaults, applies environment overrides, expands
// paths and validates the result. The environment is read from the process.
//
// Parse has no source file, so an unset server.token_path falls back to the user
// config directory. Load derives it from the directory of the configuration file
// in use instead.
func Parse(data []byte) (*Config, error) {
	return parse(data, envLookup)
}

// ParseReader is a convenience wrapper around Parse for io.Reader sources.
func ParseReader(r io.Reader) (*Config, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("%w: read config: %v", ErrInvalid, err)
	}
	return Parse(data)
}

func parse(data []byte, lookup func(string) (string, bool)) (*Config, error) {
	return parseWithin(data, lookup, "")
}

// parseWithin is parse with the configuration file the bytes came from, so a
// default server.token_path can be placed beside that file.
func parseWithin(data []byte, lookup func(string) (string, bool), sourcePath string) (*Config, error) {
	cfg := Defaults()

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	// Unknown keys are rejected rather than silently ignored, so a typo such as
	// `max_attempt` cannot quietly leave the default in place.
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: parse yaml: %v", ErrInvalid, err)
	}

	if err := applyEnv(&cfg, withLegacyAliases(lookup)); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}

	if err := expandPaths(&cfg, sourcePath); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// ResolvePath determines which file Load should read.
func ResolvePath(path string) (string, error) {
	if trimmed := strings.TrimSpace(path); trimmed != "" {
		return ExpandPath(trimmed)
	}
	if env := strings.TrimSpace(os.Getenv(EnvConfigPath)); env != "" {
		return ExpandPath(env)
	}
	return DefaultPath()
}

// DefaultPath returns $XDG_CONFIG_HOME/ccresu/config.yaml (or the platform
// equivalent) with the home directory expanded.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("%w: resolve user config dir: %v", ErrInvalid, err)
	}
	return filepath.Join(dir, DefaultDirName, "config.yaml"), nil
}

// DefaultDataDir returns the directory holding ccresu state (~/.local/share/ccresu).
func DefaultDataDir() (string, error) {
	dir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("%w: resolve home dir: %v", ErrInvalid, err)
	}
	return filepath.Join(dir, ".local", "share", DefaultDirName), nil
}

// DefaultStateDir returns the directory holding runtime state (~/.local/state/ccresu).
func DefaultStateDir() (string, error) {
	dir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("%w: resolve home dir: %v", ErrInvalid, err)
	}
	return filepath.Join(dir, ".local", "state", DefaultDirName), nil
}

// ExpandPath expands a leading ~ or ~/ and cleans the result.
func ExpandPath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", nil
	}
	if trimmed == "~" || strings.HasPrefix(trimmed, "~/") || strings.HasPrefix(trimmed, "~"+string(filepath.Separator)) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand %q: %v", path, err)
		}
		rest := strings.TrimPrefix(trimmed, "~")
		rest = strings.TrimPrefix(rest, "/")
		rest = strings.TrimPrefix(rest, string(filepath.Separator))
		trimmed = filepath.Join(home, rest)
	}
	return filepath.Clean(trimmed), nil
}

// ExpandPaths expands the user-facing paths in cfg to absolute paths.
//
// It has no source file, so an unset server.token_path falls back to the user
// config directory. Load uses ExpandPathsWithin to keep the token next to the
// configuration it belongs to.
func ExpandPaths(cfg *Config) error {
	return expandPaths(cfg, "")
}

// ExpandPathsWithin expands the user-facing paths in cfg to absolute paths and
// derives a default server.token_path from sourcePath's directory.
//
// sourcePath is the configuration file actually in use. An empty sourcePath
// means there is no file (a bare stream), so the token falls back to the user
// config directory.
func ExpandPathsWithin(cfg *Config, sourcePath string) error {
	return expandPaths(cfg, sourcePath)
}

func expandPaths(cfg *Config, sourcePath string) error {
	if cfg == nil {
		return fmt.Errorf("%w: nil config", ErrInvalid)
	}
	expanded, err := ExpandPath(cfg.Storage.Path)
	if err != nil {
		return err
	}
	cfg.Storage.Path = expanded

	expanded, err = ExpandPath(cfg.Server.TokenPath)
	if err != nil {
		return err
	}
	if expanded == "" {
		expanded, err = DefaultTokenPath(sourcePath)
		if err != nil {
			return err
		}
	}
	cfg.Server.TokenPath = expanded

	expanded, err = ExpandPath(cfg.Claude.Executable)
	if err != nil {
		return err
	}
	cfg.Claude.Executable = expanded
	return nil
}

// DefaultTokenPath returns where the bearer token lives when server.token_path
// is unset: next to the configuration file in use, so `--config
// /elsewhere/config.yaml` keeps the secret with the configuration that governs
// it instead of silently reading ~/.config/ccresu/api-token.
//
// With no sourcePath there is no configuration file to sit next to, so it falls
// back to $XDG_CONFIG_HOME/ccresu/api-token (or the platform equivalent).
func DefaultTokenPath(sourcePath string) (string, error) {
	if trimmed := strings.TrimSpace(sourcePath); trimmed != "" {
		return filepath.Join(filepath.Dir(filepath.Clean(trimmed)), "api-token"), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config dir: %v", err)
	}
	return filepath.Join(dir, DefaultDirName, "api-token"), nil
}
