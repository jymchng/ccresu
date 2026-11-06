//go:build linux

package app

import (
	"os"
	"syscall"
	"unsafe"
)

// enterRawMode puts the terminal attached to f into the input mode a keystroke
// browser needs: one byte per key, no echo, and no line buffering.
//
// ISIG is cleared on purpose. With signals enabled, Ctrl-C is a SIGINT that kills
// the process in the middle of a read; with them disabled it arrives as the byte
// 0x03, which the key decoder maps to "quit" so the browser can restore the
// screen and leave cleanly. Output post-processing (OPOST) is left alone, so a
// plain "\n" still returns the carriage and the drawing code stays simple.
func enterRawMode(f *os.File) (func(), error) {
	fd := f.Fd()
	var saved syscall.Termios
	if err := ioctlTermios(fd, syscall.TCGETS, &saved); err != nil {
		return nil, err
	}
	raw := saved
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	raw.Iflag &^= syscall.IXON | syscall.ICRNL
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if err := ioctlTermios(fd, syscall.TCSETS, &raw); err != nil {
		return nil, err
	}
	return func() { _ = ioctlTermios(fd, syscall.TCSETS, &saved) }, nil
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
