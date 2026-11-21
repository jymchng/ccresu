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

// goodResult is a resume whose turn ran to its end: exit 0 is the whole verdict.
func goodResult(id string) claude.ResumeResult {
	return claude.ResumeResult{RequestedSessionID: id, ExitCode: 0}
}

// statusFor builds a status reading with the given transcript measurement.
func statusFor(id string, lines, size int64) claude.SessionStatus {
	return claude.SessionStatus{
		Session:    claude.Session{ID: id, Recoverable: true},
		Transcript: claude.TranscriptInfo{Path: "/t/" + id + ".jsonl", Lines: lines, Size: size},
	}
}

// TestMeasureRecordsGrowthAsADiagnostic is R3: the transcript is measured after
// the turn, the measurement is recorded, and it is never promoted into a second
// opinion about work the exit status already settled.
func TestMeasureRecordsGrowthAsADiagnostic(t *testing.T) {
	provider := &providerStub{statusFn: func(int, string) (claude.SessionStatus, error) {
		return statusFor(sessionA, 9, 180), nil
	}}
	recorder := Recorder{Provider: provider}

	got := recorder.Measure(context.Background(), baselineFor(sessionA), goodResult(sessionA))
	if !got.Proven {
		t.Fatalf("Verification = %+v, want Proven: the resume exited zero", got)
	}
	if !got.TranscriptGrew {
		t.Errorf("TranscriptGrew = false, want true: the transcript advanced")
	}
	if got.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", got.ExitCode)
	}
	if got.LinesBefore != 5 || got.LinesAfter != 9 || got.SizeBefore != 100 || got.SizeAfter != 180 {
		t.Errorf("measurements = %+v, want the before/after snapshots", got)
	}
	if len(got.Notes) != 0 {
		t.Errorf("Notes = %v, want none: every measurement was taken", got.Notes)
	}
}

// TestMeasureUsesTheExitStatusAsTheVerdict pins the split: a turn that failed
// is not proven even when the transcript happened to grow, because the exit
// status is the verdict and growth is only a record.
func TestMeasureUsesTheExitStatusAsTheVerdict(t *testing.T) {
	provider := &providerStub{statusFn: func(int, string) (claude.SessionStatus, error) {
		return statusFor(sessionA, 9, 180), nil
	}}
	recorder := Recorder{Provider: provider}

	result := claude.ResumeResult{RequestedSessionID: sessionA, ExitCode: 3}
	got := recorder.Measure(context.Background(), baselineFor(sessionA), result)
	if got.Proven {
		t.Fatal("Proven = true, want false: a non-zero exit is not a finished turn")
	}
	if got.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want the resume's own status 3", got.ExitCode)
	}
	if !strings.Contains(got.Reason(), "did not finish its turn") || !strings.Contains(got.Reason(), "exit 3") {
		t.Errorf("Reason = %q, want it to name the exit status", got.Reason())
	}
}

func TestMeasureGrowthBySizeWhenLinesDoNotMove(t *testing.T) {
	provider := &providerStub{statusFn: func(int, string) (claude.SessionStatus, error) {
		return statusFor(sessionA, 5, 240), nil
	}}
	recorder := Recorder{Provider: provider}

	got := recorder.Measure(context.Background(), baselineFor(sessionA), goodResult(sessionA))
	if !got.TranscriptGrew {
		t.Fatalf("Verification = %+v, want the byte-count growth measured", got)
	}
	if !containsNote(got.Notes, "size grew") {
		t.Errorf("Notes = %v, want the byte-count explanation", got.Notes)
	}
}

func TestMeasureWithoutAProviderRecordsTheGap(t *testing.T) {
	got := Recorder{}.Measure(context.Background(), baselineFor(sessionA), goodResult(sessionA))
	if !got.Proven {
		t.Error("Proven = false, want true: the verdict is the exit status, not the measurement")
	}
	if got.TranscriptGrew {
		t.Error("TranscriptGrew = true, want false: nothing was read")
	}
	if !containsNote(got.Notes, "no session provider") {
		t.Errorf("Notes = %v, want the missing-provider explanation", got.Notes)
	}
}

// TestMeasureNeverFailsTheRecovery is the point of returning no error: the turn
// already finished, so a read taken afterwards cannot revise it.
func TestMeasureNeverFailsTheRecovery(t *testing.T) {
	provider := &providerStub{statusFn: func(int, string) (claude.SessionStatus, error) {
		return claude.SessionStatus{}, errors.New("transcript unreadable")
	}}

	got := Recorder{Provider: provider}.Measure(context.Background(), baselineFor(sessionA), goodResult(sessionA))
	if !got.Proven {
		t.Error("Proven = false, want true: an unreadable transcript does not undo a finished turn")
	}
	if got.TranscriptGrew {
		t.Error("TranscriptGrew = true, want false: nothing was measured")
	}
	if !containsNote(got.Notes, "not measured") {
		t.Errorf("Notes = %v, want the read failure recorded", got.Notes)
	}
	if got.LinesAfter != got.LinesBefore || got.SizeAfter != got.SizeBefore {
		t.Errorf("measurements = %+v, want the baseline restated when nothing was read", got)
	}
}

func TestMeasureHonoursACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got := Recorder{Provider: &providerStub{}}.Measure(ctx, baselineFor(sessionA), goodResult(sessionA))
	if got.TranscriptGrew {
		t.Error("TranscriptGrew = true, want false: no read was attempted")
	}
	if !containsNote(got.Notes, "context canceled") {
		t.Errorf("Notes = %v, want the cancellation recorded", got.Notes)
	}
}

func TestMeasureReadsTheSessionOnce(t *testing.T) {
	provider := &providerStub{statusFn: func(int, string) (claude.SessionStatus, error) {
		return statusFor(sessionA, 7, 140), nil
	}}
	_ = Recorder{Provider: provider}.Measure(context.Background(), baselineFor(sessionA), goodResult(sessionA))
	if got := provider.statusCount(); got != 1 {
		t.Errorf("status reads = %d, want 1: the turn is over, so there is nothing to wait for", got)
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

func TestVerificationReason(t *testing.T) {
	cases := []struct {
		name         string
		verification Verification
		want         string
	}{
		{"unfinished turn", Verification{ExitCode: 1}, "the resume did not finish its turn (exit 1)"},
		{"turn ran, transcript grew", Verification{Proven: true, TranscriptGrew: true}, "the resume finished its turn and the transcript grew"},
		{"turn ran, transcript unchanged", Verification{Proven: true}, "the resume finished its turn"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.verification.Reason(); got != tc.want {
				t.Errorf("Reason() = %q, want %q", got, tc.want)
			}
		})
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

// containsNote reports whether any note contains want.
func containsNote(notes []string, want string) bool {
	for _, note := range notes {
		if strings.Contains(note, want) {
			return true
		}
	}
	return false
}

// TestMeasureDoesNotWait pins the property the old verifier lacked: one read
// and no polling. A handoff needed a poll because ccresu could not see the end
// of the work; the resume now returns when the turn is over.
func TestMeasureDoesNotWait(t *testing.T) {
	var calls int
	provider := &providerStub{statusFn: func(int, string) (claude.SessionStatus, error) {
		calls++
		return statusFor(sessionA, 5, 100), nil
	}}

	start := time.Now()
	got := Recorder{Provider: provider}.Measure(context.Background(), baselineFor(sessionA), goodResult(sessionA))
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Measure took %s, want it to return after a single read", elapsed)
	}
	if calls != 1 {
		t.Errorf("provider was checked %d times, want exactly 1", calls)
	}
	if got.TranscriptGrew {
		t.Error("TranscriptGrew = true, want false: nothing moved")
	}
}
