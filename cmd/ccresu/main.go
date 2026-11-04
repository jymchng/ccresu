// Command ccresu is the ccresu watchdog, CLI and local API server.
//
// The command surface is wired up in internal/app; this entry point only owns
// process-level concerns: exit codes and version metadata injection.
package main

import (
	"io"
	"os"

	"github.com/jymchng/ccresu/internal/app"
)

// Injected at build time with:
//
//	-ldflags "-X main.version=... -X main.commit=... -X main.buildDate=..."
var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

// osExit is os.Exit. It is a variable so the test below can exercise main
// itself: the process-exiting call is the one thing a test cannot survive, and
// everything else about the wiring is worth covering.
var osExit = os.Exit

// main runs the CLI and exits with the code app.Run computed.
func main() {
	osExit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the whole program minus the exit: it maps the argument vector and the
// process streams onto app.Run and returns the documented exit code.
func run(args []string, stdout, stderr io.Writer) int {
	return app.Run(args, app.BuildInfo{
		Version:   version,
		Commit:    commit,
		BuildDate: buildDate,
	}, stdout, stderr)
}
