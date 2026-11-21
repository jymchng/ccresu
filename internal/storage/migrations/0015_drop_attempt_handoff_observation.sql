-- +migrate Up
-- The attempt row stops recording a handoff and an observation, because neither
-- describes anything a resume does any more.
--
-- `handoff_state_id`, `handoff_name`, `handoff_at` and `handoff_copied` recorded a
-- backgrounded handoff: the short state ID the CLI printed, the label the job was
-- given, when the supervisor accepted it, and whether the continuation landed in
-- a copy rather than in place. `claude --resume <id> "<message>"` runs the turn
-- itself and prints no `backgrounded · <short-id> · <name>` block, so nothing can
-- write them any more. `handoff_at` was also the "is this attempt stale?" bit, and
-- it has no reader left either: an attempt with no finish time is closed as
-- interrupted, which is the only thing that bit ever decided.
--
-- `observed_state`, `observed_status`, `observed_waiting_for`, `observed_at` and
-- `observed_listed` recorded what the supervisor published about the session when
-- the verdict was reached, so the verdict could be re-examined. The verdict is now
-- the resume's exit status: before the turn the pass reads the session only to
-- establish that it is recoverable and to snapshot the transcript, so there is no
-- published state to write down and no decision that could be re-read out of it.
-- The transcript-growth diagnostic that remains lives in `transcript_grew`, which
-- stays.
--
-- Dropping them loses no evidence. Every one of these values restated something
-- the row already carries -- its state, its success, its finish time, its cost --
-- and a column no writer fills is worse than no column, because the next reader
-- believes it.
ALTER TABLE recovery_attempts DROP COLUMN handoff_state_id;

ALTER TABLE recovery_attempts DROP COLUMN handoff_name;

ALTER TABLE recovery_attempts DROP COLUMN handoff_at;

ALTER TABLE recovery_attempts DROP COLUMN handoff_copied;

ALTER TABLE recovery_attempts DROP COLUMN observed_state;

ALTER TABLE recovery_attempts DROP COLUMN observed_status;

ALTER TABLE recovery_attempts DROP COLUMN observed_waiting_for;

ALTER TABLE recovery_attempts DROP COLUMN observed_at;

ALTER TABLE recovery_attempts DROP COLUMN observed_listed;

-- +migrate Down
-- The columns come back exactly as 0007 and 0008 added them: additive, nullable
-- or defaulted, and empty. What was in them is gone, and that is the honest
-- restore -- inventing values would put a handoff or an observation into a row
-- that never had one.
--
-- SQLite appends a re-added column at the end of the table rather than restoring
-- its original position. Nothing depends on physical order: `attemptColumns` in
-- attempts.go names every column explicitly, so a rolled-back database reads back
-- the same rows through the same list.
ALTER TABLE recovery_attempts ADD COLUMN handoff_state_id TEXT NOT NULL DEFAULT '';

ALTER TABLE recovery_attempts ADD COLUMN handoff_name TEXT NOT NULL DEFAULT '';

ALTER TABLE recovery_attempts ADD COLUMN handoff_at INTEGER;

ALTER TABLE recovery_attempts ADD COLUMN handoff_copied INTEGER;

ALTER TABLE recovery_attempts ADD COLUMN observed_state TEXT NOT NULL DEFAULT '';

ALTER TABLE recovery_attempts ADD COLUMN observed_status TEXT NOT NULL DEFAULT '';

ALTER TABLE recovery_attempts ADD COLUMN observed_waiting_for TEXT NOT NULL DEFAULT '';

ALTER TABLE recovery_attempts ADD COLUMN observed_at INTEGER;

ALTER TABLE recovery_attempts ADD COLUMN observed_listed INTEGER;
