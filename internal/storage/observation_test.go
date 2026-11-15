package storage

// PRD 2 / R13 and R8, at the storage boundary.
//
// Recorded is the predicate that keeps two different facts apart: "no
// observation was made" and "an observation found nothing". The zero record
// means the former, and every call site \u2014 the attempt writer and `ccresu
// inspect` \u2014 branches on it, so it is worth pinning here rather than only
// through a caller.

import (
	"testing"
	"time"
)

func TestObservationRecordRecorded(t *testing.T) {
	t.Parallel()

	listed := false
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name      string
		record    ObservationRecord
		want      bool
		whyItIsSo string
	}{
		{
			name:      "zero record is not recorded",
			record:    ObservationRecord{},
			want:      false,
			whyItIsSo: "an attempt from before the observation columns existed never looked, which is not the same as finding nothing",
		},
		{
			name:      "a timestamp alone is enough",
			record:    ObservationRecord{ObservedAt: at},
			want:      true,
			whyItIsSo: "the read happened, even if the CLI published no state for the session",
		},
		{
			name:      "a listing verdict alone is enough",
			record:    ObservationRecord{Listed: &listed, ObservedAt: at},
			want:      true,
			whyItIsSo: "listed=false is a recorded absence, which is an observation rather than a missing one",
		},
		{
			name:      "state without a timestamp is still recorded",
			record:    ObservationRecord{State: "blocked", Listed: &listed},
			want:      true,
			whyItIsSo: "a partially written row must not be presented as 'not recorded' when it plainly carries a value",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.record.Recorded(); got != tc.want {
				t.Errorf("Recorded() = %v, want %v: %s", got, tc.want, tc.whyItIsSo)
			}
		})
	}
}
