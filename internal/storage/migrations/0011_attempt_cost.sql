-- +migrate Up
-- PRD 5 R4: what one attempt cost, as the CLI reported it.
--
-- The number is the CLI's own estimate (`total_cost_usd`), which is the same
-- figure the CLI's own `--max-budget-usd` would have compared against — and it
-- is explicitly not a bill. It is stored per attempt rather than per session
-- because the session-level cap is ccresu's own sum over these rows: a resumed
-- conversation's running total cannot be read back from the CLI, since totals
-- restored from earlier runs do not count toward it (PRD 5 §6.3).
--
-- NULL means *unknown*, not zero. A `--bg` handoff normally reports no cost at
-- all, so most rows will be NULL, and R10 requires that an unknown attempt
-- neither counts toward a cap nor blocks a resume. Defaulting the column to 0
-- would silently turn every unknown into a free attempt, which is the one thing
-- the cap must not do.
ALTER TABLE recovery_attempts ADD COLUMN cost_usd REAL;

-- +migrate Down
ALTER TABLE recovery_attempts DROP COLUMN cost_usd;
