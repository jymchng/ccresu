-- +migrate Up
-- The attention-episode table goes away with the `needs_input` event it served.
--
-- `attention_episodes` existed for one purpose: to give the `needs_input`
-- notification an identity both producers could agree on. The recovery pass and
-- `ccresu hook` could each observe a `blocked` session, and the episode instant
-- was the anchor that made their two messages collapse into one row.
--
-- Neither producer exists any more. A `blocked` observation is handed a
-- continuation like any other, so nothing records a session as waiting for a
-- human, nothing raises the event, and no template renders it. The table can
-- therefore never hold a row that describes a live session, and keeping it would
-- mean shipping a migration-only surface no code reads: a table whose only
-- writer is a prune that deletes everything.
--
-- Dropping it loses nothing. An episode was bookkeeping for a message that is no
-- longer produced; the session row, its attempts and its transition history are
-- untouched, and `ccresu inspect` never rendered an episode.
DROP INDEX IF EXISTS idx_attention_episodes_started;
DROP TABLE IF EXISTS attention_episodes;

-- +migrate Down
-- Recreate the table exactly as 0010 created it, so a rollback to the previous
-- release leaves a schema that release understands. It is recreated empty: the
-- rows were bookkeeping for messages already delivered, and a stale episode
-- would suppress the first message of a new block rather than restore it.
CREATE TABLE attention_episodes (
    session_id TEXT    PRIMARY KEY REFERENCES sessions (id) ON DELETE CASCADE,
    started_at INTEGER NOT NULL,
    reason     TEXT    NOT NULL DEFAULT '',
    CHECK (length(session_id) > 0)
);

CREATE INDEX idx_attention_episodes_started ON attention_episodes (started_at);
