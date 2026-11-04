package storage_test

import (
	"context"
	"testing"

	"github.com/jymchng/ccresu/internal/storage"
)

// TestListTransitionsSinceFollowsTheFeed covers the cross-session scan the SSE
// endpoint uses: ids are monotonic, so a consumer remembers the highest id it
// has seen and asks only for what came after it.
func TestListTransitionsSinceFollowsTheFeed(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	for _, id := range []string{"feed-a", "feed-b"} {
		if _, err := store.UpsertSession(ctx, storage.Session{
			ID: id, ProjectPath: "/work", Type: storage.SessionTypeInteractive,
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	all, err := store.ListTransitionsSince(ctx, 0, 0)
	if err != nil {
		t.Fatalf("since 0: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("new store has %d transitions, want 0", len(all))
	}

	for _, step := range []struct {
		id    string
		state storage.State
	}{
		{"feed-a", storage.StateRunning},
		{"feed-b", storage.StateRunning},
		{"feed-a", storage.StateUsageLimitDetected},
	} {
		if _, err := store.TransitionSession(ctx, step.id, step.state, "seed", 0); err != nil {
			t.Fatalf("transition %s -> %s: %v", step.id, step.state, err)
		}
	}

	all, err = store.ListTransitionsSince(ctx, 0, 0)
	if err != nil {
		t.Fatalf("since 0: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d transitions, want 3", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i].ID <= all[i-1].ID {
			t.Fatalf("ids are not ascending: %d then %d", all[i-1].ID, all[i].ID)
		}
	}

	// Asking from the first id returns only the two rows after it.
	tail, err := store.ListTransitionsSince(ctx, all[0].ID, 0)
	if err != nil {
		t.Fatalf("since %d: %v", all[0].ID, err)
	}
	if len(tail) != 2 {
		t.Fatalf("got %d trailing transitions, want 2", len(tail))
	}
	if tail[0].SessionID != "feed-b" || tail[1].ToState != storage.StateUsageLimitDetected {
		t.Fatalf("unexpected tail: %+v", tail)
	}
	if tail[0].FromState != storage.StateDiscovered {
		t.Errorf("from state = %q, want %q", tail[0].FromState, storage.StateDiscovered)
	}

	// The limit is honoured, and asking past the end yields nothing.
	capped, err := store.ListTransitionsSince(ctx, 0, 1)
	if err != nil {
		t.Fatalf("limited: %v", err)
	}
	if len(capped) != 1 || capped[0].ID != all[0].ID {
		t.Fatalf("limit ignored: %+v", capped)
	}
	empty, err := store.ListTransitionsSince(ctx, all[len(all)-1].ID, 0)
	if err != nil {
		t.Fatalf("since latest: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("got %d transitions past the end, want 0", len(empty))
	}
}
