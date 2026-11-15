-- +migrate Up
-- PRD 4 (§6.3): the identity of one "a session needs your input" episode.
--
-- `needs_input` is the only notification event two producers can reach — the
-- recovery pass and `ccresu hook` — and both must compute the same dedupe key for
-- the same condition, or one blocked session would be announced twice.
--
-- The key needs an episode component, and the episode is "the stretch during
-- which a session was waiting for a human". It cannot be read from the hook: the
-- hook is a Notification callback, it may run before any pass has observed the
-- block, and it is explicitly forbidden from writing a session's state (R11). So
-- the anchor is recorded here, in notification bookkeeping rather than session
-- state, and both producers use it:
--
--   * `ccresu hook` inserts it when its own observation says `blocked`, so the
--     hook can announce the first block without waiting for a pass.
--   * the recovery pass inserts it too, with ON CONFLICT DO NOTHING, so whichever
--     producer looked first owns the instant and the other one collides on the
--     key.
--   * the daemon deletes rows for sessions that are no longer in
--     WAITING_FOR_INPUT, so the next block of the same session is a new episode
--     with a new key.
--
-- `started_at` is the instant the episode began *by the first observer's clock*,
-- which is why it is persisted rather than recomputed: a restart mid-episode must
-- not announce the same block again.
CREATE TABLE attention_episodes (
    session_id TEXT    PRIMARY KEY REFERENCES sessions (id) ON DELETE CASCADE,
    started_at INTEGER NOT NULL,
    reason     TEXT    NOT NULL DEFAULT '',
    CHECK (length(session_id) > 0)
);

-- The prune runs once per pass over the whole table, so it wants the newest rows
-- first rather than a scan of whatever the primary key orders by.
CREATE INDEX idx_attention_episodes_started ON attention_episodes (started_at);

-- +migrate Down
DROP INDEX IF EXISTS idx_attention_episodes_started;
DROP TABLE IF EXISTS attention_episodes;
