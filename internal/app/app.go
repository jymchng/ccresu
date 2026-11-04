package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jymchng/ccresu/internal/clock"
	"github.com/jymchng/ccresu/internal/config"
	"github.com/jymchng/ccresu/internal/logging"
	"github.com/jymchng/ccresu/internal/recovery"
	"github.com/jymchng/ccresu/internal/storage"
)

// BuildInfo carries version metadata injected at build time.
type BuildInfo struct {
	Version   string
	Commit    string
	BuildDate string
}

// Process exit codes. They are part of the CLI contract: a script can branch on
// "not found" without parsing English.
const (
	// ExitOK means the command did what it was asked.
	ExitOK = 0
	// ExitFailure means the command ran and failed.
	ExitFailure = 1
	// ExitUsage means the arguments were wrong, so nothing was attempted.
	ExitUsage = 2
	// ExitNotFound means the named session (or configuration file) does not exist.
	ExitNotFound = 3
	// ExitNotEligible means the session exists but its state forbids the
	// operation, e.g. recovery of a disabled or settled session.
	ExitNotEligible = 4
	// ExitInterrupted means the command was stopped by a signal.
	ExitInterrupted = 130
)

// ErrUnknownCommand reports a command name ccresu does not have.
var ErrUnknownCommand = errors.New("unknown command")

// exitError carries an explicit exit code alongside an error.
type exitError struct {
	code int
	err  error
}

// Error implements error.
func (e *exitError) Error() string { return e.err.Error() }

// Unwrap exposes the cause so errors.Is and errors.As still work.
func (e *exitError) Unwrap() error { return e.err }

// withCode attaches an exit code to err.
func withCode(code int, err error) error {
	if err == nil {
		return nil
	}
	return &exitError{code: code, err: err}
}

// usagef builds a usage error, which exits with ExitUsage.
func usagef(format string, args ...any) error {
	return &exitError{code: ExitUsage, err: fmt.Errorf(format, args...)}
}

// Deps is the injectable process environment.
//
// Every field is optional. A zero Deps is the real program: stdout and stderr go
// to the process streams, the configuration is read from disk, and the runtime is
// built from the real packages.
type Deps struct {
	// Stdout and Stderr receive command output and diagnostics.
	Stdout io.Writer
	Stderr io.Writer
	// LookupEnv reads environment overrides for the configuration. Nil means
	// os.LookupEnv.
	LookupEnv func(string) (string, bool)
	// Clock is the time source for every command. Nil means the system clock.
	Clock clock.Clock
	// Signals delivers process signals. Nil means a channel fed by os/signal
	// for SIGINT and SIGTERM, which is what lets daemon and serve stop cleanly.
	Signals <-chan os.Signal
	// LoadConfig reads and validates the configuration. Nil means
	// config.LoadWithEnv.
	LoadConfig func(path string, lookup func(string) (string, bool)) (*config.Config, error)
	// BuildRuntime assembles the long-lived components. Nil means BuildRuntime.
	BuildRuntime func(ctx context.Context, cfg *config.Config, clk clock.Clock, log *slog.Logger) (*Runtime, error)
	// NewServer builds the HTTP API for `serve`. Nil means internal/server's
	// implementation is used.
	NewServer func(*App, *Runtime) (Server, error)
	// DefaultConfigPath overrides where the configuration is looked for. Nil
	// means config.DefaultPath.
	DefaultConfigPath func() (string, error)
}

// App is one CLI invocation: parsed global flags plus the injected environment.
type App struct {
	deps   Deps
	build  BuildInfo
	stdout io.Writer
	stderr io.Writer
	clock  clock.Clock

	configPath string
	// serveAddr is the --addr override for `serve`; empty means the configured
	// server.host:server.port.
	serveAddr string
	helpShown bool
	json      bool
	logLevel  string
	logFormat string
}

// Run executes the CLI with the given arguments and returns a process exit code.
func Run(args []string, build BuildInfo, stdout, stderr io.Writer) int {
	return RunWith(Deps{}, args, build, stdout, stderr)
}

// RunWith executes the CLI against an injected environment.
//
// It never panics and never calls os.Exit: the caller owns the process.
func RunWith(deps Deps, args []string, build BuildInfo, stdout, stderr io.Writer) int {
	app := newApp(deps, build, stdout, stderr)
	return app.Execute(args)
}

// newApp fills the dependency defaults and returns a ready App.
func newApp(deps Deps, build BuildInfo, stdout, stderr io.Writer) *App {
	if deps.Stdout != nil {
		stdout = deps.Stdout
	}
	if deps.Stderr != nil {
		stderr = deps.Stderr
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if deps.LookupEnv == nil {
		deps.LookupEnv = os.LookupEnv
	}
	if deps.Clock == nil {
		deps.Clock = clock.System()
	}
	if deps.LoadConfig == nil {
		deps.LoadConfig = config.LoadWithEnv
	}
	if deps.BuildRuntime == nil {
		deps.BuildRuntime = BuildRuntime
	}
	if deps.NewServer == nil {
		deps.NewServer = newAPIServer
	}
	if deps.DefaultConfigPath == nil {
		deps.DefaultConfigPath = config.DefaultPath
	}
	if build.Version == "" {
		build.Version = "dev"
	}
	if build.Commit == "" {
		build.Commit = "none"
	}
	if build.BuildDate == "" {
		build.BuildDate = "unknown"
	}
	return &App{
		deps:   deps,
		build:  build,
		stdout: stdout,
		stderr: stderr,
		clock:  deps.Clock,
	}
}

// Execute runs one command and returns the process exit code.
//
// The error, when there is one, is printed once, to stderr, in the form
// "ccresu: <message>", which is what makes the exit code interpretable by a
// human as well as by a script.
func (a *App) Execute(args []string) int {
	err := a.execute(args)
	if err == nil {
		return ExitOK
	}
	if errors.Is(err, flag.ErrHelp) {
		return ExitOK
	}
	code := exitCode(err)
	fmt.Fprintf(a.stderr, "ccresu: %v\n", err)
	if code == ExitUsage {
		fmt.Fprintln(a.stderr, "Run 'ccresu --help' for usage.")
	}
	return code
}

// exitCode maps an error onto the documented exit codes.
func exitCode(err error) int {
	var explicit *exitError
	if errors.As(err, &explicit) {
		return explicit.code
	}
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, context.Canceled):
		return ExitInterrupted
	case errors.Is(err, storage.ErrNotFound):
		return ExitNotFound
	case errors.Is(err, recovery.ErrNotEligible),
		errors.Is(err, recovery.ErrAlreadyRunning),
		errors.Is(err, recovery.ErrConcurrencyLimit),
		errors.Is(err, recovery.ErrMaxAttempts),
		errors.Is(err, recovery.ErrDisabled),
		errors.Is(err, recovery.ErrUnrecoverable),
		errors.Is(err, recovery.ErrNoSession):
		return ExitNotEligible
	default:
		return ExitFailure
	}
}

// execute parses the global flags, dispatches the command and releases the
// runtime.
func (a *App) execute(args []string) error {
	rest, err := a.parseGlobal(args)
	if err != nil {
		return err
	}
	if a.helpShown {
		// --help already printed the page; asking for it is not a usage error.
		return nil
	}
	if len(rest) == 0 {
		a.printHelp(a.stdout)
		return usagef("no command given")
	}

	name := rest[0]
	commandArgs := rest[1:]
	if isHelpWord(name) {
		a.printHelp(a.stdout)
		return nil
	}
	if isVersionWord(name) && name != "version" {
		return a.cmdVersion(context.Background(), nil)
	}

	for _, spec := range a.commands() {
		if spec.name != name {
			continue
		}
		ctx, release := a.signalContext()
		defer release()
		return spec.run(a, ctx, commandArgs)
	}
	return usagef("%w %q", ErrUnknownCommand, name)
}

// parseGlobal consumes the flags that apply to every command and returns the
// remaining arguments.
//
// Only the flags written *before* the command name are consumed here. The same
// flags are also registered on every command's own FlagSet (see
// registerGlobalFlags), so `ccresu --json status` and `ccresu status --json` are
// both accepted and neither is a special case the caller has to remember.
func (a *App) parseGlobal(args []string) ([]string, error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			return args[i+1:], nil
		case arg == "-h" || arg == "--help":
			a.printHelp(a.stdout)
			a.helpShown = true
			return nil, nil
		case arg == "--json":
			a.json = true
		case arg == "--verbose" || arg == "-v":
			a.logLevel = "debug"
		case arg == "--quiet" || arg == "-q":
			a.logLevel = "error"
		case arg == "-c" || arg == "--config":
			i++
			if i >= len(args) {
				return nil, usagef("%s needs a path", arg)
			}
			a.configPath = args[i]
		case strings.HasPrefix(arg, "--config="):
			a.configPath = strings.TrimPrefix(arg, "--config=")
			if a.configPath == "" {
				return nil, usagef("--config needs a path")
			}
		case arg == "--log-level":
			i++
			if i >= len(args) {
				return nil, usagef("--log-level needs a value")
			}
			a.logLevel = args[i]
		case strings.HasPrefix(arg, "--log-level="):
			a.logLevel = strings.TrimPrefix(arg, "--log-level=")
		case arg == "--log-format":
			i++
			if i >= len(args) {
				return nil, usagef("--log-format needs a value")
			}
			a.logFormat = args[i]
		case strings.HasPrefix(arg, "--log-format="):
			a.logFormat = strings.TrimPrefix(arg, "--log-format=")
		case arg == "--version" || arg == "-V":
			// Handled by the caller: it is a command, not a flag with a value.
			return []string{"version"}, nil
		case strings.HasPrefix(arg, "-"):
			return nil, usagef("unknown flag %q", arg)
		default:
			return args[i:], nil
		}
	}
	return nil, nil
}

// signalContext returns a context cancelled by SIGINT/SIGTERM, and the function
// that releases the handler. Cancelling always stops the bridging goroutine, so a
// short-lived command leaves nothing behind.
func (a *App) signalContext() (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	signals := a.deps.Signals
	stop := func() {}
	if signals == nil {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		signals = ch
		stop = func() { signal.Stop(ch) }
	}
	go func() {
		select {
		case <-signals:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, func() {
		stop()
		cancel()
	}
}

// isHelpWord reports whether name asks for the help page.
func isHelpWord(name string) bool {
	switch name {
	case "help", "-h", "--help":
		return true
	default:
		return false
	}
}

// isVersionWord reports whether name asks for the version alone.
func isVersionWord(name string) bool {
	switch name {
	case "-V", "--version":
		return true
	default:
		return false
	}
}

// commandSpec is one row of the command table.
type commandSpec struct {
	name    string
	summary string
	usage   string
	run     func(a *App, ctx context.Context, args []string) error
}

// commands is the full, ordered command surface.
func (a *App) commands() []commandSpec {
	return []commandSpec{
		{
			name:    "daemon",
			summary: "run the watchdog loop until interrupted",
			usage:   "daemon [--once] [--interval DURATION]",
			run:     (*App).cmdDaemon,
		},
		{
			name:    "status",
			summary: "summarise session, schedule and notification state",
			usage:   "status",
			run:     (*App).cmdStatus,
		},
		{
			name:    "sessions",
			summary: "list discovered sessions",
			usage:   "sessions [--all] [--state STATE]... [--limit N]",
			run:     (*App).cmdSessions,
		},
		{
			name:    "inspect",
			summary: "show everything known about one session",
			usage:   "inspect <session-id>",
			run:     (*App).cmdInspect,
		},
		{
			name:    "recover",
			summary: "resume one session now",
			usage:   "recover <session-id> [--message TEXT] [--force]",
			run:     (*App).cmdRecover,
		},
		{
			name:    "retry",
			summary: "retry a session whose attempt budget is spent",
			usage:   "retry <session-id>",
			run:     (*App).cmdRetry,
		},
		{
			name:    "enable",
			summary: "allow ccresu to recover a session",
			usage:   "enable <session-id>",
			run:     (*App).cmdEnable,
		},
		{
			name:    "disable",
			summary: "stop ccresu from recovering a session",
			usage:   "disable <session-id>",
			run:     (*App).cmdDisable,
		},
		{
			name:    "history",
			summary: "show the attempts and state transitions of a session",
			usage:   "history <session-id> [--limit N]",
			run:     (*App).cmdHistory,
		},
		{
			name:    "config",
			summary: "validate (or print) the configuration",
			usage:   "config validate [PATH] | config show",
			run:     (*App).cmdConfig,
		},
		{
			name:    "doctor",
			summary: "check the environment ccresu runs in",
			usage:   "doctor",
			run:     (*App).cmdDoctor,
		},
		{
			name:    "serve",
			summary: "run the local REST/SSE API",
			usage:   "serve [--addr HOST:PORT]",
			run:     (*App).cmdServe,
		},
		{
			name:    "version",
			summary: "print version and build metadata",
			usage:   "version",
			run:     (*App).cmdVersion,
		},
	}
}

// printHelp writes the command index.
func (a *App) printHelp(w io.Writer) {
	fmt.Fprintf(w, "ccresu %s — resume Claude Code sessions after a usage limit resets\n\n", a.build.Version)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  ccresu [--config PATH] [--json] [--log-level LEVEL] <command> [flags]")
	fmt.Fprintln(w, "  ccresu --help | --version")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Commands:")

	specs := a.commands()
	sort.SliceStable(specs, func(i, j int) bool { return specs[i].name < specs[j].name })
	width := 0
	for _, spec := range specs {
		if len(spec.name) > width {
			width = len(spec.name)
		}
	}
	for _, spec := range specs {
		fmt.Fprintf(w, "  %-*s  %s\n", width, spec.name, spec.summary)
	}

	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Global flags:")
	fmt.Fprintln(w, "  -c, --config PATH      configuration file (default: ~/.config/ccresu/config.yaml)")
	fmt.Fprintln(w, "      --json             emit machine-readable JSON where the command supports it")
	fmt.Fprintln(w, "      --log-level LEVEL  debug, info, warn or error")
	fmt.Fprintln(w, "      --log-format FMT   json or text")
	fmt.Fprintln(w, "  -v, --verbose          shorthand for --log-level debug")
	fmt.Fprintln(w, "  -q, --quiet            shorthand for --log-level error")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Exit codes: 0 ok, 1 failure, 2 usage, 3 not found, 4 not eligible, 130 interrupted.")
	fmt.Fprintln(w, "Run 'ccresu <command> --help' for the flags of one command.")
}

// flags builds a FlagSet for one command, wired to stderr.
//
// The global flags are registered on every command's set as well as being
// consumed before the command name. Registering them here is what makes a global
// flag work in either position, and going through the command's own set is what
// keeps the split honest: only the set knows which token is a value, so
// `ccresu recover <id> --message --json` still sends the literal text "--json"
// instead of swallowing it.
func (a *App) flags(name, usage string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	fs.Usage = func() {
		fmt.Fprintf(a.stderr, "Usage: ccresu %s\n", usage)
		fs.PrintDefaults()
	}
	a.registerGlobalFlags(fs)
	return fs
}

// registerGlobalFlags adds the documented global flags to one command's set.
//
// The defaults are the values already parsed before the command name, so writing
// the flag twice (before and after the command) is the same as writing it once
// with the later value, and omitting it keeps what the earlier position set.
func (a *App) registerGlobalFlags(fs *flag.FlagSet) {
	fs.StringVar(&a.configPath, "config", a.configPath,
		"configuration file (default: ~/.config/ccresu/config.yaml)")
	fs.StringVar(&a.configPath, "c", a.configPath, "shorthand for --config")
	fs.BoolVar(&a.json, "json", a.json, "emit machine-readable JSON where the command supports it")
	fs.StringVar(&a.logLevel, "log-level", a.logLevel, "debug, info, warn or error")
	fs.StringVar(&a.logFormat, "log-format", a.logFormat, "json or text")
	fs.Var(&logLevelShorthand{target: &a.logLevel, level: "debug"}, "verbose",
		"shorthand for --log-level debug")
	fs.Var(&logLevelShorthand{target: &a.logLevel, level: "debug"}, "v",
		"shorthand for --log-level debug")
	fs.Var(&logLevelShorthand{target: &a.logLevel, level: "error"}, "quiet",
		"shorthand for --log-level error")
	fs.Var(&logLevelShorthand{target: &a.logLevel, level: "error"}, "q",
		"shorthand for --log-level error")
}

// logLevelShorthand is a flag.Value that assigns a log level when it is parsed.
//
// It assigns immediately rather than after the fact so that `-v -q` and `-q -v`
// resolve in the order they were written, exactly as they do when they appear
// before the command name.
type logLevelShorthand struct {
	target *string
	level  string
}

// String implements flag.Value: an empty default is what hides it from
// PrintDefaults, keeping the command pages readable.
func (v *logLevelShorthand) String() string { return "" }

// IsBoolFlag implements flag.boolFlag, so the shorthand needs no value.
func (v *logLevelShorthand) IsBoolFlag() bool { return true }

// Set implements flag.Value.
func (v *logLevelShorthand) Set(value string) error {
	on, err := strconv.ParseBool(value)
	if err != nil {
		return err
	}
	if on {
		*v.target = v.level
	}
	return nil
}

// parseFlags parses args for one command and turns a parse failure into a usage
// error, so a typo exits 2 instead of 1.
//
// Flags and positionals may be interleaved: `ccresu recover <id> --json` has to
// work as well as `ccresu recover --json <id>`. The standard library stops at the
// first positional, so the parse is resumed after each one and the positionals
// are handed back to the FlagSet at the end, behind a " -- " terminator, so a
// value that begins with '-' keeps its meaning instead of being read as a flag.
func (a *App) parseFlags(fs *flag.FlagSet, args []string) error {
	positional := make([]string, 0, len(args))
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return err
			}
			return withCode(ExitUsage, err)
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
	if len(positional) == 0 {
		return nil
	}
	if err := fs.Parse(append([]string{"--"}, positional...)); err != nil {
		return withCode(ExitUsage, err)
	}
	return nil
}

// sessionArg returns the single positional session id of a command.
func (a *App) sessionArg(fs *flag.FlagSet, command string) (string, error) {
	switch rest := fs.Args(); len(rest) {
	case 1:
		id := strings.TrimSpace(rest[0])
		if id == "" {
			return "", usagef("%s needs a session id", command)
		}
		return id, nil
	case 0:
		return "", usagef("%s needs a session id", command)
	default:
		return "", usagef("%s takes one session id, got %d arguments", command, len(rest))
	}
}

// loadConfig resolves and loads the configuration through the injected loader.
func (a *App) loadConfig() (*config.Config, error) {
	path, err := a.resolveConfigPath(a.configPath)
	if err != nil {
		return nil, err
	}
	cfg, err := a.deps.LoadConfig(path, a.deps.LookupEnv)
	if err != nil {
		return nil, fmt.Errorf("load configuration %s: %w", path, err)
	}
	if cfg == nil {
		return nil, fmt.Errorf("load configuration %s: empty document", path)
	}
	return cfg, nil
}

// resolveConfigPath expands the explicit path or falls back to the default one.
func (a *App) resolveConfigPath(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		path, err := config.ResolvePath(explicit)
		if err != nil {
			return "", withCode(ExitUsage, fmt.Errorf("configuration path %q: %w", explicit, err))
		}
		return path, nil
	}
	path, err := a.deps.DefaultConfigPath()
	if err != nil {
		return "", fmt.Errorf("locate the configuration file: %w", err)
	}
	return path, nil
}

// buildLogger creates the structured logger and the redactor the commands share.
func (a *App) buildLogger(cfg *config.Config) (*slog.Logger, *logging.Redactor, error) {
	level := a.logLevel
	format := a.logFormat
	if cfg != nil {
		if level == "" {
			level = cfg.App.LogLevel
		}
		if format == "" {
			format = cfg.App.LogFormat
		}
	}
	redactor := logging.NewRedactor()
	if cfg != nil {
		redactor.Add(cfg.Telegram.BotToken)
	}
	logger, err := logging.New(logging.Options{
		Level:    level,
		Format:   format,
		Output:   a.stderr,
		Redactor: redactor,
		TimeSource: func() time.Time {
			return a.clock.Now().UTC()
		},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("build logger: %w", err)
	}
	return logger, redactor, nil
}

// openRuntime loads the configuration and builds the runtime, releasing it when
// the command finishes.
func (a *App) openRuntime(ctx context.Context) (*Runtime, func(), error) {
	cfg, err := a.loadConfig()
	if err != nil {
		return nil, nil, err
	}
	logger, _, err := a.buildLogger(cfg)
	if err != nil {
		return nil, nil, err
	}
	runtime, err := a.deps.BuildRuntime(ctx, cfg, a.clock, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("open ccresu state: %w", err)
	}
	if runtime == nil {
		return nil, nil, errors.New("open ccresu state: no runtime")
	}
	release := func() {
		if err := runtime.Close(); err != nil {
			fmt.Fprintf(a.stderr, "ccresu: closing state: %v\n", err)
		}
	}
	return runtime, release, nil
}
