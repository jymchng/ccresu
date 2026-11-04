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
// Proven is the only field a caller may treat as success. It is true only when
// both independent checks passed: the CLI reported the original session id back,
// and the transcript of that session actually grew.
type Verification struct {
	// SessionIDPreserved reports that the resumed session is the requested one.
	SessionIDPreserved bool
	// TranscriptGrew reports that the transcript gained content.
	TranscriptGrew bool
	// Proven reports SessionIDPreserved && TranscriptGrew.
	Proven bool
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

// VerificationFailedError builds the error returned when a resume ran but could
// not be proven, so the caller has both the verdict and the explanation.
func VerificationFailedError(sessionID string, verification Verification) error {
	return fmt.Errorf("%w: session %s: %s", ErrVerificationFailed, sessionID, verification.Reason())
}
