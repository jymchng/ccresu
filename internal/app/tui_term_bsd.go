//go:build darwin || freebsd || netbsd || openbsd

package app

import (
	"os"
	"syscall"
	"unsafe"
)

// enterRawMode puts the terminal attached to f into the input mode a keystroke
// browser needs: one byte per key, no echo, and no line buffering.
//
// See tui_term_linux.go for why ISIG is cleared and OPOST is not. The BSDs name
// the termios ioctl requests TIOCGETA/TIOCSETA rather than Linux's
// TCGETS/TCSETS; the structure and the flags are the same.
func enterRawMode(f *os.File) (func(), error) {
	fd := f.Fd()
	var saved syscall.Termios
	if err := ioctlTermios(fd, syscall.TIOCGETA, &saved); err != nil {
		return nil, err
	}
	raw := saved
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	raw.Iflag &^= syscall.IXON | syscall.ICRNL
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if err := ioctlTermios(fd, syscall.TIOCSETA, &raw); err != nil {
		return nil, err
	}
	return func() { _ = ioctlTermios(fd, syscall.TIOCSETA, &saved) }, nil
}

// ioctlTermios reads or replaces the termios structure of one file descriptor.
func ioctlTermios(fd uintptr, request uintptr, termios *syscall.Termios) error {
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, fd, request,
		uintptr(unsafe.Pointer(termios)), 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
