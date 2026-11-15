-- +migrate Up
-- PRD 2 (R13): the attempt row records what was observed, not what was inferred.
--
-- The success decision stops being "the transcript got longer" and becomes "the
-- supervisor said so". That makes the supervisor's answer the evidence, and
-- evidence has to be written down where an operator can read it afterwards:
-- `ccresu inspect` must be able to say which state was seen, what corroborating
-- status came with it, what the session was waiting for, and -- the fact the
-- merged discovery record cannot carry -- whether the session was in the
-- listing at all.
--
-- `observed_listed` is deliberately nullable. NULL means the observation was
-- never made (an attempt recorded before this change), 0 means the session was
-- missing from the listing, and 1 means it was present. Collapsing the first two
-- would present "we did not look" as "nothing was there", which is the exact
-- confusion R8 exists to prevent.
--
-- No back-fill: the columns are additive and nullable, and an old attempt keeps
-- NULL, which the CLI renders as "not recorded" rather than as an empty state.
ALTER TABLE recovery_attempts ADD COLUMN observed_state TEXT NOT NULL DEFAULT '';

ALTER TABLE recovery_attempts ADD COLUMN observed_status TEXT NOT NULL DEFAULT '';

ALTER TABLE recovery_attempts ADD COLUMN observed_waiting_for TEXT NOT NULL DEFAULT '';

ALTER TABLE recovery_attempts ADD COLUMN observed_at INTEGER;

ALTER TABLE recovery_attempts ADD COLUMN observed_listed INTEGER;

-- +migrate Down
-- SQLite before 3.35 cannot drop a column, so reverting rebuilds the table
-- without them.
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
    handoff_state_id    TEXT    NOT NULL DEFAULT '',
    handoff_name        TEXT    NOT NULL DEFAULT '',
    handoff_at          INTEGER,
    handoff_copied      INTEGER,
    CHECK (attempt_number >= 1),
    CHECK (success IS NULL OR success IN (0, 1)),
    CHECK (session_id_verified IS NULL OR session_id_verified IN (0, 1)),
    CHECK (transcript_grew IS NULL OR transcript_grew IN (0, 1)),
    CHECK (handoff_copied IS NULL OR handoff_copied IN (0, 1)),
    UNIQUE (session_id, attempt_number)
);

INSERT INTO recovery_attempts_reverted
    (id, session_id, usage_limit_id, attempt_number, state, command, started_at,
     finished_at, success, output, error, session_id_verified, transcript_grew,
     handoff_state_id, handoff_name, handoff_at, handoff_copied)
SELECT id, session_id, usage_limit_id, attempt_number, state, command, started_at,
       finished_at, success, output, error, session_id_verified, transcript_grew,
       handoff_state_id, handoff_name, handoff_at, handoff_copied
  FROM recovery_attempts;

DROP TABLE recovery_attempts;

ALTER TABLE recovery_attempts_reverted RENAME TO recovery_attempts;

CREATE INDEX idx_attempts_session ON recovery_attempts (session_id, attempt_number);
