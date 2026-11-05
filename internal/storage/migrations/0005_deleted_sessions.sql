-- +migrate Up
-- Sessions the operator deleted with `ccresu sessions delete <id>`.
--
-- A tombstone, not a soft-delete flag: ccresu discovers sessions from the Claude
-- Code transcripts on disk, so removing the row alone would only last until the
-- next discovery pass, which would upsert the session straight back. Discovery
-- skips every id named here, which is what makes a delete stick.
--
-- Nothing under ~/.claude is touched: the session is one ccresu has stopped
-- tracking, not one whose conversation has been destroyed. The paths are kept so
-- `ccresu enable <id>` can rebuild the row without reading the transcript first.
CREATE TABLE deleted_sessions (
    id              TEXT    PRIMARY KEY,
    project_path    TEXT    NOT NULL DEFAULT '',
    transcript_path TEXT    NOT NULL DEFAULT '',
    state           TEXT    NOT NULL DEFAULT '',
    deleted_at      INTEGER NOT NULL,
    CHECK (length(id) > 0)
);

CREATE INDEX idx_deleted_sessions_at ON deleted_sessions (deleted_at DESC);

-- +migrate Down
DROP INDEX IF EXISTS idx_deleted_sessions_at;
DROP TABLE IF EXISTS deleted_sessions;
