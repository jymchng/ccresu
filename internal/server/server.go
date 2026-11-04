// Package server exposes the local REST and SSE API. The binary ships no UI.
//
// The API is the programmatic interface the specification asks for: scripts and
// integrations drive ccresu through it, and the browser is only ever involved
// through the documented Origin allowlist. There is no embedded web UI and no
// endpoint accepts a command to run, so the surface cannot be turned into remote
// execution: every write endpoint takes a session id and at most a continuation
// message, and the only external process ever started is the configured
// `claude` binary through the provider that `internal/claude` owns.
//
// Security posture, in the order a request meets it:
//
//  1. the listener binds 127.0.0.1 by default, so the API is not reachable from
//     the network unless an operator deliberately changes `server.host`;
//  2. a bearer token, generated on first start and written 0600, is required for
//     every state-changing request; it is read from the Authorization header
//     only, never from a cookie, so a cross-site form post cannot carry it;
//  3. when a browser sends an Origin it must be on the configured allowlist,
//     which blocks cross-origin reads even though the token is not required for
//     them;
//  4. requests carry size limits, rate limits and JSON-only bodies, and every
//     response carries no-store and clickjacking/CSP hardening headers.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/clock"
	"github.com/jymchng/ccresu/internal/config"
	"github.com/jymchng/ccresu/internal/recovery"
	"github.com/jymchng/ccresu/internal/storage"
)

// APIPrefix prefixes every route. It is part of the public contract, so a client
// can pin a version instead of guessing.
const APIPrefix = "/api/v1"

// Server defaults. They mirror the shipped configuration, so a hand-built
// Config behaves like the program.
const (
	// DefaultAddr is the loopback address the API binds when none is configured.
	DefaultAddr = "127.0.0.1:8787"
	// DefaultMaxBodyBytes caps a request body at one mebibyte. Every request the
	// API accepts is a small JSON document, so this is generous.
	DefaultMaxBodyBytes = 1 << 20
	// DefaultRateLimit is the sustained requests per second allowed per client.
	DefaultRateLimit = 50
	// DefaultRateBurst is how many requests may arrive at once before the
	// sustained rate applies.
	DefaultRateBurst = 100
	// DefaultEventPollInterval is how often the transition feed is polled for
	// new state changes.
	DefaultEventPollInterval = 2 * time.Second
	// DefaultHeartbeat is how often an idle SSE stream emits a comment so a
	// proxy or a sleeping client does not treat it as dead.
	DefaultHeartbeat = 15 * time.Second
	// DefaultShutdownTimeout is how long a graceful shutdown may take.
	DefaultShutdownTimeout = 5 * time.Second
	// DefaultReadHeaderTimeout bounds how long a client may dawdle over headers.
	DefaultReadHeaderTimeout = 10 * time.Second
	// DefaultIdleTimeout closes keep-alive connections that go quiet. It must
	// stay above the SSE heartbeat so a live stream is never cut.
	DefaultIdleTimeout = 120 * time.Second
	// maxSSEBacklog is how many queued events a slow subscriber may accumulate
	// before messages are dropped for it rather than blocking the publisher.
	maxSSEBacklog = 32
	// eventBatchLimit caps how many transitions one poll may publish.
	eventBatchLimit = 200
)

// Errors reported by New.
var (
	// ErrInvalidConfig reports a configuration the server cannot run with.
	ErrInvalidConfig = errors.New("invalid server configuration")
	// ErrNoToken reports a token file that exists but holds nothing usable.
	ErrNoToken = errors.New("api token file is empty or too short")
)

// BuildInfo carries the version metadata injected at build time.
type BuildInfo struct {
	Version   string
	Commit    string
	BuildDate string
}

// Notifier is the notification queue, reduced to the read-only questions the API
// answers. *telegram.Notifier satisfies it.
type Notifier interface {
	// Enabled reports whether an event kind is configured for delivery.
	Enabled(storage.NotificationEvent) bool
	// MaxAttempts reports the delivery attempt budget.
	MaxAttempts() int
}

// VersionProbe reports the Claude Code version in use. *claude.CLI satisfies it.
type VersionProbe interface {
	// Version returns the probed version, cached by the implementation.
	Version(ctx context.Context) (claude.VersionReport, error)
}

// Config is everything the API serves from.
//
// Every dependency is an interface or a value a test can build in memory, so the
// handlers are exercised against a real in-memory SQLite store and fakes rather
// than a live installation.
type Config struct {
	// Addr is the host:port to bind. Empty means DefaultAddr. A bare ":port" is
	// promoted to 127.0.0.1:port so the API is never exposed by accident.
	Addr string
	// AllowedOrigins is the browser Origin allowlist. Entries are absolute
	// scheme://host[:port] values, compared case-insensitively.
	AllowedOrigins []string
	// TokenPath is the file the bearer token is read from, and created in when
	// it does not exist yet. Required.
	TokenPath string
	// MaxBodyBytes caps request bodies. Zero means DefaultMaxBodyBytes.
	MaxBodyBytes int64
	// RateLimit and RateBurst configure the per-client limiter. Zero means the
	// defaults.
	RateLimit float64
	RateBurst int
	// EventPollInterval and Heartbeat tune the SSE feed. Zero means defaults.
	EventPollInterval time.Duration
	Heartbeat         time.Duration
	// ShutdownTimeout bounds a graceful shutdown. Zero means the default.
	ShutdownTimeout time.Duration

	// Store is the persistence layer. Required.
	Store *storage.Store
	// Provider discovers and resumes sessions. Optional; recovery needs it.
	Provider claude.SessionProvider
	// VersionProbe answers /version. Optional.
	VersionProbe VersionProbe
	// Recovery is the attempt engine. Optional.
	Recovery *recovery.Manager
	// Notifier answers /telegram/status. Optional.
	Notifier Notifier
	// Clock is the time source. Nil means the system clock.
	Clock clock.Clock
	// Log receives structured diagnostics. Nil means slog.Default.
	Log *slog.Logger
	// Config is the loaded configuration, reported (redacted) by /config.
	Config *config.Config
	// ConfigPath is where that configuration came from.
	ConfigPath string
	// Build is the injected version metadata.
	Build BuildInfo
}

// Server is the local REST/SSE API.
//
// One Server owns its listener, its subscriber set and its feed goroutine; Run
// returns only after all three are finished, so a shutdown leaves no goroutine
// behind.
type Server struct {
	cfg     Config
	token   string
	started time.Time
	hub     *hub
	limiter *rateLimiter
	routes  []route

	mu   sync.Mutex
	addr string
	wg   sync.WaitGroup

	// now is the injectable clock, kept separately for readability in handlers.
	now func() time.Time
}

// New validates cfg, loads (or creates) the bearer token and returns a ready
// server. It performs no I/O beyond the token file and binds nothing, so a test
// can call Handler directly.
func New(cfg Config) (*Server, error) {
	if cfg.Store == nil {
		return nil, fmt.Errorf("%w: a store is required", ErrInvalidConfig)
	}
	addr, err := normalizeAddr(cfg.Addr)
	if err != nil {
		return nil, err
	}
	cfg.Addr = addr
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if cfg.RateLimit <= 0 {
		cfg.RateLimit = DefaultRateLimit
	}
	if cfg.RateBurst <= 0 {
		cfg.RateBurst = DefaultRateBurst
	}
	if cfg.EventPollInterval <= 0 {
		cfg.EventPollInterval = DefaultEventPollInterval
	}
	if cfg.Heartbeat <= 0 {
		cfg.Heartbeat = DefaultHeartbeat
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = DefaultShutdownTimeout
	}
	if cfg.Clock == nil {
		cfg.Clock = clock.System()
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	origins, err := normalizeOrigins(cfg.AllowedOrigins)
	if err != nil {
		return nil, err
	}
	cfg.AllowedOrigins = origins
	if strings.TrimSpace(cfg.TokenPath) == "" {
		return nil, fmt.Errorf("%w: server.token_path is required", ErrInvalidConfig)
	}
	token, err := ensureToken(cfg.TokenPath)
	if err != nil {
		return nil, err
	}

	s := &Server{
		cfg:     cfg,
		token:   token,
		started: cfg.Clock.Now().UTC(),
		hub:     newHub(),
		limiter: newRateLimiter(cfg.RateLimit, cfg.RateBurst),
		now:     func() time.Time { return cfg.Clock.Now().UTC() },
	}
	s.routes = s.buildRoutes()
	return s, nil
}

// normalizeAddr fills in the loopback host and validates the port.
func normalizeAddr(raw string) (string, error) {
	addr := strings.TrimSpace(raw)
	if addr == "" {
		return DefaultAddr, nil
	}
	// ":8787" listens on every interface; ccresu never means that.
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("%w: address %q: %v", ErrInvalidConfig, raw, err)
	}
	if strings.TrimSpace(port) == "" {
		return "", fmt.Errorf("%w: address %q has no port", ErrInvalidConfig, raw)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port), nil
}

// normalizeOrigins validates the allowlist, which config already checked, so a
// programmatically built Config fails here too instead of at request time.
func normalizeOrigins(raw []string) ([]string, error) {
	origins := make([]string, 0, len(raw))
	for _, entry := range raw {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}
		if trimmed == "*" {
			return nil, fmt.Errorf("%w: server.allowed_origins must not contain a wildcard", ErrInvalidConfig)
		}
		lower := strings.ToLower(trimmed)
		if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
			return nil, fmt.Errorf("%w: origin %q must be http or https", ErrInvalidConfig, entry)
		}
		if strings.Contains(strings.TrimPrefix(strings.TrimPrefix(lower, "https://"), "http://"), "/") {
			return nil, fmt.Errorf("%w: origin %q must not include a path", ErrInvalidConfig, entry)
		}
		origins = append(origins, lower)
	}
	return origins, nil
}

// Handler returns the middleware-wrapped handler. It is what Run serves, and
// what a test mounts on httptest.NewServer.
func (s *Server) Handler() http.Handler { return s }

// Addr reports the bound address. It is empty until Run has listened.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

// setAddr records the bound address for tests and logs.
func (s *Server) setAddr(addr string) {
	s.mu.Lock()
	s.addr = addr
	s.mu.Unlock()
}

// Run serves until ctx is cancelled and then shuts down gracefully.
//
// Cancelling ctx stops the event feed, wakes every SSE handler and shuts the HTTP
// server down within ShutdownTimeout; Run returns only after the listener, the
// feed goroutine and the subscribers are finished.
func (s *Server) Run(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", ErrInvalidConfig)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	listener, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.cfg.Addr, err)
	}
	s.setAddr(listener.Addr().String())

	s.startEvents(ctx)

	httpServer := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: DefaultReadHeaderTimeout,
		IdleTimeout:       DefaultIdleTimeout,
		ErrorLog:          slog.NewLogLogger(s.cfg.Log.Handler(), slog.LevelDebug),
	}

	served := make(chan error, 1)
	go func() { served <- httpServer.Serve(listener) }()

	select {
	case err := <-served:
		// A bind or accept failure: nothing to shut down gracefully.
		s.hub.close()
		cancel()
		s.wg.Wait()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	case <-ctx.Done():
	}

	// Ending the feed first lets every SSE handler return, so Shutdown is not
	// stuck waiting on a stream that is designed never to end.
	s.hub.close()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
	defer cancelShutdown()
	shutdownErr := httpServer.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		// A handler that ignored the deadline must not keep the port.
		_ = httpServer.Close()
	}
	if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	s.wg.Wait()
	if shutdownErr != nil {
		return fmt.Errorf("shut down: %w", shutdownErr)
	}
	return nil
}
