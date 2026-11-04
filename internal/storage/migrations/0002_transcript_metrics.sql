-- +migrate Up
-- Transcript bookkeeping used by recovery verification: the byte size and line
-- count of the transcript at the time a limit was detected, so growth after a
-- resume proves the conversation actually continued.
ALTER TABLE sessions ADD COLUMN transcript_size INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN transcript_lines INTEGER NOT NULL DEFAULT 0;

CREATE INDEX idx_sessions_transcript_path ON sessions (transcript_path);

-- +migrate Down
DROP INDEX IF EXISTS idx_sessions_transcript_path;
ALTER TABLE sessions DROP COLUMN transcript_lines;
ALTER TABLE sessions DROP COLUMN transcript_size;
