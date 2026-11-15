-- +migrate Up
-- PRD 1 (R4, R7): a handoff is recorded on the attempt row.
--
-- Before this, the only evidence that a recovery was under way was a child
-- process, which died with ccresu. Now the supervisor owns the work, so the
-- handoff itself is the evidence: the short state ID the CLI printed, the
-- display name it was given, when it was accepted, and whether the continuation
-- landed in a copy rather than in place.
--
-- Without these columns a restart cannot tell an attempt the supervisor is
-- running from one that died inside ccresu, and the safe answer for the first
-- (leave it alone) is the wrong answer for the second (close it as
-- interrupted). `handoff_at` is therefore the "is this stale?" bit: an attempt
-- with no handoff and no finished_at was opened by a process that is gone.
--
-- The state ID is not the session UUID. `claude attach`, `claude logs` and
-- `claude stop` all take the short ID, so it is the identifier an operator
-- needs and the one ccresu must keep.
ALTER TABLE recovery_attempts ADD COLUMN handoff_state_id TEXT NOT NULL DEFAULT '';

ALTER TABLE recovery_attempts ADD COLUMN handoff_name TEXT NOT NULL DEFAULT '';

ALTER TABLE recovery_attempts ADD COLUMN handoff_at INTEGER;

ALTER TABLE recovery_attempts ADD COLUMN handoff_copied INTEGER;

-- +migrate Down
-- SQLite before 3.35 cannot drop a column, and the columns are additive and
-- nullable, so reverting means rebuilding the table without them.
CREATE TABLE recovery_attempts_reverted (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id          TEXT    NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    usage_limit_id      INTEGER REFERENCES usage_limits (id) ON DELETE SET NULL,
    attempt_number      INTEGER NOT NULL,
    state               TEXT    NOT NULL DEFAULT 'RESUMING',
    command             TEXT    NOT NULL DEFAULT '',
    started_at          INTEGER NOT NULL,
    finished_at         INTEGER,
    success             INTEGER,
    output              TEXT    NOT NULL DEFAULT '',
    error               TEXT    NOT NULL DEFAULT '',
    session_id_verified INTEGER,
    transcript_grew     INTEGER,
    CHECK (attempt_number >= 1),
    CHECK (success IS NULL OR success IN (0, 1)),
    CHECK (session_id_verified IS NULL OR session_id_verified IN (0, 1)),
    CHECK (transcript_grew IS NULL OR transcript_grew IN (0, 1)),
    UNIQUE (session_id, attempt_number)
);

INSERT INTO recovery_attempts_reverted
    (id, session_id, usage_limit_id, attempt_number, state, command, started_at,
     finished_at, success, output, error, session_id_verified, transcript_grew)
SELECT id, session_id, usage_limit_id, attempt_number, state, command, started_at,
       finished_at, success, output, error, session_id_verified, transcript_grew
  FROM recovery_attempts;

DROP TABLE recovery_attempts;

ALTER TABLE recovery_attempts_reverted RENAME TO recovery_attempts;

CREATE INDEX idx_attempts_session ON recovery_attempts (session_id, attempt_number);
