//go:build !unix

package app

import "os"

// browserSignals reports no signal the browser can intercept.
//
// The browser still restores the terminal on every exit path it controls; on a
// platform with no SIGHUP there is simply no extra way to be asked to stop.
func browserSignals() []os.Signal { return nil }
