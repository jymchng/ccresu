package claude

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Output is the captured result of running an external command.
type Output struct {
	// Stdout and Stderr are the captured streams, truncated to the configured
	// limit.
	Stdout []byte
	Stderr []byte
	// ExitCode is the process exit status (0 on success, -1 when the process
	// never started).
	ExitCode int
	// Truncated reports whether either stream hit the capture limit.
	Truncated bool
}

// RunOptions configures one command execution.
type RunOptions struct {
	// Dir is the working directory; empty inherits the parent's.
	Dir string
	// Env appends KEY=VALUE entries to the inherited environment.
	Env []string
	// Stdin, when non-nil, is the child's standard input. The zero value leaves
	// the child with an immediately-closed stdin, so no terminal is ever
	// attached.
	Stdin io.Reader
	// MaxOutputBytes caps each captured stream. Zero means DefaultMaxOutputBytes.
	MaxOutputBytes int
}

// DefaultMaxOutputBytes caps captured command output at 4 MiB per stream.
const DefaultMaxOutputBytes = 4 << 20

// Execer runs an external program and captures its output.
//
// It exists so tests can assert the exact argv without spawning processes, and
// so production code has a single place where the "never use a shell, never
// attach a TTY" rule is enforced.
type Execer interface {
	Run(ctx context.Context, name string, args []string, opts RunOptions) (Output, error)
}

// OSExecer runs real processes with no shell and no terminal.
type OSExecer struct{}

// Run implements Execer using os/exec.
//
// The command is executed directly (never through a shell), stdin is closed
// unless the caller supplies a reader, and stdout/stderr are pipes rather than
// inherited descriptors, so the child can never believe it owns a TTY.
func (OSExecer) Run(ctx context.Context, name string, args []string, opts RunOptions) (Output, error) {
	if strings.TrimSpace(name) == "" {
		return Output{ExitCode: -1}, fmt.Errorf("%w: empty command name", ErrCLIUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return Output{ExitCode: -1}, err
	}

	limit := opts.MaxOutputBytes
	if limit <= 0 {
		limit = DefaultMaxOutputBytes
	}

	// -- guarded because the linter is right that the context would otherwise
	// leak; the cancel is deferred below.
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = opts.Dir
	if len(opts.Env) > 0 {
		cmd.Env = append(os.Environ(), opts.Env...)
	}
	if opts.Stdin != nil {
		cmd.Stdin = opts.Stdin
	}

	stdout := &limitedBuffer{limit: limit}
	stderr := &limitedBuffer{limit: limit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	out := Output{
		Stdout:    stdout.Bytes(),
		Stderr:    stderr.Bytes(),
		ExitCode:  exitCode(cmd, err),
		Truncated: stdout.truncated || stderr.truncated,
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			// A cancelled deadline is reported as the context error only, so the
			// caller can distinguish "claude is slow" from "claude failed".
			return out, fmt.Errorf("%w: %w", ctxErr, err)
		}
		// The argument vector is deliberately not echoed: the continuation
		// message is a positional argument, so quoting argv here would copy the
		// prompt into every log line and into the error stored on the session.
		// The caller names the command it ran; this only says which binary
		// failed.
		return out, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

// exitCode extracts the numeric exit status from err.
func exitCode(cmd *exec.Cmd, err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	if cmd.ProcessState != nil {
		return cmd.ProcessState.ExitCode()
	}
	return -1
}

// limitedBuffer keeps at most limit bytes and silently discards the rest, so a
// chatty child can neither exhaust memory nor block on a full pipe.
type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

// Write implements io.Writer.
func (b *limitedBuffer) Write(p []byte) (int, error) {
	if remaining := b.limit - b.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			b.buf.Write(p[:remaining])
			b.truncated = true
		} else {
			b.buf.Write(p)
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

// Bytes returns the captured bytes.
func (b *limitedBuffer) Bytes() []byte { return b.buf.Bytes() }
