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

	cfg, err := Parse(data)
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
	cfg, err := parse(data, lookup)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", resolved, err)
	}
	cfg.Source = resolved
	return cfg, nil
}

// Parse decodes YAML into defaults, applies environment overrides, expands
// paths and validates the result. The environment is read from the process.
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

	if err := ExpandPaths(&cfg); err != nil {
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
func ExpandPaths(cfg *Config) error {
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
		dir, dirErr := os.UserConfigDir()
		if dirErr != nil {
			return fmt.Errorf("resolve user config dir: %v", dirErr)
		}
		expanded = filepath.Join(dir, DefaultDirName, "api-token")
	}
	cfg.Server.TokenPath = expanded

	expanded, err = ExpandPath(cfg.Claude.Executable)
	if err != nil {
		return err
	}
	cfg.Claude.Executable = expanded
	return nil
}
