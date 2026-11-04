package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jymchng/ccresu/configs"
	"github.com/jymchng/ccresu/internal/config"
)

// initDeps returns the minimum environment `config init` needs, with the real
// configuration loader so the "does the example still validate?" check is a real
// check rather than a stub that always agrees.
func initDeps(defaultPath *string) (Deps, *bytes.Buffer, *bytes.Buffer) {
	var stdout, stderr bytes.Buffer
	return Deps{
		Stdout:    &stdout,
		Stderr:    &stderr,
		LookupEnv: func(string) (string, bool) { return "", false },
		LoadConfig: func(path string, lookup func(string) (string, bool)) (*config.Config, error) {
			return config.LoadWithEnv(path, lookup)
		},
		DefaultConfigPath: func() (string, error) { return *defaultPath, nil },
	}, &stdout, &stderr
}

// TestConfigInitCreatesAValidationReadyFileInTheDefaultLocation is the headline
// promise: one command, the default path, the shipped contents, mode 0600, and a
// file `config validate` accepts.
func TestConfigInitCreatesAValidationReadyFileInTheDefaultLocation(t *testing.T) {
	home := t.TempDir()
	defaultPath := filepath.Join(home, ".config", "ccresu", "config.yaml")
	deps, stdout, stderr := initDeps(&defaultPath)

	if code := RunWith(deps, []string{"config", "init"}, BuildInfo{}, nil, nil); code != ExitOK {
		t.Fatalf("config init = exit %d, want 0: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), defaultPath) {
		t.Errorf("config init did not report the path it wrote:\n%s", stdout.String())
	}

	info, err := os.Stat(defaultPath)
	if err != nil {
		t.Fatalf("the default configuration was not created: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %04o, want 0600 (the file holds a bot token)", mode)
	}
	// The parent directory must exist and be private too: it is where the API
	// token is written later.
	dirInfo, err := os.Stat(filepath.Dir(defaultPath))
	if err != nil {
		t.Fatalf("the configuration directory was not created: %v", err)
	}
	if mode := dirInfo.Mode().Perm(); mode != 0o700 {
		t.Errorf("directory mode = %04o, want 0700", mode)
	}

	written, err := os.ReadFile(defaultPath)
	if err != nil {
		t.Fatalf("read the written configuration: %v", err)
	}
	if !bytes.Equal(written, configs.Example) {
		t.Errorf("the written file is not the shipped example (%d bytes written, %d embedded)",
			len(written), len(configs.Example))
	}

	// The acceptance criterion that matters to a user: the new file validates,
	// through the same command they would run next.
	validateDeps, validateOut, validateErr := initDeps(&defaultPath)
	if code := RunWith(validateDeps, []string{"config", "validate"}, BuildInfo{}, nil, nil); code != ExitOK {
		t.Fatalf("config validate on the file init wrote = exit %d, want 0: %s%s",
			code, validateOut.String(), validateErr.String())
	}
}

// TestConfigInitRefusesToOverwriteWithoutForce pins the non-destructive default:
// an existing file is left untouched byte for byte, the exit code says failure,
// and the message names the flag that would replace it.
func TestConfigInitRefusesToOverwriteWithoutForce(t *testing.T) {
	defaultPath := filepath.Join(t.TempDir(), "ccresu", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(defaultPath), 0o700); err != nil {
		t.Fatalf("prepare directory: %v", err)
	}
	handEdited := []byte("app:\n  name: hand-edited\n")
	if err := os.WriteFile(defaultPath, handEdited, 0o644); err != nil {
		t.Fatalf("write the existing configuration: %v", err)
	}

	deps, _, stderr := initDeps(&defaultPath)
	if code := RunWith(deps, []string{"config", "init"}, BuildInfo{}, nil, nil); code != ExitFailure {
		t.Fatalf("config init over an existing file = exit %d, want %d", code, ExitFailure)
	}
	for _, want := range []string{"already exists", "--force", "config validate"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("the refusal does not mention %q:\n%s", want, stderr.String())
		}
	}

	kept, err := os.ReadFile(defaultPath)
	if err != nil {
		t.Fatalf("read the existing configuration: %v", err)
	}
	if !bytes.Equal(kept, handEdited) {
		t.Errorf("a refused init changed the file: %q", kept)
	}
}

// TestConfigInitForceReplacesAndTightensTheMode covers the documented escape
// hatch, including a file that had been left group- and world-readable.
func TestConfigInitForceReplacesAndTightensTheMode(t *testing.T) {
	defaultPath := filepath.Join(t.TempDir(), "ccresu", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(defaultPath), 0o700); err != nil {
		t.Fatalf("prepare directory: %v", err)
	}
	if err := os.WriteFile(defaultPath, []byte("app:\n  name: hand-edited\n"), 0o644); err != nil {
		t.Fatalf("write the existing configuration: %v", err)
	}

	deps, stdout, stderr := initDeps(&defaultPath)
	if code := RunWith(deps, []string{"config", "init", "--force"}, BuildInfo{}, nil, nil); code != ExitOK {
		t.Fatalf("config init --force = exit %d, want 0: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "replaced") {
		t.Errorf("a forced init did not say it replaced the file:\n%s", stdout.String())
	}

	written, err := os.ReadFile(defaultPath)
	if err != nil {
		t.Fatalf("read the replaced configuration: %v", err)
	}
	if !bytes.Equal(written, configs.Example) {
		t.Errorf("--force did not write the shipped example")
	}
	info, err := os.Stat(defaultPath)
	if err != nil {
		t.Fatalf("stat the replaced configuration: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode after --force = %04o, want 0600 even when the old file was 0644", mode)
	}
}

// TestConfigInitHonoursAnExplicitPath covers the two ways a user names a path:
// the positional argument and the global --config flag, plus the directory they
// imply when it does not exist yet.
func TestConfigInitHonoursAnExplicitPath(t *testing.T) {
	home := t.TempDir()
	defaultPath := filepath.Join(home, ".config", "ccresu", "config.yaml")

	positional := filepath.Join(home, "nested", "deeper", "custom.yaml")
	deps, _, stderr := initDeps(&defaultPath)
	if code := RunWith(deps, []string{"config", "init", positional}, BuildInfo{}, nil, nil); code != ExitOK {
		t.Fatalf("config init <path> = exit %d, want 0: %s", code, stderr.String())
	}
	if _, err := os.Stat(positional); err != nil {
		t.Fatalf("the positional path was not created (parents must be made): %v", err)
	}
	if _, err := os.Stat(defaultPath); err == nil {
		t.Errorf("config init <path> also wrote the default file %s", defaultPath)
	}

	flagged := filepath.Join(home, "flagged", "config.yaml")
	deps, _, stderr = initDeps(&defaultPath)
	if code := RunWith(deps, []string{"--config", flagged, "config", "init"}, BuildInfo{}, nil, nil); code != ExitOK {
		t.Fatalf("--config <path> config init = exit %d, want 0: %s", code, stderr.String())
	}
	if _, err := os.Stat(flagged); err != nil {
		t.Fatalf("--config path was not created: %v", err)
	}
}

// TestConfigInitJSONReportsWhatItDid pins the machine-readable form, because the
// exit code cannot distinguish "created" from "replaced".
func TestConfigInitJSONReportsWhatItDid(t *testing.T) {
	defaultPath := filepath.Join(t.TempDir(), "ccresu", "config.yaml")
	deps, stdout, stderr := initDeps(&defaultPath)

	if code := RunWith(deps, []string{"--json", "config", "init"}, BuildInfo{}, nil, nil); code != ExitOK {
		t.Fatalf("--json config init = exit %d, want 0: %s", code, stderr.String())
	}
	var first struct {
		Path     string `json:"path"`
		Created  bool   `json:"created"`
		Replaced bool   `json:"replaced"`
		Mode     string `json:"mode"`
		Valid    bool   `json:"valid"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &first); err != nil {
		t.Fatalf("--json config init is not JSON: %v (%q)", err, stdout.String())
	}
	if first.Path != defaultPath || !first.Created || first.Replaced || first.Mode != "0600" || !first.Valid {
		t.Fatalf("unexpected report for a fresh init: %+v", first)
	}

	deps, stdout, _ = initDeps(&defaultPath)
	if code := RunWith(deps, []string{"--json", "config", "init", "--force"}, BuildInfo{}, nil, nil); code != ExitOK {
		t.Fatalf("--json config init --force = exit %d, want 0", code)
	}
	var second struct {
		Created  bool `json:"created"`
		Replaced bool `json:"replaced"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &second); err != nil {
		t.Fatalf("--json config init --force is not JSON: %v (%q)", err, stdout.String())
	}
	if second.Created || !second.Replaced {
		t.Fatalf("a forced init must report replaced, not created: %+v", second)
	}
}

// TestConfigInitRejectsBadArguments keeps the usage-error contract: nothing is
// written when the arguments do not make sense.
func TestConfigInitRejectsBadArguments(t *testing.T) {
	defaultPath := filepath.Join(t.TempDir(), "ccresu", "config.yaml")
	deps, _, _ := initDeps(&defaultPath)
	if code := RunWith(deps, []string{"config", "init", "one.yaml", "two.yaml"}, BuildInfo{}, nil, nil); code != ExitUsage {
		t.Fatalf("two paths = exit %d, want %d", code, ExitUsage)
	}
	if _, err := os.Stat(defaultPath); err == nil {
		t.Errorf("a usage error created %s", defaultPath)
	}
}

// TestConfigInitIsDiscoverable requires the command to be reachable from the
// places a user looks: the help index, and the error messages of `config`.
func TestConfigInitIsDiscoverable(t *testing.T) {
	defaultPath := filepath.Join(t.TempDir(), "ccresu", "config.yaml")
	deps, stdout, stderr := initDeps(&defaultPath)

	if code := RunWith(deps, []string{"--help"}, BuildInfo{}, nil, nil); code != ExitOK {
		t.Fatalf("--help = exit %d, want 0", code)
	}
	// The index prints one row per command, so "init" is only visible through the
	// summary; the usage line carries the subcommand names.
	var found bool
	for _, spec := range newApp(deps, BuildInfo{}, nil, nil).commands() {
		if spec.name == "config" {
			found = true
			if !strings.Contains(spec.usage, "config init") {
				t.Errorf("config usage = %q, want it to name init", spec.usage)
			}
		}
	}
	if !found {
		t.Fatal("the config command is missing from the command index")
	}
	if !strings.Contains(stdout.String(), "create, validate (or print) the configuration") {
		t.Errorf("--help does not describe config init:\n%s", stdout.String())
	}

	deps, _, stderr = initDeps(&defaultPath)
	if code := RunWith(deps, []string{"config"}, BuildInfo{}, nil, nil); code != ExitUsage {
		t.Fatalf("config = exit %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr.String(), "init") {
		t.Errorf("the bare `config` error does not name init:\n%s", stderr.String())
	}

	deps, _, stderr = initDeps(&defaultPath)
	if code := RunWith(deps, []string{"config", "nope"}, BuildInfo{}, nil, nil); code != ExitUsage {
		t.Fatalf("config nope = exit %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr.String(), "init") {
		t.Errorf("the unknown-subcommand error does not name init:\n%s", stderr.String())
	}
}
