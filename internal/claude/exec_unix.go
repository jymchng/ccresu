//go:build !windows

package claude

import (
	"os/exec"
	"syscall"
)

// detach puts the child in its own session and process group.
//
// Setsid is the whole of it: the child gets a new session with no controlling
// terminal, so a Ctrl-C or a SIGHUP delivered to the daemon's process group —
// a systemd stop, a closed terminal, a shell job control signal — does not reach
// a resume that is already running. The child is still ccresu's child and is
// still waited for; only its signal ancestry is broken.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
