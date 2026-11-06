-- +migrate Up
-- PERMANENTLY_FAILED is retired.
--
-- A session that could not be resumed is not broken for ever: it is waiting for
-- the usage limit to clear, or for a condition that has since changed. ccresu
-- now returns such a session to DISCOVERED so the daemon re-reads its transcript
-- and schedules the next reset on its own, which is why an operator no longer
-- needs `ccresu reset` (the command is gone, along with this state).
--
-- Rows already carrying the old verdict are re-opened rather than deleted: the
-- conversation, the attempt history and the transition log all stay, because a
-- rediscovered session must be able to show what was already tried.
INSERT INTO state_transitions (session_id, from_state, to_state, reason, occurred_at)
SELECT id,
       state,
       'DISCOVERED',
       'migrated: PERMANENTLY_FAILED is retired; the session is rediscovered and its attempt budget restarts',
       CAST(strftime('%s', 'now') AS INTEGER) * 1000
  FROM sessions
 WHERE state = 'PERMANENTLY_FAILED';

UPDATE sessions
   SET state      = 'DISCOVERED',
       last_error = '',
       retry_at   = NULL,
       updated_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000
 WHERE state = 'PERMANENTLY_FAILED';

-- +migrate Down
-- Best effort: the rows this migration touched are the ones whose transition row
-- it wrote, so they can be put back. A session that has since been rediscovered
-- and moved on is left alone.
UPDATE sessions
   SET state = 'PERMANENTLY_FAILED',
       updated_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000
 WHERE id IN (
       SELECT session_id
         FROM state_transitions
        WHERE to_state = 'DISCOVERED'
          AND reason LIKE 'migrated: PERMANENTLY_FAILED is retired%'
 );

DELETE FROM state_transitions
 WHERE to_state = 'DISCOVERED'
   AND reason LIKE 'migrated: PERMANENTLY_FAILED is retired%';
