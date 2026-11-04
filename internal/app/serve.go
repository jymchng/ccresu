package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// cmdServe runs the local REST/SSE API until interrupted.
//
// The server itself lives in internal/server and is injected through
// Deps.NewServer, so the CLI owns the lifecycle — signals, context propagation and
// graceful shutdown — while the API surface stays in its own package. The binary
// ships no UI: the API is the programmatic interface.
func (a *App) cmdServe(ctx context.Context, args []string) error {
	fs := a.flags("serve", "serve [--addr HOST:PORT]")
	addr := fs.String("addr", "", "address to bind (default: server.host:server.port from the configuration)")
	if err := a.parseFlags(fs, args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return usagef("serve takes no arguments, got %q", rest[0])
	}

	if a.deps.NewServer == nil {
		return withCode(ExitFailure, ErrNoServer)
	}
	rt, release, err := a.openRuntime(ctx)
	if err != nil {
		return err
	}
	defer release()

	if rt.Config != nil && !rt.Config.Server.Enabled {
		return withCode(ExitFailure, errors.New("the server is disabled in the configuration (server.enabled)"))
	}

	// The override has to be visible to NewServer, which owns the listener.
	a.serveAddr = strings.TrimSpace(*addr)

	server, err := a.deps.NewServer(a, rt)
	if err != nil {
		return fmt.Errorf("start the API server: %w", err)
	}
	if server == nil {
		return withCode(ExitFailure, ErrNoServer)
	}

	bind := a.serveAddr
	if bind == "" && rt.Config != nil {
		bind = rt.Config.Server.Addr()
	}
	fmt.Fprintf(a.stdout, "ccresu API listening on %s\n", bind)

	if err := server.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("API server: %w", err)
	}
	fmt.Fprintln(a.stdout, "ccresu API stopped")
	return nil
}
