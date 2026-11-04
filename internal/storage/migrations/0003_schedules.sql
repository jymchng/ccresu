-- +migrate Up
-- One pending reset wait per session.
--
-- A schedule row exists for exactly as long as a session is waiting for its
-- usage limit to reset, so a restart resumes the wait instead of re-parsing the
-- original banner. reset_at is NULL when the banner carried no usable reset
-- time; those rows poll with exponential backoff and keep their attempt count,
-- so a restart does not silently reset the backoff to its five-minute floor.
CREATE TABLE schedules (
    session_id     TEXT    PRIMARY KEY REFERENCES sessions (id) ON DELETE CASCADE,
    usage_limit_id INTEGER REFERENCES usage_limits (id) ON DELETE SET NULL,
    reset_at       INTEGER,
    reset_timezone TEXT    NOT NULL DEFAULT '',
    reset_raw      TEXT    NOT NULL DEFAULT '',
    unknown_reset  INTEGER NOT NULL DEFAULT 0,
    attempts       INTEGER NOT NULL DEFAULT 0,
    next_poll_at   INTEGER,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL,
    CHECK (unknown_reset IN (0, 1)),
    CHECK (attempts >= 0),
    CHECK (length(session_id) > 0),
    -- A known schedule must carry its instant; an unknown one must not pretend.
    CHECK (unknown_reset = 1 OR reset_at IS NOT NULL)
);

CREATE INDEX idx_schedules_reset ON schedules (reset_at);
CREATE INDEX idx_schedules_next_poll ON schedules (next_poll_at);

-- +migrate Down
DROP INDEX IF EXISTS idx_schedules_next_poll;
DROP INDEX IF EXISTS idx_schedules_reset;
DROP TABLE IF EXISTS schedules;
