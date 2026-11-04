package app

import "io"

// BuildInfo carries version metadata injected at build time.
type BuildInfo struct {
	Version   string
	Commit    string
	BuildDate string
}

// Run executes the CLI with the given arguments and returns a process exit code.
//
// The command surface is implemented in goal #9; until then Run only reports
// build information so the scaffold compiles and is vet-clean.
func Run(args []string, build BuildInfo, stdout, stderr io.Writer) int {
	_ = args
	_ = stdout
	_ = stderr
	_ = build
	return 0
}
