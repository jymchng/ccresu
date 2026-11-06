-- +migrate Up
-- The loop the operator asked for needs three facts about a session that the
-- schema did not hold before:
--
--   name                       -- what `claude agents --all --json` calls this
--                                 session, so `ccresu sessions` shows a name
--                                 instead of only a UUID prefix.
--   continuation_transcript_path -- a dispatched resume continues the
--                                 conversation under the job's own session id,
--                                 so the transcript that keeps growing is a
--                                 different FILE. Detection must read that file
--                                 to find the next reset time; transcript_path
--                                 and its size/line snapshot stay on the
--                                 original file, which is the one they measure.
--   job_id                     -- the background job a resume created. It is the
--                                 handle `claude agents --json` resolves, so the
--                                 daemon can poll a WORKING session until its job
--                                 is no longer working -- including after a
--                                 restart, when no goroutine remembers the job.
--
-- Empty means "not known", which is the state of every session before this
-- migration. No backfill: none of these values were ever stored, so inventing
-- them would be a guess.
ALTER TABLE sessions ADD COLUMN name TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN continuation_transcript_path TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN job_id TEXT NOT NULL DEFAULT '';

-- +migrate Down
ALTER TABLE sessions DROP COLUMN job_id;
ALTER TABLE sessions DROP COLUMN continuation_transcript_path;
ALTER TABLE sessions DROP COLUMN name;
