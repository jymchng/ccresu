-- +migrate Up
-- Sessions tracked by ccresu. The session UUID is the natural key: it is stable
-- across restarts and is what `claude --resume` accepts.
CREATE TABLE sessions (
    id                   TEXT    PRIMARY KEY,
    project_path         TEXT    NOT NULL DEFAULT '',
    transcript_path      TEXT    NOT NULL DEFAULT '',
    provider             TEXT    NOT NULL DEFAULT 'claude',
    session_type         TEXT    NOT NULL DEFAULT 'unknown',
    pid                  INTEGER NOT NULL DEFAULT 0,
    ppid                 INTEGER NOT NULL DEFAULT 0,
    process_start_time   INTEGER NOT NULL DEFAULT 0,
    state                TEXT    NOT NULL DEFAULT 'DISCOVERED',
    enabled              INTEGER NOT NULL DEFAULT 1,
    recoverable          INTEGER NOT NULL DEFAULT 1,
    reason               TEXT    NOT NULL DEFAULT '',
    last_error           TEXT    NOT NULL DEFAULT '',
    discovered_at        INTEGER NOT NULL,
    last_seen_at         INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL,
    CHECK (enabled IN (0, 1)),
    CHECK (recoverable IN (0, 1)),
    CHECK (length(id) > 0)
);

CREATE INDEX idx_sessions_state ON sessions (state);
CREATE INDEX idx_sessions_enabled ON sessions (enabled, state);
CREATE INDEX idx_sessions_last_seen ON sessions (last_seen_at DESC);

-- Usage-limit events detected in a session transcript. reset_at is NULL when
-- the message carried no parseable reset time, which is what triggers the
-- exponential-backoff polling strategy.
CREATE TABLE usage_limits (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id     TEXT    NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    limit_type     TEXT    NOT NULL DEFAULT 'other',
    raw_message    TEXT    NOT NULL DEFAULT '',
    reset_at       INTEGER,
    reset_timezone TEXT    NOT NULL DEFAULT '',
    reset_raw      TEXT    NOT NULL DEFAULT '',
    confidence     REAL    NOT NULL DEFAULT 0,
    paused_goal    INTEGER NOT NULL DEFAULT 0,
    detected_at    INTEGER NOT NULL,
    CHECK (confidence >= 0 AND confidence <= 1),
    CHECK (paused_goal IN (0, 1))
);

CREATE INDEX idx_usage_limits_session ON usage_limits (session_id, detected_at DESC);
CREATE INDEX idx_usage_limits_reset ON usage_limits (reset_at);

-- One row per resume attempt. (session_id, attempt_number) is unique so a
-- concurrent duplicate can never be recorded twice.
CREATE TABLE recovery_attempts (
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

CREATE INDEX idx_attempts_session ON recovery_attempts (session_id, attempt_number);

-- Append-only state history. Audit trail for `ccresu history` and for proving a
-- recovery went through the documented states rather than jumping.
CREATE TABLE state_transitions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id  TEXT    NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    attempt_id  INTEGER REFERENCES recovery_attempts (id) ON DELETE SET NULL,
    from_state  TEXT    NOT NULL,
    to_state    TEXT    NOT NULL,
    reason      TEXT    NOT NULL DEFAULT '',
    occurred_at INTEGER NOT NULL
);

CREATE INDEX idx_transitions_session ON state_transitions (session_id, occurred_at);
CREATE INDEX idx_transitions_attempt ON state_transitions (attempt_id);

-- Notification queue with a dedupe key so a crash/restart cannot double-send.
CREATE TABLE notifications (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT REFERENCES sessions (id) ON DELETE CASCADE,
    attempt_id INTEGER REFERENCES recovery_attempts (id) ON DELETE SET NULL,
    event      TEXT    NOT NULL,
    dedupe_key TEXT    NOT NULL UNIQUE,
    body       TEXT    NOT NULL DEFAULT '',
    status     TEXT    NOT NULL DEFAULT 'pending',
    attempts   INTEGER NOT NULL DEFAULT 0,
    last_error TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    sent_at    INTEGER,
    CHECK (attempts >= 0)
);

CREATE INDEX idx_notifications_status ON notifications (status, created_at);
CREATE INDEX idx_notifications_session ON notifications (session_id, created_at DESC);

-- Small key/value table for runtime settings that must survive restarts
-- (global enable/disable, the API token fingerprint, ...).
CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL
);

-- +migrate Down
DROP TABLE IF EXISTS settings;
DROP TABLE IF EXISTS notifications;
DROP TABLE IF EXISTS state_transitions;
DROP TABLE IF EXISTS recovery_attempts;
DROP TABLE IF EXISTS usage_limits;
DROP TABLE IF EXISTS sessions;
