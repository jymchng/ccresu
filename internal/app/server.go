package app

import (
	"github.com/jymchng/ccresu/internal/server"
)

// newAPIServer builds the real HTTP API for `serve`.
//
// It lives in the app layer because it is the only place that knows both halves:
// the parsed CLI (for the build metadata and a --addr override) and the assembled
// Runtime. internal/server depends on none of that, so it stays testable on its
// own and cannot reach back into the command layer.
func newAPIServer(a *App, rt *Runtime) (Server, error) {
	if a == nil || rt == nil || rt.Config == nil || rt.Store == nil {
		return nil, ErrNoServer
	}
	addr := a.serveAddr
	if addr == "" {
		addr = rt.Config.Server.Addr()
	}
	cfg := server.Config{
		Addr:           addr,
		AllowedOrigins: rt.Config.Server.AllowedOrigins,
		TokenPath:      rt.Config.Server.TokenPath,
		Store:          rt.Store,
		Provider:       rt.Provider,
		Recovery:       rt.Recovery,
		Clock:          rt.Clock,
		Log:            rt.Log,
		Config:         rt.Config,
		ConfigPath:     rt.ConfigPath,
		Build: server.BuildInfo{
			Version:   a.build.Version,
			Commit:    a.build.Commit,
			BuildDate: a.build.BuildDate,
		},
	}
	// Both halves are optional capabilities, so they are attached by interface
	// assertion rather than by widening Runtime.
	if probe, ok := rt.Provider.(server.VersionProbe); ok {
		cfg.VersionProbe = probe
	}
	if rt.Notifier != nil {
		cfg.Notifier = rt.Notifier
	}
	return server.New(cfg)
}
