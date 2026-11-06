package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jymchng/ccresu/internal/recovery"
	"github.com/jymchng/ccresu/internal/storage"
)

// TestSessionDTOReportsTheContinuation pins the wire name of the session the
// resumed conversation carried on under. A dispatch does not keep the id it was
// given, so the id ccresu tracks is not where the transcript keeps growing, and
// an API consumer that only sees `id` would look in the wrong place.
func TestSessionDTOReportsTheContinuation(t *testing.T) {
	const (
		resumed      = "11111111-1111-4111-8111-111111111111"
		continuation = "22222222-2222-4222-8222-222222222222"
	)

	raw, err := json.Marshal(toSessionDTO(storage.Session{
		ID:                    resumed,
		State:                 storage.StateResumed,
		ContinuationSessionID: continuation,
	}))
	if err != nil {
		t.Fatalf("marshal session dto: %v", err)
	}
	if !strings.Contains(string(raw), `"continuation_session_id":"`+continuation+`"`) {
		t.Errorf("session dto does not name the continuation: %s", raw)
	}

	// A session that was never resumed must not grow the key, so an existing
	// consumer sees exactly the object it saw before.
	raw, err = json.Marshal(toSessionDTO(storage.Session{ID: resumed}))
	if err != nil {
		t.Fatalf("marshal session dto: %v", err)
	}
	if strings.Contains(string(raw), "continuation_session_id") {
		t.Errorf("an un-resumed session reports a continuation: %s", raw)
	}
}

// TestAttemptDTOReportsContinuationNotASessionIDCheck pins both renames: the
// verdict is that a continuation was reported, and the id it reported is its own
// field. The word "verified" promised a check the dispatch cannot make.
func TestAttemptDTOReportsContinuationNotASessionIDCheck(t *testing.T) {
	confirmed := true
	const continuation = "22222222-2222-4222-8222-222222222222"

	raw, err := json.Marshal(toAttemptDTO(storage.RecoveryAttempt{
		ID:                    1,
		SessionID:             "11111111-1111-4111-8111-111111111111",
		SessionIDVerified:     &confirmed,
		ContinuationSessionID: continuation,
	}))
	if err != nil {
		t.Fatalf("marshal attempt dto: %v", err)
	}
	encoded := string(raw)
	if !strings.Contains(encoded, `"continuation_confirmed":true`) {
		t.Errorf("attempt dto does not report the continuation verdict: %s", encoded)
	}
	if !strings.Contains(encoded, `"continuation_session_id":"`+continuation+`"`) {
		t.Errorf("attempt dto does not report the continuation id: %s", encoded)
	}
	if strings.Contains(encoded, "session_id_verified") {
		t.Errorf("attempt dto still carries the retired key: %s", encoded)
	}
}

// TestVerificationDTOReportsWhereTheGrowthWasMeasured keeps the proof honest: the
// numbers belong to another session's transcript, so the DTO says which one.
func TestVerificationDTOReportsWhereTheGrowthWasMeasured(t *testing.T) {
	raw, err := json.Marshal(toVerificationDTO(recovery.Verification{
		JobID:                      "93874eb3",
		ContinuationSessionID:      "22222222-2222-4222-8222-222222222222",
		ContinuationTranscriptPath: "/home/u/.claude/projects/p/22222222.jsonl",
		ContinuationConfirmed:      true,
		TranscriptGrew:             true,
		Proven:                     true,
	}))
	if err != nil {
		t.Fatalf("marshal verification dto: %v", err)
	}
	encoded := string(raw)
	for _, want := range []string{
		`"continuation_session_id":"22222222-2222-4222-8222-222222222222"`,
		`"continuation_transcript_path":"/home/u/.claude/projects/p/22222222.jsonl"`,
		`"continuation_confirmed":true`,
	} {
		if !strings.Contains(encoded, want) {
			t.Errorf("verification dto is missing %s: %s", want, encoded)
		}
	}
}
