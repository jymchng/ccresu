package recovery

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/clock"
	"github.com/jymchng/ccresu/internal/storage"
)

// DefaultVerificationTimeout bounds how long the verifier waits for the
// dispatched job to stop working and for the continuation transcript to appear.
const DefaultVerificationTimeout = 120 * time.Second

// DefaultHealthCheckInterval is how often the verifier re-reads the job and the
// transcript while it waits.
const DefaultHealthCheckInterval = 10 * time.Second

// Baseline is the state of a session before a resume, captured so the verifier
// can prove afterwards that the conversation advanced.
type Baseline struct {
	// SessionID is the session that was addressed by the dispatch.
	SessionID string
	// TranscriptPath is the transcript ccresu expects to grow when the job
	// continues the conversation under the same session id.
	TranscriptPath string
	// TranscriptSize and TranscriptLines snapshot the transcript before the
	// resume. A zero Lines means the snapshot was never taken, and growth then
	// cannot be proven -- which is reported rather than assumed.
	TranscriptSize  int64
	TranscriptLines int64
}

// BaselineFromSession captures a baseline from a persisted session row.
//
// A resumed conversation continues under the id its job reported, in that
// session's own transcript, so a row that records a continuation must be
// measured against that file: the transcript named on the row stopped growing
// the moment the conversation moved, and a dispatch that beat its line count
// would prove nothing. The row's size and line snapshot belongs to the original
// transcript, so the continuation starts with no snapshot at all -- zero, which
// grew() reports as "cannot be proven" rather than as a transcript that grew
// from nothing. The fresh status taken before the dispatch fills both in.
func BaselineFromSession(session storage.Session) Baseline {
	if id := strings.TrimSpace(session.ContinuationSessionID); id != "" {
		return Baseline{
			SessionID:      id,
			TranscriptPath: strings.TrimSpace(session.ContinuationTranscriptPath),
		}
	}
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

// Verification is the evidence collected after a dispatch.
//
// Proven is the only field a caller may treat as success. It is true only when
// both independent checks passed: the dispatched background job reported the
// session id that continues the conversation, and that session's transcript
// grew.
//
// A verification carries no turn result, by design. The command a resume runs is
// `claude --resume <id> --bg <message>`, which answers with a job id and nothing
// else: it prints no JSON envelope, so total_cost_usd, num_turns, duration_ms,
// subtype and is_error are empty here and in the attempt row that records the
// outcome. Leaving them empty is the honest representation of "the dispatch
// cannot report them"; filling them with zeroes would claim a free, instant,
// successful turn that never happened. summaryOf renders the substitute: the job
// plus the continuation it reported.
type Verification struct {
	// JobID is the background job the dispatch created.
	JobID string
	// JobState is the last state the CLI reported for that job ("working",
	// "done", "failed", "stopped", or an unknown value from a newer CLI).
	JobState string
	// ContinuationSessionID is the session id the job reports. A background
	// agent continues the conversation under its own session id, so this is
	// normally a new id rather than the one that was resumed.
	ContinuationSessionID string
	// ContinuationTranscriptPath is where the continuation session's transcript
	// was read, as the session status reported it. It is empty when no status was
	// read. Callers record transcript metrics against *this* path: the numbers in
	// SizeAfter/LinesAfter were measured on it, and they say nothing about the
	// transcript of the session that was resumed.
	ContinuationTranscriptPath string
	// ContinuationConfirmed reports that the job reported a continuation session
	// id, i.e. that there is a conversation to measure. It replaces the old
	// "the reported id equals the requested id" check, which print mode made
	// possible and a background dispatch does not.
	ContinuationConfirmed bool
	// TranscriptGrew reports that the continuation transcript gained content.
	TranscriptGrew bool
	// Proven reports ContinuationConfirmed && TranscriptGrew.
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
	if !v.ContinuationConfirmed {
		missing = append(missing, "the background job reported no continuation session id")
	}
	if !v.TranscriptGrew {
		missing = append(missing, "the continuation transcript did not grow")
	}
	return missing
}

// Reason renders why the resume was not proven.
func (v Verification) Reason() string {
	if v.Proven {
		return fmt.Sprintf("the job %s continued the conversation as session %s and its transcript grew",
			v.JobID, v.ContinuationSessionID)
	}
	missing := v.Missing()
	if len(missing) == 0 {
		return "the resume could not be verified"
	}
	return strings.Join(missing, "; ")
}

// Verifier proves that a dispatched job continued the conversation.
//
// Provider is required: the job is read through BackgroundJob and the transcript
// through GetSessionStatus, which are the only components that know where a
// session's conversation lives.
type Verifier struct {
	// Provider supplies the job and the post-dispatch status. Required.
	Provider claude.SessionProvider
	// Clock drives the polling loops and the timeout. Nil means the system clock.
	Clock clock.Clock
	// Timeout bounds the wait for the job to stop working and for the transcript
	// to grow. Zero means DefaultVerificationTimeout; a negative value means
	// "check once, never wait".
	Timeout time.Duration
	// Interval is the gap between checks. Zero means DefaultHealthCheckInterval;
	// a negative value means "check once".
	Interval time.Duration
}

// Verify resolves the dispatched job, waits for it to stop working, and measures
// the transcript of the session it continues.
//
// It returns an error only when the checks could not be completed -- a cancelled
// context, or a job or status that could not be read. A resume that simply could
// not be proven is not an error: it is a Verification whose Proven is false,
// because "we could not check" and "the check failed" are different things and
// must not be conflated in the state machine.
func (v Verifier) Verify(ctx context.Context, baseline Baseline, result claude.ResumeResult) (Verification, error) {
	verification := Verification{
		JobID:       result.JobID,
		LinesBefore: baseline.TranscriptLines,
		SizeBefore:  baseline.TranscriptSize,
		LinesAfter:  baseline.TranscriptLines,
		SizeAfter:   baseline.TranscriptSize,
	}
	if err := ctx.Err(); err != nil {
		return verification, err
	}
	if strings.TrimSpace(result.JobID) == "" {
		verification.Notes = append(verification.Notes,
			"the dispatch reported no background job id, so there was nothing to follow")
		return verification, nil
	}
	if v.Provider == nil {
		verification.Notes = append(verification.Notes,
			"no session provider was configured, so the job and its transcript could not be read")
		return verification, nil
	}

	entry, err := v.awaitJob(ctx, result.JobID, &verification)
	if err != nil {
		return verification, err
	}
	if strings.TrimSpace(entry.SessionID) == "" {
		verification.Notes = append(verification.Notes, fmt.Sprintf(
			"the background job %s reported no session id, so the conversation it continues is unknown",
			result.JobID))
		return verification, nil
	}
	verification.ContinuationSessionID = entry.SessionID
	verification.ContinuationConfirmed = true
	verification.Notes = append(verification.Notes, fmt.Sprintf(
		"the background job %s continues the conversation as session %s (state %s)",
		result.JobID, entry.SessionID, orUnknown(entry.State)))

	status, err := v.awaitGrowth(ctx, entry.SessionID, baseline, &verification)
	if err != nil {
		return verification, err
	}
	verification.Running = status.Running
	if status.Running && !verification.TranscriptGrew {
		verification.Notes = append(verification.Notes,
			"the session has a live process, but a live process is not proof that the conversation continued")
	}
	verification.Proven = verification.ContinuationConfirmed && verification.TranscriptGrew
	return verification, nil
}

// awaitJob polls the background job until it leaves the working state, recording
// the newest state in verification.
//
// The job is what the dispatch created, so its state is the one fact that says
// whether the turn is still running: a job that has stopped working has either
// finished, failed or been stopped, and only then does waiting for the
// transcript make sense.
func (v Verifier) awaitJob(ctx context.Context, jobID string, verification *Verification) (claude.AgentEntry, error) {
	timeout, interval, clk := v.pacing()
	deadline := clk.Now().Add(timeout)
	for {
		entry, err := v.Provider.BackgroundJob(ctx, jobID)
		if err != nil {
			return entry, fmt.Errorf("recovery: read background job %s after dispatch: %w", jobID, err)
		}
		verification.JobState = entry.State
		if !jobWorking(entry) {
			return entry, nil
		}
		if timeout < 0 || interval < 0 {
			verification.Notes = append(verification.Notes, fmt.Sprintf(
				"the background job %s was still working when it was checked once", jobID))
			return entry, nil
		}
		if !clk.Now().Before(deadline) {
			verification.Notes = append(verification.Notes, fmt.Sprintf(
				"the background job %s was still working after %s", jobID, timeout))
			return entry, nil
		}
		select {
		case <-ctx.Done():
			return entry, ctx.Err()
		case <-clk.After(interval):
		}
	}
}

// awaitGrowth polls the continuation session's status until its transcript shows
// content, recording the newest measurement.
func (v Verifier) awaitGrowth(ctx context.Context, continuationID string, baseline Baseline, verification *Verification) (claude.SessionStatus, error) {
	timeout, interval, clk := v.pacing()
	deadline := clk.Now().Add(timeout)
	var last claude.SessionStatus
	for {
		status, err := v.Provider.GetSessionStatus(ctx, continuationID)
		if err != nil {
			return status, fmt.Errorf("recovery: check session %s after dispatch: %w", continuationID, err)
		}
		last = status
		verification.LinesAfter = status.Transcript.Lines
		verification.SizeAfter = status.Transcript.Size
		verification.ContinuationTranscriptPath = transcriptPathOf(status)
		if measureGrowth(continuationID, baseline, status, verification) {
			verification.TranscriptGrew = true
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

// pacing resolves the timeout, interval and clock, applying the documented
// defaults.
func (v Verifier) pacing() (time.Duration, time.Duration, clock.Clock) {
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
	return timeout, interval, clk
}

// jobWorking reports whether the job is still running its turn.
//
// "working" is the only state that means "busy" in the CLI's vocabulary; an
// unknown state from a newer CLI is treated as finished, because waiting for a
// state ccresu does not understand would hang the recovery for the whole
// timeout.
func jobWorking(entry claude.AgentEntry) bool {
	return strings.EqualFold(strings.TrimSpace(entry.State), "working")
}

// orUnknown renders an empty state honestly.
func orUnknown(state string) string {
	if strings.TrimSpace(state) == "" {
		return "unknown"
	}
	return state
}

// transcriptPathOf reports where a status reading took its transcript from.
//
// The transcript info carries the path when the file was found; the session's own
// copy is the fallback, because a status that could not read the file still knows
// where it would have been read.
func transcriptPathOf(status claude.SessionStatus) string {
	if path := strings.TrimSpace(status.Transcript.Path); path != "" {
		return path
	}
	return strings.TrimSpace(status.Session.TranscriptPath)
}

// measureGrowth records whether the conversation advanced and explains why not.
//
// A background agent continues the conversation under its own session id, so
// there are two cases. When the job kept the id that was resumed, growth is
// measured against the snapshot taken before the dispatch. When the job reports
// a different id -- the normal case -- the continuation starts a fresh
// transcript, and growth means that transcript exists and has content.
func measureGrowth(continuationID string, baseline Baseline, status claude.SessionStatus, verification *Verification) bool {
	linesNow := status.Transcript.Lines
	sizeNow := status.Transcript.Size

	if continuationID != "" && continuationID != baseline.SessionID {
		if linesNow > 0 || sizeNow > 0 {
			verification.Notes = append(verification.Notes, fmt.Sprintf(
				"the conversation continues under session %s, whose transcript holds %d lines (%d bytes)",
				continuationID, linesNow, sizeNow))
			return true
		}
		verification.Notes = append(verification.Notes, fmt.Sprintf(
			"the conversation continues under session %s, whose transcript is still empty", continuationID))
		return false
	}

	if baseline.TranscriptLines <= 0 {
		// The baseline was never snapshotted, so neither a line count nor a byte
		// count can be compared. Saying "proven" here would be a guess.
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

// summaryOf renders what an attempt row stores as its output.
//
// A background dispatch reports no turn result, so the honest summary is the job
// and the conversation it continued; the assistant text, if any, lives in the
// transcript and is never copied into the database.
func summaryOf(result claude.ResumeResult, verification Verification) string {
	if verification.ContinuationSessionID != "" {
		return fmt.Sprintf("dispatched %s; continued as session %s", result.JobID, verification.ContinuationSessionID)
	}
	if result.JobID != "" {
		return "dispatched " + result.JobID
	}
	return "dispatched"
}

// VerificationFailedError builds the error returned when a resume ran but could
// not be proven, so the caller has both the verdict and the explanation.
func VerificationFailedError(sessionID string, verification Verification) error {
	return fmt.Errorf("%w: session %s: %s", ErrVerificationFailed, sessionID, verification.Reason())
}
