package claude

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrWorkspaceUnresolved reports that neither the recorded run directory nor the
// project directory is a directory ccresu can run in.
//
// It exists because the alternative is an empty `exec.Cmd.Dir`, which silently
// inherits the daemon's working directory — under the shipped systemd unit, the
// user's home. A recovery that ran in `$HOME` reported success anyway (the CLI's
// ID search finds the session from anywhere since v2.1.223), so the wrong
// directory was invisible in an exit code. An empty resolution is therefore a
// named failure rather than a fallback to inheritance.
var ErrWorkspaceUnresolved = errors.New("claude: no usable working directory for the session")

// Workspace is the directory a recovery must run in, and how it was decided.
type Workspace struct {
	// Dir is the absolute directory handed to `exec.Cmd.Dir`.
	Dir string
	// FellBack reports that the recorded run directory was recorded but is no
	// longer a directory, so the project directory answered instead (R8).
	FellBack bool
	// Missing is the recorded run directory that could not be used, set only
	// when FellBack is true. It is carried so the caller can name the path it
	// skipped instead of logging a vague fallback.
	Missing string
}

// ResolveWorkspace answers the question this PRD exists for: which directory does
// this session occupy?
//
// The order is a rule, not a preference (R3):
//
//  1. the recorded run directory — a background session lives in its worktree,
//     not in the checkout it was dispatched from;
//  2. the project directory — where the session started, and the documented
//     fallback when a worktree has been cleared.
//
// There is deliberately no parameter that disables either step (R11): a
// `respect_worktree: false` knob would be a switch whose only effect is to
// reintroduce the bug this function removes.
//
// A recorded run directory that no longer exists is not an error (R8): it falls
// back to the project directory and says so through Workspace.FellBack, because a
// deleted worktree is a fact to observe rather than a reason to abandon the
// session. Only when neither directory is usable does resolution fail, and then
// with ErrWorkspaceUnresolved rather than an inherited cwd.
func ResolveWorkspace(runsInPath, projectPath string) (Workspace, error) {
	run := strings.TrimSpace(runsInPath)
	project := strings.TrimSpace(projectPath)
	projectUsable := project != "" && isDirectory(project)

	if run != "" {
		if isDirectory(run) {
			return Workspace{Dir: run}, nil
		}
		if projectUsable {
			return Workspace{Dir: project, FellBack: true, Missing: run}, nil
		}
	}
	if projectUsable {
		return Workspace{Dir: project}, nil
	}
	return Workspace{}, fmt.Errorf("%w: recorded run directory %q, project directory %q",
		ErrWorkspaceUnresolved, run, project)
}

// isDirectory reports whether path names an existing directory.
//
// The check is `os.Stat` rather than a git query on purpose: R6 forbids ccresu
// from running `git worktree` at all, and whether a path is a directory is all
// the resolver needs to know.
func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
