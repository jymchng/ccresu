-- +migrate Up
-- A resume is a background dispatch, so the conversation does not stay under the
-- id that was resumed: `claude --resume <id> --bg` prints `backgrounded · <job>`
-- and that job continues the conversation under its OWN session id (probed:
-- job 93874eb3 reported sessionId 93874eb3-1989-412f-8f74-2703ca1c1086 for a
-- resume of session 044e2a0a-...). Without these columns the continuation is
-- only visible in the transcript on disk, so an operator cannot tell which
-- conversation a resumed row is now growing -- and the next discovery pass would
-- keep measuring the abandoned transcript.
--
-- Empty means "no continuation recorded", which is the state of every session
-- that was never resumed and of any resume that could not be proven. No backfill
-- is attempted: the ids were never stored, so inventing them would be a guess.
ALTER TABLE sessions ADD COLUMN continuation_session_id TEXT NOT NULL DEFAULT '';
ALTER TABLE recovery_attempts ADD COLUMN continuation_session_id TEXT NOT NULL DEFAULT '';

-- +migrate Down
ALTER TABLE recovery_attempts DROP COLUMN continuation_session_id;
ALTER TABLE sessions DROP COLUMN continuation_session_id;
