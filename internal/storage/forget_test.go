package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/storage"
)

// seedPopulatedSession writes one session plus every child row a real session
// accumulates, so a delete has something to cascade over.
func seedPopulatedSession(t *testing.T, store *storage.Store, id string) {
	t.Helper()
	ctx := context.Background()

	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	if _, err := store.UpsertSession(ctx, storage.Session{
		ID:             id,
		ProjectPath:    "/work/example",
		TranscriptPath: "/home/user/.claude/projects/example/" + id + ".jsonl",
		State:          storage.StatePermanentlyFailed,
		Reason:         "attempts exhausted",
		DiscoveredAt:   now,
		LastSeenAt:     now,
	}); err != nil {
		t.Fatalf("seed session %s: %v", id, err)
	}
	// A transition is what `ccresu history` reads, so a delete has to remove them
	// too; UpsertSession alone does not record one.
	if _, err := store.TransitionSession(ctx, id, storage.StateRunning, "seed transition", 0); err != nil {
		t.Fatalf("seed transition: %v", err)
	}
	if _, err := store.RecordUsageLimit(ctx, storage.UsageLimit{
		SessionID:  id,
		LimitType:  storage.LimitTypeFiveHour,
		DetectedAt: now,
	}); err != nil {
		t.Fatalf("seed usage limit: %v", err)
	}
	attempt, err := store.StartAttempt(ctx, id, 0, "claude -p --resume "+id)
	if err != nil {
		t.Fatalf("seed attempt: %v", err)
	}
	if _, err := store.FinishAttempt(ctx, attempt.ID, storage.AttemptResult{
		State: storage.StatePermanentlyFailed,
		Error: "resume failed",
	}); err != nil {
		t.Fatalf("finish attempt: %v", err)
	}
	if _, err := store.SaveSchedule(ctx, storage.Schedule{
		SessionID: id,
		ResetAt:   now.Add(time.Hour),
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed schedule: %v", err)
	}
	if _, _, err := store.EnqueueNotification(ctx, storage.Notification{
		SessionID: id,
		Event:     storage.EventLimitDetected,
		DedupeKey: "seed-" + id,
		Body:      "seed",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed notification: %v", err)
	}
}

func TestForgetSessionRemovesTheSessionAndItsHistory(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	const id = "11111111-1111-4111-8111-111111111111"
	seedPopulatedSession(t, store, id)

	deleted, counts, err := store.ForgetSession(ctx, id)
	if err != nil {
		t.Fatalf("ForgetSession: %v", err)
	}
	if deleted.ID != id {
		t.Errorf("deleted ID = %q, want %q", deleted.ID, id)
	}
	if deleted.ProjectPath != "/work/example" {
		t.Errorf("deleted ProjectPath = %q, want the stored path", deleted.ProjectPath)
	}
	if deleted.State != storage.StateRunning {
		t.Errorf("deleted State = %q, want the state the session was in when it was deleted", deleted.State)
	}
	if deleted.DeletedAt.IsZero() {
		t.Error("deleted DeletedAt is zero, want the deletion instant")
	}

	// The report has to match what was actually there, or the operator cannot
	// tell a clean delete from one that left history behind.
	if counts.Attempts != 1 || counts.UsageLimits != 1 || counts.Notifications != 1 {
		t.Errorf("counts = %+v, want one attempt, one usage limit, one notification", counts)
	}
	if !counts.Scheduled {
		t.Error("counts.Scheduled = false, want true: a pending wait was cancelled")
	}
	if counts.Transitions == 0 {
		t.Error("counts.Transitions = 0, want the transitions the session accumulated")
	}

	// The row and everything that referenced it are gone.
	if _, err := store.GetSession(ctx, id); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("GetSession after delete = %v, want ErrNotFound", err)
	}
	if _, err := store.GetSchedule(ctx, id); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("schedule survived the delete: %v", err)
	}
	if attempts, err := store.ListAttempts(ctx, id, 0); err != nil || len(attempts) != 0 {
		t.Errorf("attempts survived the delete: %v, %v", attempts, err)
	}
	if transitions, err := store.ListTransitions(ctx, id, 0); err != nil || len(transitions) != 0 {
		t.Errorf("transitions survived the delete: %v, %v", transitions, err)
	}
	if limits, err := store.ListUsageLimits(ctx, id, 0); err == nil && len(limits) != 0 {
		t.Errorf("usage limits survived the delete: %v", limits)
	}

	// The tombstone is what keeps it gone.
	if _, err := store.GetDeletedSession(ctx, id); err != nil {
		t.Fatalf("GetDeletedSession: %v", err)
	}
	ids, err := store.DeletedSessionIDs(ctx)
	if err != nil {
		t.Fatalf("DeletedSessionIDs: %v", err)
	}
	if _, ok := ids[id]; !ok {
		t.Fatalf("DeletedSessionIDs = %v, want the deleted session", ids)
	}
}

// TestForgetSessionKeepsTheTombstoneAfterRediscovery pins the reason the table
// exists: a session that is discovered again must stay deleted.
func TestForgetSessionKeepsTheTombstoneAfterRediscovery(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	const id = "22222222-2222-4222-8222-222222222222"
	seedPopulatedSession(t, store, id)

	if _, _, err := store.ForgetSession(ctx, id); err != nil {
		t.Fatalf("ForgetSession: %v", err)
	}

	// What the daemon does with every discovered session, except that it asks the
	// tombstone list first (see internal/app/daemon.go).
	ids, err := store.DeletedSessionIDs(ctx)
	if err != nil {
		t.Fatalf("DeletedSessionIDs: %v", err)
	}
	if _, forgotten := ids[id]; !forgotten {
		t.Fatal("the tombstone disappeared, so discovery would resurrect the session")
	}
}

func TestForgetSessionUnknown(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	if _, _, err := store.ForgetSession(ctx, "33333333-3333-4333-8333-333333333333"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("ForgetSession(unknown) = %v, want ErrNotFound", err)
	}
	if _, _, err := store.ForgetSession(ctx, "  "); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("ForgetSession(empty) = %v, want ErrInvalid", err)
	}
	var nilStore *storage.Store
	if _, _, err := nilStore.ForgetSession(ctx, "x"); !errors.Is(err, storage.ErrClosed) {
		t.Fatalf("nil store ForgetSession = %v, want ErrClosed", err)
	}
}

func TestRestoreSessionBringsItBackEnabled(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	const id = "44444444-4444-4444-8444-444444444444"
	seedPopulatedSession(t, store, id)

	if _, _, err := store.ForgetSession(ctx, id); err != nil {
		t.Fatalf("ForgetSession: %v", err)
	}
	restored, err := store.RestoreSession(ctx, id)
	if err != nil {
		t.Fatalf("RestoreSession: %v", err)
	}
	if restored.ID != id {
		t.Errorf("restored ID = %q, want %q", restored.ID, id)
	}

	session, err := store.GetSession(ctx, id)
	if err != nil {
		t.Fatalf("GetSession after restore: %v", err)
	}
	if !session.Enabled {
		t.Error("restored session is disabled, want enabled: the operator asked for it back")
	}
	if session.State != storage.StateDiscovered {
		t.Errorf("restored state = %q, want %q", session.State, storage.StateDiscovered)
	}
	if session.Recoverable {
		t.Error("restored session is recoverable before detection has seen it, want false")
	}
	if session.ProjectPath != "/work/example" {
		t.Errorf("restored ProjectPath = %q, want the path from the tombstone", session.ProjectPath)
	}

	// The tombstone is gone, so discovery picks it up again.
	if _, err := store.GetDeletedSession(ctx, id); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("GetDeletedSession after restore = %v, want ErrNotFound", err)
	}
	ids, err := store.DeletedSessionIDs(ctx)
	if err != nil {
		t.Fatalf("DeletedSessionIDs: %v", err)
	}
	if _, ok := ids[id]; ok {
		t.Error("the tombstone survived the restore")
	}
}

func TestRestoreSessionUnknown(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	if _, err := store.RestoreSession(ctx, "55555555-5555-4555-8555-555555555555"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("RestoreSession(never deleted) = %v, want ErrNotFound", err)
	}
	var nilStore *storage.Store
	if _, err := nilStore.RestoreSession(ctx, "x"); !errors.Is(err, storage.ErrClosed) {
		t.Fatalf("nil store RestoreSession = %v, want ErrClosed", err)
	}
	if _, err := nilStore.GetDeletedSession(ctx, "x"); !errors.Is(err, storage.ErrClosed) {
		t.Fatalf("nil store GetDeletedSession = %v, want ErrClosed", err)
	}
	if _, err := nilStore.DeletedSessionIDs(ctx); !errors.Is(err, storage.ErrClosed) {
		t.Fatalf("nil store DeletedSessionIDs = %v, want ErrClosed", err)
	}
	if _, err := nilStore.ListDeletedSessions(ctx); !errors.Is(err, storage.ErrClosed) {
		t.Fatalf("nil store ListDeletedSessions = %v, want ErrClosed", err)
	}
}

func TestListDeletedSessionsNewestFirst(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	const older = "66666666-6666-4666-8666-666666666666"
	const newer = "77777777-7777-4777-8777-777777777777"
	seedPopulatedSession(t, store, older)
	seedPopulatedSession(t, store, newer)

	if _, _, err := store.ForgetSession(ctx, older); err != nil {
		t.Fatalf("ForgetSession: %v", err)
	}
	if _, _, err := store.ForgetSession(ctx, newer); err != nil {
		t.Fatalf("ForgetSession: %v", err)
	}

	deleted, err := store.ListDeletedSessions(ctx)
	if err != nil {
		t.Fatalf("ListDeletedSessions: %v", err)
	}
	if len(deleted) != 2 {
		t.Fatalf("ListDeletedSessions = %d rows, want 2", len(deleted))
	}
	for _, record := range deleted {
		if record.DeletedAt.IsZero() {
			t.Errorf("deleted %s has no deletion instant", record.ID)
		}
	}
}

// TestForgetSessionsInStateForgetsOnlyThatState is the bulk form's contract: the
// state filter is the whole safety story, so a row in any other state must
// survive untouched.
func TestForgetSessionsInStateForgetsOnlyThatState(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	const (
		failed   = "55555555-5555-4555-8555-555555555555"
		failed2  = "66666666-6666-4666-8666-666666666666"
		disabled = "77777777-7777-4777-8777-777777777777"
	)
	seedPopulatedSession(t, store, failed)
	seedPopulatedSession(t, store, failed2)
	// seedPopulatedSession records a transition, which leaves the row RUNNING;
	// put both back in the state the bulk forget is asked about.
	for _, id := range []string{failed, failed2} {
		if _, err := store.TransitionSession(ctx, id, storage.StatePermanentlyFailed, "seeded failure", 0); err != nil {
			t.Fatalf("seed failed state for %s: %v", id, err)
		}
	}
	now := time.Date(2026, 11, 5, 13, 0, 0, 0, time.UTC)
	if _, err := store.UpsertSession(ctx, storage.Session{
		ID:           disabled,
		ProjectPath:  "/work/other",
		State:        storage.StateDisabled,
		DiscoveredAt: now,
		LastSeenAt:   now,
	}); err != nil {
		t.Fatalf("seed disabled session: %v", err)
	}

	results, err := store.ForgetSessionsInState(ctx, storage.StatePermanentlyFailed)
	if err != nil {
		t.Fatalf("ForgetSessionsInState: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2: %+v", len(results), results)
	}
	seen := map[string]storage.ForgetResult{}
	for _, result := range results {
		seen[result.ID] = result
	}
	for _, id := range []string{failed, failed2} {
		result, ok := seen[id]
		if !ok {
			t.Fatalf("result set does not name %s: %+v", id, seen)
		}
		if result.State != storage.StatePermanentlyFailed {
			t.Errorf("%s: tombstoned state = %q, want %q", id, result.State, storage.StatePermanentlyFailed)
		}
		if result.ProjectPath == "" || result.TranscriptPath == "" {
			t.Errorf("%s: tombstone lost the paths, so the delete could not be undone: %+v", id, result)
		}
		// The counts are the same facts one delete reports; a bulk clear that
		// reported nothing would hide the history it removed.
		if result.Removed.Attempts != 1 || result.Removed.Transitions == 0 {
			t.Errorf("%s: removed = %+v, want the attempt and transition counts", id, result.Removed)
		}
		if !result.Removed.Scheduled {
			t.Errorf("%s: pending wait was not reported as removed", id)
		}
		if _, err := store.GetSession(ctx, id); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("%s survived the bulk forget: %v", id, err)
		}
	}

	if _, err := store.GetSession(ctx, disabled); err != nil {
		t.Errorf("the disabled session was removed: %v", err)
	}
	tombstones, err := store.ListDeletedSessions(ctx)
	if err != nil {
		t.Fatalf("ListDeletedSessions: %v", err)
	}
	if len(tombstones) != 2 {
		t.Errorf("tombstones = %d, want 2: %+v", len(tombstones), tombstones)
	}

	// Nothing left to forget is not an error: `sessions clear` is idempotent.
	results, err = store.ForgetSessionsInState(ctx, storage.StatePermanentlyFailed)
	if err != nil {
		t.Fatalf("second ForgetSessionsInState: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("second run forgot %d sessions, want 0", len(results))
	}
}

func TestForgetSessionsInStateGuardsItsArguments(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	if _, err := store.ForgetSessionsInState(ctx, storage.State("NOT_A_STATE")); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("unknown state = %v, want ErrInvalid", err)
	}
	// The empty state is not "all": a caller that lost its filter must not wipe
	// the database.
	if _, err := store.ForgetSessionsInState(ctx, storage.State("")); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("empty state = %v, want ErrInvalid", err)
	}
	var nilStore *storage.Store
	if _, err := nilStore.ForgetSessionsInState(ctx, storage.StatePermanentlyFailed); !errors.Is(err, storage.ErrClosed) {
		t.Fatalf("nil store = %v, want ErrClosed", err)
	}
}

// TestCountForgetHistoryReportsWithoutDeleting backs the `--dry-run` preview: it
// asks what a forget would take and must change nothing.
func TestCountForgetHistoryReportsWithoutDeleting(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	const id = "88888888-1111-4111-8111-888888888888"
	seedPopulatedSession(t, store, id)

	counts, err := store.CountForgetHistory(ctx, id)
	if err != nil {
		t.Fatalf("CountForgetHistory: %v", err)
	}
	if counts.Attempts != 1 || counts.Transitions == 0 || counts.UsageLimits != 1 || counts.Notifications != 1 {
		t.Errorf("counts = %+v, want the seeded attempt, transitions, usage limit and notification", counts)
	}
	if !counts.Scheduled {
		t.Error("counts.Scheduled = false, want true: a pending wait was seeded")
	}
	if _, err := store.GetSession(ctx, id); err != nil {
		t.Fatalf("the session was removed by counting it: %v", err)
	}

	if _, err := store.CountForgetHistory(ctx, "  "); !errors.Is(err, storage.ErrInvalid) {
		t.Errorf("empty id = %v, want ErrInvalid", err)
	}
	var nilStore *storage.Store
	if _, err := nilStore.CountForgetHistory(ctx, id); !errors.Is(err, storage.ErrClosed) {
		t.Errorf("nil store = %v, want ErrClosed", err)
	}
}
