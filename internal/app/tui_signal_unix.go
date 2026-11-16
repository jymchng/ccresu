//go:build unix

package app

import (
	"os"
	"syscall"
)

// browserSignals are the signals that must put the terminal back before the
// process leaves.
//
// The browser's defers restore the alternate screen and the cursor on every exit
// path it controls, but a signal ends the process from outside that path: the
// loop has to return for the defers to run, and these are the signals that ask it
// to. SIGHUP is the one a closing terminal sends; SIGTERM is how a service
// manager asks; SIGINT reaches the process only from outside, because the browser
// clears ISIG and reads ctrl-c as a byte instead.
func browserSignals() []os.Signal {
	return []os.Signal{syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT}
}
