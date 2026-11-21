package recovery

import (
	"context"
	"fmt"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/storage"
)

// Baseline is the state of a session before a resume, captured so a later read
// can say whether the conversation advanced.
//
// It is kept for one purpose only: comparing it with a fresh read after the turn
// produces a diagnostic. It is never a verdict. The verdict is the resume's exit
// status, because `claude --resume <id> "<message>"` runs the turn itself and
// prints no handshake or result block to a pipe.
type Baseline struct {
	// SessionID is the session that must still be the one addressed.
	SessionID string
	// TranscriptPath is the transcript ccresu expects to grow.
	TranscriptPath string
	// TranscriptSize and TranscriptLines snapshot the transcript before the
	// resume. A zero Lines means the snapshot was never taken, and growth then
	// cannot be measured — which is reported rather than assumed.
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

// Verification is what one finished resume left behind.
//
// It is a record, not a judgement handed down after the fact. The resume ran the
// turn to its end, so by the time one of these exists the work is already done
// and nothing here can revise it: Proven restates the exit status the caller has
// already branched on, and the transcript measurements below are the diagnostic
// an operator reads afterwards.
type Verification struct {
	// Proven reports that the resume finished its turn, which is its exit
	// status being zero. Every other outcome arrives as an error from the
	// provider, so a Verification is only ever built for a turn that ran.
	Proven bool
	// ExitCode is the process exit status of the resume.
	ExitCode int
	// TranscriptGrew reports that the transcript gained content between the
	// baseline and the read taken after the turn.
	//
	// It is a diagnostic and never evidence: a session can be continued without
	// the transcript ccresu is watching changing shape, and a transcript can
	// grow without the original conversation being the one that advanced. The
	// exit status is what settles it, which is why this value is recorded and
	// then left alone.
	TranscriptGrew bool
	// LinesBefore/After and SizeBefore/After are the measurements the
	// diagnostic rests on, kept for logs and for `ccresu inspect`.
	LinesBefore int64
	LinesAfter  int64
	SizeBefore  int64
	SizeAfter   int64
	// Notes explains every conclusion, including why a measurement could not be
	// made.
	Notes []string
}

// Reason renders what the verification establishes, for a log line or a stored
// attempt note.
func (v Verification) Reason() string {
	if !v.Proven {
		return fmt.Sprintf("the resume did not finish its turn (exit %d)", v.ExitCode)
	}
	if v.TranscriptGrew {
		return "the resume finished its turn and the transcript grew"
	}
	return "the resume finished its turn"
}

// Recorder measures a session after a successful resume.
//
// It reads once and never waits. Waiting is what the old verifier did, and it
// existed only to watch a backgrounded handoff whose end it could not see; the
// resume now runs the turn itself, so when it returns there is nothing left to
// wait for and a single read is all there is to take.
type Recorder struct {
	// Provider supplies the post-resume status. Required for a measurement;
	// nil means the diagnosis is recorded as absent rather than guessed.
	Provider claude.SessionProvider
}

// Measure reads the session once after a turn that ran, recording whether its
// transcript grew.
//
// It cannot fail a recovery, and deliberately does not return an error. The turn
// ran or it did not, and a read taken afterwards cannot change that: a read that
// fails is recorded as a note and the diagnosis is simply missing, which is the
// honest answer rather than a second verdict.
func (r Recorder) Measure(ctx context.Context, baseline Baseline, result claude.ResumeResult) Verification {
	verification := Verification{
		Proven:      result.ExitCode == 0,
		ExitCode:    result.ExitCode,
		LinesBefore: baseline.TranscriptLines,
		SizeBefore:  baseline.TranscriptSize,
		LinesAfter:  baseline.TranscriptLines,
		SizeAfter:   baseline.TranscriptSize,
	}
	if r.Provider == nil {
		verification.Notes = append(verification.Notes,
			"no session provider was configured, so the transcript could not be measured")
		return verification
	}
	if err := ctx.Err(); err != nil {
		verification.Notes = append(verification.Notes,
			fmt.Sprintf("the transcript was not measured: %v", err))
		return verification
	}
	status, err := r.Provider.GetSessionStatus(ctx, baseline.SessionID)
	if err != nil {
		verification.Notes = append(verification.Notes,
			fmt.Sprintf("the transcript was not measured: %v", err))
		return verification
	}
	verification.LinesAfter = status.Transcript.Lines
	verification.SizeAfter = status.Transcript.Size
	verification.TranscriptGrew = measureGrowth(baseline, status, &verification)
	return verification
}

// measureGrowth records whether the transcript advanced and explains why not.
func measureGrowth(baseline Baseline, status claude.SessionStatus, verification *Verification) bool {
	linesNow := status.Transcript.Lines
	sizeNow := status.Transcript.Size

	if baseline.TranscriptLines <= 0 {
		// The baseline was never snapshotted, so neither a line count nor a
		// byte count can be compared. Saying "grew" here would be a guess.
		if linesNow > 0 {
			verification.Notes = append(verification.Notes,
				"the transcript line count before the resume was unknown, so growth could not be measured")
		}
		return false
	}
	if linesNow > baseline.TranscriptLines {
		return true
	}
	if linesNow == baseline.TranscriptLines && sizeNow > baseline.TranscriptSize && baseline.TranscriptSize > 0 {
		verification.Notes = append(verification.Notes,
			"the transcript line count did not change, but its size grew, so the conversation did advance")
		return true
	}
	return false
}
