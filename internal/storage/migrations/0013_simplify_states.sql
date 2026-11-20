-- +migrate Up
-- The state machine collapses from sixteen states to seven.
--
-- ccresu does one thing: it parses the reset time out of a usage-limit banner,
-- waits for it, hands the backgrounded session a continuation, and goes back to
-- reading the transcript. The other nine states were detours off that loop --
-- pre-flight checks, a backoff, a park for a `blocked` supervisor, a budget
-- verdict -- and each of them is now part of the beat it belonged to.
--
-- A row carrying one of the retired names is not corrupt: it is a session in an
-- equivalent state, spelled the old way. The seven survivors are a subset of the
-- sixteen, so every row has a home, and this migration moves it there. Nothing
-- is deleted: the conversation, the attempt history and the transition log all
-- survive, because a session that is still waiting for its reset must be able to
-- show what was already tried.
--
-- The mapping is many-to-one, so it is written once, as a table, and applied to
-- every column that holds a state name.
CREATE TEMP TABLE state_collapse (
    old_state TEXT PRIMARY KEY,
    new_state TEXT NOT NULL
);

INSERT INTO state_collapse (old_state, new_state) VALUES
    -- A detected limit and a reached reset time were both "parked on the
    -- instant"; WAITING_FOR_RESET is that beat.
    ('USAGE_LIMIT_DETECTED', 'WAITING_FOR_RESET'),
    ('RESET_TIME_REACHED',   'WAITING_FOR_RESET'),
    -- Checking the session and checking the limit are both pre-flight work
    -- inside one resume; RESUMING is that beat.
    ('CHECKING_SESSION',     'RESUMING'),
    ('CHECKING_LIMIT',       'RESUMING'),
    -- A running session is one that is alive after a resume: RESUMED. It is
    -- still scanned, which is how the next limit is noticed.
    ('RUNNING',              'RESUMED'),
    -- A backoff and a `blocked` park both end by reading the transcript again,
    -- which is DISCOVERED. The backoff itself is not lost: it lives on
    -- sessions.retry_at, not in a state.
    ('RETRY_WAIT',           'DISCOVERED'),
    ('WAITING_FOR_INPUT',    'DISCOVERED'),
    -- Both are verdicts that stop the loop until an operator acts, which is
    -- exactly DISABLED -- and `ccresu enable` is the way back for all of them.
    ('USER_TERMINATED',      'DISABLED'),
    ('SPEND_CAP_REACHED',    'DISABLED');

-- DISCOVERED owns no retry instant, so a session arriving there from RETRY_WAIT
-- or WAITING_FOR_INPUT must not keep one: `ccresu next` would report a wait that
-- nothing will ever act on. This runs before the rename, while the old name is
-- still readable.
UPDATE sessions
   SET retry_at = NULL
 WHERE state IN ('RETRY_WAIT', 'WAITING_FOR_INPUT');

UPDATE sessions
   SET state = (SELECT new_state FROM state_collapse WHERE old_state = sessions.state)
 WHERE state IN (SELECT old_state FROM state_collapse);

-- The attempt log names the state the attempt was in. It is rendered by
-- `ccresu inspect`, so it has to speak the current vocabulary too.
UPDATE recovery_attempts
   SET state = (SELECT new_state FROM state_collapse WHERE old_state = recovery_attempts.state)
 WHERE state IN (SELECT old_state FROM state_collapse);

-- The transition history is what `ccresu history` prints, and every consumer of
-- it validates the names it reads: a retired name there is not history, it is an
-- unreadable row. The timeline survives; the retired distinctions do not, which
-- is the price of collapsing many states into one.
UPDATE state_transitions
   SET from_state = (SELECT new_state FROM state_collapse WHERE old_state = state_transitions.from_state)
 WHERE from_state IN (SELECT old_state FROM state_collapse);

UPDATE state_transitions
   SET to_state = (SELECT new_state FROM state_collapse WHERE old_state = state_transitions.to_state)
 WHERE to_state IN (SELECT old_state FROM state_collapse);

-- Tombstones keep the state the session was in when it was deleted, so
-- `ccresu sessions deleted` can still explain it.
UPDATE deleted_sessions
   SET state = (SELECT new_state FROM state_collapse WHERE old_state = deleted_sessions.state)
 WHERE state IN (SELECT old_state FROM state_collapse);

DROP TABLE state_collapse;

-- +migrate Down
-- There is nothing to undo, and nothing that can be undone.
--
-- The seven states are a subset of the sixteen, so a binary rolled back to the
-- previous release reads every row this migration wrote without complaint: it
-- knows all seven names. What cannot be restored is the distinction the collapse
-- removed -- a session that is now WAITING_FOR_RESET may have been
-- USAGE_LIMIT_DETECTED or RESET_TIME_REACHED, and nothing recorded which.
-- Writing one of them back would put a session in a state it was never in, which
-- is worse than leaving it where it is.
--
-- A migration needs a non-empty Down section, so this one is a statement that
-- deliberately does nothing.
SELECT 1;
