// Package integration_test drives the shipped ccresu binary over real
// processes, a real configuration file, a real SQLite file and a real TCP
// listener.
//
// The unit suites call internal/app against fakes and internal/server against
// httptest, and the end-to-end suite replays the recovery scenario in-process.
// Neither ever runs the command surface the way a user does: `main`'s wiring,
// flag parsing in a fresh process, configuration loading from disk, the token
// file, signal handling and graceful shutdown are all skipped. This suite covers
// exactly that gap, so `go test ./...` exercises every non-web package including
// cmd/ccresu.
package integration_test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/storage"
)

// The session the live API test seeds. It is a lowercase v4 UUID because the
// recovery engine validates ids before it resumes anything.
const seededSessionID = "2f7c4b9e-51a3-4c8d-9e06-83b1d5a7c240"

var (
	// binaryPath is the compiled ccresu under test.
	binaryPath string
	// buildFailure records why the build failed, so every test reports it
	// instead of failing with a confusing "file not found".
	buildFailure error
)

// TestMain compiles the command once for the whole package.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ccresu-integration-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create build directory: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	binaryPath = filepath.Join(dir, "ccresu")
	if err := buildBinary(binaryPath); err != nil {
		buildFailure = err
	}

	os.Exit(m.Run())
}

// buildBinary compiles ./cmd/ccresu, which is the package under test: the tests
// cannot use go test's own build of it because they need a process they can
// signal and whose exit status they can read.
func buildBinary(out string) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", out, "./cmd/ccresu")
	cmd.Dir = root
	// The build must not inherit a test binary's environment; GOFLAGS and
	// friends belong to the outer invocation.
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go build ./cmd/ccresu: %v\n%s", err, output)
	}
	return nil
}

// repoRoot locates the module root from this package's directory.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	root := filepath.Clean(filepath.Join(dir, "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return "", fmt.Errorf("locate module root from %s: %v", dir, err)
	}
	return root, nil
}

// fixture is one isolated ccresu installation: its own HOME, configuration,
// database, token file and a stub claude binary, so a test never reads or writes
// the developer's real environment.
type fixture struct {
	t *testing.T

	// Dir is the fixture root; ConfigPath, DBPath, TokenPath and HomeDir live
	// inside it.
	Dir        string
	ConfigPath string
	DBPath     string
	TokenPath  string
	HomeDir    string
	ClaudePath string
	Port       int

	// ServerEnabled and MonitoringEnabled are written into the configuration.
	ServerEnabled     bool
	MonitoringEnabled bool

	// claudeScript, when set, replaces the default stub program.
	claudeScript string
}

// fixtureOption customises a fixture before its configuration is written.
type fixtureOption func(*fixture)

// withServerEnabled writes server.enabled.
func withServerEnabled(enabled bool) fixtureOption {
	return func(f *fixture) { f.ServerEnabled = enabled }
}

// withMonitoringEnabled writes monitoring.enabled.
func withMonitoringEnabled(enabled bool) fixtureOption {
	return func(f *fixture) { f.MonitoringEnabled = enabled }
}

// withPort pins the configured server port.
func withPort(port int) fixtureOption {
	return func(f *fixture) { f.Port = port }
}

// withClaudeScript installs a different stand-in for the claude binary, for the
// tests that need it to misbehave.
func withClaudeScript(script string) fixtureOption {
	return func(f *fixture) { f.claudeScript = script }
}

// newFixture creates an isolated installation and writes its configuration.
func newFixture(t *testing.T, options ...fixtureOption) *fixture {
	t.Helper()
	if buildFailure != nil {
		t.Fatalf("the ccresu binary could not be built: %v", buildFailure)
	}

	dir := t.TempDir()
	f := &fixture{
		t:                 t,
		Dir:               dir,
		ConfigPath:        filepath.Join(dir, "config.yaml"),
		DBPath:            filepath.Join(dir, "state", "ccresu.db"),
		TokenPath:         filepath.Join(dir, "secrets", "api-token"),
		HomeDir:           filepath.Join(dir, "home"),
		ClaudePath:        filepath.Join(dir, "home", "bin", "claude"),
		Port:              freePort(t),
		ServerEnabled:     true,
		MonitoringEnabled: true,
	}
	for _, option := range options {
		option(f)
	}

	// HOME isolates the Claude Code store (~/.claude), the configuration
	// directory and the data directory, none of which may touch the real user's.
	if err := os.MkdirAll(f.HomeDir, 0o700); err != nil {
		t.Fatalf("create fixture home: %v", err)
	}
	writeClaudeStub(t, f.ClaudePath, f.claudeScript)
	f.writeConfig(t)
	return f
}

// writeConfig renders the configuration file the binary will load.
//
// It is deliberately partial: the defaults fill in everything else, which is the
// same path a user's short config file takes.
func (f *fixture) writeConfig(t *testing.T) {
	t.Helper()
	config := fmt.Sprintf(`app:
  name: ccresu
  log_level: info
  log_format: text
  timezone: UTC

claude:
  executable: %s
  discover_all_sessions: false

monitoring:
  enabled: %t

telegram:
  enabled: false

server:
  enabled: %t
  host: 127.0.0.1
  port: %d
  allowed_origins:
    - http://127.0.0.1:%d
  token_path: %s

storage:
  driver: sqlite
  path: %s
  busy_timeout: 5s
`, f.ClaudePath, f.MonitoringEnabled, f.ServerEnabled, f.Port, f.Port, f.TokenPath, f.DBPath)
	if err := os.WriteFile(f.ConfigPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write fixture config: %v", err)
	}
}

// writeClaudeStub installs a stand-in `claude` binary.
//
// It answers the two read-only commands ccresu runs before it would ever resume
// anything — `--version` and `agents --json` — and exits non-zero for the rest,
// so a test that accidentally reaches a resume fails loudly instead of silently
// pretending a conversation continued.
func writeClaudeStub(t *testing.T, path, script string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create stub directory: %v", err)
	}
	if script == "" {
		script = `#!/bin/sh
case "$1" in
--version)
	echo "2.1.223 (Claude Code)"
	exit 0
	;;
agents)
	echo "[]"
	exit 0
	;;
esac
echo "ccresu integration stub: refusing to run $*" >&2
exit 1
`
	}
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write claude stub: %v", err)
	}
}

// env is the environment the binary runs with: the fixture's HOME and nothing
// inherited that could redirect the configuration or the store.
func (f *fixture) env() []string {
	env := []string{
		"HOME=" + f.HomeDir,
		"PATH=" + os.Getenv("PATH"),
		"TMPDIR=" + f.Dir,
		"TZ=UTC",
	}
	if tmp := os.Getenv("GOCACHE"); tmp != "" {
		env = append(env, "GOCACHE="+tmp)
	}
	return env
}

// result is one finished CLI invocation.
type result struct {
	code   int
	stdout string
	stderr string
}

// run executes the binary and returns its exit code and streams.
func (f *fixture) run(args ...string) result {
	f.t.Helper()
	return f.runWithEnv(nil, args...)
}

// runWithEnv executes the binary with extra environment variables appended, so a
// test can prove the documented CCRESU_* precedence without hand-editing the
// configuration file.
func (f *fixture) runWithEnv(extra []string, args ...string) result {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, binaryPath, args...)
	cmd.Dir = f.Dir
	cmd.Env = append(f.env(), extra...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		f.t.Fatalf("ccresu %s timed out\nstdout: %s\nstderr: %s",
			strings.Join(args, " "), stdout.String(), stderr.String())
	}
	code := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			f.t.Fatalf("run ccresu %s: %v", strings.Join(args, " "), err)
		}
		code = exitErr.ExitCode()
	}
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// mustRun runs the binary and fails unless it exited with wantCode.
func (f *fixture) mustRun(wantCode int, args ...string) result {
	f.t.Helper()
	got := f.run(args...)
	if got.code != wantCode {
		f.t.Fatalf("ccresu %s exited %d, want %d\nstdout: %s\nstderr: %s",
			strings.Join(args, " "), got.code, wantCode, got.stdout, got.stderr)
	}
	return got
}

// seedSession writes one session straight into the fixture database, the way the
// daemon would after discovering it. It runs in the test process, before or after
// the binary touches the file, which is also a proof that the two processes
// agree on the on-disk schema.
func (f *fixture) seedSession(t *testing.T, id string, mutate func(*storage.Session)) {
	t.Helper()
	ctx := context.Background()
	store, err := storage.Open(ctx, storage.Options{Path: f.DBPath})
	if err != nil {
		t.Fatalf("open %s: %v", f.DBPath, err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Fatalf("close %s: %v", f.DBPath, err)
		}
	}()
	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate %s: %v", f.DBPath, err)
	}

	session := storage.Session{
		ID:              id,
		ProjectPath:     "/home/dev/src/widget",
		TranscriptPath:  "/home/dev/.claude/projects/-home-dev-src-widget/" + id + ".jsonl",
		Provider:        "claude",
		Type:            storage.SessionTypeInteractive,
		State:           storage.StateRunning,
		Enabled:         true,
		Recoverable:     true,
		TranscriptSize:  2048,
		TranscriptLines: 12,
	}
	if mutate != nil {
		mutate(&session)
	}
	if _, err := store.UpsertSession(ctx, session); err != nil {
		t.Fatalf("seed session %s: %v", id, err)
	}
}

// readSession reopens the fixture database and returns one session, so a test
// can assert what the binary persisted rather than what it printed.
func (f *fixture) readSession(t *testing.T, id string) storage.Session {
	t.Helper()
	ctx := context.Background()
	store, err := storage.Open(ctx, storage.Options{Path: f.DBPath})
	if err != nil {
		t.Fatalf("open %s: %v", f.DBPath, err)
	}
	defer func() { _ = store.Close() }()
	session, err := store.GetSession(ctx, id)
	if err != nil {
		t.Fatalf("read session %s: %v", id, err)
	}
	return session
}

// freePort returns a loopback port that was free a moment ago. The binary binds
// it immediately, so the race is theoretical; listeners here are short-lived test
// processes, not a service under load.
func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a free port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release the reserved port: %v", err)
	}
	return port
}

// lockedBuffer is a bytes.Buffer that a process's output pipe and the test
// goroutine can use at the same time.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write implements io.Writer.
func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String returns everything captured so far.
func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// process is a long-running ccresu invocation (serve, daemon).
type process struct {
	cmd    *exec.Cmd
	stdout *lockedBuffer
	stderr *lockedBuffer
	// done is closed once the process has exited, after waitErr is set.
	done    chan struct{}
	mu      sync.Mutex
	waitErr error
}

// startProcess launches the binary in the background.
func (f *fixture) startProcess(args ...string) *process {
	f.t.Helper()
	stdout := &lockedBuffer{}
	stderr := &lockedBuffer{}
	cmd := exec.Command(binaryPath, args...)
	cmd.Dir = f.Dir
	cmd.Env = f.env()
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		f.t.Fatalf("start ccresu %s: %v", strings.Join(args, " "), err)
	}
	p := &process{cmd: cmd, stdout: stdout, stderr: stderr, done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		p.waitErr = err
		p.mu.Unlock()
		close(p.done)
	}()
	f.t.Cleanup(func() {
		select {
		case <-p.done:
			return
		default:
		}
		_ = cmd.Process.Kill()
		<-p.done
	})
	return p
}

// exited reports whether the process has finished.
func (p *process) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// waitResult returns what cmd.Wait reported; call it only after done is closed.
func (p *process) waitResult() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitErr
}

// output returns the captured stdout and stderr.
func (p *process) output() (string, string) {
	return p.stdout.String(), p.stderr.String()
}

// waitForOutput waits until the process printed text.
func (p *process) waitForOutput(t *testing.T, text string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(p.stdout.String(), text) {
			return
		}
		if p.exited() {
			stdout, stderr := p.output()
			t.Fatalf("ccresu exited (%v) before printing %q\nstdout: %s\nstderr: %s",
				p.waitResult(), text, stdout, stderr)
		}
		time.Sleep(5 * time.Millisecond)
	}
	stdout, stderr := p.output()
	t.Fatalf("timed out waiting for %q\nstdout: %s\nstderr: %s", text, stdout, stderr)
}

// waitExit waits for the process to finish and returns its exit code.
func (p *process) waitExit(t *testing.T, timeout time.Duration) int {
	t.Helper()
	select {
	case <-p.done:
		err := p.waitResult()
		if err == nil {
			return 0
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		t.Fatalf("wait for ccresu: %v", err)
	case <-time.After(timeout):
		_ = p.cmd.Process.Kill()
		<-p.done
		stdout, stderr := p.output()
		t.Fatalf("ccresu did not exit within %s\nstdout: %s\nstderr: %s", timeout, stdout, stderr)
	}
	return -1
}

// interrupt sends SIGINT, which is how an operator stops serve and daemon.
func (p *process) interrupt(t *testing.T) {
	t.Helper()
	if err := p.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("send SIGINT: %v", err)
	}
}

// portClosed reports whether nothing is listening on the loopback port.
func portClosed(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
	if err != nil {
		return true
	}
	_ = conn.Close()
	return false
}
