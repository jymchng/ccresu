package recovery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/clock"
	"github.com/jymchng/ccresu/internal/storage"
)

// DefaultVerificationTimeout bounds how long the verifier waits for the
// transcript to grow after a resume returns.
const DefaultVerificationTimeout = 120 * time.Second

// DefaultHealthCheckInterval is how often the verifier re-reads the session
// while waiting for the transcript to grow.
const DefaultHealthCheckInterval = 10 * time.Second

// Baseline is the state of a session before a resume, captured so the verifier
// can prove afterwards that the same conversation advanced.
type Baseline struct {
	// SessionID is the session that must still be the one addressed.
	SessionID string
	// TranscriptPath is the transcript ccresu expects to grow.
	TranscriptPath string
	// TranscriptSize and TranscriptLines snapshot the transcript before the
	// resume. A zero Lines means the snapshot was never taken, and growth then
	// cannot be proven — which is reported rather than assumed.
	TranscriptSize  int64
	TranscriptLines int64
}

// BaselineFromSession captures a baseline from a persisted session row.
func BaselineFromSession(session storage.Session) Baseline {
	return Baseline{
		SessionID:       session.ID,
		TranscriptPath:  session.TranscriptPath,
		TranscriptSize:  session.TranscriptSize,
		TranscriptLines: session.TranscriptLines,
	}
}

// BaselineFromStatus captures a baseline from a freshly checked status, which
// is the more accurate snapshot because it re-reads the transcript.
func BaselineFromStatus(status claude.SessionStatus) Baseline {
	id := status.Session.ID
	if id == "" {
		id = status.Transcript.SessionID
	}
	path := status.Transcript.Path
	if path == "" {
		path = status.Session.TranscriptPath
	}
	return Baseline{
		SessionID:       id,
		TranscriptPath:  path,
		TranscriptSize:  status.Transcript.Size,
		TranscriptLines: status.Transcript.Lines,
	}
}

// Verification is the evidence collected after a resume.
//
// Proven is the only field a caller may treat as success, and it means one of two
// things depending on how the continuation was made. For a print-mode resume it
// means both independent checks passed: the CLI reported the original session id
// back, and the transcript of that session actually grew. For a handoff it means
// the supervisor accepted the session and named it, because the work is no longer
// ccresu's to measure — PRD 1 retired print mode, and the fields below record
// which of the two shapes was proven.
type Verification struct {
	// SessionIDPreserved reports that the resumed session is the requested one.
	SessionIDPreserved bool
	// TranscriptGrew reports that the transcript gained content.
	TranscriptGrew bool
	// Proven reports SessionIDPreserved && TranscriptGrew, or HandoffAccepted
	// when the continuation was handed to the supervisor.
	Proven bool
	// HandoffAccepted reports that the CLI acknowledged the continuation with a
	// `backgrounded` block, which is what a handoff is proven by.
	HandoffAccepted bool
	// HandoffStateID is the short state ID from that block. It is not the
	// session UUID: `claude attach`, `claude logs` and `claude stop` take this.
	HandoffStateID string
	// HandoffName is the label the supervisor shows for the session.
	HandoffName string
	// Copied reports that the CLI continued the conversation in a copy rather
	// than in place, which is an expected outcome rather than a failure.
	Copied bool
	// LinesBefore/After and SizeBefore/After are the measurements the decision
	// rests on, kept for logs and for `ccresu inspect`.
	LinesBefore int64
	LinesAfter  int64
	SizeBefore  int64
	SizeAfter   int64
	// Running records that the session had a live process at verification time.
	// It is reported but never used as proof: a launched process is not a
	// resumed conversation.
	Running bool
	// Notes explains every conclusion, including why a check could not be made.
	Notes []string
}

// Missing returns the failed checks, for an error message.
func (v Verification) Missing() []string {
	if v.HandoffAccepted {
		// A handoff has no checks to miss: it is accepted or it never printed a
		// block, and the latter is an error rather than an unproven resume.
		return nil
	}
	var missing []string
	if !v.SessionIDPreserved {
		missing = append(missing, "the original session id was not confirmed")
	}
	if !v.TranscriptGrew {
		missing = append(missing, "the transcript did not grow")
	}
	return missing
}

// Reason renders why the resume was not proven.
func (v Verification) Reason() string {
	if v.Proven {
		if v.HandoffAccepted {
			return "the continuation was handed to the background supervisor"
		}
		return "the original session id was confirmed and its transcript grew"
	}
	missing := v.Missing()
	if len(missing) == 0 {
		return "the resume could not be verified"
	}
	return strings.Join(missing, "; ")
}

// Verifier proves a resume continued the original conversation.
//
// Provider is required: the transcript is read through GetSessionStatus, which
// is the only component that knows where a session's transcript lives.
type Verifier struct {
	// Provider supplies post-resume status. Required.
	Provider claude.SessionProvider
	// Clock drives the polling loop and the timeout. Nil means the system clock.
	Clock clock.Clock
	// Timeout bounds the wait for the transcript to grow. Zero means
	// DefaultVerificationTimeout; a negative value means "check once, never
	// wait".
	Timeout time.Duration
	// Interval is the gap between checks. Zero means
	// DefaultHealthCheckInterval; a negative value means "check once".
	Interval time.Duration
}

// Verify performs the two independent checks and reports the evidence.
//
// It returns an error only when the checks could not be completed — a cancelled
// context or an unreadable status. A resume that simply could not be proven is
// not an error: it is a Verification whose Proven is false, because "we could
// not check" and "the check failed" are different things and must not be
// conflated in the state machine.
func (v Verifier) Verify(ctx context.Context, baseline Baseline, result claude.ResumeResult) (Verification, error) {
	verification := Verification{
		LinesBefore: baseline.TranscriptLines,
		SizeBefore:  baseline.TranscriptSize,
		LinesAfter:  baseline.TranscriptLines,
		SizeAfter:   baseline.TranscriptSize,
	}
	if err := ctx.Err(); err != nil {
		return verification, err
	}
	if strings.TrimSpace(baseline.SessionID) == "" {
		return verification, errors.New("recovery: verification needs a baseline session id")
	}

	verification.SessionIDPreserved, verification.Notes = checkSessionID(baseline.SessionID, result, verification.Notes)

	// A handoff is judged on the CLI's own acknowledgement, not on the
	// transcript. The work belongs to the supervisor from the moment the
	// `backgrounded` block is printed, so growth now arrives on someone else's
	// schedule: waiting for it would time out a perfectly healthy recovery, and
	// observing it would still not say whether the continuation landed in the
	// original conversation. PRD 2 replaces this judgement with the
	// supervisor's published state; until then the block is the only evidence
	// there is, so it is recorded plainly instead of being dressed up as proof
	// the conversation advanced.
	if result.Backgrounded {
		return acceptHandoff(baseline, result, verification), nil
	}

	if v.Provider == nil {
		verification.Notes = append(verification.Notes,
			"no session provider was configured, so transcript growth could not be measured")
		return verification, nil
	}

	status, err := v.await(ctx, baseline, result, &verification)
	if err != nil {
		return verification, err
	}
	verification.Running = status.Running
	if status.Running && !verification.TranscriptGrew {
		verification.Notes = append(verification.Notes,
			"the session has a live process, but a live process is not proof that the conversation continued")
	}
	verification.Proven = verification.SessionIDPreserved && verification.TranscriptGrew
	return verification, nil
}

// await polls GetSessionStatus until the transcript grows or the timeout
// expires, recording the newest measurement in verification.
func (v Verifier) await(ctx context.Context, baseline Baseline, result claude.ResumeResult, verification *Verification) (claude.SessionStatus, error) {
	timeout := v.Timeout
	if timeout == 0 {
		timeout = DefaultVerificationTimeout
	}
	interval := v.Interval
	if interval == 0 {
		interval = DefaultHealthCheckInterval
	}
	clk := v.Clock
	if clk == nil {
		clk = clock.System()
	}

	deadline := clk.Now().Add(timeout)
	var last claude.SessionStatus
	for {
		status, err := v.Provider.GetSessionStatus(ctx, baseline.SessionID)
		if err != nil {
			return status, fmt.Errorf("recovery: check session %s after resume: %w", baseline.SessionID, err)
		}
		last = status
		verification.LinesAfter = status.Transcript.Lines
		verification.SizeAfter = status.Transcript.Size
		verification.SessionIDPreserved, verification.Notes = checkSessionID(
			baseline.SessionID, result, verification.Notes)
		if id := status.Session.ID; id != "" && id != baseline.SessionID {
			verification.SessionIDPreserved = false
			verification.Notes = append(verification.Notes, fmt.Sprintf(
				"the transcript now belongs to session %s instead of %s, so the original conversation was not continued",
				id, baseline.SessionID))
		}

		if measureGrowth(baseline, status, verification) {
			verification.TranscriptGrew = true
			verification.Proven = verification.SessionIDPreserved
			verification.Proven = verification.Proven && verification.TranscriptGrew
			return last, nil
		}
		if timeout < 0 || interval < 0 {
			verification.Notes = append(verification.Notes,
				"transcript growth was checked once and the transcript had not grown")
			return last, nil
		}
		if !clk.Now().Before(deadline) {
			verification.Notes = append(verification.Notes, fmt.Sprintf(
				"the transcript did not grow within %s", timeout))
			return last, nil
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-clk.After(interval):
		}
	}
}

// measureGrowth records whether the transcript advanced and explains why not.
func measureGrowth(baseline Baseline, status claude.SessionStatus, verification *Verification) bool {
	linesNow := status.Transcript.Lines
	sizeNow := status.Transcript.Size

	if baseline.TranscriptLines <= 0 {
		// The baseline was never snapshotted, so neither a line count nor a
		// byte count can be compared. Saying "proven" here would be a guess.
		if linesNow > 0 {
			verification.Notes = append(verification.Notes,
				"the transcript line count before the resume was unknown, so growth could not be proven")
		}
		return false
	}
	if linesNow > baseline.TranscriptLines {
		return true
	}
	if linesNow == baseline.TranscriptLines && sizeNow > baseline.TranscriptSize && baseline.TranscriptSize > 0 {
		verification.Notes = append(verification.Notes,
			"the transcript line count did not change, but its size grew, which means the conversation advanced")
		return true
	}
	return false
}

// checkSessionID compares the id the CLI reported with the id that was asked
// for. It appends to notes and returns the verdict.
func checkSessionID(want string, result claude.ResumeResult, notes []string) (bool, []string) {
	if requested := result.RequestedSessionID; requested != "" && requested != want {
		notes = append(notes, fmt.Sprintf(
			"ccresu asked to resume %s but the command addressed %s", want, requested))
		return false, notes
	}
	if reported := result.SessionID; reported == "" {
		notes = append(notes, "the CLI reported no session id for the resumed turn, so the conversation identity is unproven")
		return false, notes
	}
	if result.SessionID != want {
		notes = append(notes, fmt.Sprintf(
			"the CLI reported session %s instead of %s, which means a new conversation was created",
			result.SessionID, want))
		return false, notes
	}
	if !result.Verified {
		notes = append(notes, "the provider did not confirm that the resumed session kept its id")
		return false, notes
	}
	return true, notes
}

// acceptHandoff records a recovery the supervisor has taken over.
//
// It is not a weaker Verification: it is a different one. Nothing was measured,
// because there is nothing to measure yet — the session is not a transcript that
// grew under ccresu's process, it is work that has been accepted elsewhere. So
// Proven means "the continuation is under way in a known conversation", which is
// exactly as much as the CLI's block can establish, and the fields that would
// otherwise imply a measurement are left at their baseline values.
func acceptHandoff(baseline Baseline, result claude.ResumeResult, verification Verification) Verification {
	verification.HandoffAccepted = true
	verification.HandoffStateID = result.StateID
	verification.HandoffName = result.Name
	verification.Copied = result.Copied
	verification.Proven = true

	switch {
	case result.Copied && result.CopySessionID != "":
		verification.SessionIDPreserved = false
		verification.Notes = append(verification.Notes, fmt.Sprintf(
			"the CLI started a copy in session %s instead of continuing %s in place; a copy is an expected outcome and the recovery is reported against it",
			result.CopySessionID, baseline.SessionID))
	case result.Copied:
		verification.SessionIDPreserved = false
		verification.Notes = append(verification.Notes,
			"the CLI said it started a copy but named no session id, so the new conversation cannot be addressed yet")
	default:
		verification.SessionIDPreserved = true
	}

	if result.StateID == "" {
		// An acknowledged block always carries a short ID; saying so here keeps
		// an operator from hunting for `claude attach` arguments that do not
		// exist.
		verification.Notes = append(verification.Notes,
			"the handoff block carried no state id, so `claude attach` and `claude logs` cannot be pointed at it")
	}
	if result.Handoff.StartedService {
		verification.Notes = append(verification.Notes,
			"a background service was started to accept the session")
	}
	return verification
}

// VerificationFailedError builds the error returned when a resume ran but could
// not be proven, so the caller has both the verdict and the explanation.
func VerificationFailedError(sessionID string, verification Verification) error {
	return fmt.Errorf("%w: session %s: %s", ErrVerificationFailed, sessionID, verification.Reason())
}
