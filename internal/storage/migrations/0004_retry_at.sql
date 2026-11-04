-- +migrate Up
-- When ccresu will next act on a session, when that is a wait it computed
-- itself rather than a reset time it read from a transcript.
--
-- A WAITING_FOR_RESET session is woken by its row in `schedules`, so its instant
-- already persists there. A RETRY_WAIT session has no such row: the backoff is
-- computed in memory from the retry policy, and until now it existed only as
-- text inside the transition reason ("next attempt in 1m4s"). Persisting the
-- instant means `ccresu sessions` and `ccresu inspect` can say exactly when the
-- next attempt is due without re-deriving it, and without guessing.
--
-- NULL means "no retry pending". The column is only meaningful while a session
-- is in RETRY_WAIT; every other state transition clears it, so a stale wait can
-- never be reported for a session that has moved on.
ALTER TABLE sessions ADD COLUMN retry_at INTEGER;

-- +migrate Down
ALTER TABLE sessions DROP COLUMN retry_at;
