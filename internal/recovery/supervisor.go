package recovery

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jymchng/ccresu/internal/claude"
)

// ErrSupervisorReadFailed reports that `claude daemon status` failed for a reason
// other than "there is no supervisor".
//
// The distinction is the whole of R1: an exit code of 1 is the documented answer
// on a machine that has never run `claude --bg`, so it is a normal pass with no
// supervisor attached. Anything else — a timeout, a crash, a CLI without the
// `daemon` subcommand — is its own named condition, because the remedy is
// different and the operator has to be told which one happened.
var ErrSupervisorReadFailed = errors.New("recovery: the supervisor's status could not be read")

// SupervisorReader is the supervisor's read-only surface (PRD 3 R1, R2).
//
// It is two reads and a constant rather than one, because the version comparison
// needs the version of the executable ccresu invokes as well as the supervisor's:
// the mismatch is a fact about the pair, and a comparison made against a version
// read by somebody else could disagree with the CLI that will perform the
// handoff.
type SupervisorReader interface {
	// DaemonStatus runs `claude daemon status`.
	DaemonStatus(ctx context.Context) (claude.SupervisorStatus, error)
	// CLIVersion reports the version of the claude ccresu invokes.
	CLIVersion(ctx context.Context) (claude.Version, error)
	// MinimumVersion is the version floor this build supports.
	MinimumVersion() claude.Version
}

// SupervisorSource reads `claude daemon status` at most once per recovery pass.
//
// The supervisor's state, version and worker count describe the machine, not one
// session, so asking once per session would turn one bookkeeping command into N
// and could answer two sessions from two different supervisors — the exact
// confusion R9 exists to prevent. One read per pass keeps the answer a single
// fact.
//
// A Manager with no supervisor configured reads nothing and reports no
// supervisor, which is the same answer as a machine that has never started one
// (R1) rather than an error.
type SupervisorSource struct {
	// Reader supplies the reads. Nil means no supervisor capability: every pass
	// reports "no supervisor" and continues.
	Reader SupervisorReader

	mu     sync.Mutex
	status claude.SupervisorStatus
	read   bool
	err    error
}

// NewSupervisorSource builds a source over a reader.
func NewSupervisorSource(reader SupervisorReader) *SupervisorSource {
	return &SupervisorSource{Reader: reader}
}

// Reset ends the current pass, so the next Check reads the supervisor again.
func (s *SupervisorSource) Reset() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.read, s.err = claude.SupervisorStatus{}, false, nil
}

// Check returns the supervisor's status and, when the running supervisor differs
// from the CLI ccresu invokes, the named mismatch condition (R2).
//
// Three outcomes are deliberately distinct:
//
//   - No reader, or exit 1: no supervisor, nil error. The next `--bg` handoff
//     starts one, which is documented behaviour and not a failure.
//   - An unreadable status: ErrSupervisorReadFailed. It is reported with the
//     documented remedy by the caller and nothing is handed off, because a
//     handoff issued while the supervisor cannot answer could start a second one.
//   - A status whose version differs from the CLI's, or falls below the floor:
//     claude.SupervisorVersionMismatch, carrying all three versions and the
//     remedy. The caller runs nothing: the remedy stops the sessions the
//     supervisor hosts, so it is the operator's decision, not a retry step.
func (s *SupervisorSource) Check(ctx context.Context) (claude.SupervisorStatus, error) {
	status, err := s.readStatus(ctx)
	if err != nil {
		return status, err
	}
	if !status.Running || s.Reader == nil {
		return status, nil
	}
	version, versionErr := s.Reader.CLIVersion(ctx)
	if versionErr != nil {
		// The CLI's own version is unknown, so there is nothing to compare
		// against. That is a fact about a different problem — a CLI ccresu cannot
		// version at all — and inventing a mismatch from it would send the
		// operator to the remedy for the wrong reason.
		return status, nil
	}
	if mismatch := claude.CompareSupervisorVersion(status, version, s.Reader.MinimumVersion()); mismatch != nil {
		return status, mismatch
	}
	return status, nil
}

// readStatus performs the cached read.
func (s *SupervisorSource) readStatus(ctx context.Context) (claude.SupervisorStatus, error) {
	if s == nil || s.Reader == nil {
		return claude.SupervisorStatus{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.read {
		s.status, s.err = s.Reader.DaemonStatus(ctx)
		s.read = true
	}
	if s.err != nil {
		return s.status, fmt.Errorf("%w: %w", ErrSupervisorReadFailed, s.err)
	}
	return s.status, nil
}

// SupervisorSummary is the one-line description of a reading that both the log
// and `ccresu inspect` use, so the two cannot describe the same pass differently.
//
// It says "no supervisor" rather than printing empty fields, because an absent
// supervisor and a supervisor that answered with nothing parseable are different
// answers and only one of them is normal.
func SupervisorSummary(status claude.SupervisorStatus) string {
	if !status.Running {
		return "no supervisor"
	}
	parts := "supervisor running"
	if status.State != "" {
		parts += ", state " + status.State
	}
	if status.Version.Parsed {
		parts += ", version " + status.Version.String()
	} else if status.Version.Raw != "" {
		parts += ", version " + status.Version.Raw
	}
	if status.WorkersKnown {
		parts += fmt.Sprintf(", %d worker(s)", status.Workers)
	}
	return parts
}
