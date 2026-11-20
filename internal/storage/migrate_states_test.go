package storage_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jymchng/ccresu/internal/storage"
)

// collapsedStates is the translation migration 0013 performs, restated here so
// the test fails if the SQL and this table ever disagree.
var collapsedStates = map[string]storage.State{
	"USAGE_LIMIT_DETECTED": storage.StateWaitingForReset,
	"RESET_TIME_REACHED":   storage.StateWaitingForReset,
	"CHECKING_SESSION":     storage.StateResuming,
	"CHECKING_LIMIT":       storage.StateResuming,
	"RUNNING":              storage.StateResumed,
	"RETRY_WAIT":           storage.StateDiscovered,
	"WAITING_FOR_INPUT":    storage.StateDiscovered,
	"USER_TERMINATED":      storage.StateDisabled,
	"SPEND_CAP_REACHED":    storage.StateDisabled,
}

// collapsedOrder is the nine retired states in a fixed order, so the fixture's
// session ids are stable and a failure names the same row every run.
var collapsedOrder = []string{
	"USAGE_LIMIT_DETECTED",
	"RESET_TIME_REACHED",
	"CHECKING_SESSION",
	"CHECKING_LIMIT",
	"RUNNING",
	"RETRY_WAIT",
	"WAITING_FOR_INPUT",
	"USER_TERMINATED",
	"SPEND_CAP_REACHED",
}

// survivingStates are the seven the machine keeps. The migration must not move a
// row that is already in one of them.
var survivingStates = []storage.State{
	storage.StateDiscovered,
	storage.StateWaitingForReset,
	storage.StateResuming,
	storage.StateResumed,
	storage.StateCompleted,
	storage.StateDisabled,
	storage.StateUnrecoverable,
}

// TestStateCollapseMigrationRewritesEveryRow is the upgrade test for the
// sixteen-to-seven collapse.
//
// It builds the database an older release would have left behind — a session, a
// transition, an attempt and a tombstone in each of the nine retired states —
// runs the startup migration, and asserts that every row is readable and carries
// a state the current machine accepts. Without it, an upgrade would hand the
// engine rows it cannot transition out of, and the session would be stranded.
func TestStateCollapseMigrationRewritesEveryRow(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)

	// Step back to the schema the previous release shipped, so the fixture can be
	// written in the vocabulary that release used.
	const preCollapse = 12
	if _, err := store.Rollback(ctx, preCollapse); err != nil {
		t.Fatalf("roll back to %d: %v", preCollapse, err)
	}
	if version, err := store.Version(ctx); err != nil || version != preCollapse {
		t.Fatalf("Version after rollback = %d, %v, want %d", version, err, preCollapse)
	}

	// A retry instant on the states that owned one, so the test can prove the
	// migration clears it where it is meaningless and leaves it where it is not.
	const retryAt = int64(1_700_000_000_000)
	ownedRetry := func(old string) bool {
		return old == "RETRY_WAIT" || old == "WAITING_FOR_INPUT" || old == "RUNNING"
	}

	for i, old := range collapsedOrder {
		sessionID := fmt.Sprintf("old-%02d", i)
		var instant any
		if ownedRetry(old) {
			instant = retryAt
		}
		execFixture(t, store, ctx,
			`INSERT INTO sessions (id, project_path, state, enabled, recoverable, discovered_at, last_seen_at, updated_at, retry_at)
			 VALUES (?, '', ?, 1, 1, 0, 0, 0, ?)`, sessionID, old, instant)
		execFixture(t, store, ctx,
			`INSERT INTO state_transitions (session_id, from_state, to_state, reason, occurred_at)
			 VALUES (?, 'RUNNING', ?, 'seeded by the upgrade test', 0)`, sessionID, old)
		execFixture(t, store, ctx,
			`INSERT INTO recovery_attempts (session_id, attempt_number, state, started_at)
			 VALUES (?, 1, ?, 0)`, sessionID, old)
		execFixture(t, store, ctx,
			`INSERT INTO deleted_sessions (id, project_path, transcript_path, state, deleted_at)
			 VALUES (?, '', '', ?, 0)`, "tomb-"+sessionID, old)
	}

	// Rows already in a surviving state, to prove the migration is not a blanket
	// rewrite.
	for i, state := range survivingStates {
		execFixture(t, store, ctx,
			`INSERT INTO sessions (id, project_path, state, enabled, recoverable, discovered_at, last_seen_at, updated_at)
			 VALUES (?, '', ?, 1, 1, 0, 0, 0)`, fmt.Sprintf("kept-%02d", i), string(state))
	}

	// The fixture must really be pre-migration, or the test proves nothing.
	stranded, err := store.GetSession(ctx, "old-04") // RUNNING
	if err != nil {
		t.Fatalf("GetSession(old-04): %v", err)
	}
	if stranded.State.Valid() {
		t.Fatalf("the fixture's state %q is already valid; the test would prove nothing", stranded.State)
	}

	applied, err := store.Migrate(ctx)
	if err != nil {
		t.Fatalf("migrate after seeding: %v", err)
	}
	if !containsVersion(applied, 13) {
		t.Fatalf("migrate applied %v, want it to include 13", applied)
	}

	// Every session is readable and carries a state the machine accepts, in the
	// state the mapping names.
	for i, old := range collapsedOrder {
		sessionID := fmt.Sprintf("old-%02d", i)
		session, err := store.GetSession(ctx, sessionID)
		if err != nil {
			t.Fatalf("GetSession(%s) after migration: %v", sessionID, err)
		}
		if !session.State.Valid() {
			t.Errorf("session %s (was %s) is in %q, which is not a known state", sessionID, old, session.State)
		}
		if want := collapsedStates[old]; session.State != want {
			t.Errorf("session %s: %s -> %s, want %s", sessionID, old, session.State, want)
		}
		switch {
		case old == "RETRY_WAIT" || old == "WAITING_FOR_INPUT":
			if session.RetryAt != nil {
				t.Errorf("session %s became DISCOVERED but kept a retry instant %v; nothing owns it now",
					sessionID, *session.RetryAt)
			}
		case ownedRetry(old):
			if session.RetryAt == nil {
				t.Errorf("session %s (%s) lost a retry instant it was entitled to", sessionID, old)
			}
		}
	}

	for i, state := range survivingStates {
		session, err := store.GetSession(ctx, fmt.Sprintf("kept-%02d", i))
		if err != nil {
			t.Fatalf("GetSession(kept-%02d) after migration: %v", i, err)
		}
		if session.State != state {
			t.Errorf("a session in %s moved to %s; the migration must leave surviving states alone",
				state, session.State)
		}
	}

	// ListSessions reads every row through the same scanner the daemon uses, so
	// a row that survived the rewrite but not the read would fail here.
	all, err := store.ListSessions(ctx, storage.SessionFilter{})
	if err != nil {
		t.Fatalf("ListSessions after migration: %v", err)
	}
	if len(all) != len(collapsedOrder)+len(survivingStates) {
		t.Fatalf("ListSessions returned %d sessions, want %d", len(all), len(collapsedOrder)+len(survivingStates))
	}
	for _, session := range all {
		if !session.State.Valid() {
			t.Errorf("session %s is in %q after migration", session.ID, session.State)
		}
	}

	// The history, the attempt log and the tombstones speak the current
	// vocabulary too, or `ccresu history` and `ccresu sessions deleted` would
	// print states that do not exist.
	for _, transition := range []struct{ column, selector string }{
		{"from_state", "from_state FROM state_transitions"},
		{"to_state", "to_state FROM state_transitions"},
	} {
		for _, state := range columnStates(t, store, ctx, transition.selector) {
			if !storage.State(state).Valid() {
				t.Errorf("state_transitions.%s = %q after migration, which is not a known state",
					transition.column, state)
			}
		}
	}
	for _, state := range columnStates(t, store, ctx, "state FROM recovery_attempts") {
		if !storage.State(state).Valid() {
			t.Errorf("recovery_attempts.state = %q after migration, which is not a known state", state)
		}
	}
	for _, state := range columnStates(t, store, ctx, "state FROM deleted_sessions") {
		if !storage.State(state).Valid() {
			t.Errorf("deleted_sessions.state = %q after migration, which is not a known state", state)
		}
	}

	// The mapping is complete: nothing in the database still carries a retired
	// name anywhere it would be read back as a state.
	for _, old := range collapsedOrder {
		if n := countRows(t, store, ctx,
			`SELECT COUNT(*) FROM sessions WHERE state = ?
			 UNION ALL SELECT COUNT(*) FROM recovery_attempts WHERE state = ?
			 UNION ALL SELECT COUNT(*) FROM state_transitions WHERE from_state = ? OR to_state = ?
			 UNION ALL SELECT COUNT(*) FROM deleted_sessions WHERE state = ?`,
			old, old, old, old, old); n != 0 {
			t.Errorf("%d row(s) still carry the retired state %q", n, old)
		}
	}

	// And the migration is a one-off: a second startup applies nothing.
	again, err := store.Migrate(ctx)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second migrate applied %v, want nothing", again)
	}
}

// execFixture runs one statement against the store, failing the test on error.
func execFixture(t *testing.T, store *storage.Store, ctx context.Context, query string, args ...any) {
	t.Helper()
	if _, err := store.DB().ExecContext(ctx, query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// columnStates reads one column as strings. The selector is either a column name
// or a "column FROM table" fragment, so the same helper serves both forms.
func columnStates(t *testing.T, store *storage.Store, ctx context.Context, selector string) []string {
	t.Helper()
	rows, err := store.DB().QueryContext(ctx, "SELECT "+selector)
	if err != nil {
		t.Fatalf("select %s: %v", selector, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatalf("scan %s: %v", selector, err)
		}
		out = append(out, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s: %v", selector, err)
	}
	return out
}

// countRows sums every count a UNION ALL query returns.
func countRows(t *testing.T, store *storage.Store, ctx context.Context, query string, args ...any) int {
	t.Helper()
	rows, err := store.DB().QueryContext(ctx, query, args...)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	defer rows.Close()
	total := 0
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan count: %v", err)
		}
		total += n
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate counts: %v", err)
	}
	return total
}

// containsVersion reports whether versions includes want.
func containsVersion(versions []int, want int) bool {
	for _, v := range versions {
		if v == want {
			return true
		}
	}
	return false
}
