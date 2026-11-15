package storage_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/storage"
)

// blockedSession seeds a session and moves it to WAITING_FOR_INPUT, which is the
// state an attention episode is allowed to describe.
func blockedSession(t *testing.T, store *storage.Store, id string) storage.Session {
	t.Helper()
	session := mustSeedSession(t, store, id)
	if _, err := store.TransitionSession(context.Background(), session.ID,
		storage.StateWaitingForInput, "supervisor published blocked", 0); err != nil {
		t.Fatalf("transition %s to WAITING_FOR_INPUT: %v", session.ID, err)
	}
	return session
}

// TestAttentionEpisodeLifecycle walks the open/read/close path: the first
// observer's instant is the episode's identity, a second observation does not
// move it, and closing it makes the next block a new episode.
func TestAttentionEpisodeLifecycle(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	session := blockedSession(t, store, "attention-lifecycle")

	started, err := store.BeginAttentionEpisode(ctx, session.ID, "permission prompt", time.Time{})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if !started.Equal(fixedTime) {
		t.Fatalf("started_at = %s, want the store clock %s", started, fixedTime)
	}

	episode, err := store.GetAttentionEpisode(ctx, session.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if episode.SessionID != session.ID || episode.Reason != "permission prompt" {
		t.Fatalf("round trip lost data: %+v", episode)
	}
	if !episode.StartedAt.Equal(fixedTime) {
		t.Fatalf("round trip started_at = %s, want %s", episode.StartedAt, fixedTime)
	}

	// A second observer — the recovery pass after the hook, or the other way
	// round — must not rewrite the instant or the reason, because both producers
	// derive the dedupe key from them.
	later := fixedTime.Add(90 * time.Second)
	again, err := store.BeginAttentionEpisode(ctx, session.ID, "a different reason", later)
	if err != nil {
		t.Fatalf("second begin: %v", err)
	}
	if !again.Equal(fixedTime) {
		t.Fatalf("second begin returned %s, want the first observer's instant %s", again, fixedTime)
	}
	kept, err := store.GetAttentionEpisode(ctx, session.ID)
	if err != nil {
		t.Fatalf("get after second begin: %v", err)
	}
	if !kept.StartedAt.Equal(fixedTime) || kept.Reason != "permission prompt" {
		t.Fatalf("second begin overwrote the episode: %+v", kept)
	}

	if _, ok, err := store.AttentionEpisodeStart(ctx, session.ID); err != nil || !ok {
		t.Fatalf("AttentionEpisodeStart = ok=%t err=%v, want an open episode", ok, err)
	}

	if err := store.EndAttentionEpisode(ctx, session.ID); err != nil {
		t.Fatalf("end: %v", err)
	}
	if _, err := store.GetAttentionEpisode(ctx, session.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("get after end = %v, want ErrNotFound", err)
	}
	if start, ok, err := store.AttentionEpisodeStart(ctx, session.ID); err != nil || ok || !start.IsZero() {
		t.Fatalf("AttentionEpisodeStart after end = %s ok=%t err=%v, want absent", start, ok, err)
	}

	// The session's next block is a new episode with its own identity, which is
	// the whole reason the row is closed rather than reused.
	next := fixedTime.Add(time.Hour)
	restarted, err := store.BeginAttentionEpisode(ctx, session.ID, "", next)
	if err != nil {
		t.Fatalf("begin after end: %v", err)
	}
	if !restarted.Equal(next) {
		t.Fatalf("new episode started_at = %s, want %s", restarted, next)
	}
	reopened, err := store.GetAttentionEpisode(ctx, session.ID)
	if err != nil {
		t.Fatalf("get reopened: %v", err)
	}
	if reopened.Reason != "" {
		t.Fatalf("reopened reason = %q, want the empty reason preserved rather than the old one", reopened.Reason)
	}
}

// TestAttentionEpisodeTruncatesToSeconds proves the stored instant is the one
// both producers will compare, so a nanosecond difference between two callers
// cannot produce two keys for one episode.
func TestAttentionEpisodeTruncatesToSeconds(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	session := blockedSession(t, store, "attention-truncate")

	precise := time.Date(2026, 7, 8, 9, 10, 11, 987654321, time.FixedZone("CEST", 2*60*60))
	started, err := store.BeginAttentionEpisode(ctx, session.ID, "", precise)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	want := time.Date(2026, 7, 8, 7, 10, 11, 0, time.UTC)
	if !started.Equal(want) {
		t.Fatalf("started_at = %s, want %s (UTC, whole seconds)", started, want)
	}
}

// TestBeginAttentionEpisodeIsIdempotentUnderConcurrency proves the ON CONFLICT
// insert is enough: racing producers all read back one instant, so they compute
// one dedupe key.
func TestBeginAttentionEpisodeIsIdempotentUnderConcurrency(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	session := blockedSession(t, store, "attention-race")

	const goroutines = 8
	starts := make(chan time.Time, goroutines)
	errs := make(chan error, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			at := fixedTime.Add(time.Duration(i) * time.Second)
			start, err := store.BeginAttentionEpisode(ctx, session.ID, "race", at)
			if err != nil {
				errs <- err
				return
			}
			starts <- start
		}(i)
	}
	wg.Wait()
	close(starts)
	close(errs)
	for err := range errs {
		t.Fatalf("racing begin: %v", err)
	}

	seen := map[time.Time]bool{}
	for start := range starts {
		seen[start] = true
	}
	if len(seen) != 1 {
		t.Fatalf("racing producers saw %d distinct instants, want exactly 1: %v", len(seen), seen)
	}
	var rows int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM attention_episodes`).Scan(&rows); err != nil {
		t.Fatalf("count episodes: %v", err)
	}
	if rows != 1 {
		t.Fatalf("stored %d episodes, want 1", rows)
	}
}

// TestPruneAttentionEpisodes proves the pass closes exactly the episodes whose
// session has left WAITING_FOR_INPUT, so the next block is announced afresh.
func TestPruneAttentionEpisodes(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	waiting := blockedSession(t, store, "attention-prune-waiting")
	answered := blockedSession(t, store, "attention-prune-answered")
	never := mustSeedSession(t, store, "attention-prune-never")

	for _, session := range []storage.Session{waiting, answered} {
		if _, err := store.BeginAttentionEpisode(ctx, session.ID, "blocked", time.Time{}); err != nil {
			t.Fatalf("begin %s: %v", session.ID, err)
		}
	}

	// The operator answered: the state leaves WAITING_FOR_INPUT, so the episode
	// it described is over.
	if _, err := store.TransitionSession(ctx, answered.ID, storage.StateRunning, "operator answered", 0); err != nil {
		t.Fatalf("transition answered session: %v", err)
	}

	closed, err := store.PruneAttentionEpisodes(ctx)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if closed != 1 {
		t.Fatalf("pruned %d episodes, want 1", closed)
	}
	if _, ok, err := store.AttentionEpisodeStart(ctx, waiting.ID); err != nil || !ok {
		t.Fatalf("waiting episode closed by the prune: ok=%t err=%v", ok, err)
	}
	if _, ok, err := store.AttentionEpisodeStart(ctx, answered.ID); err != nil || ok {
		t.Fatalf("answered episode survived the prune: ok=%t err=%v", ok, err)
	}
	// A session that was never blocked and never had an episode is not acted on.
	if _, ok, err := store.AttentionEpisodeStart(ctx, never.ID); err != nil || ok {
		t.Fatalf("never-blocked session has an episode: ok=%t err=%v", ok, err)
	}

	// Running the prune again is a no-op rather than an error.
	if closed, err := store.PruneAttentionEpisodes(ctx); err != nil || closed != 0 {
		t.Fatalf("second prune = %d, %v; want 0, nil", closed, err)
	}
}

// TestAttentionEpisodeCascadesWithSession proves the row cannot outlive its
// session, so a forgotten session cannot leave a stale episode behind.
func TestAttentionEpisodeCascadesWithSession(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	session := blockedSession(t, store, "attention-cascade")

	if _, err := store.BeginAttentionEpisode(ctx, session.ID, "blocked", time.Time{}); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := store.DeleteSession(ctx, session.ID); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	var rows int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM attention_episodes`).Scan(&rows); err != nil {
		t.Fatalf("count episodes: %v", err)
	}
	if rows != 0 {
		t.Fatalf("%d episodes survived the session delete", rows)
	}
}

// TestAttentionEpisodeRejectsBadInput covers the validation and closed-store
// paths, because the hook calls these and must not panic on a bad payload.
func TestAttentionEpisodeRejectsBadInput(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	if _, err := store.BeginAttentionEpisode(ctx, "", "", time.Time{}); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("begin with no session id = %v, want ErrInvalid", err)
	}
	if _, err := store.BeginAttentionEpisode(ctx, "unknown-session", "", time.Time{}); err == nil {
		// The foreign key refuses an episode for a session ccresu does not know,
		// which is how R11 stays true without a lookup in the hook.
		t.Fatal("begin for an unknown session was accepted")
	}
	if _, err := store.GetAttentionEpisode(ctx, "unknown-session"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("get unknown session = %v, want ErrNotFound", err)
	}
	if err := store.EndAttentionEpisode(ctx, "unknown-session"); err != nil {
		t.Fatalf("end unknown session = %v, want a no-op", err)
	}

	closed, err := storage.Open(ctx, storage.Options{Memory: true})
	if err != nil {
		t.Fatalf("open second store: %v", err)
	}
	if err := closed.Close(); err != nil {
		t.Fatalf("close second store: %v", err)
	}
	if _, err := closed.BeginAttentionEpisode(ctx, "x", "", time.Time{}); !errors.Is(err, storage.ErrClosed) {
		t.Fatalf("begin after close = %v, want ErrClosed", err)
	}
	if _, err := closed.GetAttentionEpisode(ctx, "x"); !errors.Is(err, storage.ErrClosed) {
		t.Fatalf("get after close = %v, want ErrClosed", err)
	}
	if err := closed.EndAttentionEpisode(ctx, "x"); !errors.Is(err, storage.ErrClosed) {
		t.Fatalf("end after close = %v, want ErrClosed", err)
	}
	if _, err := closed.PruneAttentionEpisodes(ctx); !errors.Is(err, storage.ErrClosed) {
		t.Fatalf("prune after close = %v, want ErrClosed", err)
	}
	if _, _, err := closed.AttentionEpisodeStart(ctx, "x"); !errors.Is(err, storage.ErrClosed) {
		t.Fatalf("AttentionEpisodeStart after close = %v, want ErrClosed", err)
	}
}

// TestAttentionEpisodeSurvivesReopen is the restart guarantee: an episode open
// when the process stops is the same episode when it starts, so a restart
// mid-block cannot announce the block twice.
func TestAttentionEpisodeSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "attention.db")

	first, err := storage.Open(ctx, storage.Options{Path: path, Clock: func() time.Time { return fixedTime }})
	if err != nil {
		t.Fatalf("open first: %v", err)
	}
	if _, err := first.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	session := blockedSession(t, first, "attention-reopen")
	if _, err := first.BeginAttentionEpisode(ctx, session.ID, "blocked", time.Time{}); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}

	second := openAt(t, path)
	defer second.Close()
	if applied, err := second.Migrate(ctx); err != nil {
		t.Fatalf("reopen migrate: %v", err)
	} else if len(applied) != 0 {
		t.Fatalf("reopen applied migrations: %v", applied)
	}

	episode, err := second.GetAttentionEpisode(ctx, session.ID)
	if err != nil {
		t.Fatalf("get after reopen: %v", err)
	}
	if !episode.StartedAt.Equal(fixedTime) || episode.Reason != "blocked" {
		t.Fatalf("episode not preserved: %+v", episode)
	}
	// The second process's begin keeps the first process's instant.
	later := fixedTime.Add(2 * time.Hour)
	if start, err := second.BeginAttentionEpisode(ctx, session.ID, "blocked again", later); err != nil {
		t.Fatalf("begin after reopen: %v", err)
	} else if !start.Equal(fixedTime) {
		t.Fatalf("begin after reopen moved the instant to %s, want %s", start, fixedTime)
	}
}
