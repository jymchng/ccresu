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

	// A key an older release documented is dropped before the strict decode, so
	// an upgrade does not leave a configuration file unloadable. Every other
	// unknown key still fails below.
	pruned, retired := pruneRetiredKeys(data)
	cfg.retired = retired

	decoder := yaml.NewDecoder(bytes.NewReader(pruned))
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

// retiredKeys names configuration keys that a previous release documented and
// this one no longer has, addressed by their path in the document.
//
// A file written against the older release still carries them, and the strict
// decode would then refuse it with `field notify_needs_input not found in type
// config.Telegram` — so a plain upgrade would leave ccresu unable to start, over
// a key that no longer means anything. Dropping exactly these keys keeps the
// upgrade working while every other unknown key (including a typo of a retired
// one) is still rejected: a key that never existed must not silently leave a
// default in place.
//
// Entries are permanent. A key listed here was withdrawn in a released version,
// so removing the entry would break the upgrade it exists to smooth.
var retiredKeys = [][]string{
	// 0.9.0: a `blocked` session is resumed rather than announced, so no
	// needs-input message exists and nothing reads its switch.
	{"telegram", "notify_needs_input"},
	// 0.10.0: a usage window is only ever learned from the resume itself, so
	// there is no pre-resume recheck to switch on. Nothing read this key even
	// while it was documented, and the shipped daemon never installed a limit
	// recheck, so dropping it changes no behaviour — it only stops the file
	// from claiming one.
	{"scheduler", "recheck_before_resume"},
	// 0.10.0: the three timeouts bounded a handshake that no longer happens —
	// watching a child start, polling a transcript for growth, and the gap
	// between the polls. A resume is one blocking command bounded by
	// recovery.turn_timeout, so each of these outlived the pass that read it.
	// They are pruned rather than left as dead fields because a timeout in a
	// file reads as a promise that something is still being watched.
	{"recovery", "startup_timeout"},
	{"recovery", "verification_timeout"},
	{"recovery", "health_check_interval"},
	// 0.10.0: the four guardrail flags rode the handoff vector, and a resume now
	// carries no flags at all. They are pruned by name rather than by dropping
	// `recovery.guardrails`, because the two spend caps in the same section keep
	// their meaning: those are ccresu's own checks over the series of attempts,
	// not CLI flags, so an operator's caps must survive the upgrade.
	{"recovery", "guardrails", "model"},
	{"recovery", "guardrails", "effort"},
	{"recovery", "guardrails", "fallback_model"},
	{"recovery", "guardrails", "permission_mode"},
}

// pruneRetiredKeys returns data without the retiredKeys it carries, and the
// dotted paths it removed, in document order.
//
// A document that does not parse, or whose shape never reaches a retired key, is
// returned unchanged: the strict decode that follows owns malformed YAML and
// unknown-key reporting, and this pass may only take a key away, never add one
// or change a value it keeps.
func pruneRetiredKeys(data []byte) ([]byte, []string) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return data, nil
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return data, nil
	}
	var removed []string
	for _, path := range retiredKeys {
		if dropKeyPath(doc.Content[0], path) {
			removed = append(removed, strings.Join(path, "."))
		}
	}
	if len(removed) == 0 {
		return data, nil
	}
	pruned, err := yaml.Marshal(&doc)
	if err != nil {
		// The document parsed, so this is unreachable; keeping the original bytes
		// means the strict decode still reports something true about the file.
		return data, nil
	}
	return pruned, removed
}

// dropKeyPath deletes every key named path[0] from the mapping node, recursing
// for the rest of path, and reports whether it found one.
//
// Only mappings are followed and only an exact key name matches at each step, so
// `telegram.notify_needs_inputt` and a top-level `notify_needs_input` are left
// for the strict decode to reject.
func dropKeyPath(node *yaml.Node, path []string) bool {
	if node == nil || len(path) == 0 || node.Kind != yaml.MappingNode {
		return false
	}
	found := false
	kept := node.Content[:0]
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Value == path[0] {
			if len(path) == 1 {
				found = true
				continue
			}
			if dropKeyPath(value, path[1:]) {
				found = true
			}
		}
		kept = append(kept, key, value)
	}
	node.Content = kept
	return found
}

// RetiredKeys reports the configuration keys the loaded document carried that
// this release no longer has, as dotted paths such as
// `telegram.notify_needs_input`. They were ignored; Warnings explains that once
// so an operator does not wonder why a setting stopped working.
//
// It is empty for a file that carries none, and for a configuration built in
// code.
func (c Config) RetiredKeys() []string {
	if len(c.retired) == 0 {
		return nil
	}
	out := make([]string, len(c.retired))
	copy(out, c.retired)
	return out
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
