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
)

// baselineFor returns a baseline that has a transcript snapshot, so growth can
// be measured.
func baselineFor(id string) Baseline {
	return Baseline{SessionID: id, TranscriptPath: "/home/u/.claude/projects/p/" + id + ".jsonl", TranscriptLines: 5, TranscriptSize: 100}
}

// goodResult is a resume result that preserved the session id.
func goodResult(id string) claude.ResumeResult {
	return claude.ResumeResult{RequestedSessionID: id, SessionID: id, Verified: true, Result: "done"}
}

// statusFor builds a status reading with the given transcript measurement.
func statusFor(id string, lines, size int64) claude.SessionStatus {
	return claude.SessionStatus{
		Session:    claude.Session{ID: id, Recoverable: true},
		Transcript: claude.TranscriptInfo{Path: "/t/" + id + ".jsonl", Lines: lines, Size: size},
	}
}

func TestVerifierMeasuresGrowthByLineCount(t *testing.T) {
	provider := &providerStub{statusFn: func(int, string) (claude.SessionStatus, error) {
		return statusFor(sessionA, 9, 180), nil
	}}
	verifier := Verifier{Provider: provider, Timeout: -1, Interval: -1}

	got, err := verifier.Verify(context.Background(), baselineFor(sessionA), goodResult(sessionA))
	if err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	// PRD 2 / R3: growth is measured, recorded and never believed.
	if !got.SessionIDPreserved || !got.TranscriptGrew {
		t.Fatalf("Verification = %+v, want the growth measured and the id preserved", got)
	}
	if got.Proven {
		t.Fatal("Proven = true, want false: growth alone is not proof that the conversation continued")
	}
	if got.LinesBefore != 5 || got.LinesAfter != 9 || got.SizeBefore != 100 || got.SizeAfter != 180 {
		t.Errorf("measurements = %+v, want the before/after snapshots", got)
	}
	if !containsNote(got.Notes, "growth alone is not proof") {
		t.Errorf("Notes = %v, want the diagnostic disclaimer", got.Notes)
	}
	if missing := got.Missing(); len(missing) != 1 {
		t.Errorf("Missing = %v, want only the missing published state", missing)
	}
}

func TestVerifierMeasuresGrowthBySizeWhenLinesDoNotMove(t *testing.T) {
	provider := &providerStub{statusFn: func(int, string) (claude.SessionStatus, error) {
		return statusFor(sessionA, 5, 240), nil
	}}
	verifier := Verifier{Provider: provider, Timeout: -1, Interval: -1}

	got, err := verifier.Verify(context.Background(), baselineFor(sessionA), goodResult(sessionA))
	if err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	if !got.TranscriptGrew {
		t.Fatalf("Verification = %+v, want the byte-count growth measured", got)
	}
	if got.Proven {
		t.Fatal("Proven = true, want false: a byte count is not the supervisor's verdict")
	}
	if !containsNote(got.Notes, "size grew") {
		t.Errorf("Notes = %v, want the byte-count explanation", got.Notes)
	}
}

// TestVerificationProvenByPublishedState covers the one way a non-handoff resume
// can be proven now that growth is a diagnostic: the supervisor publishes a
// state that establishes the conversation continued.
func TestVerificationProvenByPublishedState(t *testing.T) {
	obs := StateObservation{SessionID: sessionA, State: "done", ObservedAt: time.Unix(1, 0).UTC(), InListing: true}
	decision := Observe(obs, true, false)

	verification := Verification{SessionIDPreserved: true, TranscriptGrew: true}
	verification.ObservePublished(obs, decision)

	if !verification.Proven {
		t.Fatalf("Proven = false, want true for a handed-off session the supervisor reports as done: %+v", verification)
	}
	if verification.PublishedState != "done" || !verification.ObservationRecorded {
		t.Errorf("Verification = %+v, want the published state recorded verbatim", verification)
	}
	if missing := verification.Missing(); len(missing) != 0 {
		t.Errorf("Missing = %v, want none", missing)
	}

	// The same observation with no handoff outstanding means the conversation
	// had already finished, which is not a resumed one.
	if decision := Observe(obs, false, false); decision.State != StateCompleted {
		t.Errorf("decision = %+v, want COMPLETED for a done session with no handoff", decision)
	}

	// A blocked session is never a recovered one.
	blocked := StateObservation{SessionID: sessionA, State: "blocked", WaitingFor: "permission prompt",
		ObservedAt: time.Unix(1, 0).UTC(), InListing: true}
	verification = Verification{SessionIDPreserved: true}
	verification.ObservePublished(blocked, Observe(blocked, true, false))
	if verification.Proven {
		t.Fatal("Proven = true for a blocked session, want false")
	}
	if verification.PublishedState != "blocked" {
		t.Errorf("PublishedState = %q, want the raw value", verification.PublishedState)
	}
}

func TestVerifierReportsUnprovenResumeOnceChecked(t *testing.T) {
	provider := &providerStub{statusFn: func(int, string) (claude.SessionStatus, error) {
		return statusFor(sessionA, 5, 100), nil
	}}
	verifier := Verifier{Provider: provider, Timeout: -1, Interval: -1}

	got, err := verifier.Verify(context.Background(), baselineFor(sessionA), goodResult(sessionA))
	if err != nil {
		t.Fatalf("Verify = %v, want nil: an unproven resume is not an error", err)
	}
	if got.Proven || got.TranscriptGrew {
		t.Fatalf("Verification = %+v, want an unproven resume", got)
	}
	if !containsNote(got.Notes, "checked once") {
		t.Errorf("Notes = %v, want the single-check explanation", got.Notes)
	}
	if reason := got.Reason(); !strings.Contains(reason, "no session state") {
		t.Errorf("Reason = %q, want it to name the missing observation", reason)
	}
}

func TestVerifierTimesOutWaitingForGrowth(t *testing.T) {
	var calls int
	provider := &providerStub{statusFn: func(int, string) (claude.SessionStatus, error) {
		calls++
		return statusFor(sessionA, 5, 100), nil
	}}
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

func TestVerifierNeverAcceptsARunningProcessAsProof(t *testing.T) {
	provider := &providerStub{statusFn: func(int, string) (claude.SessionStatus, error) {
		status := statusFor(sessionA, 5, 100)
		status.Running = true
		return status, nil
	}}
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

func TestVerifierRejectsATranscriptThatChangedSession(t *testing.T) {
	provider := &providerStub{statusFn: func(int, string) (claude.SessionStatus, error) {
		return statusFor(sessionB, 12, 300), nil
	}}
	verifier := Verifier{Provider: provider, Timeout: -1, Interval: -1}

	got, err := verifier.Verify(context.Background(), baselineFor(sessionA), goodResult(sessionA))
	if err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	if got.SessionIDPreserved {
		t.Fatal("a transcript that belongs to another session must not preserve identity")
	}
	if got.Proven {
		t.Fatal("growth of a different conversation is not proof")
	}
	if !containsNote(got.Notes, "belongs to session "+sessionB) {
		t.Errorf("Notes = %v, want the identity change to be explained", got.Notes)
	}
}

func TestVerifierWithoutProviderCannotProveGrowth(t *testing.T) {
	verifier := Verifier{Timeout: -1, Interval: -1}
	got, err := verifier.Verify(context.Background(), baselineFor(sessionA), goodResult(sessionA))
	if err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	if !got.SessionIDPreserved {
		t.Error("SessionIDPreserved = false, want true: the CLI did report the right id")
	}
	if got.Proven {
		t.Fatal("without a provider the transcript cannot be measured, so the resume is unproven")
	}
	if !containsNote(got.Notes, "no session provider") {
		t.Errorf("Notes = %v, want the missing-provider explanation", got.Notes)
	}
}

func TestVerifierRejectsAnEmptyBaselineSessionID(t *testing.T) {
	verifier := Verifier{}
	if _, err := verifier.Verify(context.Background(), Baseline{}, goodResult(sessionA)); err == nil {
		t.Fatal("Verify = nil error, want a baseline requirement error")
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

func TestVerifierReportsAnUnreadableStatus(t *testing.T) {
	boom := errors.New("transcript unreadable")
	provider := &providerStub{statusFn: func(int, string) (claude.SessionStatus, error) {
		return claude.SessionStatus{}, boom
	}}
	verifier := Verifier{Provider: provider, Timeout: -1, Interval: -1}

	_, err := verifier.Verify(context.Background(), baselineFor(sessionA), goodResult(sessionA))
	if !errors.Is(err, boom) {
		t.Fatalf("Verify error = %v, want the provider failure", err)
	}
	if !strings.Contains(err.Error(), "check session "+sessionA) {
		t.Errorf("error = %q, want it to name the session", err)
	}
}

func TestVerifierSessionIDChecks(t *testing.T) {
	cases := []struct {
		name   string
		note   string
		result claude.ResumeResult
	}{
		{
			name:   "addressed another session",
			note:   "asked to resume " + sessionA + " but the command addressed " + sessionB,
			result: claude.ResumeResult{RequestedSessionID: sessionB, SessionID: sessionA, Verified: true},
		},
		{
			name:   "no session id reported",
			note:   "reported no session id",
			result: claude.ResumeResult{RequestedSessionID: sessionA, Verified: true},
		},
		{
			name:   "a different session was created",
			note:   "reported session " + sessionB + " instead of " + sessionA,
			result: claude.ResumeResult{SessionID: sessionB, Verified: false},
		},
		{
			name:   "provider did not confirm",
			note:   "did not confirm",
			result: claude.ResumeResult{RequestedSessionID: sessionA, SessionID: sessionA, Verified: false},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verifier := Verifier{Timeout: -1, Interval: -1}
			got, err := verifier.Verify(context.Background(), baselineFor(sessionA), tc.result)
			if err != nil {
				t.Fatalf("Verify = %v, want nil", err)
			}
			if got.SessionIDPreserved {
				t.Fatal("SessionIDPreserved = true, want false")
			}
			if got.Proven {
				t.Fatal("Proven = true, want false")
			}
			if !containsNote(got.Notes, tc.note) {
				t.Errorf("Notes = %v, want a note containing %q", got.Notes, tc.note)
			}
		})
	}
}

func TestMeasureGrowthWithoutABaselineSnapshot(t *testing.T) {
	baseline := Baseline{SessionID: sessionA}
	verification := Verification{}

	if measureGrowth(baseline, statusFor(sessionA, 4, 90), &verification) {
		t.Fatal("growth must not be claimed without a baseline snapshot")
	}
	if !containsNote(verification.Notes, "before the resume was unknown") {
		t.Errorf("Notes = %v, want the unknown-baseline explanation", verification.Notes)
	}

	verification = Verification{}
	if measureGrowth(baseline, statusFor(sessionA, 0, 0), &verification) {
		t.Fatal("an empty transcript proves nothing")
	}
	if len(verification.Notes) != 0 {
		t.Errorf("Notes = %v, want none when there was nothing to compare", verification.Notes)
	}

	baseline = Baseline{SessionID: sessionA, TranscriptLines: 5}
	verification = Verification{}
	if measureGrowth(baseline, statusFor(sessionA, 5, 400), &verification) {
		t.Fatal("a size increase without a baseline size cannot prove growth")
	}
}

func TestVerificationMissingAndReason(t *testing.T) {
	empty := Verification{}
	if got := empty.Missing(); len(got) != 2 {
		t.Errorf("Missing() = %v, want both checks", got)
	}
	partial := Verification{SessionIDPreserved: true}
	if got := partial.Missing(); len(got) != 1 {
		t.Errorf("Missing() = %v, want only the missing published state", got)
	}
	// A preserved id and a handoff that was never proven: nothing is missing,
	// because a handoff has no checks to miss, so the reason is the generic
	// refusal to claim success.
	unproven := Verification{SessionIDPreserved: true, HandoffAccepted: true}
	if got := unproven.Reason(); got != "the resume could not be verified" {
		t.Errorf("Reason() = %q, want the generic unproven message", got)
	}
	if got := empty.Reason(); !strings.Contains(got, "session id") || !strings.Contains(got, "no session state") {
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
	proven := Verification{SessionIDPreserved: true, TranscriptGrew: true, Proven: true}
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
