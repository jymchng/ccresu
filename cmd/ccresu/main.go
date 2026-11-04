// Command ccresu is the ccresu watchdog, CLI and local API server.
//
// The command surface is wired up in internal/app; this entry point only owns
// process-level concerns: exit codes and version metadata injection.
package main

import (
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

func main() {
	os.Exit(app.Run(os.Args[1:], app.BuildInfo{
		Version:   version,
		Commit:    commit,
		BuildDate: buildDate,
	}, os.Stdout, os.Stderr))
}
