-- +migrate Up
-- Two measurements of the CONTINUATION transcript, kept apart on purpose
-- because they answer two different questions and must not be confused:
--
--   continuation_transcript_lines -- how many lines the continuation transcript
--                                   held when the last resume was verified. It
--                                   is written once per proved resume and never
--                                   advanced, so "has the conversation moved
--                                   past the point the last attempt saw?" stays
--                                   answerable for ever. Detection uses it to
--                                   decide whether a usage-limit banner it just
--                                   read is the one that produced that attempt
--                                   (do not re-plan it: the conversation is
--                                   still working) or new material after it
--                                   (plan it).
--
--   rediscover_lines             -- how many lines the continuation transcript
--                                   held the last time the rediscovery stage
--                                   looked at it. It advances on every look, so
--                                   "is the continuation STILL producing output
--                                   between two looks?" is answerable. That is
--                                   what lets the daemon defer a row whose
--                                   conversation is still working instead of
--                                   re-reading it every pass.
--
-- Zero means "not measured": every session that was never resumed, and every
-- resume that could not be proven. No backfill is attempted, because neither
-- measurement was ever stored -- inventing them would make the first
-- comparison after an upgrade meaningless.
ALTER TABLE sessions ADD COLUMN continuation_transcript_lines INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN rediscover_lines INTEGER NOT NULL DEFAULT 0;

-- +migrate Down
ALTER TABLE sessions DROP COLUMN rediscover_lines;
ALTER TABLE sessions DROP COLUMN continuation_transcript_lines;
