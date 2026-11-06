package recovery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/storage"
)

const (
	sessionA = "11111111-1111-4111-8111-111111111111"
	sessionB = "22222222-2222-4222-8222-222222222222"
	sessionC = "33333333-3333-4333-8333-333333333333"

	jobID = "93874eb3"
)

// baselineFor returns a baseline that has a transcript snapshot, so growth can
// be measured.
func baselineFor(id string) Baseline {
	return Baseline{SessionID: id, TranscriptPath: "/home/u/.claude/projects/p/" + id + ".jsonl", TranscriptLines: 5, TranscriptSize: 100}
}

// goodResult is the dispatch acknowledgement: the job the CLI created.
func goodResult(_ string) claude.ResumeResult {
	return claude.ResumeResult{RequestedSessionID: sessionA, JobID: jobID}
}

// statusFor builds a status reading with the given transcript measurement.
func statusFor(id string, lines, size int64) claude.SessionStatus {
	return claude.SessionStatus{
		Session:    claude.Session{ID: id, Recoverable: true},
		Transcript: claude.TranscriptInfo{Path: "/t/" + id + ".jsonl", Lines: lines, Size: size},
	}
}

func TestVerifierProvesGrowthOfTheContinuationSession(t *testing.T) {
	provider := &providerStub{
		jobFn: func(int, string) (claude.AgentEntry, error) { return finishedJob(jobID, sessionB), nil },
		statusFn: func(_ int, sessionID string) (claude.SessionStatus, error) {
			return statusFor(sessionID, 9, 180), nil
		},
	}
	verifier := Verifier{Provider: provider, Timeout: -1, Interval: -1}

	got, err := verifier.Verify(context.Background(), baselineFor(sessionA), goodResult(sessionA))
	if err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	if !got.ContinuationConfirmed || !got.TranscriptGrew || !got.Proven {
		t.Fatalf("Verification = %+v, want a proven resume", got)
	}
	if got.JobID != jobID || got.ContinuationSessionID != sessionB || got.JobState != "done" {
		t.Errorf("Verification = %+v, want the job and the continuation it reported", got)
	}
	if reason := got.Reason(); !strings.Contains(reason, "grew") {
		t.Errorf("Reason = %q, want it to explain the success", reason)
	}
	if missing := got.Missing(); len(missing) != 0 {
		t.Errorf("Missing = %v, want none", missing)
	}
}

func TestVerifierProvesGrowthByLineCountWhenTheJobKeptTheSession(t *testing.T) {
	provider := &providerStub{
		jobFn: func(int, string) (claude.AgentEntry, error) { return finishedJob(jobID, sessionA), nil },
		statusFn: func(_ int, sessionID string) (claude.SessionStatus, error) {
			return statusFor(sessionID, 9, 180), nil
		},
	}
	verifier := Verifier{Provider: provider, Timeout: -1, Interval: -1}

	got, err := verifier.Verify(context.Background(), baselineFor(sessionA), goodResult(sessionA))
	if err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	if !got.TranscriptGrew || !got.Proven {
		t.Fatalf("Verification = %+v, want the line count to prove growth", got)
	}
	if got.LinesBefore != 5 || got.LinesAfter != 9 || got.SizeBefore != 100 || got.SizeAfter != 180 {
		t.Errorf("measurements = %+v, want the before/after snapshots", got)
	}
}

func TestVerifierProvesGrowthBySizeWhenLinesDoNotMove(t *testing.T) {
	provider := &providerStub{
		jobFn: func(int, string) (claude.AgentEntry, error) { return finishedJob(jobID, sessionA), nil },
		statusFn: func(_ int, sessionID string) (claude.SessionStatus, error) {
			return statusFor(sessionID, 5, 240), nil
		},
	}
	verifier := Verifier{Provider: provider, Timeout: -1, Interval: -1}

	got, err := verifier.Verify(context.Background(), baselineFor(sessionA), goodResult(sessionA))
	if err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	if !got.TranscriptGrew || !got.Proven {
		t.Fatalf("Verification = %+v, want the byte-count to prove growth", got)
	}
	if !containsNote(got.Notes, "size grew") {
		t.Errorf("Notes = %v, want the byte-count explanation", got.Notes)
	}
}

func TestVerifierReportsUnprovenResumeOnceChecked(t *testing.T) {
	provider := &providerStub{
		jobFn: func(int, string) (claude.AgentEntry, error) { return finishedJob(jobID, sessionA), nil },
		statusFn: func(_ int, sessionID string) (claude.SessionStatus, error) {
			return statusFor(sessionID, 5, 100), nil
		},
	}
	verifier := Verifier{Provider: provider, Timeout: -1, Interval: -1}

	got, err := verifier.Verify(context.Background(), baselineFor(sessionA), goodResult(sessionA))
	if err != nil {
		t.Fatalf("Verify = %v, want nil: an unproven resume is not an error", err)
	}
	if got.Proven || got.TranscriptGrew {
		t.Fatalf("Verification = %+v, want an unproven resume", got)
	}
	if !got.ContinuationConfirmed {
		t.Error("ContinuationConfirmed = false, want true: the job did report a continuation")
	}
	if !containsNote(got.Notes, "checked once") {
		t.Errorf("Notes = %v, want the single-check explanation", got.Notes)
	}
	if reason := got.Reason(); !strings.Contains(reason, "did not grow") {
		t.Errorf("Reason = %q, want it to name the failed check", reason)
	}
}

func TestVerifierTimesOutWaitingForGrowth(t *testing.T) {
	var calls int
	provider := &providerStub{
		jobFn: func(int, string) (claude.AgentEntry, error) { return finishedJob(jobID, sessionA), nil },
		statusFn: func(_ int, sessionID string) (claude.SessionStatus, error) {
			calls++
			return statusFor(sessionID, 5, 100), nil
		},
	}
	// A real clock with a tiny budget exercises the polling loop and its
	// deadline without a real wait.
	verifier := Verifier{Provider: provider, Timeout: time.Millisecond, Interval: time.Millisecond}

	got, err := verifier.Verify(context.Background(), baselineFor(sessionA), goodResult(sessionA))
	if err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	if got.Proven {
		t.Fatalf("Verification = %+v, want it unproven", got)
	}
	if !containsNote(got.Notes, "did not grow within") {
		t.Errorf("Notes = %v, want the timeout explanation", got.Notes)
	}
	if calls < 2 {
		t.Errorf("provider was checked %d times, want it polled until the deadline", calls)
	}
}

// TestVerifierWaitsForTheJobToStopWorking is the core of the recorded probe: the
// job is polled until it leaves `working`, and only then is the continuation
// transcript measured.
func TestVerifierWaitsForTheJobToStopWorking(t *testing.T) {
	var jobCalls int
	provider := &providerStub{
		jobFn: func(call int, _ string) (claude.AgentEntry, error) {
			jobCalls = call
			if call < 3 {
				entry := finishedJob(jobID, sessionB)
				entry.State = "working"
				return entry, nil
			}
			return finishedJob(jobID, sessionB), nil
		},
		statusFn: func(_ int, sessionID string) (claude.SessionStatus, error) {
			return statusFor(sessionID, 4, 400), nil
		},
	}
	verifier := Verifier{Provider: provider, Timeout: time.Second, Interval: time.Millisecond}

	got, err := verifier.Verify(context.Background(), baselineFor(sessionA), goodResult(sessionA))
	if err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	if !got.Proven {
		t.Fatalf("Verification = %+v, want the job to be followed to completion", got)
	}
	if jobCalls < 3 {
		t.Errorf("the job was read %d times, want it polled until it left working", jobCalls)
	}
	if got.JobState != "done" {
		t.Errorf("JobState = %q, want the last state the CLI reported", got.JobState)
	}
}

func TestVerifierReportsAJobStillWorking(t *testing.T) {
	provider := &providerStub{
		jobFn: func(int, string) (claude.AgentEntry, error) {
			entry := finishedJob(jobID, sessionB)
			entry.State = "working"
			return entry, nil
		},
	}
	verifier := Verifier{Provider: provider, Timeout: -1, Interval: -1}

	got, err := verifier.Verify(context.Background(), baselineFor(sessionA), goodResult(sessionA))
	if err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	if got.Proven {
		t.Fatal("a job that is still working proves nothing")
	}
	if !containsNote(got.Notes, "still working") {
		t.Errorf("Notes = %v, want the still-working explanation", got.Notes)
	}
}

func TestVerifierNeverAcceptsARunningProcessAsProof(t *testing.T) {
	provider := &providerStub{
		jobFn: func(int, string) (claude.AgentEntry, error) { return finishedJob(jobID, sessionA), nil },
		statusFn: func(_ int, sessionID string) (claude.SessionStatus, error) {
			status := statusFor(sessionID, 5, 100)
			status.Running = true
			return status, nil
		},
	}
	verifier := Verifier{Provider: provider, Timeout: -1, Interval: -1}

	got, err := verifier.Verify(context.Background(), baselineFor(sessionA), goodResult(sessionA))
	if err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	if got.Proven {
		t.Fatal("a live process must never be treated as proof that the conversation continued")
	}
	if !got.Running {
		t.Error("Running should still be reported")
	}
	if !containsNote(got.Notes, "live process is not proof") {
		t.Errorf("Notes = %v, want the live-process caveat", got.Notes)
	}
}

// TestVerifierRejectsAContinuationWithAnEmptyTranscript covers the new-session
// case: a job that reports its own session id proves continuation only when that
// session's transcript holds content.
func TestVerifierRejectsAContinuationWithAnEmptyTranscript(t *testing.T) {
	provider := &providerStub{
		jobFn: func(int, string) (claude.AgentEntry, error) { return finishedJob(jobID, sessionB), nil },
		statusFn: func(_ int, sessionID string) (claude.SessionStatus, error) {
			return statusFor(sessionID, 0, 0), nil
		},
	}
	verifier := Verifier{Provider: provider, Timeout: -1, Interval: -1}

	got, err := verifier.Verify(context.Background(), baselineFor(sessionA), goodResult(sessionA))
	if err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	if got.TranscriptGrew || got.Proven {
		t.Fatalf("Verification = %+v, want an empty continuation transcript to prove nothing", got)
	}
	if !containsNote(got.Notes, "transcript is still empty") {
		t.Errorf("Notes = %v, want the empty-transcript explanation", got.Notes)
	}
}

func TestVerifierWithoutProviderCannotProveGrowth(t *testing.T) {
	verifier := Verifier{Timeout: -1, Interval: -1}
	got, err := verifier.Verify(context.Background(), baselineFor(sessionA), goodResult(sessionA))
	if err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	if got.Proven {
		t.Fatal("without a provider the transcript cannot be measured, so the resume is unproven")
	}
	if !containsNote(got.Notes, "no session provider") {
		t.Errorf("Notes = %v, want the missing-provider explanation", got.Notes)
	}
}

func TestVerifierRejectsADispatchWithoutAJobID(t *testing.T) {
	verifier := Verifier{Provider: &providerStub{}, Timeout: -1, Interval: -1}
	got, err := verifier.Verify(context.Background(), baselineFor(sessionA), claude.ResumeResult{RequestedSessionID: sessionA})
	if err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	if got.Proven {
		t.Fatal("a dispatch that named no job proves nothing")
	}
	if !containsNote(got.Notes, "no background job id") {
		t.Errorf("Notes = %v, want the missing-job explanation", got.Notes)
	}
}

func TestVerifierReportsAJobWithoutAContinuationSession(t *testing.T) {
	provider := &providerStub{jobFn: func(int, string) (claude.AgentEntry, error) {
		return finishedJob(jobID, ""), nil
	}}
	verifier := Verifier{Provider: provider, Timeout: -1, Interval: -1}

	got, err := verifier.Verify(context.Background(), baselineFor(sessionA), goodResult(sessionA))
	if err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	if got.ContinuationConfirmed || got.Proven {
		t.Fatalf("Verification = %+v, want no continuation to be confirmed", got)
	}
	if !containsNote(got.Notes, "reported no session id") {
		t.Errorf("Notes = %v, want the missing-session explanation", got.Notes)
	}
	if len(got.Missing()) != 2 {
		t.Errorf("Missing() = %v, want both checks", got.Missing())
	}
}

func TestVerifierHonoursACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	verifier := Verifier{Provider: &providerStub{}, Timeout: -1, Interval: -1}

	got, err := verifier.Verify(ctx, baselineFor(sessionA), goodResult(sessionA))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Verify error = %v, want context.Canceled", err)
	}
	if got.Proven {
		t.Error("a cancelled verification must never be proven")
	}
}

func TestVerifierReportsAnUnreadableJob(t *testing.T) {
	boom := errors.New("agents listing unreadable")
	provider := &providerStub{jobFn: func(int, string) (claude.AgentEntry, error) {
		return claude.AgentEntry{}, boom
	}}
	verifier := Verifier{Provider: provider, Timeout: -1, Interval: -1}

	_, err := verifier.Verify(context.Background(), baselineFor(sessionA), goodResult(sessionA))
	if !errors.Is(err, boom) {
		t.Fatalf("Verify error = %v, want the provider failure", err)
	}
	if !strings.Contains(err.Error(), jobID) {
		t.Errorf("error = %q, want it to name the job", err)
	}
}

func TestVerifierReportsAnUnreadableStatus(t *testing.T) {
	boom := errors.New("transcript unreadable")
	provider := &providerStub{
		jobFn: func(int, string) (claude.AgentEntry, error) { return finishedJob(jobID, sessionA), nil },
		statusFn: func(_ int, _ string) (claude.SessionStatus, error) {
			return claude.SessionStatus{}, boom
		},
	}
	verifier := Verifier{Provider: provider, Timeout: -1, Interval: -1}

	_, err := verifier.Verify(context.Background(), baselineFor(sessionA), goodResult(sessionA))
	if !errors.Is(err, boom) {
		t.Fatalf("Verify error = %v, want the provider failure", err)
	}
	if !strings.Contains(err.Error(), "check session "+sessionA) {
		t.Errorf("error = %q, want it to name the session", err)
	}
}

func TestMeasureGrowthWithoutABaselineSnapshot(t *testing.T) {
	baseline := Baseline{SessionID: sessionA}
	verification := Verification{}

	if measureGrowth(baseline.SessionID, baseline, statusFor(sessionA, 4, 90), &verification) {
		t.Fatal("growth must not be claimed without a baseline snapshot")
	}
	if !containsNote(verification.Notes, "before the resume was unknown") {
		t.Errorf("Notes = %v, want the unknown-baseline explanation", verification.Notes)
	}

	verification = Verification{}
	if measureGrowth(baseline.SessionID, baseline, statusFor(sessionA, 0, 0), &verification) {
		t.Fatal("an empty transcript proves nothing")
	}
	if len(verification.Notes) != 0 {
		t.Errorf("Notes = %v, want none when there was nothing to compare", verification.Notes)
	}

	baseline = Baseline{SessionID: sessionA, TranscriptLines: 5}
	verification = Verification{}
	if measureGrowth(baseline.SessionID, baseline, statusFor(sessionA, 5, 400), &verification) {
		t.Fatal("a size increase without a baseline size cannot prove growth")
	}

	// A continuation under a new session id needs content, not a delta.
	verification = Verification{}
	if !measureGrowth(sessionB, baseline, statusFor(sessionB, 1, 10), &verification) {
		t.Fatal("a continuation with a non-empty transcript proves the conversation moved")
	}
}

func TestVerificationMissingAndReason(t *testing.T) {
	empty := Verification{}
	if got := empty.Missing(); len(got) != 2 {
		t.Errorf("Missing() = %v, want both checks", got)
	}
	partial := Verification{ContinuationConfirmed: true}
	if got := partial.Missing(); len(got) != 1 {
		t.Errorf("Missing() = %v, want only the growth check", got)
	}
	inconsistent := Verification{ContinuationConfirmed: true, TranscriptGrew: true}
	if got := inconsistent.Reason(); got != "the resume could not be verified" {
		t.Errorf("Reason() = %q, want the generic unproven message", got)
	}
	if got := empty.Reason(); !strings.Contains(got, "session id") || !strings.Contains(got, "did not grow") {
		t.Errorf("Reason() = %q, want both failures", got)
	}
}

func TestBaselineFromSessionAndStatus(t *testing.T) {
	session := storage.Session{
		ID:              sessionA,
		TranscriptPath:  "/t/a.jsonl",
		TranscriptSize:  120,
		TranscriptLines: 7,
	}
	got := BaselineFromSession(session)
	if got.SessionID != sessionA || got.TranscriptPath != "/t/a.jsonl" ||
		got.TranscriptSize != 120 || got.TranscriptLines != 7 {
		t.Errorf("BaselineFromSession = %+v, want the stored snapshot", got)
	}

	fromStatus := BaselineFromStatus(claude.SessionStatus{
		Session:    claude.Session{ID: sessionB},
		Transcript: claude.TranscriptInfo{Path: "/t/b.jsonl", Size: 200, Lines: 11},
	})
	if fromStatus.SessionID != sessionB || fromStatus.TranscriptPath != "/t/b.jsonl" ||
		fromStatus.TranscriptSize != 200 || fromStatus.TranscriptLines != 11 {
		t.Errorf("BaselineFromStatus = %+v, want the fresh snapshot", fromStatus)
	}

	// Without a session id or path on the status, the transcript's values win.
	fallback := BaselineFromStatus(claude.SessionStatus{
		Session:    claude.Session{TranscriptPath: "/t/c.jsonl"},
		Transcript: claude.TranscriptInfo{SessionID: sessionC},
	})
	if fallback.SessionID != sessionC || fallback.TranscriptPath != "/t/c.jsonl" {
		t.Errorf("BaselineFromStatus fallback = %+v, want the transcript identity", fallback)
	}
}

func TestVerificationFailedError(t *testing.T) {
	err := VerificationFailedError(sessionA, Verification{})
	if !errors.Is(err, ErrVerificationFailed) {
		t.Fatalf("error = %v, want ErrVerificationFailed", err)
	}
	if !strings.Contains(err.Error(), sessionA) {
		t.Errorf("error = %q, want it to name the session", err)
	}
	proven := Verification{ContinuationConfirmed: true, TranscriptGrew: true, Proven: true}
	if err := VerificationFailedError(sessionA, proven); err != nil && !errors.Is(err, ErrVerificationFailed) {
		t.Errorf("error = %v, want ErrVerificationFailed", err)
	}
}

// containsNote reports whether any note contains want.
func containsNote(notes []string, want string) bool {
	for _, note := range notes {
		if strings.Contains(note, want) {
			return true
		}
	}
	return false
}
