//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd

package app

import (
	"errors"
	"os"
)

// errRawModeUnsupported is returned on platforms without the termios ioctl.
//
// The browser is not disabled there: it falls back to reading newline-terminated
// keys, which is what a terminal in canonical mode delivers anyway.
var errRawModeUnsupported = errors.New("raw terminal mode is not supported on this platform")

// enterRawMode reports that this platform has no termios ioctl to change.
func enterRawMode(*os.File) (func(), error) { return nil, errRawModeUnsupported }
