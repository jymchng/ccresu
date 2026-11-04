// Package app wires the ccresu daemon, CLI commands and API server together.
//
// It owns orchestration only: discovery, detection, scheduling, recovery,
// notification and HTTP concerns live in their own packages.
//
// The package is built for testing. Run is the only function that touches the
// process: every dependency a command needs is a field of Deps with a production
// default, so a test can drive the whole CLI — arguments, exit codes, stdout and
// stderr — against in-memory fakes and never open a real database, run a real
// command or send a real notification.
package app
