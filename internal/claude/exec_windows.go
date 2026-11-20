//go:build windows

package claude

import (
	"os/exec"
	"syscall"
)

// createNewProcessGroup is CREATE_NEW_PROCESS_GROUP. It is spelled as a literal
// because syscall does not export it and x/sys is not a dependency of this
// package.
const createNewProcessGroup = 0x00000200

// detach puts the child in its own process group.
//
// A console control event (Ctrl-C, Ctrl-Break, a service stop) is delivered to
// the process group, so a group of its own keeps a resume out of the daemon's
// signal path. There is no session concept to set: on Windows the console is
// the terminal, and the child has none of its own.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}
